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
// to strategy.Handler for GET endpoints; POST stays 501 until Plan 7.
type StrategyHandler struct {
	inner *strategy.Handler
}

// NewStrategyHandler builds a StrategyHandler backed by the given read store.
func NewStrategyHandler(store strategy.ReadStore) *StrategyHandler {
	return &StrategyHandler{inner: strategy.NewHandler(store)}
}

// RegisterStrategyRoutes mounts the strategy endpoints on the provided
// router. The zero-value argument keeps compatibility with Plan 2's stub
// signature; callers that want real handlers use RegisterStrategyRoutesWith.
func RegisterStrategyRoutes(r fiber.Router) {
	r.Get("/strategies", stubListStrategies)
	r.Post("/strategies", stubRegisterUnofficialStrategy)
	r.Get("/strategies/:shortCode", stubGetStrategy)
}

// RegisterStrategyRoutesWith mounts the strategy endpoints, delegating to
// the given handler for GETs. POST stays 501.
func RegisterStrategyRoutesWith(r fiber.Router, h *StrategyHandler) {
	r.Get("/strategies", h.inner.List)
	r.Post("/strategies", stubRegisterUnofficialStrategy)
	r.Get("/strategies/:shortCode", h.inner.Get)
}

func stubListStrategies(c fiber.Ctx) error             { return WriteProblem(c, ErrNotImplemented) }
func stubRegisterUnofficialStrategy(c fiber.Ctx) error { return WriteProblem(c, ErrNotImplemented) }
func stubGetStrategy(c fiber.Ctx) error                { return WriteProblem(c, ErrNotImplemented) }
