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

	"github.com/penny-vault/pv-api/portfolio"
)

// RegisterPortfolioRoutes mounts every portfolio endpoint as stubs (501).
// Kept for test harnesses that do not supply a pool.
func RegisterPortfolioRoutes(r fiber.Router) {
	r.Get("/portfolios", stubPortfolio)
	r.Post("/portfolios", stubPortfolio)
	r.Get("/portfolios/:slug", stubPortfolio)
	r.Patch("/portfolios/:slug", stubPortfolio)
	r.Delete("/portfolios/:slug", stubPortfolio)
	r.Get("/portfolios/:slug/summary", stubPortfolio)
	r.Get("/portfolios/:slug/drawdowns", stubPortfolio)
	r.Get("/portfolios/:slug/statistics", stubPortfolio)
	r.Get("/portfolios/:slug/performance", stubPortfolio)
	r.Get("/portfolios/:slug/transactions", stubPortfolio)
	r.Get("/portfolios/:slug/trailing-returns", stubPortfolio)
	r.Get("/portfolios/:slug/holdings", stubPortfolio)
	r.Get("/portfolios/:slug/holdings/history", stubPortfolio)
	r.Get("/portfolios/:slug/holdings/:date", stubPortfolio)
	r.Post("/portfolios/:slug/run", stubPortfolio)
	r.Get("/portfolios/:slug/runs", stubPortfolio)
	r.Get("/portfolios/:slug/runs/:runId", stubPortfolio)
}

// RegisterPortfolioRoutesWith mounts all portfolio endpoints backed by h.
func RegisterPortfolioRoutesWith(r fiber.Router, h *portfolio.Handler) {
	r.Get("/portfolios", h.List)
	r.Post("/portfolios", h.Create)
	r.Get("/portfolios/:slug", h.Get)
	r.Patch("/portfolios/:slug", h.Patch)
	r.Delete("/portfolios/:slug", h.Delete)
	r.Get("/portfolios/:slug/summary", h.Summary)
	r.Get("/portfolios/:slug/drawdowns", h.Drawdowns)
	r.Get("/portfolios/:slug/statistics", h.Statistics)
	r.Get("/portfolios/:slug/trailing-returns", h.TrailingReturns)
	r.Get("/portfolios/:slug/holdings", h.Holdings)
	r.Get("/portfolios/:slug/holdings/history", h.HoldingsHistory) // MUST precede :date
	r.Get("/portfolios/:slug/holdings/:date", h.HoldingsAsOf)
	r.Get("/portfolios/:slug/performance", h.Performance)
	r.Get("/portfolios/:slug/transactions", h.Transactions)
	r.Post("/portfolios/:slug/run", h.CreateRun)
	r.Get("/portfolios/:slug/runs", h.ListRuns)
	r.Get("/portfolios/:slug/runs/:runId", h.GetRun)
}

func stubPortfolio(c fiber.Ctx) error { return WriteProblem(c, ErrNotImplemented) }
