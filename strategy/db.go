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

package strategy

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a requested strategy row does not exist.
var ErrNotFound = errors.New("strategy not found")

const strategyColumns = `
	short_code, repo_owner, repo_name, clone_url, is_official, owner_sub,
	description, categories, stars,
	installed_ver, installed_at, last_attempted_ver, install_error,
	artifact_kind, artifact_ref, describe_json,
	cagr, max_drawdown, sharpe, stats_as_of,
	discovered_at, updated_at
`

// List returns every strategy row. Caller applies filtering.
func List(ctx context.Context, pool *pgxpool.Pool) ([]Strategy, error) {
	rows, err := pool.Query(ctx, `SELECT `+strategyColumns+` FROM strategies ORDER BY short_code`)
	if err != nil {
		return nil, fmt.Errorf("querying strategies: %w", err)
	}
	defer rows.Close()

	var out []Strategy
	for rows.Next() {
		s, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating strategies: %w", err)
	}
	return out, nil
}

// Get returns one strategy by short code.
func Get(ctx context.Context, pool *pgxpool.Pool, shortCode string) (Strategy, error) {
	row := pool.QueryRow(ctx,
		`SELECT `+strategyColumns+` FROM strategies WHERE short_code = $1`,
		shortCode,
	)
	s, err := scan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Strategy{}, ErrNotFound
	}
	return s, err
}

// GetByCloneURL returns the strategy row whose clone_url matches. Used by the
// sync loop to look up existing records by repository identity rather than
// short code, since the short code is authoritative from the binary itself.
func GetByCloneURL(ctx context.Context, pool *pgxpool.Pool, cloneURL string) (Strategy, error) {
	row := pool.QueryRow(ctx,
		`SELECT `+strategyColumns+` FROM strategies WHERE clone_url = $1 LIMIT 1`,
		cloneURL,
	)
	s, err := scan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Strategy{}, ErrNotFound
	}
	return s, err
}

