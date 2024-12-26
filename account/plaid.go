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
	"encoding/json"

	"github.com/gofiber/fiber/v2"
	"github.com/penny-vault/pv-api/sql"
	"github.com/penny-vault/pv-api/types"
	"github.com/plaid/plaid-go/v31/plaid"
	"github.com/rs/zerolog/log"
)

type StatusResponse struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	TraceID string `json:"trace_id"`
}

// PlaidConfig is a struct that holds the configuration for the Plaid API
type PlaidConfig struct {
	ClientID    string `mapstructure:"client_id"`
	Secret      string
	Environment string
}

var plaidConfiguration *plaid.Configuration

// SetupPlaid sets up the Plaid API configuration
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

// PlaidExchangeToken interacts with the Plaid API to exchange a public token
// and stores the resulting item_id and access_token in the users database.
// For further details see: https://plaid.com/docs/api/items/#itempublic_tokenexchange
func PlaidExchangeToken(ctx *fiber.Ctx) error {
	traceId := ctx.Locals(types.TraceIdKey{}).(string)
	myLogger := log.With().Str("TraceID", traceId).Logger()

	client := plaid.NewAPIClient(plaidConfiguration)

	payload := struct {
		Accounts []struct {
			ID                 string `json:"id"`
			Name               string `json:"name"`
			Mask               string `json:"mask"`
			Type               string `json:"type"`
			Subtype            string `json:"subtype"`
			VerificationStatus string `json:"verification_status"`
			ClassType          string `json:"class_type"`
		} `json:"accounts"`
		Institution struct {
			Name          string `json:"name"`
			InstitutionID string `json:"institution_id"`
		} `json:"institution"`
		PublicToken string `json:"public_token"`
	}{}

	if err := ctx.BodyParser(&payload); err != nil {
		return err
	}

	request := plaid.NewItemPublicTokenExchangeRequest(payload.PublicToken)

	resp, _, err := client.PlaidApi.ItemPublicTokenExchange(ctx.UserContext()).ItemPublicTokenExchangeRequest(*request).Execute()
	if err != nil {
		myLogger.Error().Err(err).Msg("error exchanging public token")
		return ctx.Status(fiber.StatusBadRequest).JSON(StatusResponse{
			Status:  400,
			Message: err.Error(),
			TraceID: traceId,
		})
	}

	itemID := resp.GetItemId()
	accessToken := resp.GetAccessToken()

	// Store the item_id and access_token in the users database
	tx, err := sql.TrxForUser(ctx.UserContext(), ctx.Locals(types.UserKey{}).(string))
	if err != nil {
		myLogger.Error().Err(err).Msg("error starting database transaction")
		return ctx.Status(fiber.StatusInternalServerError).JSON(StatusResponse{
			Status:  503, // database error
			Message: "error creating database connection for user",
			TraceID: traceId,
		})
	}

	for _, account := range payload.Accounts {
		credentials, err := json.Marshal(struct {
			ItemID      string `json:"item_id"`
			AccessToken string `json:"access_token"`
		}{
			ItemID:      itemID,
			AccessToken: accessToken,
		})
		if err != nil {
			myLogger.Error().Err(err).Msg("error marshalling credentials")
			if err := tx.Rollback(ctx.UserContext()); err != nil {
				myLogger.Error().Err(err).Msg("error rolling back transaction")
			}

			return ctx.Status(fiber.StatusInternalServerError).JSON(StatusResponse{
				Status:  503, // database error
				Message: "error marshalling credentials",
				TraceID: traceId,
			})
		}

		accountType := "Unknown"
		switch account.Type {
		case "depository":
			accountType = "Banking"
		default:
			log.Error().Str("PlaidAccountType", account.Type).Str("PlaidAccountSubType", account.Subtype).Msg("unknown account type")
			accountType = "Unknown"
		}

		_, err = tx.Exec(ctx.UserContext(), `INSERT INTO accounts (
					"reference_id",
					"user_id",
					"credentials",
					"name",
					"account_type",
					"institution")
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT ON CONSTRAINT accounts_user_id_reference_id_key
				DO UPDATE SET
					reference_id=$1,
					user_id=$2,
					credentials=$3,
					name=$4,
					account_type=$5,
					institution=$6`,
			account.ID, ctx.Locals(types.UserKey{}), credentials, account.Name,
			accountType, payload.Institution.Name)

		if err != nil {
			myLogger.Error().Err(err).Msg("error inserting account into database")
			if err := tx.Rollback(ctx.UserContext()); err != nil {
				myLogger.Error().Err(err).Msg("error rolling back transaction")
			}

			return ctx.Status(fiber.StatusInternalServerError).JSON(StatusResponse{
				Status:  503, // database error
				Message: "error inserting account into database",
				TraceID: traceId,
			})
		}
	}

	err = tx.Commit(ctx.UserContext())
	if err != nil {
		myLogger.Error().Err(err).Msg("error committing transaction when creating account")
		return ctx.Status(fiber.StatusCreated).JSON(StatusResponse{
			Status:  503,
			Message: "database transaction commit failed",
			TraceID: traceId,
		})
	}

	return ctx.Status(fiber.StatusCreated).JSON(StatusResponse{
		Status:  201,
		Message: "success",
		TraceID: traceId,
	})
}

// PlaidLinkToken interacts with the Plaid API to generate a new link token.
// For further details see: https://plaid.com/docs/api/link/#linktokencreate
func PlaidLinkToken(ctx *fiber.Ctx) error {
	traceId := ctx.Locals(types.TraceIdKey{}).(string)
	myLogger := log.With().Str("TraceID", traceId).Logger()

	client := plaid.NewAPIClient(plaidConfiguration)

	// NOTE: If penny vault ever supports non-us / non-english users this
	// will need to change to use a value from the users jwt
	request := plaid.NewLinkTokenCreateRequest(
		"Penny Vault",
		"en",
		[]plaid.CountryCode{plaid.COUNTRYCODE_US},
		plaid.LinkTokenCreateRequestUser{
			ClientUserId: ctx.Locals(types.UserKey{}).(string),
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
		myLogger.Error().Err(err).Msg("error getting link token")
		return ctx.Status(fiber.StatusBadRequest).JSON(StatusResponse{
			Status:  400,
			Message: err.Error(),
			TraceID: traceId,
		})
	}

	linkToken := resp.GetLinkToken()

	return ctx.Status(fiber.StatusCreated).JSON(struct {
		Status int    `json:"status"`
		Token  string `json:"token"`
	}{
		Status: 201,
		Token:  linkToken,
	})
}
