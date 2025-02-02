// Copyright 2021-2024
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
	"github.com/gofiber/fiber/v2"
	"github.com/penny-vault/pv-api/account"
)

func setupRoutes(app *fiber.App) {
	api := app.Group("api")
	v1 := api.Group("v1")

	plaid := v1.Group("plaid")
	plaid.Post("link", hasAuth(), hasRole("plaid"), account.PlaidLinkToken)
	plaid.Post("item", hasAuth(), hasRole("plaid"), account.PlaidExchangeToken)
	plaid.Post("sync", hasAuth(), hasRole("plaid"), account.PlaidSync)
}
