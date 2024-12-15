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

package account

import (
	"github.com/gofiber/fiber/v2"
	"github.com/plaid/plaid-go/v31/plaid"
	"github.com/rs/zerolog/log"
)

type PlaidConfig struct {
	ClientID    string `mapstructure:"client_id"`
	Secret      string
	Environment string
}

var plaidConfiguration *plaid.Configuration

func SetupPlaid(config PlaidConfig) {
	plaidConfiguration = plaid.NewConfiguration()
	plaidConfiguration.AddDefaultHeader("PLAID-CLIENT-ID", config.ClientID)
	plaidConfiguration.AddDefaultHeader("PLAID-SECRET", config.Secret)

	switch config.Environment {
	case "sandbox":
		plaidConfiguration.UseEnvironment(plaid.Sandbox)
	case "production":
		plaidConfiguration.UseEnvironment(plaid.Production)
	default:
		log.Panic().Str("plaid.environment", config.Environment).Msg("plaid.environment must be one of 'sandbox' or 'production'")
	}
}

// PlaidLinkToken interacts with the Plaid API to generate a new link token.
// For further details see: https://plaid.com/docs/api/link/#linktokencreate
func PlaidLinkToken(userKey interface{}) fiber.Handler {
	return func(ctx *fiber.Ctx) error {
		client := plaid.NewAPIClient(plaidConfiguration)

		// NOTE: If penny vault ever supports non-us / non-english users this
		// will need to change to use a value from the users jwt
		request := plaid.NewLinkTokenCreateRequest(
			"Penny Vault",
			"en",
			[]plaid.CountryCode{plaid.COUNTRYCODE_US},
			plaid.LinkTokenCreateRequestUser{
				ClientUserId: ctx.Locals(userKey).(string),
			},
		)

		request.SetProducts([]plaid.Products{plaid.PRODUCTS_TRANSACTIONS})

		baseUrl := ctx.BaseURL()
		if baseUrl == "http://" {
			baseUrl = "https://test.pennyvault.app"
		}

		request.SetWebhook(baseUrl + "/api/v1/plaid/hook")

		resp, _, err := client.PlaidApi.LinkTokenCreate(ctx.UserContext()).LinkTokenCreateRequest(*request).Execute()
		if err != nil {
			log.Error().Err(err).Msg("error getting link token")
			return ctx.Status(fiber.StatusBadRequest).JSON(struct {
				Status  string `json:"status"`
				Message string `json:"message"`
			}{
				Status:  "400",
				Message: err.Error(),
			})
		}

		linkToken := resp.GetLinkToken()

		return ctx.Status(fiber.StatusCreated).JSON(struct {
			Token string `json:"token"`
		}{
			Token: linkToken,
		})
	}
}
