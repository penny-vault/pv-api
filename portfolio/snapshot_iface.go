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

package portfolio

import (
	"context"
	"errors"
	"time"

	"github.com/penny-vault/pv-api/openapi"
)

// ErrSnapshotNotFound is returned by date-parameterized snapshot readers
// (e.g. HoldingsAsOf) when the requested date falls outside the backtest
// window. snapshot.Opener translates snapshot.ErrNotFound to this sentinel
// so the portfolio layer does not need to import the snapshot package.
var ErrSnapshotNotFound = errors.New("snapshot: not found")

// SnapshotReader is the subset of snapshot.Reader that portfolio handlers
// need. Redeclared here so handler tests can provide a fake without
// linking snapshot's modernc-sqlite dependency.
type SnapshotReader interface {
	Summary(ctx context.Context) (*openapi.PortfolioSummary, error)
	Drawdowns(ctx context.Context) ([]openapi.Drawdown, error)
	Statistics(ctx context.Context) ([]openapi.PortfolioStatistic, error)
	TrailingReturns(ctx context.Context) ([]openapi.TrailingReturnRow, error)
	CurrentHoldings(ctx context.Context) (*openapi.HoldingsResponse, error)
	HoldingsAsOf(ctx context.Context, date time.Time) (*openapi.HoldingsResponse, error)
	HoldingsHistory(ctx context.Context, from, to *time.Time) (*openapi.HoldingsHistoryResponse, error)
	Performance(ctx context.Context, slug string, from, to *time.Time) (*openapi.PortfolioPerformance, error)
	Transactions(ctx context.Context, filter SnapshotTxFilter) (*openapi.TransactionsResponse, error)
	Metrics(ctx context.Context, windows, metrics []string) (*openapi.PortfolioMetrics, error)
	Close() error
}

// SnapshotTxFilter mirrors snapshot.TransactionFilter for the portfolio
// layer (avoids importing snapshot from portfolio).
type SnapshotTxFilter struct {
	From  *time.Time
	To    *time.Time
	Types []string
}

// SnapshotOpener opens a SnapshotReader for a given snapshot file path.
// Production wires snapshot.Opener; tests wire a fake.
type SnapshotOpener interface {
	Open(path string) (SnapshotReader, error)
}
