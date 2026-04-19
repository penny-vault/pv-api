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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the subset of db operations the handler needs.
type Store interface {
	RunStore
	List(ctx context.Context, ownerSub string) ([]Portfolio, error)
	Get(ctx context.Context, ownerSub, slug string) (Portfolio, error)
	Insert(ctx context.Context, p Portfolio) error
	UpdateName(ctx context.Context, ownerSub, slug, name string) error
	Delete(ctx context.Context, ownerSub, slug string) error
}

// PoolStore adapts *pgxpool.Pool to the Store interface.
type PoolStore struct {
	Pool *pgxpool.Pool
	*PoolRunStore
}

// NewPoolStore constructs a PoolStore backed by pool.
func NewPoolStore(pool *pgxpool.Pool) *PoolStore {
	return &PoolStore{Pool: pool, PoolRunStore: NewPoolRunStore(pool)}
}

func (p PoolStore) List(ctx context.Context, ownerSub string) ([]Portfolio, error) {
	return List(ctx, p.Pool, ownerSub)
}

func (p PoolStore) Get(ctx context.Context, ownerSub, slug string) (Portfolio, error) {
	return Get(ctx, p.Pool, ownerSub, slug)
}

func (p PoolStore) Insert(ctx context.Context, port Portfolio) error {
	return Insert(ctx, p.Pool, port)
}

func (p PoolStore) UpdateName(ctx context.Context, ownerSub, slug, name string) error {
	return UpdateName(ctx, p.Pool, ownerSub, slug, name)
}

func (p PoolStore) Delete(ctx context.Context, ownerSub, slug string) error {
	return Delete(ctx, p.Pool, ownerSub, slug)
}

// GetByID fetches a portfolio by id without owner scoping (backtest path).
func (p PoolStore) GetByID(ctx context.Context, id uuid.UUID) (Portfolio, error) {
	return GetByID(ctx, p.Pool, id)
}

// SetRunning marks the portfolio as running (called by backtest orchestrator).
func (p PoolStore) SetRunning(ctx context.Context, id uuid.UUID) error {
	return SetRunning(ctx, p.Pool, id)
}

// SetReady marks the portfolio as ready and writes KPI columns (backtest path).
func (p PoolStore) SetReady(ctx context.Context, id uuid.UUID, snapshotPath string,
	currentValue, ytdReturn, maxDrawdown, sharpe, cagr float64, inceptionDate time.Time) error {
	return SetReady(ctx, p.Pool, id, snapshotPath, currentValue, ytdReturn, maxDrawdown, sharpe, cagr, inceptionDate)
}

// SetFailed marks the portfolio as failed (backtest path).
func (p PoolStore) SetFailed(ctx context.Context, id uuid.UUID, errMsg string) error {
	return SetFailed(ctx, p.Pool, id, errMsg)
}

// MarkAllRunningAsFailed implements backtest.PortfolioSweeper. It flips every
// portfolio whose status is 'running' to 'failed' at server startup.
func (p PoolStore) MarkAllRunningAsFailed(ctx context.Context, reason string) (int, error) {
	return MarkAllRunningAsFailed(ctx, p.Pool, reason)
}
