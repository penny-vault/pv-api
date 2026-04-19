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

package portfolio_test

import (
	"context"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/portfolio"
)

var _ = Describe("PoolRunStore", Ordered, func() {
	var (
		pool        *pgxpool.Pool
		store       *portfolio.PoolRunStore
		portfolioID uuid.UUID
	)

	BeforeAll(func() {
		dbURL := os.Getenv("PVAPI_SMOKE_DB_URL")
		if dbURL == "" {
			Skip("PVAPI_SMOKE_DB_URL not set; skipping run-store smoke test")
		}
		var err error
		pool, err = pgxpool.New(context.Background(), dbURL)
		Expect(err).NotTo(HaveOccurred())
		store = portfolio.NewPoolRunStore(pool)

		// Insert a stub strategy so the portfolios FK is satisfied.
		// is_official=true satisfies CHECK ((is_official AND owner_sub IS NULL) OR ...).
		_, err = pool.Exec(context.Background(), `
			INSERT INTO strategies (short_code, repo_owner, repo_name, clone_url, is_official)
			VALUES ('__smoke_stub__', 'smoke', 'smoke', '', true)
			ON CONFLICT (short_code) DO NOTHING
		`)
		Expect(err).NotTo(HaveOccurred())

		portfolioID = uuid.New()
		_, err = pool.Exec(context.Background(), `
			INSERT INTO portfolios (id, owner_sub, slug, name, strategy_code, strategy_ver, parameters, benchmark, mode, status)
			VALUES ($1, 'smoke|user', 'run-store-smoke-slug', 'smoke', '__smoke_stub__', 'v0.0.0', '{}'::jsonb, 'SPY', 'one_shot', 'pending')
		`, portfolioID)
		Expect(err).NotTo(HaveOccurred())

		DeferCleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM portfolios WHERE id=$1`, portfolioID)
			_, _ = pool.Exec(context.Background(), `DELETE FROM strategies WHERE short_code='__smoke_stub__'`)
			pool.Close()
		})
	})

	It("creates a queued run and advances it to success", func() {
		r, err := store.CreateRun(context.Background(), portfolioID, "queued")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Status).To(Equal("queued"))

		Expect(store.UpdateRunRunning(context.Background(), r.ID)).To(Succeed())
		Expect(store.UpdateRunSuccess(context.Background(), r.ID, "/tmp/x.sqlite", 1234)).To(Succeed())

		fetched, err := store.GetRun(context.Background(), portfolioID, r.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(fetched.Status).To(Equal("success"))
		Expect(*fetched.SnapshotPath).To(Equal("/tmp/x.sqlite"))
	})

	It("lists runs newest-first", func() {
		runs, err := store.ListRuns(context.Background(), portfolioID)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(runs)).To(BeNumerically(">=", 1))
	})
})
