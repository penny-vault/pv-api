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

package sql

import (
	"embed"
	"errors"

	"github.com/golang-migrate/migrate/v4"

	// import pgx v5 driver
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"

	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/rs/zerolog/log"
)

//go:embed migrations/*.sql
var fs embed.FS

type DatabaseSchema struct {
	migration *migrate.Migrate
}

// NewDatabaseSchema creates a new migration source from the embedded file
// system and connects to the database. dbConnStr is the connection string to
// connect to the database. Should be of the form:
//
// pgx5://user:password@host:port/dbname?query
func NewDatabaseSchema(dbConnStr string) *DatabaseSchema {
	migrations, err := iofs.New(fs, "migrations")
	if err != nil {
		log.Error().Err(err).Msg("creating migrations embedded filesystem failed")
		return nil
	}

	m, err := migrate.NewWithSourceInstance("iofs", migrations, dbConnStr)
	if err != nil {
		log.Error().Err(err).Msg("could not create go-migrate instance")
		return nil
	}

	return &DatabaseSchema{
		migration: m,
	}
}

// Up applies all migrations between the current version of the database and the
// version defined the migrations source
func (db *DatabaseSchema) Up() error {
	err := db.migration.Up()
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		log.Error().Err(err).Msg("database migration failed -- manual intervention is necessary")
		return err
	}

	return nil
}

// Down applies all down migrations between the current version of the database
// and the version defined the migrations source
func (db *DatabaseSchema) Down() error {
	err := db.migration.Down()
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		log.Error().Err(err).Msg("database migration failed -- manual intervention is necessary")
		return err
	}

	return nil
}

// Version returns the current version of the database
func (db *DatabaseSchema) Version() (uint, bool, error) {
	version, dirty, err := db.migration.Version()
	if err != nil {
		log.Error().Err(err).Msg("could not retrieve database schema version")
		return 0, true, err
	}

	return version, dirty, err
}

// Close releases any resources used by the database connection and disconnects
// from the database. It potentially returns two errors; one for failure to
// close the schema source and one for failures related to closing the
// database connection
func (db *DatabaseSchema) Close() (error, error) {
	sourceErr, dbErr := db.migration.Close()
	if sourceErr != nil {
		log.Error().Err(sourceErr).Msg("closing migration source failed")
	}

	if dbErr != nil {
		log.Error().Err(dbErr).Msg("closing migration database failed")
	}

	return sourceErr, dbErr
}
