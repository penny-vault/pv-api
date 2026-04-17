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
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
)

// Config holds HTTP-layer configuration. Populated from cmd.serverConf.
type Config struct {
	Port         int
	AllowOrigins string
}

// NewApp builds a Fiber v3 app with pvapi's middleware stack and routes.
// The caller is responsible for calling app.Listen.
func NewApp(conf Config) *fiber.App {
	app := fiber.New(fiber.Config{
		JSONEncoder: sonic.Marshal,
		JSONDecoder: sonic.Unmarshal,
	})

	// Order: request-id first (so all later middleware + handlers see it),
	// then timer (so it measures everything inside), then CORS, then logger.
	app.Use(requestIDMiddleware())
	app.Use(timerMiddleware())

	if conf.AllowOrigins != "" {
		app.Use(cors.New(cors.Config{
			AllowOrigins: []string{conf.AllowOrigins},
		}))
	}

	app.Use(loggerMiddleware())

	registerRoutes(app)

	return app
}

func registerRoutes(app *fiber.App) {
	app.Get("/healthz", Healthz)
}
