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

package sql_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rs/zerolog/log"
	"golang.org/x/exp/rand"

	"github.com/jackc/pgx/v5"
	"github.com/penny-vault/pv-api/sql"
)

// generateRandomName returns a string suitable to use in postgresql
func generateRandomName(n int) string {
	r := rand.New(rand.NewSource(uint64(time.Now().UnixNano())))

	letters := []rune("abcdefghijklmnopqrstuvwxyz")
	lettersAndNumbers := []rune("abcdefghijklmnopqrstuvwxyz0123456789")

	s := make([]rune, n)
	s[0] = letters[r.Intn(len(letters))]
	for ii := range n - 1 {
		s[ii+1] = lettersAndNumbers[r.Intn(len(lettersAndNumbers))]
	}

	return string(s)
}

var _ = Describe("Database Migrations", func() {
	var (
		ctx      context.Context
		dbSchema *sql.DatabaseSchema
		dbConn   *pgx.Conn
		dbName   string
	)

	BeforeEach(func() {
		var err error

		ctx = context.Background()

		pgxConnStr := fmt.Sprintf("postgres://%s@%s:%s/%s", dbUser, dbHost, dbPort, adminDbName)
		log.Debug().Str("DbConnStr", pgxConnStr).Send()

		dbConn, err = pgx.Connect(ctx, pgxConnStr)
		Expect(err).To(BeNil())

		dbName = generateRandomName(12)

		log.Debug().Str("DbName", dbName).Msg("creating temp database")

		_, err = dbConn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s;", dbName))
		Expect(err).To(BeNil())

		// migrate database to latest version
		golangMigrateConnStr := fmt.Sprintf("pgx5://%s@%s:%s/%s", dbUser, dbHost, dbPort, dbName)
		dbSchema = sql.NewDatabaseSchema(golangMigrateConnStr)
	})

	AfterEach(func() {
		dbSchema.Close()

		log.Debug().Str("DbName", dbName).Msg("dropping temp database")
		_, err := dbConn.Exec(ctx, fmt.Sprintf("DROP DATABASE %s;", dbName))
		Expect(err).To(BeNil())

		dbConn.Close(ctx)
	})

	When("Database exists and contains no schema", func() {
		It("should migrate up without error", func() {
			err := dbSchema.Up()
			Expect(err).To(BeNil())
		})

		It("should have a version greater than 0", func() {
			err := dbSchema.Up()
			Expect(err).To(BeNil())

			version, _, err := dbSchema.Version()
			Expect(err).To(BeNil())
			Expect(version).To(BeNumerically(">=", 1))
		})

		It("should not be dirty", func() {
			err := dbSchema.Up()
			Expect(err).To(BeNil())

			_, dirty, err := dbSchema.Version()
			Expect(err).To(BeNil())
			Expect(dirty).To(BeFalse())
		})
	})
})
