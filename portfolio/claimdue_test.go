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

var _ = Describe("ClaimDue scheduled-vs-manual gating", Ordered, func() {
	var (
		ctx      = context.Background()
		pool     *pgxpool.Pool
		store    *portfolio.PoolStore
		runStore *portfolio.PoolRunStore
		ownerSub = "smoke|claimdue-user"
	)

	BeforeAll(func() {
		dbURL := os.Getenv("PVAPI_SMOKE_DB_URL")
		if dbURL == "" {
			Skip("PVAPI_SMOKE_DB_URL not set; skipping ClaimDue smoke test")
		}
		var err error
		pool, err = pgxpool.New(ctx, dbURL)
		Expect(err).NotTo(HaveOccurred())
		store = portfolio.NewPoolStore(pool)
		runStore = portfolio.NewPoolRunStore(pool)

		_, err = pool.Exec(ctx, `
			INSERT INTO strategies (short_code, repo_owner, repo_name, clone_url, is_official)
			VALUES ('__claimdue_stub__', 'smoke', 'smoke', '', true)
			ON CONFLICT (short_code) DO NOTHING
		`)
		Expect(err).NotTo(HaveOccurred())

		DeferCleanup(func() {
			// ON DELETE CASCADE removes the portfolios' backtest_runs too.
			_, _ = pool.Exec(ctx, `DELETE FROM portfolios WHERE owner_sub=$1`, ownerSub)
			_, _ = pool.Exec(ctx, `DELETE FROM strategies WHERE short_code='__claimdue_stub__'`)
			pool.Close()
		})
	})

	// seedReady inserts a ready, open-ended portfolio and returns its id.
	seedReady := func(slug string) uuid.UUID {
		Expect(store.Insert(ctx, portfolio.Portfolio{
			OwnerSub:             ownerSub,
			Slug:                 slug,
			Name:                 slug,
			StrategyCode:         "__claimdue_stub__",
			StrategyDescribeJSON: []byte("{}"),
			Parameters:           map[string]any{},
			Benchmark:            "SPY",
			Status:               portfolio.StatusReady,
			RunRetention:         2,
		})).To(Succeed())
		got, err := store.Get(ctx, ownerSub, slug)
		Expect(err).NotTo(HaveOccurred())
		return got.ID
	}

	// completedRunToday creates a run with the given trigger and advances it to
	// a terminal state. started_at is stamped today, and the success status
	// means the run no longer trips the queued/running guard -- isolating the
	// "scheduled run already happened today" gate under test.
	completedRunToday := func(portfolioID uuid.UUID, trigger string) {
		run, err := runStore.CreateRun(ctx, portfolioID, "queued", trigger)
		Expect(err).NotTo(HaveOccurred())
		Expect(runStore.UpdateRunRunning(ctx, run.ID)).To(Succeed())
		Expect(runStore.UpdateRunSuccess(ctx, run.ID, "/tmp/claimdue.sqlite", 100)).To(Succeed())
	}

	It("skips a portfolio whose scheduled run already ran today but claims one with only a manual run", func() {
		scheduledID := seedReady("claimdue-scheduled")
		completedRunToday(scheduledID, "scheduled")

		manualID := seedReady("claimdue-manual")
		completedRunToday(manualID, "manual")

		ids, err := portfolio.ClaimDue(ctx, pool, 10000)
		Expect(err).NotTo(HaveOccurred())

		Expect(ids).NotTo(ContainElement(scheduledID), "a scheduled run today should satisfy the daily run")
		Expect(ids).To(ContainElement(manualID), "a manual run today must not suppress the scheduled daily run")
	})
})
