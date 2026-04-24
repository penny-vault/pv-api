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
	"sort"
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
	defer func() { _ = rows.Close() }()

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

// ledgerRow tracks a replayed transaction position for HoldingsAsOf.
type ledgerRow struct {
	figi      string
	quantity  float64
	totalCost float64
	lastPrice float64
}

// HoldingsAsOf replays transactions up to (and including) date to reconstruct
// the per-ticker position. The returned values use lastTradeValue
// (qty × last trade price from the replay), not a mark-to-market total.
func (r *Reader) HoldingsAsOf(ctx context.Context, date time.Time) (*openapi.HoldingsAsOfResponse, error) {
	startDate, endDate, err := r.readDateWindow(ctx)
	if err != nil {
		return nil, err
	}
	if date.Before(startDate) || date.After(endDate) {
		return nil, ErrNotFound
	}

	ledger, err := r.ledgerAsOf(ctx, date)
	if err != nil {
		return nil, err
	}
	return ledgerToAsOfResponse(date, ledger), nil
}

// ledgerAsOf replays transactions in date order up to and including date and
// returns the resulting per-ticker ledger.
func (r *Reader) ledgerAsOf(ctx context.Context, date time.Time) (map[string]*ledgerRow, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT ticker, figi, type, quantity, price FROM transactions
		  WHERE date <= ? ORDER BY date, rowid`,
		date.Format(dateLayout))
	if err != nil {
		return nil, fmt.Errorf("ledger asof query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	ledger := map[string]*ledgerRow{}
	for rows.Next() {
		if err := replayTxIntoLedger(rows, ledger); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger asof iterate: %w", err)
	}
	return ledger, nil
}

// replayTxIntoLedger scans one transaction row and updates the ledger.
func replayTxIntoLedger(rows interface {
	Scan(...any) error
}, ledger map[string]*ledgerRow) error {
	var (
		ticker, typeStr string
		figi            sql.NullString
		quantity, price float64
	)
	if err := rows.Scan(&ticker, &figi, &typeStr, &quantity, &price); err != nil {
		return fmt.Errorf("holdings asof scan: %w", err)
	}
	if ticker == "" {
		return nil
	}
	pos := ledger[ticker]
	if pos == nil {
		pos = &ledgerRow{figi: figi.String}
		ledger[ticker] = pos
	}
	if price > 0 {
		pos.lastPrice = price
	}
	applyTxToLedger(pos, typeStr, quantity, price)
	return nil
}

// applyTxToLedger updates one ledger position given a transaction type.
func applyTxToLedger(pos *ledgerRow, typeStr string, quantity, price float64) {
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

// ledgerToAsOfResponse converts a ledger map into a HoldingsAsOfResponse.
// Totals use lastTradeValue (qty × last trade price from the replay).
func ledgerToAsOfResponse(date time.Time, ledger map[string]*ledgerRow) *openapi.HoldingsAsOfResponse {
	out := &openapi.HoldingsAsOfResponse{
		Date:  types.Date{Time: date},
		Items: []openapi.HistoricalHolding{},
	}
	tickers := make([]string, 0, len(ledger))
	for t := range ledger {
		tickers = append(tickers, t)
	}
	sort.Strings(tickers)
	for _, ticker := range tickers {
		pos := ledger[ticker]
		if pos.quantity <= 0 {
			continue
		}
		ltv := pos.quantity * pos.lastPrice
		h := openapi.HistoricalHolding{
			Ticker:         ticker,
			Quantity:       pos.quantity,
			AvgCost:        pos.totalCost / pos.quantity,
			LastTradeValue: ltv,
		}
		if pos.figi != "" {
			figi := pos.figi
			h.Figi = &figi
		}
		out.Items = append(out.Items, h)
		out.TotalLastTradeValue += ltv
	}
	return out
}

// HoldingsHistory emits one entry per rebalance batch. For each batch we
// replay transactions by trade date (not batch_id) up to and including the
// batch's date, so batch_id=0 rows — pvbt's dividends and end-of-backtest
// liquidations — don't leak into earlier batches.
func (r *Reader) HoldingsHistory(ctx context.Context, from, to *time.Time) (*openapi.HoldingsHistoryResponse, error) {
	batchQ := `SELECT batch_id, timestamp FROM batches`
	var (
		where []string
		args  []any
	)
	if from != nil {
		where = append(where, "timestamp >= ?")
		args = append(args, from.UnixNano())
	}
	if to != nil {
		endOfTo := to.Add(24*time.Hour - time.Second)
		where = append(where, "timestamp <= ?")
		args = append(args, endOfTo.UnixNano())
	}
	if len(where) > 0 {
		batchQ += " WHERE " + strings.Join(where, " AND ") //nolint:gosec // G202: where clauses use only "timestamp >= ?" and "timestamp <= ?", no user input
	}
	batchQ += " ORDER BY batch_id"

	rows, err := r.db.QueryContext(ctx, batchQ, args...)
	if err != nil {
		return nil, fmt.Errorf("holdings history batches: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var batches []batchKey
	for rows.Next() {
		var b batchKey
		var tsNanos int64
		if err := rows.Scan(&b.id, &tsNanos); err != nil {
			return nil, fmt.Errorf("holdings history scan: %w", err)
		}
		b.ts = time.Unix(0, tsNanos).UTC()
		batches = append(batches, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := &openapi.HoldingsHistoryResponse{Items: []openapi.HoldingsHistoryEntry{}}
	for _, b := range batches {
		entry, err := r.holdingsHistoryBatch(ctx, b.id, b.ts)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, entry)
	}
	return out, nil
}

type batchKey struct {
	id int64
	ts time.Time
}

// holdingsHistoryBatch builds one HoldingsHistoryEntry for the given batch by
// replaying transactions (date-ordered) up to and including the batch's date.
// Item values use lastTradeValue (qty × last trade price from the replay);
// portfolioValue is the authoritative mark-to-market total from perf_data.
func (r *Reader) holdingsHistoryBatch(ctx context.Context, batchID int64, ts time.Time) (openapi.HoldingsHistoryEntry, error) {
	entry := openapi.HoldingsHistoryEntry{
		BatchId:   batchID,
		Timestamp: ts,
		Items:     []openapi.HistoricalHolding{},
	}

	ledger, err := r.ledgerAsOf(ctx, ts)
	if err != nil {
		return entry, fmt.Errorf("holdings history batch %d: %w", batchID, err)
	}

	tickers := make([]string, 0, len(ledger))
	for t := range ledger {
		tickers = append(tickers, t)
	}
	sort.Strings(tickers)
	for _, ticker := range tickers {
		pos := ledger[ticker]
		if pos.quantity <= 0 {
			continue
		}
		h := openapi.HistoricalHolding{
			Ticker:         ticker,
			Quantity:       pos.quantity,
			AvgCost:        pos.totalCost / pos.quantity,
			LastTradeValue: pos.quantity * pos.lastPrice,
		}
		if pos.figi != "" {
			figi := pos.figi
			h.Figi = &figi
		}
		entry.Items = append(entry.Items, h)
	}

	pv, err := r.readPortfolioValueOn(ctx, ts)
	if err != nil {
		return entry, fmt.Errorf("holdings history batch %d: %w", batchID, err)
	}
	entry.PortfolioValue = pv

	ann, err := r.readBatchAnnotations(ctx, batchID)
	if err != nil {
		return entry, err
	}
	if len(ann) > 0 {
		entry.Annotations = &ann
	}
	return entry, nil
}

// readPortfolioValueOn returns the perf_data portfolio equity on the given
// date, or nil if no row exists (e.g. batch timestamp lands on a non-trading
// day). Accepts both the legacy 'portfolio_value' and pvbt 'PortfolioEquity'
// metric names.
func (r *Reader) readPortfolioValueOn(ctx context.Context, ts time.Time) (*float64, error) {
	var v float64
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data
		  WHERE metric IN ('portfolio_value','PortfolioEquity')
		    AND date = ? LIMIT 1`,
		ts.Format(dateLayout)).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read portfolio value: %w", err)
	}
	return &v, nil
}

