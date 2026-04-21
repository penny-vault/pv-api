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

	"github.com/penny-vault/pv-api/strategy"
)

// StrategyHandler is the real-handler shim owned by api/. It delegates
// to strategy.Handler for GET list/get endpoints, and to
// strategy.DescribeHandler for the describe endpoint.
type StrategyHandler struct {
	inner    *strategy.Handler
	describe *strategy.DescribeHandler
}

// NewStrategyHandler builds a StrategyHandler backed by the given read store,
// builder, URL validator, and ephemeral options.
func NewStrategyHandler(
	store strategy.ReadStore,
	builder strategy.BuilderFunc,
	validator strategy.URLValidatorFunc,
	opts strategy.EphemeralOptions,
) *StrategyHandler {
	return &StrategyHandler{
		inner: strategy.NewHandler(store),
		describe: &strategy.DescribeHandler{
			Builder:       builder,
			URLValidator:  validator,
			EphemeralOpts: opts,
		},
	}
}

// RegisterStrategyRoutes mounts stub strategy endpoints on the provided
// router. Used when no DB pool is configured (e.g. tests that don't need
// real handlers).
func RegisterStrategyRoutes(r fiber.Router) {
	r.Get("/strategies", stubListStrategies)
	r.Get("/strategies/describe", stubDescribeStrategy)
	r.Get("/strategies/:shortCode", stubGetStrategy)
}

// RegisterStrategyRoutesWith mounts the strategy endpoints, delegating to
// the given handler.
func RegisterStrategyRoutesWith(r fiber.Router, h *StrategyHandler) {
	r.Get("/strategies", h.inner.List)
	r.Get("/strategies/describe", h.describe.Describe)
	r.Get("/strategies/:shortCode", h.inner.Get)
}

func stubListStrategies(c fiber.Ctx) error   { return WriteProblem(c, ErrNotImplemented) }
func stubDescribeStrategy(c fiber.Ctx) error { return WriteProblem(c, ErrNotImplemented) }
func stubGetStrategy(c fiber.Ctx) error      { return WriteProblem(c, ErrNotImplemented) }
