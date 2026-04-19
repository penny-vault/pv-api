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

package snapshot

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// BuildTestSnapshot creates a pvbt-shaped SQLite file at path with known
// values. Exported (non-_test) so backtest/ tests can use it as the
// fakestrat FAKESTRAT_FIXTURE source.
//
// The fixture represents a 5-day backtest with known equity curve,
// three batches (rebalance points), one BUY and one DIVIDEND transaction,
// and per-batch annotations. Note: the batches table and batch_id columns
// on transactions/annotations anticipate in-flight pvbt schema additions
// — pvapi development is coordinated with that work.
func BuildTestSnapshot(path string) error {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return fmt.Errorf("build fixture open: %w", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE perf_data (date TEXT NOT NULL, metric TEXT NOT NULL, value REAL NOT NULL)`,
		`CREATE TABLE batches (batch_id INTEGER PRIMARY KEY, timestamp TEXT NOT NULL)`,
		`CREATE TABLE transactions (batch_id INTEGER REFERENCES batches(batch_id), date TEXT, type TEXT, ticker TEXT, figi TEXT, quantity REAL, price REAL, amount REAL, qualified INTEGER, justification TEXT)`,
		`CREATE TABLE holdings (asset_ticker TEXT, asset_figi TEXT, quantity REAL, avg_cost REAL, market_value REAL)`,
		`CREATE TABLE tax_lots (asset_ticker TEXT, asset_figi TEXT, date TEXT, quantity REAL, price REAL, id TEXT DEFAULT '')`,
		`CREATE TABLE metrics (date TEXT, name TEXT, window TEXT, value REAL)`,
		`CREATE TABLE annotations (batch_id INTEGER REFERENCES batches(batch_id), timestamp INTEGER, key TEXT, value TEXT)`,

		`INSERT INTO metadata VALUES ('schema_version', '4')`,
		`INSERT INTO metadata VALUES ('start_date', '2024-01-02')`,
		`INSERT INTO metadata VALUES ('end_date', '2024-01-08')`,
		`INSERT INTO metadata VALUES ('benchmark', 'SPY')`,
		`INSERT INTO metadata VALUES ('perf_data_frequency', 'daily')`,

		`INSERT INTO perf_data VALUES ('2024-01-02', 'portfolio_value', 100000)`,
		`INSERT INTO perf_data VALUES ('2024-01-03', 'portfolio_value', 101000)`,
		`INSERT INTO perf_data VALUES ('2024-01-04', 'portfolio_value', 100500)`,
		`INSERT INTO perf_data VALUES ('2024-01-05', 'portfolio_value', 102000)`,
		`INSERT INTO perf_data VALUES ('2024-01-08', 'portfolio_value', 103000)`,
		`INSERT INTO perf_data VALUES ('2024-01-02', 'benchmark_value', 100000)`,
		`INSERT INTO perf_data VALUES ('2024-01-03', 'benchmark_value', 100500)`,
		`INSERT INTO perf_data VALUES ('2024-01-04', 'benchmark_value', 100800)`,
		`INSERT INTO perf_data VALUES ('2024-01-05', 'benchmark_value', 101500)`,
		`INSERT INTO perf_data VALUES ('2024-01-08', 'benchmark_value', 102000)`,

		`INSERT INTO batches VALUES (1, '2024-01-02T14:30:00Z')`,
		`INSERT INTO batches VALUES (2, '2024-01-05T14:30:00Z')`,
		`INSERT INTO batches VALUES (3, '2024-01-08T14:30:00Z')`,

		`INSERT INTO transactions VALUES (1, '2024-01-02', 'buy', 'VTI', 'BBG000BDTBL9', 100, 100, 10000, 0, 'initial buy')`,
		`INSERT INTO transactions VALUES (2, '2024-01-05', 'dividend', 'VTI', 'BBG000BDTBL9', 0, 0, 25.50, 1, 'qualified div')`,

		`INSERT INTO annotations VALUES (1, 1704205800, 'reason', 'initial allocation')`,
		`INSERT INTO annotations VALUES (2, 1704464200, 'reason', 'dividend payment')`,
		`INSERT INTO annotations VALUES (3, 1704722600, 'reason', 'final state')`,

		`INSERT INTO holdings VALUES ('VTI', 'BBG000BDTBL9', 100, 100, 10300)`,
		`INSERT INTO holdings VALUES ('$CASH', '', 1, 93000, 93000)`,

		`INSERT INTO metrics VALUES ('2024-01-08', 'sharpe_ratio', 'full', 1.23)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'sortino_ratio', 'full', 1.80)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'beta', 'full', 0.95)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'alpha', 'full', 0.02)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'std_dev', 'full', 0.11)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'ulcer_index', 'full', 0.50)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'tax_cost_ratio', 'full', 0.01)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'max_drawdown', 'full', -0.00495)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("build fixture: %w (SQL: %s)", err, stmt)
		}
	}
	return nil
}
