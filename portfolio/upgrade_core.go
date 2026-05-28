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
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/penny-vault/pv-api/strategy"
)

// errStoredDescribeInvalid and errRegistryDescribeInvalid are returned from
// decodeDescribes when the respective JSON blob fails to unmarshal.
var (
	errStoredDescribeInvalid   = errors.New("stored describe is invalid")
	errRegistryDescribeInvalid = errors.New("registry describe is invalid")
)

// UpgradeOutcome categorises the result of an upgrade attempt.
type UpgradeOutcome int

const (
	UpgradeOutcomeUnknown UpgradeOutcome = iota
	// UpgradeOutcomeAlreadyAtLatest is returned when the portfolio's
	// strategy_ver already equals the registry's installed_ver.
	UpgradeOutcomeAlreadyAtLatest
	// UpgradeOutcomeApplied is returned after a successful ApplyUpgrade.
	// A run may or may not have been dispatched; check RunID and RunErr.
	UpgradeOutcomeApplied
	// UpgradeOutcomeIncompatibleParams is returned when the params diff
	// between old and new describes is not auto-mergeable and the caller did
	// not provide explicit body params.
	UpgradeOutcomeIncompatibleParams
	// UpgradeOutcomeInvalidParams is returned when the caller-supplied body
	// params fail validation against the new describe.
	UpgradeOutcomeInvalidParams
)

// UpgradeResult captures everything callers need to react to an upgrade
// attempt without touching a fiber.Ctx.
type UpgradeResult struct {
	Outcome     UpgradeOutcome
	FromVersion string
	ToVersion   string

	// Applied path:
	RunID  *uuid.UUID
	RunErr error // dispatcher error if Submit failed; the upgrade is still committed

	// IncompatibleParams path:
	Diff        ParameterDiff
	NewDescribe json.RawMessage

	// InvalidParams path:
	ValidateErr error
}

// doUpgrade contains the upgrade logic shared by the HTTP handler and the
// auto-upgrade service. It does not touch fiber.Ctx and does not write
// responses. The caller is responsible for loading p and s and for shaping
// the result for its medium (HTTP body vs log line).
//
// bodyParams=nil means "no explicit params from the caller; use the merge
// path if the diff is compatible". Non-nil means "validate these against
// the new describe and use them if valid".
//
// The dispatcher field on h is used to enqueue a backtest run after a
// successful ApplyUpgrade. If h.dispatcher is nil, the upgrade is still
// committed and Outcome==UpgradeOutcomeApplied with RunID==nil.
func (h *Handler) doUpgrade(
	ctx context.Context, p Portfolio, s strategy.Strategy, bodyParams map[string]any,
) (UpgradeResult, error) {
	res := UpgradeResult{
		FromVersion: deref(p.StrategyVer),
		ToVersion:   deref(s.InstalledVer),
	}

	if p.StrategyVer != nil && s.InstalledVer != nil && *p.StrategyVer == *s.InstalledVer {
		res.Outcome = UpgradeOutcomeAlreadyAtLatest
		return res, nil
	}

	oldDescribe, newDescribe, err := decodeDescribes(p, s)
	if err != nil {
		return res, err
	}

	diff := DiffParameters(oldDescribe, newDescribe)
	res.Diff = diff
	res.NewDescribe = json.RawMessage(s.DescribeJSON)

	var nextParams map[string]any
	switch {
	case bodyParams != nil:
		if err := validateParameters(bodyParams, newDescribe); err != nil {
			res.Outcome = UpgradeOutcomeInvalidParams
			res.ValidateErr = err
			return res, nil
		}
		nextParams = bodyParams
	case diff.Compatible():
		nextParams = mergeKeptAndDefaults(p.Parameters, newDescribe, diff)
	default:
		res.Outcome = UpgradeOutcomeIncompatibleParams
		return res, nil
	}

	paramsJSON, err := json.Marshal(nextParams)
	if err != nil {
		return res, err
	}
	presetName := MatchPresetName(nextParams, newDescribe)

	if err := h.store.ApplyUpgrade(ctx, p.ID, *s.InstalledVer,
		json.RawMessage(s.DescribeJSON), paramsJSON, presetName); err != nil {
		return res, err
	}

	res.Outcome = UpgradeOutcomeApplied
	if h.dispatcher == nil {
		return res, nil
	}
	runID, err := h.dispatcher.Submit(ctx, p.ID)
	if err != nil {
		res.RunErr = err
		return res, nil
	}
	res.RunID = &runID
	return res, nil
}

// decodeDescribes loads the old describe (frozen on the portfolio row) and
// the new describe (from the registry strategy row). Returns the unmarshal
// error verbatim — callers wrap it for their medium.
func decodeDescribes(p Portfolio, s strategy.Strategy) (strategy.Describe, strategy.Describe, error) {
	var oldDescribe strategy.Describe
	if len(p.StrategyDescribeJSON) > 0 {
		if err := json.Unmarshal(p.StrategyDescribeJSON, &oldDescribe); err != nil {
			return strategy.Describe{}, strategy.Describe{}, fmt.Errorf("%w: %w", errStoredDescribeInvalid, err)
		}
	}
	var newDescribe strategy.Describe
	if err := json.Unmarshal(s.DescribeJSON, &newDescribe); err != nil {
		return strategy.Describe{}, strategy.Describe{}, fmt.Errorf("%w: %w", errRegistryDescribeInvalid, err)
	}
	return oldDescribe, newDescribe, nil
}
