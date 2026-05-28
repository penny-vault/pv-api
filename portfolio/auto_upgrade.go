// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package portfolio

import (
	"context"
	"errors"

	"github.com/Masterminds/semver/v3"
	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pv-api/strategy"
)

// AutoUpgrader is the portfolio-side hook the strategy Syncer calls after a
// new version is installed. It scans portfolios on that strategy and
// upgrades the ones whose pinned version differs only in the semver patch
// component (e.g. 0.2.1 → 0.2.2). Anything else — minor bump, major bump,
// unparseable version, incompatible parameter diff — is left to the user's
// explicit POST /portfolios/{slug}/upgrade.
type AutoUpgrader struct {
	handler    *Handler
	strategies strategy.ReadStore
}

// NewAutoUpgrader builds an AutoUpgrader that reuses the Handler's store
// and dispatcher for the upgrade and run-submit steps.
func NewAutoUpgrader(h *Handler, strategies strategy.ReadStore) *AutoUpgrader {
	return &AutoUpgrader{handler: h, strategies: strategies}
}

// AutoUpgradeAfterInstall is invoked by the strategy Syncer after a
// successful install. It enumerates portfolios on shortCode and applies the
// patch-only auto-upgrade rule to each.
//
// Errors are logged, not returned: this runs in a background goroutine and
// the syncer does not need to react to per-portfolio failures. The strategy
// install itself has already committed.
func (a *AutoUpgrader) AutoUpgradeAfterInstall(ctx context.Context, shortCode, newVer string) {
	if a == nil || a.handler == nil {
		return
	}

	s, err := a.strategies.Get(ctx, shortCode)
	if err != nil {
		log.Warn().Err(err).Str("short_code", shortCode).Msg("auto-upgrade: load strategy failed")
		return
	}
	if s.InstalledVer == nil || s.InstallError != nil {
		log.Debug().Str("short_code", shortCode).Msg("auto-upgrade: strategy not installable; skipping")
		return
	}

	newParsed, err := semver.NewVersion(newVer)
	if err != nil {
		log.Warn().Err(err).Str("short_code", shortCode).Str("new_ver", newVer).
			Msg("auto-upgrade: unparseable new version; skipping")
		return
	}

	portfolios, err := a.handler.store.ListByStrategyCode(ctx, shortCode)
	if err != nil {
		log.Warn().Err(err).Str("short_code", shortCode).Msg("auto-upgrade: list portfolios failed")
		return
	}

	for _, p := range portfolios {
		a.tryUpgradeOne(ctx, p, s, newParsed)
	}
}

// tryUpgradeOne applies the patch-only rule to a single portfolio and
// performs the upgrade if eligible.
func (a *AutoUpgrader) tryUpgradeOne(ctx context.Context, p Portfolio, s strategy.Strategy, newVer *semver.Version) {
	logger := log.With().
		Str("short_code", s.ShortCode).
		Str("portfolio_id", p.ID.String()).
		Str("slug", p.Slug).
		Logger()

	if p.Status == StatusRunning {
		logger.Debug().Msg("auto-upgrade skipped: portfolio is running")
		return
	}
	if p.StrategyVer == nil {
		logger.Debug().Msg("auto-upgrade skipped: portfolio has no pinned version (unofficial strategy)")
		return
	}
	curVer, err := semver.NewVersion(*p.StrategyVer)
	if err != nil {
		logger.Warn().Err(err).Str("portfolio_ver", *p.StrategyVer).
			Msg("auto-upgrade skipped: unparseable portfolio version")
		return
	}
	if !isPatchBump(curVer, newVer) {
		logger.Debug().
			Str("portfolio_ver", curVer.Original()).
			Str("new_ver", newVer.Original()).
			Msg("auto-upgrade skipped: not a patch-only bump")
		return
	}

	res, err := a.handler.doUpgrade(ctx, p, s, nil /* no body params; use merge path */)
	if err != nil {
		logger.Warn().Err(err).Msg("auto-upgrade failed")
		return
	}

	switch res.Outcome {
	case UpgradeOutcomeApplied:
		runID := ""
		if res.RunID != nil {
			runID = res.RunID.String()
		}
		ev := logger.Info().
			Str("from_version", res.FromVersion).
			Str("to_version", res.ToVersion).
			Str("run_id", runID)
		if res.RunErr != nil {
			ev = ev.AnErr("dispatch_err", res.RunErr)
		}
		ev.Msg("auto-upgrade applied")
	case UpgradeOutcomeIncompatibleParams:
		logger.Info().
			Str("from_version", res.FromVersion).
			Str("to_version", res.ToVersion).
			Msg("auto-upgrade skipped: incompatible parameters, manual upgrade required")
	case UpgradeOutcomeAlreadyAtLatest:
		// Possible if a second tick races a previous upgrade; benign.
		logger.Debug().Msg("auto-upgrade skipped: already at latest")
	case UpgradeOutcomeInvalidParams:
		// Should not happen on the merge path (we pass nil bodyParams); log
		// loudly if it does so we notice.
		logger.Warn().AnErr("validate_err", errors.Unwrap(res.ValidateErr)).
			Msg("auto-upgrade unexpected: invalid params on merge path")
	}
}

// isPatchBump returns true when newVer differs from curVer only in the patch
// component and is strictly greater. Pre-release identifiers are ignored:
// 0.2.1-rc.1 → 0.2.2 still counts as a patch bump.
func isPatchBump(curVer, newVer *semver.Version) bool {
	if curVer.Major() != newVer.Major() {
		return false
	}
	if curVer.Minor() != newVer.Minor() {
		return false
	}
	return newVer.GreaterThan(curVer)
}
