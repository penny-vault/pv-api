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
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oapi-codegen/runtime/types"

	"github.com/penny-vault/pv-api/openapi"
)

// Prediction reads the next-trade prediction written by pvbt v0.12.0+
// (schema 6): the prediction, predicted_transactions, and predicted_holdings
// tables. Returns ErrNotFound when the snapshot predates schema 6 or
// recorded no prediction. An empty transactions list with a populated
// prediction row is valid and means the strategy would not trade.
func (r *Reader) Prediction(ctx context.Context) (*openapi.PredictionResponse, error) {
	var tables int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'prediction'`).Scan(&tables)
	if err != nil {
		return nil, fmt.Errorf("prediction table lookup: %w", err)
	}
	if tables == 0 {
		return nil, ErrNotFound
	}

	var dateStr string
	err = r.db.QueryRowContext(ctx, `SELECT date FROM prediction LIMIT 1`).Scan(&dateStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("prediction query: %w", err)
	}
	date, err := time.Parse(dateLayout, dateStr)
	if err != nil {
		return nil, fmt.Errorf("prediction parse date %q: %w", dateStr, err)
	}

	out := &openapi.PredictionResponse{
		Date:         types.Date{Time: date},
		Transactions: []openapi.PredictedTransaction{},
		Holdings:     []openapi.PredictedHolding{},
	}

	if err := r.readPredictedTransactions(ctx, out); err != nil {
		return nil, err
	}
	if err := r.readPredictedHoldings(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Reader) readPredictedTransactions(ctx context.Context, out *openapi.PredictionResponse) error {
	rows, err := r.db.QueryContext(ctx,
		`SELECT type, ticker, figi, quantity, price, amount, justification
		   FROM predicted_transactions ORDER BY rowid`)
	if err != nil {
		return fmt.Errorf("predicted transactions query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var t openapi.PredictedTransaction
		if err := rows.Scan(&t.Type, &t.Ticker, &t.Figi, &t.Quantity, &t.Price, &t.Amount, &t.Justification); err != nil {
			return fmt.Errorf("predicted transactions scan: %w", err)
		}
		out.Transactions = append(out.Transactions, t)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("predicted transactions iterate: %w", err)
	}
	return nil
}

func (r *Reader) readPredictedHoldings(ctx context.Context, out *openapi.PredictionResponse) error {
	rows, err := r.db.QueryContext(ctx,
		`SELECT asset_ticker, asset_figi, quantity, market_value
		   FROM predicted_holdings ORDER BY asset_ticker, asset_figi`)
	if err != nil {
		return fmt.Errorf("predicted holdings query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var total float64
	for rows.Next() {
		var h openapi.PredictedHolding
		var figi string
		if err := rows.Scan(&h.Ticker, &figi, &h.Quantity, &h.MarketValue); err != nil {
			return fmt.Errorf("predicted holdings scan: %w", err)
		}
		if figi != "" {
			h.Figi = &figi
		}
		total += h.MarketValue
		out.Holdings = append(out.Holdings, h)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("predicted holdings iterate: %w", err)
	}

	out.TotalMarketValue = total
	if total > 0 {
		for i := range out.Holdings {
			out.Holdings[i].Weight = out.Holdings[i].MarketValue / total
		}
	}
	return nil
}
