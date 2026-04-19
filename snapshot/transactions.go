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
	"fmt"
	"strings"
	"time"

	"github.com/oapi-codegen/runtime/types"

	"github.com/penny-vault/pv-api/openapi"
)

// TransactionFilter scopes a Transactions read.
type TransactionFilter struct {
	From  *time.Time // inclusive
	To    *time.Time // inclusive
	Types []string   // e.g. []string{"buy","sell"}
}

// Transactions returns a filtered list of transactions from the snapshot.
func (r *Reader) Transactions(ctx context.Context, f TransactionFilter) (*openapi.TransactionsResponse, error) {
	var (
		where []string
		args  []any
	)
	if f.From != nil {
		where = append(where, "date >= ?")
		args = append(args, f.From.Format("2006-01-02"))
	}
	if f.To != nil {
		where = append(where, "date <= ?")
		args = append(args, f.To.Format("2006-01-02"))
	}
	if len(f.Types) > 0 {
		placeholders := strings.Repeat("?,", len(f.Types))
		placeholders = placeholders[:len(placeholders)-1]
		where = append(where, "type IN ("+placeholders+")")
		for _, t := range f.Types {
			args = append(args, t)
		}
	}

	q := `SELECT batch_id, date, type, ticker, figi, quantity, price, amount, qualified, justification
	        FROM transactions`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY batch_id, rowid"

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("transactions query: %w", err)
	}
	defer rows.Close()

	var out openapi.TransactionsResponse
	out.Items = []openapi.Transaction{}
	for rows.Next() {
		var (
			batchID                     int64
			dateStr                     string
			typeStr                     string
			ticker, figi, justification *string
			quantity, price, amount     *float64
			qualified                   *int64
		)
		if err := rows.Scan(&batchID, &dateStr, &typeStr, &ticker, &figi, &quantity, &price, &amount, &qualified, &justification); err != nil {
			return nil, fmt.Errorf("transactions scan: %w", err)
		}
		d, perr := time.Parse("2006-01-02", dateStr)
		if perr != nil {
			return nil, fmt.Errorf("transactions parse date %q: %w", dateStr, perr)
		}
		t := openapi.Transaction{
			BatchId:       batchID,
			Date:          types.Date{Time: d},
			Type:          openapi.TransactionType(typeStr),
			Ticker:        ticker,
			Figi:          figi,
			Justification: justification,
			Quantity:      quantity,
			Price:         price,
			Amount:        amount,
		}
		if qualified != nil {
			v := *qualified != 0
			t.Qualified = &v
		}
		out.Items = append(out.Items, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("transactions iterate: %w", err)
	}
	return &out, nil
}
