// Copyright 2021-2025
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

	"github.com/jackc/pgx/v5"
	"github.com/penny-vault/pv-api/sql"
	"github.com/rs/zerolog/log"
)

func GetAccounts(ctx context.Context, userId string) ([]Account, error) {
	tx, err := sql.TrxForUser(ctx, userId)
	if err != nil {
		log.Error().Err(err).Msg("error starting database transaction")
		return nil, err
	}

	rows, err := tx.Query(ctx, "SELECT id, COALESCE(reference_id, '') AS reference_id, user_id, COALESCE(name, '') AS name, COALESCE(credentials->>'access_token', '') AS access_token, COALESCE(credentials->>'cursor', '') AS cursor, COALESCE(credentials->>'item_id', '') AS item_id FROM accounts WHERE user_id = $1", userId)
	if err != nil {
		log.Error().Err(err).Msg("database error when retrieving accounts")
		if err := tx.Rollback(ctx); err != nil {
			log.Error().Err(err).Msg("error rolling back transaction")
		}
		return nil, err
	}

	accounts, err := pgx.CollectRows(rows, pgx.RowToStructByName[Account])
	if err != nil {
		log.Error().Err(err).Msg("database error when retrieving accounts")
		if err := tx.Rollback(ctx); err != nil {
			log.Error().Err(err).Msg("error rolling back transaction")
		}
		return nil, err
	}

	return accounts, nil
}
