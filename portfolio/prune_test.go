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
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/portfolio"
)

// seedPrunePortfolio inserts a test portfolio and returns (pool, PoolStore, PoolRunStore, portfolioID, ownerSub, slug).
// DeferCleanup is registered to delete the portfolio at end of the enclosing node.
func seedPrunePortfolio(ctx context.Context, pool *pgxpool.Pool) (portfolio.Store, *portfolio.PoolRunStore, uuid.UUID, string, string) {
	store := portfolio.NewPoolStore(pool)
	runStore := portfolio.NewPoolRunStore(pool)

	ownerSub := "smoke|prune-" + uuid.NewString()[:8]
	slug := "prune-" + uuid.NewString()[:8]

	p := portfolio.Portfolio{
		OwnerSub:     ownerSub,
		Slug:         slug,
		Name:         "Prune test portfolio",
		StrategyCode: "__smoke_stub__",
		Parameters:   map[string]any{},
		Benchmark:    "SPY",
		Status:       portfolio.StatusPending,
		RunRetention: 2, // default retention
	}
	Expect(store.Insert(ctx, p)).To(Succeed())

	DeferCleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM portfolios WHERE owner_sub=$1 AND slug=$2`, ownerSub, slug)
	})

	got, err := store.Get(ctx, ownerSub, slug)
	Expect(err).NotTo(HaveOccurred())

	return store, runStore, got.ID, ownerSub, slug
}

var _ = Describe("PruneRuns", Ordered, func() {
	var (
		ctx  context.Context
		pool *pgxpool.Pool
	)

	BeforeAll(func() {
		dbURL := os.Getenv("PVAPI_SMOKE_DB_URL")
		if dbURL == "" {
			Skip("PVAPI_SMOKE_DB_URL not set; skipping PruneRuns smoke test")
		}
		ctx = context.Background()
		var err error
		pool, err = pgxpool.New(ctx, dbURL)
		Expect(err).NotTo(HaveOccurred())

		// Ensure the strategy stub exists (may already be present from other test suites).
		_, err = pool.Exec(ctx, `
			INSERT INTO strategies (short_code, repo_owner, repo_name, clone_url, is_official)
			VALUES ('__smoke_stub__', 'smoke', 'smoke', '', true)
			ON CONFLICT (short_code) DO NOTHING
		`)
		Expect(err).NotTo(HaveOccurred())

		DeferCleanup(func() {
			pool.Close()
		})
	})

	It("returns no deleted snapshots when run count <= retention", func() {
		store, runStore, portID, _, _ := seedPrunePortfolio(ctx, pool)

		// retention is 2; create only 1 run (no snapshot path)
		run, err := runStore.CreateRun(ctx, portID, "queued")
		Expect(err).NotTo(HaveOccurred())
		_ = run

		deleted, err := store.PruneRuns(ctx, portID)
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeEmpty())
	})

	It("keeps the most recent N runs and returns paths of pruned ones", func() {
		store, runStore, portID, _, _ := seedPrunePortfolio(ctx, pool)

		// Create 4 runs with snapshot paths; retention is 2 so 2 oldest should be pruned.
		var snapshots []string
		for i := 0; i < 4; i++ {
			run, err := runStore.CreateRun(ctx, portID, "queued")
			Expect(err).NotTo(HaveOccurred())
			path := fmt.Sprintf("/tmp/snap-%d-%s.sqlite", i, uuid.NewString()[:8])
			Expect(runStore.UpdateRunSuccess(ctx, run.ID, path, 100)).To(Succeed())
			snapshots = append(snapshots, path)
		}

		deleted, err := store.PruneRuns(ctx, portID)
		Expect(err).NotTo(HaveOccurred())
		// The 2 oldest snapshots should be returned for deletion.
		Expect(deleted).To(ConsistOf(snapshots[0], snapshots[1]))

		rows, err := runStore.ListRuns(ctx, portID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(2))
	})

	It("honors a per-portfolio override", func() {
		store, runStore, portID, ownerSub, slug := seedPrunePortfolio(ctx, pool)

		Expect(store.UpdateRunRetention(ctx, ownerSub, slug, 1)).To(Succeed())

		for i := 0; i < 3; i++ {
			run, err := runStore.CreateRun(ctx, portID, "queued")
			Expect(err).NotTo(HaveOccurred())
			path := fmt.Sprintf("/tmp/x-%d-%s.sqlite", i, uuid.NewString()[:8])
			Expect(runStore.UpdateRunSuccess(ctx, run.ID, path, 100)).To(Succeed())
		}

		deleted, err := store.PruneRuns(ctx, portID)
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(HaveLen(2))

		rows, err := runStore.ListRuns(ctx, portID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
	})
})
