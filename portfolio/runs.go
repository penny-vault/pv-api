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

// Run represents one row in the backtest_runs table.
type Run struct {
	ID           uuid.UUID
	PortfolioID  uuid.UUID
	Status       string // queued | running | success | failed
	StartedAt    *time.Time
	FinishedAt   *time.Time
	DurationMs   *int32
	Error        *string
	SnapshotPath *string
}

// RunStore exposes the backtest_runs table. Ownership is enforced at
// the portfolio layer (we only expose runs the user owns via their
// portfolio).
type RunStore interface {
	CreateRun(ctx context.Context, portfolioID uuid.UUID, status string) (Run, error)
	UpdateRunRunning(ctx context.Context, runID uuid.UUID) error
	UpdateRunSuccess(ctx context.Context, runID uuid.UUID, snapshotPath string, durationMs int32) error
	UpdateRunFailed(ctx context.Context, runID uuid.UUID, errMsg string, durationMs int32) error
	ListRuns(ctx context.Context, portfolioID uuid.UUID) ([]Run, error)
	GetRun(ctx context.Context, portfolioID, runID uuid.UUID) (Run, error)
}

// PoolRunStore is the pgxpool-backed RunStore.
type PoolRunStore struct {
	pool *pgxpool.Pool
}

func NewPoolRunStore(pool *pgxpool.Pool) *PoolRunStore { return &PoolRunStore{pool: pool} }

func (s *PoolRunStore) CreateRun(ctx context.Context, portfolioID uuid.UUID, status string) (Run, error) {
	const q = `
		INSERT INTO backtest_runs (id, portfolio_id, status)
		VALUES (uuidv7(), $1, $2)
		RETURNING id, portfolio_id, status, started_at, finished_at, duration_ms, error, snapshot_path
	`
	return scanRun(s.pool.QueryRow(ctx, q, portfolioID, status))
}

func (s *PoolRunStore) UpdateRunRunning(ctx context.Context, runID uuid.UUID) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE backtest_runs SET status='running', started_at=NOW() WHERE id=$1`, runID)
	return err
}

func (s *PoolRunStore) UpdateRunSuccess(ctx context.Context, runID uuid.UUID, snapshotPath string, durationMs int32) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE backtest_runs SET status='success', finished_at=NOW(),
		                          snapshot_path=$2, duration_ms=$3
		  WHERE id=$1`, runID, snapshotPath, durationMs)
	return err
}

func (s *PoolRunStore) UpdateRunFailed(ctx context.Context, runID uuid.UUID, errMsg string, durationMs int32) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE backtest_runs SET status='failed', finished_at=NOW(),
		                          error=$2, duration_ms=$3
		  WHERE id=$1`, runID, errMsg, durationMs)
	return err
}

func (s *PoolRunStore) ListRuns(ctx context.Context, portfolioID uuid.UUID) ([]Run, error) {
	const q = `
		SELECT id, portfolio_id, status, started_at, finished_at, duration_ms, error, snapshot_path
		  FROM backtest_runs
		 WHERE portfolio_id=$1
		 ORDER BY COALESCE(started_at, '0001-01-01'::timestamptz) DESC
	`
	rows, err := s.pool.Query(ctx, q, portfolioID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PoolRunStore) GetRun(ctx context.Context, portfolioID, runID uuid.UUID) (Run, error) {
	const q = `
		SELECT id, portfolio_id, status, started_at, finished_at, duration_ms, error, snapshot_path
		  FROM backtest_runs
		 WHERE id=$1 AND portfolio_id=$2
	`
	r, err := scanRun(s.pool.QueryRow(ctx, q, runID, portfolioID))
	if err != nil {
		return Run{}, ErrNotFound
	}
	return r, nil
}

// rowScanner is satisfied by both pgx.Row and pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRun(s rowScanner) (Run, error) {
	var r Run
	err := s.Scan(&r.ID, &r.PortfolioID, &r.Status, &r.StartedAt, &r.FinishedAt, &r.DurationMs, &r.Error, &r.SnapshotPath)
	return r, err
}
