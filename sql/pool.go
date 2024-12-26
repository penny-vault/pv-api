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

package sql

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

var (
	once             sync.Once
	pool             *pgxpool.Pool
	openTransactions map[string]string
	ErrEmptyUserID   = errors.New("userID cannot be an empty string")
)

// Instance returns a singleton instance of the pgxpool.Pool connection
func Instance(ctx context.Context) *pgxpool.Pool {
	once.Do(func() {
		openTransactions = make(map[string]string)
		dbUrl := viper.GetString("db.url")

		var err error
		pool, err = pgxpool.New(ctx, dbUrl)
		if err != nil {
			log.Panic().Err(err).Msg("could not create postgresql pool")
		}

		if err = pool.Ping(ctx); err != nil {
			log.Panic().Stack().Err(err).Msg("could not ping database server")
		}

		log.Info().Str("Database", pool.Config().ConnConfig.Database).Str("DbHost", pool.Config().ConnConfig.Host).Msg("connected to database")
	})

	return pool
}

// Acquire returns a connection from the pool
func Acquire(ctx context.Context) *pgxpool.Conn {
	conn, err := Instance(ctx).Acquire(ctx)
	if err != nil {
		log.Panic().Err(err).Msg("could not acquire postgresql connection")
	}

	return conn
}

// TrxForUser creates a transaction with the appropriate user set
// NOTE: the default use is pvapi which only has enough privileges to create new roles and switch to them.
// Any kind of real work must be done with a user role which limits access to only that user
func TrxForUser(ctx context.Context, userID string) (pgx.Tx, error) {
	myPool := Instance(ctx)
	trx, err := myPool.Begin(ctx)
	if err != nil {
		return nil, err
	}

	// record transactions in openTransaction log
	_, file, lineno, ok := runtime.Caller(1)
	caller := fmt.Sprintf("[%v] %s:%d", ok, file, lineno)
	trxID := uuid.New().String()
	openTransactions[trxID] = caller

	wrappedTrx := &PvDbTx{
		id:   trxID,
		user: userID,
		tx:   trx,
	}

	subLog := log.With().Str("UserID", userID).Logger()

	// set user
	ident := pgx.Identifier{userID}
	sql := fmt.Sprintf("SET ROLE %s", ident.Sanitize())
	_, err = wrappedTrx.Exec(ctx, sql)
	if err != nil {
		// user doesn't exist -- create it
		subLog.Warn().Stack().Err(err).Msg("role does not exist")
		if err := wrappedTrx.Rollback(ctx); err != nil { // rollback completes the current transaction
			log.Error().Stack().Err(err).Msg("could not rollback transaction")
			return nil, err
		}

		err = createUser(ctx, userID)
		if err != nil {
			log.Error().Stack().Err(err).Msg("could not create user")
			return nil, err
		}

		// try again now that the user has been created
		return TrxForUser(ctx, userID)
	}

	return wrappedTrx, nil
}

func createUser(ctx context.Context, userID string) error {
	if userID == "" {
		log.Error().Stack().Msg("userID cannot be an empty string")
		return ErrEmptyUserID
	}

	subLog := log.With().Str("UserID", userID).Logger()
	subLog.Info().Msg("creating new role")

	myPool := Instance(ctx)

	trx, err := myPool.Begin(ctx)
	if err != nil {
		subLog.Error().Stack().Err(err).Msg("could not create new transaction")
		return err
	}

	// Make sure the current role is pvapi
	_, err = trx.Exec(ctx, "SET ROLE pvapi")
	if err != nil {
		subLog.Error().Stack().Err(err).Msg("could not switch to pvapi role")
		if err := trx.Rollback(ctx); err != nil {
			subLog.Error().Stack().Err(err).Msg("could not rollback transaction")
		}
		return err
	}

	// Create the role
	// NOTE: We have to do our own sanitization because postgresql can only do sanitization on
	// select, insert, update, and delete queries
	ident := pgx.Identifier{userID}
	sql := fmt.Sprintf("CREATE ROLE %s WITH nologin IN ROLE pvuser;", ident.Sanitize())
	_, err = trx.Exec(ctx, sql)
	if err != nil {
		if err := trx.Rollback(ctx); err != nil {
			subLog.Error().Stack().Err(err).Str("Query", sql).Msg("could not rollback transaction")
		}
		subLog.Error().Stack().Err(err).Str("Query", sql).Msg("failed to create role")
		return err
	}

	// Grant privileges
	// NOTE: We have to do our own sanitization because postgresql can only do sanitization on
	// select, insert, update, and delete queries
	sql = fmt.Sprintf("GRANT %s TO pvapi;", ident.Sanitize())
	_, err = trx.Exec(ctx, sql)
	if err != nil {
		if err := trx.Rollback(ctx); err != nil {
			subLog.Error().Stack().Err(err).Str("Query", sql).Msg("could not rollback transaction")
		}
		subLog.Error().Stack().Err(err).Str("Query", sql).Msg("failed to grant privileges to role")
		return err
	}

	err = trx.Commit(ctx)
	if err != nil {
		if err := trx.Rollback(ctx); err != nil {
			subLog.Error().Stack().Err(err).Str("Query", sql).Msg("could not rollback transaction")
		}
		subLog.Error().Stack().Err(err).Msg("failed to commit changes")
		return err
	}

	return nil
}

// LogOpenTransactions writes an INFO log for each open transaction
func LogOpenTransactions() {
	for k, v := range openTransactions {
		log.Info().Str("TrxId", k).Str("Caller", v).Msg("open transaction")
	}
}
