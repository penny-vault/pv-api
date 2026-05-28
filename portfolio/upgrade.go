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
//  3. Delegate the upgrade decision to doUpgrade (shared with the
//     auto-upgrader).
//  4. Shape the result as an HTTP response.
//
// See docs/superpowers/specs/2026-04-25-portfolio-strategy-upgrade-design.md.
func (h *Handler) Upgrade(c fiber.Ctx) error {
	p, s, ok, err := h.loadUpgradeTargets(c)
	if !ok {
		return err
	}

	bodyParams, ok, err := parseUpgradeBody(c)
	if !ok {
		return err
	}

	res, err := h.doUpgrade(c.Context(), p, s, bodyParams)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return writeUpgradeResponse(c, p, s, res)
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

// writeUpgradeResponse converts an UpgradeResult into the HTTP response
// shape defined by the upgrade endpoint contract.
func writeUpgradeResponse(c fiber.Ctx, p Portfolio, s strategy.Strategy, res UpgradeResult) error {
	switch res.Outcome {
	case UpgradeOutcomeAlreadyAtLatest:
		return writeJSON(c, fiber.StatusOK, fiber.Map{
			"status":  "already_at_latest",
			"version": *s.InstalledVer,
		})
	case UpgradeOutcomeInvalidParams:
		return writeProblem(c, fiber.StatusBadRequest, "Bad Request", res.ValidateErr.Error())
	case UpgradeOutcomeIncompatibleParams:
		return writeIncompatibleParametersResponse(c, p, s, res.Diff)
	case UpgradeOutcomeApplied:
		body := fiber.Map{
			"status":       "upgraded",
			"from_version": res.FromVersion,
			"to_version":   res.ToVersion,
		}
		if res.RunErr != nil {
			switch {
			case errors.Is(res.RunErr, ErrRunInFlight):
				body["note"] = "a backtest run was already in flight; the upgrade is committed and the existing run will continue"
				return writeJSON(c, fiber.StatusOK, body)
			case errors.Is(res.RunErr, ErrQueueFull):
				return writeProblem(c, fiber.StatusServiceUnavailable, "Service Unavailable",
					"backtest queue is full; the upgrade was committed but no run was queued. Retry by calling POST /portfolios/{slug}/runs")
			default:
				return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", res.RunErr.Error())
			}
		}
		if res.RunID != nil {
			body["run_id"] = res.RunID.String()
		}
		return writeJSON(c, fiber.StatusOK, body)
	}
	return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", "unknown upgrade outcome")
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
