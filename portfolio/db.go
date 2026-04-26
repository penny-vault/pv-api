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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a portfolio lookup does not match any row
// owned by the calling user.
var ErrNotFound = errors.New("portfolio not found")

// ErrDuplicateSlug is returned on (owner_sub, slug) unique-constraint hits.
var ErrDuplicateSlug = errors.New("duplicate portfolio slug")

const portfolioColumns = `
	id, owner_sub, slug, name, strategy_code, strategy_ver, strategy_clone_url,
	strategy_describe_json, parameters,
	preset_name, benchmark, start_date, end_date, status, last_run_at,
	last_error, snapshot_path, created_at, updated_at, run_retention
`

// List returns every portfolio owned by ownerSub, sorted newest-first.
func List(ctx context.Context, pool *pgxpool.Pool, ownerSub string) ([]Portfolio, error) {
	rows, err := pool.Query(ctx,
		`SELECT `+portfolioColumns+` FROM portfolios WHERE owner_sub = $1 ORDER BY created_at DESC`,
		ownerSub,
	)
	if err != nil {
		return nil, fmt.Errorf("querying portfolios: %w", err)
	}
	defer rows.Close()

	var out []Portfolio
	for rows.Next() {
		p, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get returns one portfolio by (ownerSub, slug). ErrNotFound if no row.
func Get(ctx context.Context, pool *pgxpool.Pool, ownerSub, slug string) (Portfolio, error) {
	row := pool.QueryRow(ctx,
		`SELECT `+portfolioColumns+` FROM portfolios WHERE owner_sub = $1 AND slug = $2`,
		ownerSub, slug,
	)
	p, err := scan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Portfolio{}, ErrNotFound
	}
	return p, err
}

// Insert writes a new portfolio row. The caller must have populated every
// field on p (slug, strategy_ver, parameters, benchmark, status,
// etc.) before calling. Returns ErrDuplicateSlug on a
// (owner_sub, slug) UNIQUE violation.
func Insert(ctx context.Context, pool *pgxpool.Pool, p Portfolio) error {
	paramsJSON, err := json.Marshal(p.Parameters)
	if err != nil {
		return fmt.Errorf("marshaling parameters: %w", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO portfolios (
			owner_sub, slug, name, strategy_code, strategy_ver,
			strategy_clone_url, strategy_describe_json, parameters,
			preset_name, benchmark, start_date, end_date, status, run_retention
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`, p.OwnerSub, p.Slug, p.Name, p.StrategyCode, p.StrategyVer,
		p.StrategyCloneURL, p.StrategyDescribeJSON, paramsJSON,
		p.PresetName, p.Benchmark, p.StartDate, p.EndDate,
		string(p.Status), retentionOrDefault(p.RunRetention))
	if err != nil {
		if uniqueViolation(err) {
			return ErrDuplicateSlug
		}
		return fmt.Errorf("inserting portfolio: %w", err)
	}
	return nil
}

// UpdateName updates a portfolio's display name. Returns ErrNotFound if
// the (ownerSub, slug) pair does not match any row.
func UpdateName(ctx context.Context, pool *pgxpool.Pool, ownerSub, slug, name string) error {
	tag, err := pool.Exec(ctx, `
		UPDATE portfolios
		   SET name = $3, updated_at = NOW()
		 WHERE owner_sub = $1 AND slug = $2
	`, ownerSub, slug, name)
	if err != nil {
		return fmt.Errorf("updating portfolio name: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateDates updates a portfolio's start_date and/or end_date.
// Only non-nil values overwrite existing ones. Returns ErrNotFound if no row matched.
// The caller must ensure end is not before start; no validation is performed here.
func UpdateDates(ctx context.Context, pool *pgxpool.Pool, ownerSub, slug string, startDate, endDate *time.Time) error {
	tag, err := pool.Exec(ctx, `
		UPDATE portfolios
		   SET start_date = COALESCE($3, start_date),
		       end_date   = COALESCE($4, end_date),
		       updated_at = NOW()
		 WHERE owner_sub = $1 AND slug = $2
	`, ownerSub, slug, startDate, endDate)
	if err != nil {
		return fmt.Errorf("updating portfolio dates: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a portfolio by (ownerSub, slug). Returns ErrNotFound if
// no row was deleted.
func Delete(ctx context.Context, pool *pgxpool.Pool, ownerSub, slug string) error {
	tag, err := pool.Exec(ctx,
		`DELETE FROM portfolios WHERE owner_sub = $1 AND slug = $2`,
		ownerSub, slug,
	)
	if err != nil {
		return fmt.Errorf("deleting portfolio: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetByID fetches a portfolio by id without owner scoping. Used by
// backtest orchestration (internal call path, not user-facing).
func GetByID(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (Portfolio, error) {
	row := pool.QueryRow(ctx,
		`SELECT `+portfolioColumns+` FROM portfolios WHERE id=$1`, id)
	p, err := scan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Portfolio{}, ErrNotFound
	}
	return p, err
}

// SetRunning marks the portfolio as running.
func SetRunning(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) error {
	_, err := pool.Exec(ctx,
		`UPDATE portfolios SET status='running', updated_at=NOW() WHERE id=$1`, id)
	return err
}

// SetReady marks the portfolio as ready and writes all KPI columns.
// inceptionDate is written only if the row has no existing inception_date.
func SetReady(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, snapshotPath string,
	currentValue, ytdReturn, maxDrawdown, sharpe, cagr float64, inceptionDate time.Time) error {
	const q = `
		UPDATE portfolios SET
			status='ready',
			last_run_at=NOW(),
			last_error=NULL,
			snapshot_path=$2,
			current_value=$3,
			ytd_return=$4,
			max_drawdown=$5,
			sharpe=$6,
			cagr_since_inception=$7,
			inception_date=COALESCE(inception_date, $8),
			updated_at=NOW()
		  WHERE id=$1`
	_, err := pool.Exec(ctx, q, id, snapshotPath, currentValue, ytdReturn, maxDrawdown, sharpe, cagr, inceptionDate)
	return err
}

// SetFailed marks the portfolio as failed and records the error message.
func SetFailed(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, errMsg string) error {
	_, err := pool.Exec(ctx,
		`UPDATE portfolios SET status='failed', last_error=$2, updated_at=NOW() WHERE id=$1`,
		id, errMsg)
	return err
}

// MarkRunningTx atomically marks both the portfolio and its run as running
// inside a single Postgres transaction.
func MarkRunningTx(ctx context.Context, pool *pgxpool.Pool, portfolioID, runID uuid.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback on failure is best-effort
	if _, err := tx.Exec(ctx,
		`UPDATE portfolios SET status='running', updated_at=NOW() WHERE id=$1`, portfolioID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE backtest_runs SET status='running', started_at=NOW() WHERE id=$1`, runID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkReadyTx atomically marks the portfolio as ready and the run as success,
// writing all KPI columns in the same transaction.
func MarkReadyTx(ctx context.Context, pool *pgxpool.Pool, portfolioID, runID uuid.UUID,
	snapshotPath string, currentValue, ytdReturn, maxDrawdown, sharpe, cagr float64,
	inceptionDate time.Time, durationMs int32) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback on failure is best-effort
	const portfolioQ = `
		UPDATE portfolios SET
			status='ready',
			last_run_at=NOW(),
			last_error=NULL,
			snapshot_path=$2,
			current_value=$3,
			ytd_return=$4,
			max_drawdown=$5,
			sharpe=$6,
			cagr_since_inception=$7,
			inception_date=COALESCE(inception_date, $8),
			updated_at=NOW()
		  WHERE id=$1`
	if _, err := tx.Exec(ctx, portfolioQ,
		portfolioID, snapshotPath, currentValue, ytdReturn, maxDrawdown, sharpe, cagr, inceptionDate); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE backtest_runs SET status='success', finished_at=NOW(),
		                          snapshot_path=$2, duration_ms=$3
		  WHERE id=$1`, runID, snapshotPath, durationMs); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkFailedTx atomically marks the portfolio as failed and the run as failed
// in a single Postgres transaction.
func MarkFailedTx(ctx context.Context, pool *pgxpool.Pool, portfolioID, runID uuid.UUID,
	errMsg string, durationMs int32) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback on failure is best-effort
	if _, err := tx.Exec(ctx,
		`UPDATE portfolios SET status='failed', last_error=$2, updated_at=NOW() WHERE id=$1`,
		portfolioID, errMsg); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE backtest_runs SET status='failed', finished_at=NOW(), error=$2, duration_ms=$3
		  WHERE id=$1`, runID, errMsg, durationMs); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkAllRunningAsFailed flips every portfolio whose status is 'running' to
// 'failed' with the supplied reason. Called at startup to clear any portfolios
// that were left mid-run when the server was previously killed.
// Returns the number of rows updated.
func MarkAllRunningAsFailed(ctx context.Context, pool *pgxpool.Pool, reason string) (int, error) {
	tag, err := pool.Exec(ctx,
		`UPDATE portfolios SET status='failed', last_error=$1, updated_at=NOW()
		  WHERE status='running'`, reason)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ClaimDue returns up to batchSize portfolio IDs that are open-ended
// (end_date IS NULL) and have not yet run today.
func ClaimDue(ctx context.Context, pool *pgxpool.Pool, batchSize int) ([]uuid.UUID, error) {
	rows, err := pool.Query(ctx, `
		SELECT id
		  FROM portfolios
		 WHERE end_date IS NULL
		   AND status IN ('ready', 'failed')
		   AND (last_run_at IS NULL OR last_run_at::date < CURRENT_DATE)
		 ORDER BY last_run_at NULLS FIRST
		 LIMIT $1
	`, batchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(r scanner) (Portfolio, error) {
	var (
		p          Portfolio
		statusStr  string
		paramsJSON []byte
	)
	err := r.Scan(
		&p.ID, &p.OwnerSub, &p.Slug, &p.Name, &p.StrategyCode, &p.StrategyVer,
		&p.StrategyCloneURL, &p.StrategyDescribeJSON, &paramsJSON,
		&p.PresetName, &p.Benchmark, &p.StartDate, &p.EndDate,
		&statusStr, &p.LastRunAt, &p.LastError, &p.SnapshotPath,
		&p.CreatedAt, &p.UpdatedAt, &p.RunRetention,
	)
	if err != nil {
		return Portfolio{}, err
	}
	p.Status = Status(statusStr)
	if len(paramsJSON) > 0 {
		if err := json.Unmarshal(paramsJSON, &p.Parameters); err != nil {
			return Portfolio{}, fmt.Errorf("unmarshaling parameters: %w", err)
		}
	}
	return p, nil
}

// uniqueViolation reports whether err carries Postgres 23505 (unique_violation).
// pgx v5 wraps database errors; the most reliable cross-version check is
// inspecting the formatted message for "SQLSTATE 23505".
func uniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "SQLSTATE 23505")
}

// retentionOrDefault returns v when v >= 1, otherwise the default of 2.
func retentionOrDefault(v int) int {
	if v <= 0 {
		return 2
	}
	return v
}
