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
	"errors"

	"github.com/gofiber/fiber/v3"
)

// Upgrade implements POST /portfolios/{slug}/upgrade.
//
// This skeleton handles the early-exit paths only:
//   - 401 unauthorized
//   - 404 not found
//   - 409 run_in_progress
//   - 422 strategy_not_installable
//   - 200 already_at_latest
//
// The compatible-upgrade and 409 parameters_incompatible paths are wired in
// Tasks 10 and 11.
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

	// Compatibility checks and apply happen in Tasks 10 + 11.
	return writeProblem(c, fiber.StatusNotImplemented, "Not Implemented", "upgrade flow not yet implemented")
}
