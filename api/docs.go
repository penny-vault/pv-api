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
	"github.com/penny-vault/pv-api/openapi"
)

const scalarHTML = `<!DOCTYPE html>
<html>
<head>
  <title>pv-api</title>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
</head>
<body>
  <script id="api-reference" data-url="/openapi/spec"></script>
  <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
</body>
</html>`

func RegisterDocsRoutes(app *fiber.App) {
	app.Get("/openapi", func(c fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		return c.SendString(scalarHTML)
	})
	app.Get("/openapi/spec", func(c fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, "application/yaml")
		return c.Send(openapi.Spec)
	})
}