// Upsert inserts or updates a strategy row based on short_code. Used by the
// sync loop to reconcile discovered listings.
func Upsert(ctx context.Context, pool *pgxpool.Pool, s Strategy) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO strategies (
			short_code, repo_owner, repo_name, clone_url, is_official, owner_sub,
			description, categories, stars
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (short_code) DO UPDATE SET
			repo_owner = EXCLUDED.repo_owner,
			repo_name  = EXCLUDED.repo_name,
			clone_url  = EXCLUDED.clone_url,
			description = EXCLUDED.description,
			categories = EXCLUDED.categories,
			stars      = EXCLUDED.stars,
			updated_at = NOW()
	`, s.ShortCode, s.RepoOwner, s.RepoName, s.CloneURL, s.IsOfficial, s.OwnerSub,
		s.Description, s.Categories, s.Stars)
	if err != nil {
		return fmt.Errorf("upsert strategy %s: %w", s.ShortCode, err)
	}
	return nil
}

// MarkAttempt records that a given version was attempted for a strategy.
// Clears any previous install_error so a fresh attempt starts clean.
func MarkAttempt(ctx context.Context, pool *pgxpool.Pool, shortCode, version string) error {
	_, err := pool.Exec(ctx, `
		UPDATE strategies
		   SET last_attempted_ver = $2,
		       install_error      = NULL,
		       updated_at         = NOW()
		 WHERE short_code = $1
	`, shortCode, version)
	if err != nil {
		return fmt.Errorf("mark attempt %s@%s: %w", shortCode, version, err)
	}
	return nil
}

// MarkSuccess records a successful install. Sets installed_ver, installed_at,
// last_attempted_ver, artifact_kind, artifact_ref, describe_json. Clears install_error.
func MarkSuccess(ctx context.Context, pool *pgxpool.Pool,
	shortCode, version, artifactKind, artifactRef string, describeJSON []byte,
) error {
	_, err := pool.Exec(ctx, `
		UPDATE strategies
		   SET installed_ver      = $2,
		       installed_at       = NOW(),
		       last_attempted_ver = $2,
		       artifact_kind      = $3,
		       artifact_ref       = $4,
		       describe_json      = $5,
		       install_error      = NULL,
		       updated_at         = NOW()
		 WHERE short_code = $1
	`, shortCode, version, artifactKind, artifactRef, describeJSON)
	if err != nil {
		return fmt.Errorf("mark success %s@%s: %w", shortCode, version, err)
	}
	return nil
}

// MarkFailure records a failed install. Sets install_error; leaves
// installed_ver and artifact_* alone so an older working version stays live.
func MarkFailure(ctx context.Context, pool *pgxpool.Pool, shortCode, version, errText string) error {
	_, err := pool.Exec(ctx, `
		UPDATE strategies
		   SET last_attempted_ver = $2,
		       install_error      = $3,
		       updated_at         = NOW()
		 WHERE short_code = $1
	`, shortCode, version, errText)
	if err != nil {
		return fmt.Errorf("mark failure %s@%s: %w", shortCode, version, err)
	}
	return nil
}

// ListInstalled returns all strategies that have a non-NULL artifact_ref.
func ListInstalled(ctx context.Context, pool *pgxpool.Pool) ([]Strategy, error) {
	rows, err := pool.Query(ctx,
		`SELECT `+strategyColumns+` FROM strategies WHERE artifact_ref IS NOT NULL ORDER BY short_code`)
	if err != nil {
		return nil, fmt.Errorf("listing installed strategies: %w", err)
	}
	defer rows.Close()
	var out []Strategy
	for rows.Next() {
		s, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpdateStats writes performance stats and clears any previous stats_error.
func UpdateStats(ctx context.Context, pool *pgxpool.Pool, shortCode string, r StatsResult) error {
	_, err := pool.Exec(ctx,
		`UPDATE strategies
		    SET cagr=$2, max_drawdown=$3, sharpe=$4, stats_as_of=$5,
		        stats_error=NULL, updated_at=NOW()
		  WHERE short_code=$1`,
		shortCode, r.CAGR, r.MaxDrawdown, r.Sharpe, r.AsOf)
	return err
}

// MarkStatsError records a stats failure without touching existing stats values.
func MarkStatsError(ctx context.Context, pool *pgxpool.Pool, shortCode, errText string) error {
	_, err := pool.Exec(ctx,
		`UPDATE strategies SET stats_error=$2, updated_at=NOW() WHERE short_code=$1`,
		shortCode, errText)
	return err
}

// LookupArtifact returns the artifact_ref for an official strategy matching the
// given clone URL and version, or ErrNotFound if no such row exists with a
// successful install (install_error IS NULL).
func LookupArtifact(ctx context.Context, pool *pgxpool.Pool, cloneURL, installedVer string) (string, error) {
	var ref string
	err := pool.QueryRow(ctx, `
		SELECT artifact_ref
		  FROM strategies
		 WHERE clone_url = $1 AND installed_ver = $2 AND install_error IS NULL
		 LIMIT 1
	`, cloneURL, installedVer).Scan(&ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("looking up artifact: %w", err)
	}
	return ref, nil
}

// scanner is the subset of pgx.Rows / pgx.Row used by scan.
type scanner interface {
	Scan(dest ...any) error
}

func scan(r scanner) (Strategy, error) {
	var s Strategy
	err := r.Scan(
		&s.ShortCode, &s.RepoOwner, &s.RepoName, &s.CloneURL, &s.IsOfficial, &s.OwnerSub,
		&s.Description, &s.Categories, &s.Stars,
		&s.InstalledVer, &s.InstalledAt, &s.LastAttemptedVer, &s.InstallError,
		&s.ArtifactKind, &s.ArtifactRef, &s.DescribeJSON,
		&s.CAGR, &s.MaxDrawdown, &s.Sharpe, &s.StatsAsOf,
		&s.DiscoveredAt, &s.UpdatedAt,
	)
	if err != nil {
		return Strategy{}, fmt.Errorf("scanning strategy row: %w", err)
	}
	return s, nil
}
