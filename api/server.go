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
	"context"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
)

// Config holds HTTP-layer configuration.
type Config struct {
	Port         int
	AllowOrigins string
	Auth         AuthConfig
}

// NewApp builds a Fiber v3 app with pvapi's middleware stack and routes.
// /healthz is public; every other route is mounted under the auth middleware.
// ctx controls the JWK cache lifecycle.
func NewApp(ctx context.Context, conf Config) (*fiber.App, error) {
	app := fiber.New(fiber.Config{
		JSONEncoder: sonic.Marshal,
		JSONDecoder: sonic.Unmarshal,
	})

	app.Use(requestIDMiddleware())
	app.Use(timerMiddleware())

	if conf.AllowOrigins != "" {
		app.Use(cors.New(cors.Config{
			AllowOrigins: []string{conf.AllowOrigins},
		}))
	}

	app.Use(loggerMiddleware())

	// Public routes.
	app.Get("/healthz", Healthz)

	// Protected routes.
	auth, err := NewAuthMiddleware(ctx, conf.Auth)
	if err != nil {
		return nil, fmt.Errorf("build auth middleware: %w", err)
	}
	protected := app.Group("", auth)
	RegisterPortfolioRoutes(protected)
	RegisterStrategyRoutes(protected)

	return app, nil
}
