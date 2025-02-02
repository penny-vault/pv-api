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
	"context"
	"encoding/json"
	"fmt"
	"time"

	_ "embed"

	"github.com/gofiber/fiber/v2"
	"github.com/pelletier/go-toml/v2"
	"github.com/penny-vault/pv-api/sql"
	"github.com/penny-vault/pv-api/types"
	"github.com/plaid/plaid-go/v31/plaid"
	"github.com/rs/zerolog/log"
)

//go:embed categories.toml
var categoriesTOML []byte
var categories []Category
var categoryMapper map[string]Category

func init() {
	categories = make([]Category, 0)
	categoryMapper = make(map[string]Category)

	err := toml.Unmarshal(categoriesTOML, &categories)
	if err != nil {
		log.Fatal().Err(err).Msg("could not load standard categories")
	}

	for _, category := range categories {
		categoryMapper[category.Secondary] = category
	}
}

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

// PlaidSync interacts with the Plaid API to retrieve the latest transactions for an item
func PlaidSync(ctx *fiber.Ctx) error {
	traceId := ctx.Locals(types.TraceIdKey{}).(string)
	myLogger := log.With().Str("TraceID", traceId).Logger()

	// Get the account ID map for the user
	accounts, err := GetAccounts(ctx.UserContext(), ctx.Locals(types.UserKey{}).(string))
	if err != nil {
		myLogger.Error().Err(err).Msg("error getting account id map")
		return ctx.Status(fiber.StatusInternalServerError).JSON(StatusResponse{
			Status:  503,
			Message: "error getting account id map",
			TraceID: traceId,
		})
	}

	accountIdMap := make(map[string]int64, len(accounts))
	for _, account := range accounts {
		accountIdMap[account.ReferenceID] = account.ID
	}

	// sync each account with a unique item id
	completedItems := make(map[string]bool)
	for _, account := range accounts {
		if account.AccessToken == "" {
			continue
		}

		if _, ok := completedItems[account.ItemID]; !ok {
			completedItems[account.ItemID] = true

			// Sync transactions
			added, modified, removed, cursor, err := SyncTransactions(ctx.UserContext(), account.AccessToken, accountIdMap)
			if err != nil {
				myLogger.Error().Err(err).Msg("error syncing transactions")
				return ctx.Status(fiber.StatusInternalServerError).JSON(StatusResponse{
					Status:  503,
					Message: "error syncing transactions",
					TraceID: traceId,
				})
			}

			err = UpdateDBWithTransactionsFromPlaid(ctx.UserContext(), account, added, modified, removed, cursor)
			if err != nil {
				myLogger.Error().Err(err).Msg("error updating database with transactions")
				return ctx.Status(fiber.StatusInternalServerError).JSON(StatusResponse{
					Status:  503,
					Message: "error updating database with transactions",
					TraceID: traceId,
				})
			}
		}
	}

	return ctx.Status(fiber.StatusOK).JSON(StatusResponse{
		Status:  201,
		Message: "success",
		TraceID: traceId,
	})
}

