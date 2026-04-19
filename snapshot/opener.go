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
	"errors"
	"time"

	"github.com/penny-vault/pv-api/openapi"
	"github.com/penny-vault/pv-api/portfolio"
)

// readerAdapter wraps *Reader to present the portfolio.SnapshotReader
// interface (translating SnapshotTxFilter to TransactionFilter and
// translating snapshot.ErrNotFound to portfolio.ErrSnapshotNotFound).
type readerAdapter struct {
	*Reader
}

func (a readerAdapter) Transactions(ctx context.Context, f portfolio.SnapshotTxFilter) (*openapi.TransactionsResponse, error) {
	return a.Reader.Transactions(ctx, TransactionFilter{From: f.From, To: f.To, Types: f.Types})
}

func (a readerAdapter) HoldingsAsOf(ctx context.Context, d time.Time) (*openapi.HoldingsResponse, error) {
	resp, err := a.Reader.HoldingsAsOf(ctx, d)
	if errors.Is(err, ErrNotFound) {
		return nil, portfolio.ErrSnapshotNotFound
	}
	return resp, err
}

var _ portfolio.SnapshotReader = readerAdapter{}

// Opener satisfies portfolio.SnapshotOpener.
type Opener struct{}

// Open opens a snapshot file and returns a portfolio.SnapshotReader.
func (Opener) Open(path string) (portfolio.SnapshotReader, error) {
	r, err := Open(path)
	if err != nil {
		return nil, err
	}
	return readerAdapter{Reader: r}, nil
}