// readBatchAnnotations returns the annotations map for the given batch ID.
func (r *Reader) readBatchAnnotations(ctx context.Context, batchID int64) (map[string]string, error) {
	aRows, err := r.db.QueryContext(ctx,
		`SELECT key, value FROM annotations WHERE batch_id = ? ORDER BY key`, batchID)
	if err != nil {
		return nil, fmt.Errorf("holdings history annotations %d: %w", batchID, err)
	}
	defer func() { _ = aRows.Close() }()
	ann := map[string]string{}
	for aRows.Next() {
		var k, v string
		if err := aRows.Scan(&k, &v); err != nil {
			return nil, err
		}
		ann[k] = v
	}
	if err := aRows.Err(); err != nil {
		return nil, fmt.Errorf("holdings history annotations iterate: %w", err)
	}
	return ann, nil
}

func (r *Reader) readEndDate(ctx context.Context) (time.Time, error) {
	var s string
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM metadata WHERE key IN ('end_date','run.end') ORDER BY key LIMIT 1`).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read end_date: %w", err)
	}
	return time.Parse(dateLayout, s)
}

func (r *Reader) readDateWindow(ctx context.Context) (time.Time, time.Time, error) {
	// pvbt binaries write run.start/run.end; older fixtures use start_date/end_date.
	rows, err := r.db.QueryContext(ctx,
		`SELECT key, value FROM metadata WHERE key IN ('start_date','end_date','run.start','run.end')`)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("read window: %w", err)
	}
	defer func() { _ = rows.Close() }()
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
		if k == "start_date" || k == "run.start" {
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
