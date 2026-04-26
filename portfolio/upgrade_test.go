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

var _ = Describe("ApplyUpgrade", Ordered, func() {
	var (
		ctx      context.Context
		pool     *pgxpool.Pool
		store    *portfolio.PoolStore
		runStore *portfolio.PoolRunStore
	)

	BeforeAll(func() {
		dbURL := os.Getenv("PVAPI_SMOKE_DB_URL")
		if dbURL == "" {
			Skip("PVAPI_SMOKE_DB_URL not set; skipping ApplyUpgrade smoke test")
		}
		ctx = context.Background()

		var err error
		pool, err = pgxpool.New(ctx, dbURL)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { pool.Close() })
		store = portfolio.NewPoolStore(pool)
		runStore = portfolio.NewPoolRunStore(pool)

		_, err = pool.Exec(ctx, `
			INSERT INTO strategies (short_code, repo_owner, repo_name, clone_url, is_official)
			VALUES ('__smoke_stub__', 'smoke', 'smoke', '', true)
			ON CONFLICT (short_code) DO NOTHING
		`)
		Expect(err).NotTo(HaveOccurred())
	})

	seed := func() uuid.UUID {
		ownerSub := "smoke|" + uuid.NewString()
		slug := "upgrade-" + uuid.NewString()[:8]
		ver := "v1.0.0"
		p := portfolio.Portfolio{
			OwnerSub: ownerSub, Slug: slug, Name: "upgrade smoke",
			StrategyCode: "__smoke_stub__", StrategyVer: &ver,
			StrategyCloneURL: "", StrategyDescribeJSON: []byte(`{}`),
			Parameters: map[string]any{}, Benchmark: "SPY",
			Status: portfolio.StatusReady, RunRetention: 2,
		}
		Expect(store.Insert(ctx, p)).To(Succeed())
		got, err := store.Get(ctx, ownerSub, slug)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_, _ = pool.Exec(ctx, `DELETE FROM portfolios WHERE id=$1`, got.ID)
		})
		return got.ID
	}

	It("replaces version, describe, parameters, preset_name; inserts a queued run; sets status pending", func() {
		portID := seed()

		newDescribe := []byte(`{"shortcode":"__smoke_stub__","name":"smoke","parameters":[{"name":"riskOn","type":"universe"}],"schedule":"@monthend","benchmark":"SPY"}`)
		newParams := []byte(`{"riskOn":"QQQ"}`)
		presetName := "balanced"

		runID, err := store.ApplyUpgrade(ctx, portID,
			"v1.3.0", newDescribe, newParams, &presetName,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(runID).NotTo(Equal(uuid.Nil))

		p, err := store.GetByID(ctx, portID)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.StrategyVer).NotTo(BeNil())
		Expect(*p.StrategyVer).To(Equal("v1.3.0"))
		Expect(string(p.StrategyDescribeJSON)).To(MatchJSON(string(newDescribe)))
		Expect(p.PresetName).NotTo(BeNil())
		Expect(*p.PresetName).To(Equal("balanced"))
		Expect(string(p.Status)).To(Equal("pending"))

		rs, err := runStore.ListRuns(ctx, portID)
		Expect(err).NotTo(HaveOccurred())
		// Newest first; the queued run we just inserted should be index 0.
		Expect(rs).NotTo(BeEmpty())
		Expect(rs[0].Status).To(Equal("queued"))
		Expect(rs[0].ID).To(Equal(runID))
	})

	It("nils preset_name when nil is passed", func() {
		portID := seed()

		runID, err := store.ApplyUpgrade(ctx, portID,
			"v1.3.0", []byte(`{}`), []byte(`{}`), nil,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(runID).NotTo(Equal(uuid.Nil))

		p, err := store.GetByID(ctx, portID)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.PresetName).To(BeNil())
	})

	It("returns ErrNotFound when the portfolio does not exist", func() {
		_, err := store.ApplyUpgrade(ctx, uuid.New(),
			"v1.3.0", []byte(`{}`), []byte(`{}`), nil,
		)
		Expect(err).To(MatchError(portfolio.ErrNotFound))
	})

	_ = fmt.Sprintf // keep fmt import live in case we extend
})
