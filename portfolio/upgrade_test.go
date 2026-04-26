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
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/strategy"
	"github.com/penny-vault/pv-api/types"
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
})

// ---------------------------------------------------------------------------
// Handler skeleton tests
// ---------------------------------------------------------------------------

var _ = Describe("Upgrade handler skeleton", func() {
	const (
		ownerSub = "auth0|user-1"
		slug     = "adm-standard-xxxx"
	)

	installedVer := "v1.0.0"
	portfolioVer := "v1.0.0"
	admDescribeJSON := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"riskOn","type":"universe"}],"presets":[{"name":"standard","parameters":{"riskOn":"VFINX,PRIDX,QQQ"}}],"schedule":"@monthend","benchmark":"SPY"}`)

	var (
		store      *fakeStore
		strategies *fakeStrategyStore
		app        *fiber.App
	)

	// newUpgradeApp wires a fresh app with only the upgrade route registered.
	newUpgradeApp := func(st *fakeStore, ss *fakeStrategyStore, withAuth bool) *fiber.App {
		h := portfolio.NewHandler(st, ss, nil, nil, nil, nil, strategy.EphemeralOptions{})
		a := fiber.New()
		if withAuth {
			a.Use(func(c fiber.Ctx) error {
				sub := c.Get("X-Test-Sub")
				if sub == "" {
					sub = ownerSub
				}
				c.Locals(types.AuthSubjectKey{}, sub)
				return c.Next()
			})
		}
		a.Post("/portfolios/:slug/upgrade", h.Upgrade)
		return a
	}

	// doUpgrade sends POST /portfolios/<slug>/upgrade and returns (status, body).
	doUpgrade := func(a *fiber.App, targetSlug, sub string, body any) (int, map[string]any) {
		var reader io.Reader
		if body != nil {
			b, err := sonic.Marshal(body)
			Expect(err).NotTo(HaveOccurred())
			reader = bytes.NewReader(b)
		}
		req := httptest.NewRequest("POST", "/portfolios/"+targetSlug+"/upgrade", reader)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if sub != "" {
			req.Header.Set("X-Test-Sub", sub)
		}
		resp, err := a.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		rb, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		var out map[string]any
		_ = sonic.Unmarshal(rb, &out)
		return resp.StatusCode, out
	}

	BeforeEach(func() {
		store = &fakeStore{
			rows: []portfolio.Portfolio{{
				ID:                   uuid.Must(uuid.NewV7()),
				OwnerSub:             ownerSub,
				Slug:                 slug,
				Name:                 "ADM standard",
				StrategyCode:         "adm",
				StrategyVer:          &portfolioVer,
				StrategyDescribeJSON: admDescribeJSON,
				Parameters:           map[string]any{"riskOn": "VFINX,PRIDX,QQQ"},
				Benchmark:            "SPY",
				Status:               portfolio.StatusReady,
				RunRetention:         2,
			}},
		}
		strategies = &fakeStrategyStore{
			row: strategy.Strategy{
				ShortCode:    "adm",
				IsOfficial:   true,
				InstalledVer: &installedVer,
				DescribeJSON: admDescribeJSON,
			},
		}
		app = newUpgradeApp(store, strategies, true)
	})

	It("returns 200 already_at_latest when the portfolio is at installed_ver", func() {
		status, body := doUpgrade(app, slug, ownerSub, map[string]any{})
		Expect(status).To(Equal(200))
		Expect(body["status"]).To(Equal("already_at_latest"))
		Expect(body["version"]).To(Equal("v1.0.0"))
		Expect(store.ApplyUpgradeCalls).To(BeEmpty())
	})

	It("returns 404 when the portfolio is missing", func() {
		status, _ := doUpgrade(app, "does-not-exist", ownerSub, map[string]any{})
		Expect(status).To(Equal(404))
	})

	It("returns 409 run_in_progress when status='running'", func() {
		store.rows[0].Status = portfolio.StatusRunning
		status, body := doUpgrade(app, slug, ownerSub, map[string]any{})
		Expect(status).To(Equal(409))
		Expect(body["error"]).To(Equal("run_in_progress"))
		Expect(store.ApplyUpgradeCalls).To(BeEmpty())
	})

	It("returns 422 strategy_not_installable when the strategy is not in the registry", func() {
		store.rows[0].StrategyCode = "unknown-code"
		status, body := doUpgrade(app, slug, ownerSub, map[string]any{})
		Expect(status).To(Equal(422))
		Expect(body["error"]).To(Equal("strategy_not_installable"))
	})

	It("returns 422 strategy_not_installable when InstalledVer is nil", func() {
		strategies.row.InstalledVer = nil
		status, body := doUpgrade(app, slug, ownerSub, map[string]any{})
		Expect(status).To(Equal(422))
		Expect(body["error"]).To(Equal("strategy_not_installable"))
	})

	It("returns 422 strategy_not_installable when InstallError is set", func() {
		installErr := "download failed: timeout"
		strategies.row.InstalledVer = &installedVer
		strategies.row.InstallError = &installErr
		status, body := doUpgrade(app, slug, ownerSub, map[string]any{})
		Expect(status).To(Equal(422))
		Expect(body["error"]).To(Equal("strategy_not_installable"))
	})

	It("returns 401 when no auth subject", func() {
		a := newUpgradeApp(store, strategies, false /* no auth middleware */)
		status, _ := doUpgrade(a, slug, "", map[string]any{})
		Expect(status).To(Equal(401))
	})
})
