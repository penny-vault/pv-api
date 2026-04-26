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
	"encoding/json"
	"errors"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"

	"github.com/penny-vault/pv-api/strategy"
)

// Upgrade implements POST /portfolios/{slug}/upgrade.
//
// Flow:
//  1. Authenticate; load the portfolio; reject if status='running'.
//  2. Look up the registry strategy; return 422 if not installable.
//  3. If portfolio.strategy_ver == registry.installed_ver → 200 already_at_latest.
//  4. Diff the old (frozen on the portfolio) vs new (registry) describe.
//  5. If body has parameters → validate against new describe; on success use them.
//     Else if diff is compatible → merge kept values + new defaults.
//     Else → 409 parameters_incompatible with diff body.
//  6. Atomically update the portfolio (ApplyUpgrade).
//  7. Enqueue a new run via Dispatcher.Submit; return 200 with run_id.
//
// See docs/superpowers/specs/2026-04-25-portfolio-strategy-upgrade-design.md.
func (h *Handler) Upgrade(c fiber.Ctx) error {
	ownerSub, err := subject(c)
	if err != nil {
		return writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
	}
	slug := c.Params("slug")

	p, err := h.store.Get(c.Context(), ownerSub, slug)
	if errors.Is(err, ErrNotFound) {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "portfolio not found: "+slug)
	}
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	if p.Status == StatusRunning {
		return writeJSON(c, fiber.StatusConflict, fiber.Map{"error": "run_in_progress"})
	}

	s, err := h.strategies.Get(c.Context(), p.StrategyCode)
	if err != nil {
		// Unofficial portfolios won't have a registry row; they fall through here as
		// strategy_not_installable. That's intentional — official-only upgrade for now.
		return writeJSON(c, fiber.StatusUnprocessableEntity, fiber.Map{"error": "strategy_not_installable"})
	}
	if s.InstalledVer == nil || s.InstallError != nil {
		return writeJSON(c, fiber.StatusUnprocessableEntity, fiber.Map{"error": "strategy_not_installable"})
	}

	if p.StrategyVer != nil && *p.StrategyVer == *s.InstalledVer {
		return writeJSON(c, fiber.StatusOK, fiber.Map{
			"status":  "already_at_latest",
			"version": *s.InstalledVer,
		})
	}

	// Body is optional. Parse only if present.
	type upgradeRequestBody struct {
		Parameters map[string]any `json:"parameters"`
	}
	var body upgradeRequestBody
	if raw := c.Body(); len(raw) > 0 {
		if err := sonic.Unmarshal(raw, &body); err != nil {
			return writeProblem(c, fiber.StatusBadRequest, "Bad Request", "body is not valid JSON")
		}
	}

	// Old describe is on the portfolio row.
	var oldDescribe strategy.Describe
	if len(p.StrategyDescribeJSON) > 0 {
		if err := json.Unmarshal(p.StrategyDescribeJSON, &oldDescribe); err != nil {
			return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error",
				"stored describe is invalid: "+err.Error())
		}
	}

	// New describe comes from the registry.
	var newDescribe strategy.Describe
	if err := json.Unmarshal(s.DescribeJSON, &newDescribe); err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error",
			"registry describe is invalid: "+err.Error())
	}

	diff := DiffParameters(oldDescribe, newDescribe)

	var nextParams map[string]any
	switch {
	case body.Parameters != nil:
		// Resubmit branch: caller supplies explicit parameters for the new describe.
		if err := validateParameters(body.Parameters, newDescribe); err != nil {
			return writeProblem(c, fiber.StatusBadRequest, "Bad Request", err.Error())
		}
		nextParams = body.Parameters
	case diff.Compatible():
		nextParams = mergeKeptAndDefaults(p.Parameters, newDescribe, diff)
	default:
		// Parameters changed in a breaking way; caller must resubmit with parameters.
		removed := diff.Removed
		if removed == nil {
			removed = []string{}
		}
		addedNoDefault := diff.AddedWithoutDefault
		if addedNoDefault == nil {
			addedNoDefault = []string{}
		}
		retyped := diff.Retyped
		if retyped == nil {
			retyped = []ParameterRetype{}
		}
		return writeJSON(c, fiber.StatusConflict, fiber.Map{
			"error":        "parameters_incompatible",
			"from_version": deref(p.StrategyVer),
			"to_version":   *s.InstalledVer,
			"incompatibilities": fiber.Map{
				"removed":               removed,
				"added_without_default": addedNoDefault,
				"retyped":               retyped,
			},
			"current_parameters": p.Parameters,
			"new_describe":       json.RawMessage(s.DescribeJSON),
		})
	}

	paramsJSON, err := json.Marshal(nextParams)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	presetName := MatchPresetName(nextParams, newDescribe)

	// Atomic portfolio update; run insert happens via Dispatcher.Submit below.
	if err := h.store.ApplyUpgrade(c.Context(), p.ID, *s.InstalledVer,
		json.RawMessage(s.DescribeJSON), paramsJSON, presetName); err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	// Enqueue the run via the same path Create uses.
	if h.dispatcher == nil {
		// Without a dispatcher the upgrade is committed but no run is queued. The
		// user can call POST /portfolios/:slug/run later.
		return writeJSON(c, fiber.StatusOK, fiber.Map{
			"status":       "upgraded",
			"from_version": deref(p.StrategyVer),
			"to_version":   *s.InstalledVer,
		})
	}
	runID, err := h.dispatcher.Submit(c.Context(), p.ID)
	if err != nil {
		if errors.Is(err, ErrQueueFull) {
			return writeProblem(c, fiber.StatusServiceUnavailable, "Service Unavailable",
				"backtest queue is full; the upgrade was committed but no run was queued. Retry by calling POST /portfolios/{slug}/runs")
		}
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	return writeJSON(c, fiber.StatusOK, fiber.Map{
		"status":       "upgraded",
		"from_version": deref(p.StrategyVer),
		"to_version":   *s.InstalledVer,
		"run_id":       runID.String(),
	})
}

// mergeKeptAndDefaults builds the next parameter map from kept parameters and
// newly-added parameters that have defaults.
func mergeKeptAndDefaults(current map[string]any, d strategy.Describe, diff ParameterDiff) map[string]any {
	out := make(map[string]any, len(current)+len(diff.AddedWithDefault))
	for _, k := range diff.Kept {
		out[k] = current[k]
	}
	for _, name := range diff.AddedWithDefault {
		for _, p := range d.Parameters {
			if p.Name == name {
				out[name] = p.Default
				break
			}
		}
	}
	return out
}

// deref returns the string a pointer points to, or "" if the pointer is nil.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