// UpdateDBWithTransactionsFromPlaid updates the database with the transactions from Plaid
func UpdateDBWithTransactionsFromPlaid(ctx context.Context, account Account, added []*Transaction, modified []*Transaction, removed []plaid.RemovedTransaction, cursor string) error {
	dbTransaction, err := sql.TrxForUser(ctx, account.UserID)
	if err != nil {
		log.Error().Err(err).Msg("error starting database transaction")
		return err
	}

	// save the cursor to all accounts with the same item id
	/*
		_, err = dbTransaction.Exec(ctx, `UPDATE accounts SET credentials = credentials || jsonb_build_object('cursor', $1) WHERE credentials->>item_id = $2`, cursor, account.ItemID)
		if err != nil {
			log.Error().Err(err).Msg("error updating cursor in database")
			return err
		}
	*/

	// insert new transactions
	for _, trx := range added {
		// Use following pg/sql function to insert a new transaction
		// 	CREATE OR REPLACE FUNCTION insert_transaction(
		//   in_id              UUID, -- Use NULL to insert a new transaction
		//   in_account_id      BIGINT,
		//   in_source          TEXT,
		//   in_source_id       TEXT,
		//   in_sequence_num    BIGINT,
		//   in_tx_date         DATE,
		//   in_payee           TEXT,
		//   in_category        JSONB,
		//   in_tags            TEXT[],
		//   in_justification   JSONB,
		//   in_reviewed        BOOLEAN,
		//   in_cleared         BOOLEAN,
		//   in_amount          MONEY,
		//   in_memo            TEXT,
		//   in_related         UUID[],
		//   in_commission      NUMERIC(9, 2),
		//   in_composite_figi  TEXT,
		//   in_num_shares      NUMERIC(15, 5),
		//   in_price_per_share NUMERIC(15, 5),
		//   in_ticker          TEXT,
		//   in_tax_treatment   tax_disposition,
		//   in_gain_loss       NUMERIC(12, 5)
		// )

		_, err = dbTransaction.Exec(ctx, `SELECT insert_transaction(
			NULL,  -- in_id
			$1,    -- in_account_id
			$2,    -- in_source
			$3,    -- in_source_id
			NULL,  -- in_sequence_num
			$4,    -- in_tx_date
			$5,    -- in_payee
			$6,    -- in_category
			NULL,  -- in_tags
			NULL,  -- in_justification
			false, -- in_reviewed
			false, -- in_cleared
			$7,    -- in_amount
			$8,    -- in_memo
			NULL,  -- in_related
			NULL,  -- in_commission
			NULL,  -- in_composite_figi
			NULL,  -- in_num_shares
			NULL,  -- in_price_per_share
			NULL,  -- in_ticker
			NULL,  -- in_tax_treatment
			NULL   -- in_gain_loss
		)`,
			trx.AccountID, // 1
			trx.Source,    // 2
			trx.SourceID,  // 3
			trx.TxDate,    // 4
			trx.Payee,     // 5
			trx.Category,  // 6
			trx.Amount,    // 7
			trx.Memo,      // 8
		)

		if err != nil {
			log.Error().Err(err).Msg("error inserting transaction into database")

			err2 := dbTransaction.Rollback(ctx)
			if err2 != nil {
				// rollback failed
				log.Fatal().Err(err2).Msg("rollback transaction failed -- must be in bad state. exiting.")
			}

			return err
		}
	}

	// remove transactions
	for _, deleteTx := range removed {
		_, err := dbTransaction.Exec(ctx, "DELETE FROM transactions WHERE reference_id = $1 AND source_id = $2", deleteTx.GetAccountId(), deleteTx.GetTransactionId())
		if err != nil {
			log.Error().Err(err).Msg("could not delete transaction from database")

			err2 := dbTransaction.Rollback(ctx)
			if err2 != nil {
				// rollback failed
				log.Fatal().Err(err2).Msg("rollback transaction failed -- must be in bad state. exiting.")
			}

			return err
		}
	}

	if err := dbTransaction.Commit(ctx); err != nil {
		log.Error().Err(err).Msg("could not commit transactions to database")

		err2 := dbTransaction.Rollback(ctx)
		if err2 != nil {
			// rollback failed
			log.Fatal().Err(err2).Msg("rollback transaction failed -- must be in bad state. exiting.")
		}

		return err
	}

	return nil
}

