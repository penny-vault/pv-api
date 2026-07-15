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
	// Include batches with trades OR annotations; pvbt emits annotation-only
	// batches for "hold" decisions where no trades occur.
	batchQ := `SELECT b.batch_id, b.timestamp FROM batches b
		WHERE (EXISTS (SELECT 1 FROM transactions t WHERE t.batch_id = b.batch_id AND t.type IN ('buy','sell','split'))
		    OR EXISTS (SELECT 1 FROM annotations a WHERE a.batch_id = b.batch_id))`
	var args []any
	if from != nil {
		batchQ += " AND b.timestamp >= ?"
		args = append(args, from.UnixNano())
	}
	if to != nil {
		endOfTo := to.Add(24*time.Hour - time.Second)
		batchQ += " AND b.timestamp <= ?"
		args = append(args, endOfTo.UnixNano())
	}
	batchQ += " ORDER BY b.batch_id"

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
// Per-position values prefer positions_daily market values (current prices);
// portfolioValue uses perf_data when available, falling back to the
// positions_daily sum — needed for hold batches that have no perf_data row.
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

	// positions_daily holds current market values; use them to override the
	// stale lastPrice from the ledger (relevant for hold batches).
	dailyMV, err := r.readPositionsDailyOn(ctx, ts)
	if err != nil {
		return entry, fmt.Errorf("holdings history batch %d: %w", batchID, err)
	}

	entry.Items = ledgerToHistoricalHoldings(ledger, dailyMV)

	pv, err := r.readPortfolioValueOn(ctx, ts)
	if err != nil {
		return entry, fmt.Errorf("holdings history batch %d: %w", batchID, err)
	}
	if pv == nil && len(dailyMV) > 0 {
		var sum float64
		for _, mv := range dailyMV {
			sum += mv
		}
		pv = &sum
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

// ledgerToHistoricalHoldings converts a replayed ledger into sorted
// HistoricalHoldings, preferring positions_daily market values (current
// prices) over the stale ledger lastPrice. The cash sleeve is then appended
// from positions_daily: cash never flows through transactions, so without
// this the Tickers column omits cash (showing only the equity/hedge leg) and
// a 100%-cash batch yields an empty item list. Cash quantity equals its
// market value (unit price $1).
func ledgerToHistoricalHoldings(ledger map[string]*ledgerRow, dailyMV map[string]float64) []openapi.HistoricalHolding {
	items := []openapi.HistoricalHolding{}

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
		if mv, ok := dailyMV[ticker]; ok {
			ltv = mv
		}
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
		items = append(items, h)
	}

	if cash, ok := dailyMV[cashTicker]; ok && cash != 0 {
		items = append(items, openapi.HistoricalHolding{
			Ticker:         cashTicker,
			Quantity:       cash,
			AvgCost:        1,
			LastTradeValue: cash,
		})
	}

	return items
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

// readPositionsDailyOn returns ticker→market_value from positions_daily for
// the given date. Returns an empty map (not an error) when no rows exist.
func (r *Reader) readPositionsDailyOn(ctx context.Context, ts time.Time) (map[string]float64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT ticker, market_value FROM positions_daily WHERE date = ?`,
		ts.Format(dateLayout))
	if err != nil {
		return nil, fmt.Errorf("positions daily on %s: %w", ts.Format(dateLayout), err)
	}
	defer func() { _ = rows.Close() }()
	mv := map[string]float64{}
	for rows.Next() {
		var ticker string
		var val float64
		if err := rows.Scan(&ticker, &val); err != nil {
			return nil, fmt.Errorf("positions daily scan: %w", err)
		}
		mv[ticker] = val
	}
	return mv, rows.Err()
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
	return parseMetadataDate(s)
}

// parseMetadataDate parses a date value from the metadata table. pvbt writes
// run.start/run.end as RFC3339 timestamps (e.g. 2026-07-14T23:59:59-04:00);
// legacy snapshots store bare dates. RFC3339 values are truncated to midnight
// UTC of the calendar day in the timestamp's own offset, matching what a bare
// date parses to.
func parseMetadataDate(s string) (time.Time, error) {
	if t, err := time.Parse(dateLayout, s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse metadata date %q: %w", s, err)
	}
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC), nil
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
		t, perr := parseMetadataDate(v)
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
