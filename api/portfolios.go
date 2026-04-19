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
	"github.com/penny-vault/pv-api/strategy"
)

// PortfolioHandler is the real-handler shim owned by api/. It delegates
// to portfolio.Handler for the CRUD endpoints. Every derived-data route
// remains 501 until Plan 5.
type PortfolioHandler struct {
	inner *portfolio.Handler
}

// NewPortfolioHandler builds a PortfolioHandler backed by a portfolio.Store
// and a strategy.ReadStore.
func NewPortfolioHandler(store portfolio.Store, strategies strategy.ReadStore) *PortfolioHandler {
	return &PortfolioHandler{inner: portfolio.NewHandler(store, strategies)}
}

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
	r.Get("/portfolios/:slug/metrics", stubPortfolio)
	r.Get("/portfolios/:slug/trailing-returns", stubPortfolio)
	r.Get("/portfolios/:slug/holdings", stubPortfolio)
	r.Get("/portfolios/:slug/holdings/:date", stubPortfolio)
	r.Get("/portfolios/:slug/measurements", stubPortfolio)
	r.Post("/portfolios/:slug/runs", stubPortfolio)
	r.Get("/portfolios/:slug/runs", stubPortfolio)
	r.Get("/portfolios/:slug/runs/:runId", stubPortfolio)
}

// RegisterPortfolioRoutesWith mounts CRUD endpoints backed by h; all
// derived-data endpoints stay 501 until Plan 5 (backtest runner).
func RegisterPortfolioRoutesWith(r fiber.Router, h *PortfolioHandler) {
	r.Get("/portfolios", h.inner.List)
	r.Post("/portfolios", h.inner.Create)
	r.Get("/portfolios/:slug", h.inner.Get)
	r.Patch("/portfolios/:slug", h.inner.Patch)
	r.Delete("/portfolios/:slug", h.inner.Delete)

	// derived-data stubs — land in Plan 5
	r.Get("/portfolios/:slug/summary", stubPortfolio)
	r.Get("/portfolios/:slug/drawdowns", stubPortfolio)
	r.Get("/portfolios/:slug/metrics", stubPortfolio)
	r.Get("/portfolios/:slug/trailing-returns", stubPortfolio)
	r.Get("/portfolios/:slug/holdings", stubPortfolio)
	r.Get("/portfolios/:slug/holdings/:date", stubPortfolio)
	r.Get("/portfolios/:slug/measurements", stubPortfolio)
	r.Post("/portfolios/:slug/runs", stubPortfolio)
	r.Get("/portfolios/:slug/runs", stubPortfolio)
	r.Get("/portfolios/:slug/runs/:runId", stubPortfolio)
}

func stubPortfolio(c fiber.Ctx) error { return WriteProblem(c, ErrNotImplemented) }
