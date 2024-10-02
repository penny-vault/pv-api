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

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rs/zerolog/log"

	"github.com/jackc/pgx/v5"
	"github.com/penny-vault/pv-api/sql"
)

var _ = Describe("PlPgSql Functions", func() {
	var (
		ctx         context.Context
		dbSchema    *sql.DatabaseSchema
		dbConn      *pgx.Conn
		dbConnAdmin *pgx.Conn
		dbName      string
	)

	BeforeEach(func() {
		var err error

		ctx = context.Background()

		pgxConnStr := fmt.Sprintf("postgres://%s@%s:%s/%s", dbUser, dbHost, dbPort, adminDbName)
		log.Debug().Str("DbConnStr", pgxConnStr).Send()

		dbConnAdmin, err = pgx.Connect(ctx, pgxConnStr)
		Expect(err).To(BeNil())

		dbName = generateRandomName(12)

		log.Debug().Str("DbName", dbName).Msg("creating temp database")

		_, err = dbConnAdmin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s;", dbName))
		Expect(err).To(BeNil())

		// migrate database to latest version
		golangMigrateConnStr := fmt.Sprintf("pgx5://%s@%s:%s/%s", dbUser, dbHost, dbPort, dbName)
		dbSchema = sql.NewDatabaseSchema(golangMigrateConnStr)
		err = dbSchema.Up()
		Expect(err).To(BeNil())

		// Create a connection to the temp database
		pgxConnStr = fmt.Sprintf("postgres://%s@%s:%s/%s", dbUser, dbHost, dbPort, dbName)
		dbConn, err = pgx.Connect(ctx, pgxConnStr)
		Expect(err).To(BeNil())
	})

	AfterEach(func() {
		dbSchema.Close()
		dbConn.Close(ctx)

		log.Debug().Str("DbName", dbName).Msg("dropping temp database")
		_, err := dbConnAdmin.Exec(ctx, fmt.Sprintf("DROP DATABASE %s;", dbName))
		Expect(err).To(BeNil())

		dbConnAdmin.Close(ctx)
	})

	When("Transaction sequence numbers are updated in a database with a single transaction", func() {
		It("should have the same balance", func() {
			// insert test data
			trxId, err := uuid.NewV7()
			Expect(err).To(BeNil())

			accountId := 0
			trxDate := "2024-09-27"
			payee := "payee"
			amount := 500

			_, err = dbConn.Exec(ctx, `INSERT INTO accounts ("id", "name", "account_type") VALUES ($1, 'account', 'bank');`, accountId)
			Expect(err).To(BeNil())

			_, err = dbConn.Exec(ctx, `INSERT INTO transactions ("id", "account_id", "sequence_num", "tx_date", "payee", "amount", "balance") VALUES ($1, $2, $3, $4, $5, $6, $7)`, trxId.String(), accountId, 0, trxDate, payee, amount, amount)
			Expect(err).To(BeNil())

			// update sequence number
			sequenceUpdateCmd := fmt.Sprintf(`[{"id": "%s", "sequence_num": 5, "tx_date": "2024-09-27"}]`, trxId.String())
			rows, err := dbConn.Query(ctx, `SELECT update_transaction_seq_nums($1, $2)`, sequenceUpdateCmd, accountId)
			Expect(err).To(BeNil())

			rows.Close()
			Expect(rows.Err()).To(BeNil())

			// check that balance is still correct
			var queryAmount float64
			var queryBalance float64
			var sequenceNum int

			row := dbConn.QueryRow(ctx, `SELECT amount::numeric, balance::numeric, sequence_num FROM transactions WHERE id=$1`, trxId.String())
			err = row.Scan(&queryAmount, &queryBalance, &sequenceNum)
			Expect(err).To(BeNil())

			Expect(queryAmount).To(BeNumerically("~", amount))
			Expect(queryBalance).To(BeNumerically("~", amount))
			Expect(sequenceNum).To(Equal(5))
		})
	})

	Context("having multiple transactions on the same date", func() {
		var (
			accountId int
			amounts   []float64
			balance   float64
			trxDate   string
			trxIds    []string
		)

		BeforeEach(func() {
			accountId = 0
			trxDate = "2024-09-27"
			payee := "payee"
			balance = 0.0

			amounts = []float64{25, 40, 70, 35}
			trxIds = make([]string, 4)

			_, err := dbConn.Exec(ctx, `INSERT INTO accounts ("id", "name", "account_type") VALUES ($1, 'account', 'bank');`, accountId)
			Expect(err).To(BeNil())

			for idx, seq := range []int{1, 2, 3, 4} {
				trxId, err := uuid.NewV7()
				Expect(err).To(BeNil())

				trxIds[idx] = trxId.String()

				amount := amounts[idx]
				balance += amount

				_, err = dbConn.Exec(ctx, `INSERT INTO transactions ("id", "account_id", "sequence_num", "tx_date", "payee", "amount", "balance") VALUES ($1, $2, $3, $4, $5, $6, $7)`, trxId.String(), accountId, seq, trxDate, payee, amount, balance)
				log.Debug().Str("txId", trxIds[idx]).Int("sequence", seq).Float64("amount", amount).Float64("balance", balance).Send()
				Expect(err).To(BeNil())
			}
		})

		When("all sequence numbers are shuffled", func() {
			It("should update the balance", func() {
				// sequence shuffle:
				// 1 = 4 (amount = 35; balance = 35)
				// 2 = 3 (amount = 70; balance = 105)
				// 3 = 2 (amount = 40; balance = 145)
				// 4 = 1 (amount = 25; balance = 170)
				sequenceUpdateCmd := fmt.Sprintf(`[{"id": "%s", "sequence_num": 1, "tx_date": "%s"}, {"id": "%s", "sequence_num": 2, "tx_date": "%s"}, {"id": "%s", "sequence_num": 3, "tx_date": "%s"}, {"id": "%s", "sequence_num": 4, "tx_date": "%s"}]`,
					trxIds[3], trxDate, trxIds[2], trxDate, trxIds[1], trxDate, trxIds[0], trxDate)
				log.Debug().Str("UpdateCommand", sequenceUpdateCmd).Send()

				rows, err := dbConn.Query(ctx, `SELECT update_transaction_seq_nums($1, $2)`, sequenceUpdateCmd, accountId)
				Expect(err).To(BeNil())

				rows.Close()
				Expect(rows.Err()).To(BeNil())

				// check that balance is still correct
				var queryAmount float64
				var queryBalance float64
				var sequenceNum int

				expectedBalances := []float64{170, 145, 105, 35}
				expectedSequenceNums := []int{4, 3, 2, 1}

				for idx := range 4 {
					row := dbConn.QueryRow(ctx, `SELECT amount::numeric, balance::numeric, sequence_num FROM transactions WHERE id=$1`, trxIds[idx])
					err = row.Scan(&queryAmount, &queryBalance, &sequenceNum)
					Expect(err).To(BeNil())

					log.Debug().Str("id", trxIds[idx]).Float64("amount", queryAmount).Float64("balance", queryBalance).Int("seq", sequenceNum).Msg("new transaction values")

					Expect(queryAmount).To(BeNumerically("~", amounts[idx]))
					Expect(queryBalance).To(BeNumerically("~", expectedBalances[idx]))
					Expect(sequenceNum).To(Equal(expectedSequenceNums[idx]))
				}
			})
		})

		When("first sequence becomes last sequence", func() {
			It("should update the balance", func() {
				sequenceUpdateCmd := fmt.Sprintf(`[{"id": "%s", "sequence_num": 1, "tx_date": "%s"}, {"id": "%s", "sequence_num": 4, "tx_date": "%s"}]`,
					trxIds[3], trxDate, trxIds[0], trxDate)
				log.Debug().Str("UpdateCommand", sequenceUpdateCmd).Send()

				rows, err := dbConn.Query(ctx, `SELECT update_transaction_seq_nums($1, $2)`, sequenceUpdateCmd, accountId)
				Expect(err).To(BeNil())

				rows.Close()
				Expect(rows.Err()).To(BeNil())

				// check that balance is still correct
				var queryAmount float64
				var queryBalance float64
				var sequenceNum int

				expectedBalances := []float64{170, 0, 0, 35}
				expectedSequenceNums := []int{4, 0, 0, 1}

				for _, idx := range []int{0, 3} {
					row := dbConn.QueryRow(ctx, `SELECT amount::numeric, balance::numeric, sequence_num FROM transactions WHERE id=$1`, trxIds[idx])
					err = row.Scan(&queryAmount, &queryBalance, &sequenceNum)
					Expect(err).To(BeNil())

					log.Debug().Str("id", trxIds[idx]).Float64("amount", queryAmount).Float64("balance", queryBalance).Int("seq", sequenceNum).Msg("new transaction values")

					Expect(queryAmount).To(BeNumerically("~", amounts[idx]))
					Expect(queryBalance).To(BeNumerically("~", expectedBalances[idx]))
					Expect(sequenceNum).To(Equal(expectedSequenceNums[idx]))
				}
			})
		})
	})
})
