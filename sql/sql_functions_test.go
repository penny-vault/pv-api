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
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rs/zerolog/log"

	"github.com/jackc/pgx/v5"
	"github.com/penny-vault/pv-api/sql"
)

type updateCmd struct {
	Id          string `json:"id"`
	SequenceNum int    `json:"sequence_num"`
	TxDate      string `json:"tx_date"`
}

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
				sequenceUpdateCmds := make([]updateCmd, 4)
				seq := []int{4, 3, 2, 1}

				for idx, id := range trxIds {
					sequenceUpdateCmds[idx] = updateCmd{
						Id:          id,
						SequenceNum: seq[idx],
						TxDate:      trxDate,
					}
				}

				seqCmdJson, err := json.Marshal(sequenceUpdateCmds)
				Expect(err).To(BeNil())

				log.Debug().Str("UpdateCommand", string(seqCmdJson)).Send()

				rows, err := dbConn.Query(ctx, `SELECT update_transaction_seq_nums($1, $2)`, string(seqCmdJson), accountId)
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

	Context("having multiple transactions on multiple dates", func() {
		var (
			accountId int
			amounts   []float64
			balance   float64
			dates     []string
			trxIds    []string
		)

		BeforeEach(func() {
			accountId = 0
			payee := "payee"
			balance = 0.0

			dates = []string{"2024-09-27", "2024-09-27", "2024-09-27",
				"2024-09-28", "2024-09-28", "2024-09-28", "2024-09-29",
				"2024-09-29", "2024-09-29"}
			amounts = []float64{25, 40, 70, 35, 22, 15, 12, 90, 101}
			trxIds = make([]string, 9)

			_, err := dbConn.Exec(ctx, `INSERT INTO accounts ("id", "name", "account_type") VALUES ($1, 'account', 'bank');`, accountId)
			Expect(err).To(BeNil())

			for idx, seq := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9} {
				trxId, err := uuid.NewV7()
				Expect(err).To(BeNil())

				trxIds[idx] = trxId.String()

				amount := amounts[idx]
				balance += amount

				_, err = dbConn.Exec(ctx, `INSERT INTO transactions ("id", "account_id", "sequence_num", "tx_date", "payee", "amount", "balance") VALUES ($1, $2, $3, $4, $5, $6, $7)`, trxId.String(), accountId, seq, dates[idx], payee, amount, balance)
				log.Debug().Str("txId", trxIds[idx]).Int("sequence", seq).Float64("amount", amount).Float64("balance", balance).Send()
				Expect(err).To(BeNil())
			}
		})

		When("all sequence numbers are shuffled", func() {
			It("should update the balance", func() {
				// sequence shuffle:
				// date = 2024-09-29, seq = 9, new seq = 1, amount = 101, balance = 308
				// date = 2024-09-29, seq = 8, new seq = 2, amount = 90, balance = 398
				// date = 2024-09-29, seq = 7, new seq = 3, amount = 12, balance = 410
				// date = 2024-09-28, seq = 6, new seq = 4, amount = 15, balance = 150
				// date = 2024-09-28, seq = 5, new seq = 5, amount = 22, balance = 172
				// date = 2024-09-28, seq = 4, new seq = 6, amount = 35, balance = 207
				// date = 2024-09-27, seq = 3, new seq = 7, amount = 70, balance = 70
				// date = 2024-09-27, seq = 2, new seq = 8, amount = 40, balance = 110
				// date = 2024-09-27, seq = 1, new seq = 9, amount = 25, balance = 135

				expectedSequenceNums := []int{9, 8, 7, 6, 5, 4, 3, 2, 1}
				sequenceUpdateCmds := make([]updateCmd, 9)

				for idx, id := range trxIds {
					sequenceUpdateCmds[idx] = updateCmd{
						Id:          id,
						SequenceNum: expectedSequenceNums[idx],
						TxDate:      dates[idx],
					}
				}

				seqUpdateJson, err := json.Marshal(sequenceUpdateCmds)
				Expect(err).To(BeNil())

				log.Debug().Str("UpdateCommand", string(seqUpdateJson)).Send()

				rows, err := dbConn.Query(ctx, `SELECT update_transaction_seq_nums($1, $2)`, string(seqUpdateJson), accountId)
				Expect(err).To(BeNil())

				rows.Close()
				Expect(rows.Err()).To(BeNil())

				// check that balance is still correct
				var queryAmount float64
				var queryBalance float64
				var sequenceNum int

				expectedBalances := []float64{135, 110, 70, 207, 172, 150, 410, 398, 308}

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
				// sequence shuffle:
				// date = 2024-09-27, seq = 1, new seq = 9, amount = 25, balance = 135
				// date = 2024-09-27, seq = 2, new seq = 2, amount = 40, balance = 40
				// date = 2024-09-27, seq = 3, new seq = 3, amount = 70, balance = 110
				// date = 2024-09-28, seq = 4, new seq = 4, amount = 35, balance = 170
				// date = 2024-09-28, seq = 5, new seq = 5, amount = 22, balance = 192
				// date = 2024-09-28, seq = 6, new seq = 6, amount = 15, balance = 207
				// date = 2024-09-29, seq = 7, new seq = 7, amount = 12, balance = 320
				// date = 2024-09-29, seq = 8, new seq = 8, amount = 90, balance = 410
				// date = 2024-09-29, seq = 9, new seq = 1, amount = 101, balance = 308

				expectedBalances := []float64{135, 40, 110, 170, 192, 207, 320, 410, 308}
				expectedSequenceNums := []int{9, 2, 3, 4, 5, 6, 7, 8, 1}

				seqUpdates := []updateCmd{
					{
						Id:          trxIds[0],
						SequenceNum: 9,
						TxDate:      dates[0],
					},
					{
						Id:          trxIds[8],
						SequenceNum: 1,
						TxDate:      dates[8],
					},
				}

				sequenceUpdateCmdJson, err := json.Marshal(seqUpdates)
				Expect(err).To(BeNil())

				sequenceUpdateCmd := string(sequenceUpdateCmdJson)
				log.Debug().Str("UpdateCommand", sequenceUpdateCmd).Send()

				rows, err := dbConn.Query(ctx, `SELECT update_transaction_seq_nums($1, $2)`, sequenceUpdateCmd, accountId)
				Expect(err).To(BeNil())

				rows.Close()
				Expect(rows.Err()).To(BeNil())

				// check that balance is still correct
				var queryAmount float64
				var queryBalance float64
				var sequenceNum int

				for idx, trxId := range trxIds {
					row := dbConn.QueryRow(ctx, `SELECT amount::numeric, balance::numeric, sequence_num FROM transactions WHERE id=$1`, trxId)
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
