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

package api

import (
	"github.com/gofiber/fiber/v3"
)

// RegisterPortfolioRoutes mounts every portfolio-related endpoint on the
// provided router. All handlers are stubs returning 501 until Plan 3 fills
// them in.
func RegisterPortfolioRoutes(r fiber.Router) {
	r.Get("/portfolios", listPortfolios)
	r.Post("/portfolios", createPortfolio)
	r.Get("/portfolios/:slug", getPortfolio)
	r.Patch("/portfolios/:slug", updatePortfolio)
	r.Delete("/portfolios/:slug", deletePortfolio)
	r.Get("/portfolios/:slug/measurements", getPortfolioMeasurements)
	r.Post("/portfolios/:slug/runs", triggerPortfolioRun)
	r.Get("/portfolios/:slug/runs", listPortfolioRuns)
	r.Get("/portfolios/:slug/runs/:runId", getPortfolioRun)
}

func listPortfolios(c fiber.Ctx) error           { return WriteProblem(c, ErrNotImplemented) }
func createPortfolio(c fiber.Ctx) error          { return WriteProblem(c, ErrNotImplemented) }
func getPortfolio(c fiber.Ctx) error             { return WriteProblem(c, ErrNotImplemented) }
func updatePortfolio(c fiber.Ctx) error          { return WriteProblem(c, ErrNotImplemented) }
func deletePortfolio(c fiber.Ctx) error          { return WriteProblem(c, ErrNotImplemented) }
func getPortfolioMeasurements(c fiber.Ctx) error { return WriteProblem(c, ErrNotImplemented) }
func triggerPortfolioRun(c fiber.Ctx) error      { return WriteProblem(c, ErrNotImplemented) }
func listPortfolioRuns(c fiber.Ctx) error        { return WriteProblem(c, ErrNotImplemented) }
func getPortfolioRun(c fiber.Ctx) error          { return WriteProblem(c, ErrNotImplemented) }
