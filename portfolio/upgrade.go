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
	p, s, ok, err := h.loadUpgradeTargets(c)
	if !ok {
		return err
	}
	if p.StrategyVer != nil && *p.StrategyVer == *s.InstalledVer {
		return writeJSON(c, fiber.StatusOK, fiber.Map{
			"status":  "already_at_latest",
			"version": *s.InstalledVer,
		})
	}

	bodyParams, ok, err := parseUpgradeBody(c)
	if !ok {
		return err
	}

	oldDescribe, newDescribe, ok, err := decodeUpgradeDescribes(c, p, s)
	if !ok {
		return err
	}

	diff := DiffParameters(oldDescribe, newDescribe)
	nextParams, ok, err := h.resolveUpgradeParams(c, p, s, bodyParams, diff, newDescribe)
	if !ok {
		return err
	}

	paramsJSON, err := json.Marshal(nextParams)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	presetName := MatchPresetName(nextParams, newDescribe)

	if err := h.store.ApplyUpgrade(c.Context(), p.ID, *s.InstalledVer,
		json.RawMessage(s.DescribeJSON), paramsJSON, presetName); err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	return h.finalizeUpgrade(c, p, s)
}

// loadUpgradeTargets loads the portfolio and the registry strategy row.
// Returns ok=false (with the response already written via the fiber ctx) on
// any precondition failure; the caller should propagate the returned error
// — which is whatever the response writer returned, typically nil — and
// stop processing.
func (h *Handler) loadUpgradeTargets(c fiber.Ctx) (Portfolio, strategy.Strategy, bool, error) {
	ownerSub, err := subject(c)
	if err != nil {
		return Portfolio{}, strategy.Strategy{}, false, writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
	}
	slug := c.Params("slug")
	p, err := h.store.Get(c.Context(), ownerSub, slug)
	if errors.Is(err, ErrNotFound) {
		return Portfolio{}, strategy.Strategy{}, false, writeProblem(c, fiber.StatusNotFound, "Not Found", "portfolio not found: "+slug)
	}
	if err != nil {
		return Portfolio{}, strategy.Strategy{}, false, writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	if p.Status == StatusRunning {
		return Portfolio{}, strategy.Strategy{}, false, writeJSON(c, fiber.StatusConflict, fiber.Map{"error": "run_in_progress"})
	}
	s, err := h.strategies.Get(c.Context(), p.StrategyCode)
	if err != nil {
		return Portfolio{}, strategy.Strategy{}, false, writeJSON(c, fiber.StatusUnprocessableEntity, fiber.Map{"error": "strategy_not_installable"})
	}
	if s.InstalledVer == nil || s.InstallError != nil {
		return Portfolio{}, strategy.Strategy{}, false, writeJSON(c, fiber.StatusUnprocessableEntity, fiber.Map{"error": "strategy_not_installable"})
	}
	return p, s, true, nil
}

// parseUpgradeBody returns the optional parameters map sent in the request
// body. ok=false means a malformed-body response was written and the caller
// must stop. An empty body returns (nil, true, nil).
func parseUpgradeBody(c fiber.Ctx) (map[string]any, bool, error) {
	raw := c.Body()
	if len(raw) == 0 {
		return nil, true, nil
	}
	var body struct {
		Parameters map[string]any `json:"parameters"`
	}
	if err := sonic.Unmarshal(raw, &body); err != nil {
		return nil, false, writeProblem(c, fiber.StatusBadRequest, "Bad Request", "body is not valid JSON")
	}
	return body.Parameters, true, nil
}

// decodeUpgradeDescribes loads the old describe (frozen on the portfolio
// row) and the new describe (from the registry strategy row). ok=false
// means a 500 response was written and the caller must stop.
func decodeUpgradeDescribes(c fiber.Ctx, p Portfolio, s strategy.Strategy) (strategy.Describe, strategy.Describe, bool, error) {
	var oldDescribe strategy.Describe
	if len(p.StrategyDescribeJSON) > 0 {
		if err := json.Unmarshal(p.StrategyDescribeJSON, &oldDescribe); err != nil {
			return strategy.Describe{}, strategy.Describe{}, false, writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error",
				"stored describe is invalid: "+err.Error())
		}
	}
	var newDescribe strategy.Describe
	if err := json.Unmarshal(s.DescribeJSON, &newDescribe); err != nil {
		return strategy.Describe{}, strategy.Describe{}, false, writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error",
			"registry describe is invalid: "+err.Error())
	}
	return oldDescribe, newDescribe, true, nil
}

// resolveUpgradeParams picks the parameters that the upgraded portfolio
// should use. ok=false means a response was written and the caller must
// stop; the returned error is whatever the response writer returned.
func (h *Handler) resolveUpgradeParams(
	c fiber.Ctx, p Portfolio, s strategy.Strategy, bodyParams map[string]any,
	diff ParameterDiff, newDescribe strategy.Describe,
) (map[string]any, bool, error) {
	switch {
	case bodyParams != nil:
		if err := validateParameters(bodyParams, newDescribe); err != nil {
			return nil, false, writeProblem(c, fiber.StatusBadRequest, "Bad Request", err.Error())
		}
		return bodyParams, true, nil
	case diff.Compatible():
		return mergeKeptAndDefaults(p.Parameters, newDescribe, diff), true, nil
	default:
		return nil, false, writeIncompatibleParametersResponse(c, p, s, diff)
	}
}

// writeIncompatibleParametersResponse emits the 409 body that asks the
// caller to resubmit with explicit parameters when a parameter rename or
// retype is detected.
func writeIncompatibleParametersResponse(c fiber.Ctx, p Portfolio, s strategy.Strategy, diff ParameterDiff) error {
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

// finalizeUpgrade dispatches the post-upgrade backtest run and writes the
// success response. Handles the various dispatcher error modes (queue full,
// run in flight, no dispatcher configured) gracefully.
func (h *Handler) finalizeUpgrade(c fiber.Ctx, p Portfolio, s strategy.Strategy) error {
	if h.dispatcher == nil {
		return writeJSON(c, fiber.StatusOK, fiber.Map{
			"status":       "upgraded",
			"from_version": deref(p.StrategyVer),
			"to_version":   *s.InstalledVer,
		})
	}
	runID, err := h.dispatcher.Submit(c.Context(), p.ID)
	switch {
	case errors.Is(err, ErrRunInFlight):
		return writeJSON(c, fiber.StatusOK, fiber.Map{
			"status":       "upgraded",
			"from_version": deref(p.StrategyVer),
			"to_version":   *s.InstalledVer,
			"note":         "a backtest run was already in flight; the upgrade is committed and the existing run will continue",
		})
	case errors.Is(err, ErrQueueFull):
		return writeProblem(c, fiber.StatusServiceUnavailable, "Service Unavailable",
			"backtest queue is full; the upgrade was committed but no run was queued. Retry by calling POST /portfolios/{slug}/runs")
	case err != nil:
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
