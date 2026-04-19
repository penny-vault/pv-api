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
	"strings"
	"time"

	"github.com/oapi-codegen/runtime/types"

	"github.com/penny-vault/pv-api/openapi"
)

const dateLayout = "2006-01-02"

// CurrentHoldings reads the holdings table as-is and sums totalMarketValue.
func (r *Reader) CurrentHoldings(ctx context.Context) (*openapi.HoldingsResponse, error) {
	endDate, err := r.readEndDate(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT asset_ticker, asset_figi, quantity, avg_cost, market_value FROM holdings`)
	if err != nil {
		return nil, fmt.Errorf("holdings query: %w", err)
	}
	defer rows.Close()

	var out openapi.HoldingsResponse
	out.Date = types.Date{Time: endDate}
	out.Items = []openapi.Holding{}

	var total float64
	for rows.Next() {
		var h openapi.Holding
		var figi string
		if err := rows.Scan(&h.Ticker, &figi, &h.Quantity, &h.AvgCost, &h.MarketValue); err != nil {
			return nil, fmt.Errorf("holdings scan: %w", err)
		}
		if figi != "" {
			h.Figi = &figi
		}
		total += h.MarketValue
		out.Items = append(out.Items, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("holdings iterate: %w", err)
	}
	out.TotalMarketValue = total
	return &out, nil
}

// HoldingsAsOf replays transactions up to (and including) date to reconstruct
// the per-ticker position.
func (r *Reader) HoldingsAsOf(ctx context.Context, date time.Time) (*openapi.HoldingsResponse, error) {
	startDate, endDate, err := r.readDateWindow(ctx)
	if err != nil {
		return nil, err
	}
	if date.Before(startDate) || date.After(endDate) {
		return nil, ErrNotFound
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT ticker, figi, type, quantity, price FROM transactions
		  WHERE date <= ? ORDER BY date, rowid`,
		date.Format(dateLayout))
	if err != nil {
		return nil, fmt.Errorf("holdings asof query: %w", err)
	}
	defer rows.Close()

	type ledgerRow struct {
		figi      string
		quantity  float64
		totalCost float64
		lastPrice float64
	}
	ledger := map[string]*ledgerRow{}

	for rows.Next() {
		var (
			ticker, typeStr string
			figi            sql.NullString
			quantity, price float64
		)
		if err := rows.Scan(&ticker, &figi, &typeStr, &quantity, &price); err != nil {
			return nil, fmt.Errorf("holdings asof scan: %w", err)
		}
		if ticker == "" {
			continue
		}
		pos := ledger[ticker]
		if pos == nil {
			pos = &ledgerRow{figi: figi.String}
			ledger[ticker] = pos
		}
		if price > 0 {
			pos.lastPrice = price
		}
		switch typeStr {
		case "buy":
			pos.totalCost += quantity * price
			pos.quantity += quantity
		case "sell":
			if pos.quantity > 0 {
				avg := pos.totalCost / pos.quantity
				pos.totalCost -= avg * quantity
			}
			pos.quantity -= quantity
		case "split":
			pos.quantity *= price
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("holdings asof iterate: %w", err)
	}

	out := openapi.HoldingsResponse{
		Date:  types.Date{Time: date},
		Items: []openapi.Holding{},
	}
	for ticker, pos := range ledger {
		if pos.quantity <= 0 {
			continue
		}
		avg := 0.0
		if pos.quantity > 0 {
			avg = pos.totalCost / pos.quantity
		}
		mv := pos.quantity * pos.lastPrice
		h := openapi.Holding{
			Ticker:      ticker,
			Quantity:    pos.quantity,
			AvgCost:     avg,
			MarketValue: mv,
		}
		if pos.figi != "" {
			figi := pos.figi
			h.Figi = &figi
		}
		out.Items = append(out.Items, h)
		out.TotalMarketValue += mv
	}
	return &out, nil
}

// HoldingsHistory emits one entry per batch. Uses the pvbt-recommended
// batches-join approach: SUM(CASE) over transactions grouped by
// (batch_id, ticker, figi) with HAVING quantity != 0.
func (r *Reader) HoldingsHistory(ctx context.Context, from, to *time.Time) (*openapi.HoldingsHistoryResponse, error) {
	batchQ := `SELECT batch_id, timestamp FROM batches`
	var (
		where []string
		args  []any
	)
	if from != nil {
		where = append(where, "timestamp >= ?")
		args = append(args, from.Format("2006-01-02T15:04:05Z"))
	}
	if to != nil {
		endOfTo := to.Add(24*time.Hour - time.Second)
		where = append(where, "timestamp <= ?")
		args = append(args, endOfTo.Format("2006-01-02T15:04:05Z"))
	}
	if len(where) > 0 {
		batchQ += " WHERE " + strings.Join(where, " AND ")
	}
	batchQ += " ORDER BY batch_id"

	rows, err := r.db.QueryContext(ctx, batchQ, args...)
	if err != nil {
		return nil, fmt.Errorf("holdings history batches: %w", err)
	}
	defer rows.Close()

	type batchKey struct {
		id int64
		ts time.Time
	}
	var batches []batchKey
	for rows.Next() {
		var b batchKey
		var tsStr string
		if err := rows.Scan(&b.id, &tsStr); err != nil {
			return nil, fmt.Errorf("holdings history scan: %w", err)
		}
		b.ts, _ = time.Parse(time.RFC3339, tsStr)
		batches = append(batches, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := &openapi.HoldingsHistoryResponse{Items: []openapi.HoldingsHistoryEntry{}}
	if len(batches) == 0 {
		return out, nil
	}

	for _, b := range batches {
		entry := openapi.HoldingsHistoryEntry{
			BatchId:   b.id,
			Timestamp: b.ts,
			Items:     []openapi.Holding{},
		}
		hRows, err := r.db.QueryContext(ctx, `
			SELECT ticker, figi,
			       SUM(CASE type
			           WHEN 'buy'   THEN  quantity
			           WHEN 'sell'  THEN -quantity
			           WHEN 'split' THEN  quantity
			           ELSE 0 END) AS q
			  FROM transactions
			 WHERE batch_id <= ?
			 GROUP BY ticker, figi
			HAVING q != 0
			 ORDER BY ticker
		`, b.id)
		if err != nil {
			return nil, fmt.Errorf("holdings history batch %d: %w", b.id, err)
		}
		for hRows.Next() {
			var ticker string
			var figi sql.NullString
			var q float64
			if err := hRows.Scan(&ticker, &figi, &q); err != nil {
				hRows.Close()
				return nil, fmt.Errorf("holdings history batch scan: %w", err)
			}
			h := openapi.Holding{Ticker: ticker, Quantity: q}
			if figi.Valid && figi.String != "" {
				s := figi.String
				h.Figi = &s
			}
			entry.Items = append(entry.Items, h)
		}
		hRows.Close()

		aRows, err := r.db.QueryContext(ctx,
			`SELECT key, value FROM annotations WHERE batch_id = ? ORDER BY key`, b.id)
		if err != nil {
			return nil, fmt.Errorf("holdings history annotations %d: %w", b.id, err)
		}
		ann := map[string]string{}
		for aRows.Next() {
			var k, v string
			if err := aRows.Scan(&k, &v); err != nil {
				aRows.Close()
				return nil, err
			}
			ann[k] = v
		}
		aRows.Close()
		if len(ann) > 0 {
			entry.Annotations = &ann
		}
		out.Items = append(out.Items, entry)
	}
	return out, nil
}

func (r *Reader) readEndDate(ctx context.Context) (time.Time, error) {
	var s string
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM metadata WHERE key='end_date'`).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read end_date: %w", err)
	}
	return time.Parse(dateLayout, s)
}

func (r *Reader) readDateWindow(ctx context.Context) (time.Time, time.Time, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT key, value FROM metadata WHERE key IN ('start_date','end_date')`)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("read window: %w", err)
	}
	defer rows.Close()
	var start, end time.Time
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return time.Time{}, time.Time{}, err
		}
		t, perr := time.Parse(dateLayout, v)
		if perr != nil {
			return time.Time{}, time.Time{}, perr
		}
		if k == "start_date" {
			start = t
		} else {
			end = t
		}
	}
	if start.IsZero() || end.IsZero() {
		return time.Time{}, time.Time{}, ErrNotFound
	}
	return start, end, nil
}