// SyncTransactions interacts with the Plaid API to retrieve the latest transactions for an item
func SyncTransactions(ctx context.Context, accessToken string, accountIdMap map[string]int64, cursor ...string) ([]*Transaction, []*Transaction, []plaid.RemovedTransaction, string, error) {
	var added []plaid.Transaction
	var modified []plaid.Transaction
	var removed []plaid.RemovedTransaction // Removed transaction ids

	hasMore := true
	includePersonalFinanceCategory := true

	client := plaid.NewAPIClient(plaidConfiguration)

	var myCursor *string = nil

	if len(cursor) > 0 {
		myCursor = &cursor[0]
	}

	// Iterate through each page of new transaction updates for item
	for hasMore {
		request := plaid.NewTransactionsSyncRequest(accessToken)
		request.SetOptions(plaid.TransactionsSyncRequestOptions{
			IncludePersonalFinanceCategory: &includePersonalFinanceCategory,
		})

		if myCursor != nil {
			request.SetCursor(*myCursor)
		}

		resp, _, err := client.PlaidApi.TransactionsSync(ctx).TransactionsSyncRequest(*request).Execute()
		if err != nil {
			// let upstream decide how to log and handle this error
			return nil, nil, nil, "", err
		}

		// Add this page of results
		added = append(added, resp.GetAdded()...)
		modified = append(modified, resp.GetModified()...)
		removed = append(removed, resp.GetRemoved()...)

		hasMore = resp.GetHasMore()

		// Update cursor to the next cursor
		nextCursor := resp.GetNextCursor()
		myCursor = &nextCursor
	}

	// convert into penny vault types
	addedTransactions := make([]*Transaction, 0, len(added))
	for _, trx := range added {
		pvTrx := convertPlaidTransactionToPV(trx)
		pvTrx.AccountID = accountIdMap[trx.GetAccountId()]
		addedTransactions = append(addedTransactions, pvTrx)
	}

	modifiedTransactions := make([]*Transaction, 0, len(modified))
	for _, trx := range modified {
		pvTrx := convertPlaidTransactionToPV(trx)
		pvTrx.AccountID = accountIdMap[trx.GetAccountId()]

		// lookup existing ID based on the source ID
		var err error
		pvTrx.ID, err = lookupIDFromSourceID(ctx, pvTrx)
		if err != nil {
			// could not find the original transaction, added it to added
			pvTrx.ID = ""
			addedTransactions = append(addedTransactions, pvTrx)
			continue
		}

		modifiedTransactions = append(modifiedTransactions, pvTrx)
	}

	return addedTransactions, modifiedTransactions, removed, *myCursor, nil
}

func lookupIDFromSourceID(ctx context.Context, trx *Transaction) (string, error) {
	dbTx, err := sql.TrxForUser(ctx, trx.UserID)
	if err != nil {
		log.Error().Err(err).Msg("could not lookup transaction ID")
		return "", err
	}

	var myID string
	dbTx.QueryRow(ctx, "SELECT id FROM transaction WHERE account_id = $1 AND source_id = $2 LIMIT 1", trx.AccountID, trx.SourceID).Scan(&myID)

	if err := dbTx.Commit(ctx); err != nil {
		log.Error().Err(err).Msg("error committing transaction")

		err2 := dbTx.Rollback(ctx)
		if err2 != nil {
			log.Panic().Msg("cannot rollback transaction")
			return "", err2
		}
	}

	return myID, nil
}

// convertPlaidTransactionToPV takes a plaid transaction and reformats it into the form necessary for penny vault
func convertPlaidTransactionToPV(trx plaid.Transaction) *Transaction {
	category := Category{
		Name: trx.GetPersonalFinanceCategory().Detailed,
	}

	// lookup the category name and convert it into a native PV category
	if nativeCategory, ok := categoryMapper[category.Name]; ok {
		category.Name = nativeCategory.Name
	} else {
		log.Warn().Str("CategoryName", category.Name).Msg("plaid returned an un-recognized category")
	}

	var date time.Time
	var err error

	if trx.GetAuthorizedDate() != "" {
		date, err = time.Parse("2006-01-02", trx.GetAuthorizedDate())
		if err != nil {
			// couldn't parse the date just move on
			date = time.Time{}
		}
	}

	if date.Equal(time.Time{}) && trx.GetDate() != "" {
		date, err = time.Parse("2006-01-02", trx.GetDate())
		if err != nil {
			// couldn't parse the date just move on
			date = time.Time{}
		}
	}

	memo := trx.GetCheckNumber()
	if memo != "" {
		memo = fmt.Sprintf("Check number: %s", memo)
	}

	plaidLoc := trx.GetLocation()
	location := Location{
		Lat:         plaidLoc.GetLat(),
		Lon:         plaidLoc.GetLon(),
		Address:     plaidLoc.GetAddress(),
		City:        plaidLoc.GetCity(),
		Country:     plaidLoc.GetCountry(),
		Region:      plaidLoc.GetRegion(),
		PostalCode:  plaidLoc.GetPostalCode(),
		StoreNumber: plaidLoc.GetStoreNumber(),
	}

	return &Transaction{
		Amount:   trx.Amount,
		TxDate:   date,
		Memo:     memo,
		Payee:    trx.GetMerchantName(),
		Location: location,
		Icon:     trx.GetLogoUrl(),
		Category: []Category{category},
		SourceID: trx.TransactionId,
		Source:   "Downloaded",
	}
}
