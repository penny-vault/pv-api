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

package alert

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("alert not found")

type Store interface {
	Create(ctx context.Context, portfolioID uuid.UUID, frequency string, recipients []string) (Alert, error)
	List(ctx context.Context, portfolioID uuid.UUID) ([]Alert, error)
	Get(ctx context.Context, id uuid.UUID) (Alert, error)
	Update(ctx context.Context, id uuid.UUID, frequency string, recipients []string) (Alert, error)
	Delete(ctx context.Context, id uuid.UUID) error
	MarkSent(ctx context.Context, id uuid.UUID, sentAt time.Time, value float64) error
	RemoveRecipient(ctx context.Context, id uuid.UUID, recipient string) error
}

const alertColumns = `id, portfolio_id, frequency, recipients, last_sent_at, last_sent_value, created_at, updated_at`

type PoolStore struct {
	pool *pgxpool.Pool
}

func NewPoolStore(pool *pgxpool.Pool) *PoolStore {
	return &PoolStore{pool: pool}
}

func scanAlert(row pgx.Row) (Alert, error) {
	var a Alert
	err := row.Scan(&a.ID, &a.PortfolioID, &a.Frequency, &a.Recipients,
		&a.LastSentAt, &a.LastSentValue, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Alert{}, ErrNotFound
	}
	return a, err
}

func (s *PoolStore) Create(ctx context.Context, portfolioID uuid.UUID, frequency string, recipients []string) (Alert, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO portfolio_alerts (portfolio_id, frequency, recipients)
		VALUES ($1, $2, $3)
		RETURNING `+alertColumns,
		portfolioID, frequency, recipients,
	)
	a, err := scanAlert(row)
	if err != nil {
		return Alert{}, fmt.Errorf("create alert: %w", err)
	}
	return a, nil
}

func (s *PoolStore) List(ctx context.Context, portfolioID uuid.UUID) ([]Alert, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+alertColumns+` FROM portfolio_alerts WHERE portfolio_id=$1 ORDER BY created_at`,
		portfolioID,
	)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		a, scanErr := scanAlert(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *PoolStore) Get(ctx context.Context, id uuid.UUID) (Alert, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+alertColumns+` FROM portfolio_alerts WHERE id=$1`, id)
	a, err := scanAlert(row)
	if err != nil {
		return Alert{}, fmt.Errorf("get alert: %w", err)
	}
	return a, nil
}

func (s *PoolStore) Update(ctx context.Context, id uuid.UUID, frequency string, recipients []string) (Alert, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE portfolio_alerts
		   SET frequency=$2, recipients=$3, updated_at=now()
		 WHERE id=$1
		RETURNING `+alertColumns,
		id, frequency, recipients,
	)
	a, err := scanAlert(row)
	if err != nil {
		return Alert{}, fmt.Errorf("update alert: %w", err)
	}
	return a, nil
}

func (s *PoolStore) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM portfolio_alerts WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete alert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PoolStore) MarkSent(ctx context.Context, id uuid.UUID, sentAt time.Time, value float64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE portfolio_alerts
		   SET last_sent_at=$2, last_sent_value=$3, updated_at=now()
		 WHERE id=$1`,
		id, sentAt, value,
	)
	return err
}

// RemoveRecipient removes one recipient from the alert. If recipients becomes
// empty, the alert is deleted.
func (s *PoolStore) RemoveRecipient(ctx context.Context, id uuid.UUID, recipient string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE portfolio_alerts
		   SET recipients = array_remove(recipients, $2), updated_at = now()
		 WHERE id = $1`,
		id, recipient,
	)
	if err != nil {
		return fmt.Errorf("remove recipient: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = s.pool.Exec(ctx, `
		DELETE FROM portfolio_alerts
		 WHERE id = $1 AND array_length(recipients, 1) IS NULL`,
		id,
	)
	return err
}
