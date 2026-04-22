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

package sql

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

var (
	once sync.Once
	pool *pgxpool.Pool
)

// Instance returns a process-wide singleton pgxpool.Pool. First call creates
// the pool, pings the server, and runs all pending migrations. The url
// argument is only used on the first call; subsequent calls return the
// existing pool.
func Instance(ctx context.Context, url string) *pgxpool.Pool {
	once.Do(func() {
		var err error
		pool, err = pgxpool.New(ctx, url)
		if err != nil {
			log.Panic().Err(err).Msg("could not create postgresql pool")
		}

		if err = pool.Ping(ctx); err != nil {
			log.Panic().Stack().Err(err).Msg("could not ping database server")
		}

		migrate := NewDatabaseSchema(CreateConnStrFromPool(pool))
		if err := migrate.Up(); err != nil {
			log.Panic().Err(err).Msg("could not migrate database")
		}

		log.Info().
			Str("Database", pool.Config().ConnConfig.Database).
			Str("DbHost", pool.Config().ConnConfig.Host).
			Msg("connected to database")
	})

	return pool
}

// Acquire returns a connection from the pool.
func Acquire(ctx context.Context) *pgxpool.Conn {
	conn, err := Instance(ctx, "").Acquire(ctx)
	if err != nil {
		log.Panic().Err(err).Msg("could not acquire postgresql connection")
	}

	return conn
}
