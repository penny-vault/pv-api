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
		ctx   context.Context
		pool  *pgxpool.Pool
		store *portfolio.PoolStore
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

	It("replaces version, describe, parameters, preset_name; sets status pending; last_error nil", func() {
		portID := seed()

		newDescribe := []byte(`{"shortcode":"__smoke_stub__","name":"smoke","parameters":[{"name":"riskOn","type":"universe"}],"schedule":"@monthend","benchmark":"SPY"}`)
		newParams := []byte(`{"riskOn":"QQQ"}`)
		presetName := "balanced"

		err := store.ApplyUpgrade(ctx, portID,
			"v1.3.0", newDescribe, newParams, &presetName,
		)
		Expect(err).NotTo(HaveOccurred())

		p, err := store.GetByID(ctx, portID)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.StrategyVer).NotTo(BeNil())
		Expect(*p.StrategyVer).To(Equal("v1.3.0"))
		Expect(string(p.StrategyDescribeJSON)).To(MatchJSON(string(newDescribe)))
		Expect(p.PresetName).NotTo(BeNil())
		Expect(*p.PresetName).To(Equal("balanced"))
		Expect(string(p.Status)).To(Equal("pending"))
		Expect(p.LastError).To(BeNil())
	})

	It("nils preset_name when nil is passed", func() {
		portID := seed()

		err := store.ApplyUpgrade(ctx, portID,
			"v1.3.0", []byte(`{}`), []byte(`{}`), nil,
		)
		Expect(err).NotTo(HaveOccurred())

		p, err := store.GetByID(ctx, portID)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.PresetName).To(BeNil())
	})

	It("returns ErrNotFound when the portfolio does not exist", func() {
		err := store.ApplyUpgrade(ctx, uuid.New(),
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

	It("upgrades when the registry has a newer compatible version (empty body)", func() {
		// Put the portfolio on v1.0.0 and the registry on v1.1.0.
		portfolioVer1 := "v1.0.0"
		registryVer := "v1.1.0"
		portID := uuid.Must(uuid.NewV7())
		singleParamDescribe := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"riskOn","type":"universe"}],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`)

		st := &fakeStore{
			rows: []portfolio.Portfolio{{
				ID:                   portID,
				OwnerSub:             ownerSub,
				Slug:                 slug,
				Name:                 "ADM standard",
				StrategyCode:         "adm",
				StrategyVer:          &portfolioVer1,
				StrategyDescribeJSON: singleParamDescribe,
				Parameters:           map[string]any{"riskOn": "SPY"},
				Benchmark:            "SPY",
				Status:               portfolio.StatusReady,
				RunRetention:         2,
			}},
		}
		ss := &fakeStrategyStore{
			row: strategy.Strategy{
				ShortCode:    "adm",
				IsOfficial:   true,
				InstalledVer: &registryVer,
				DescribeJSON: singleParamDescribe, // same describe → all params kept → compatible
			},
		}
		knownRunID := uuid.Must(uuid.NewV7())
		disp := &countingDispatcher{runID: knownRunID}
		h := portfolio.NewHandler(st, ss, nil, disp, nil, nil, strategy.EphemeralOptions{})
		a := fiber.New()
		a.Use(func(c fiber.Ctx) error {
			c.Locals(types.AuthSubjectKey{}, ownerSub)
			return c.Next()
		})
		a.Post("/portfolios/:slug/upgrade", h.Upgrade)

		status, body := doUpgrade(a, slug, ownerSub, nil /* empty body */)

		Expect(status).To(Equal(200))
		Expect(body["status"]).To(Equal("upgraded"))
		Expect(body["from_version"]).To(Equal("v1.0.0"))
		Expect(body["to_version"]).To(Equal("v1.1.0"))
		Expect(body["run_id"]).To(Equal(knownRunID.String()))
		Expect(st.ApplyUpgradeCalls).To(HaveLen(1))
		Expect(st.ApplyUpgradeCalls[0].PortfolioID).To(Equal(portID))
		Expect(st.ApplyUpgradeCalls[0].NewVer).To(Equal("v1.1.0"))
		var gotParams map[string]any
		Expect(sonic.Unmarshal(st.ApplyUpgradeCalls[0].NewParams, &gotParams)).To(Succeed())
		Expect(gotParams).To(HaveKeyWithValue("riskOn", "SPY"))
		Expect(disp.SubmitCalls).To(HaveLen(1))
		Expect(disp.SubmitCalls[0]).To(Equal(portID))
	})

	It("returns 409 parameters_incompatible with a diff body when params changed", func() {
		// Portfolio at v1.0.0 with parameters {"riskOn":"SPY"}.
		// Old describe declares only `riskOn` of type universe.
		// New describe declares only `riskOff` of type universe (no default).
		portfolioVer1 := "v1.0.0"
		registryVer := "v2.0.0"
		portID := uuid.Must(uuid.NewV7())
		oldDescribeJSON := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"riskOn","type":"universe"}],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`)
		newDescribeJSON := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"riskOff","type":"universe"}],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`)

		st := &fakeStore{
			rows: []portfolio.Portfolio{{
				ID:                   portID,
				OwnerSub:             ownerSub,
				Slug:                 slug,
				Name:                 "ADM standard",
				StrategyCode:         "adm",
				StrategyVer:          &portfolioVer1,
				StrategyDescribeJSON: oldDescribeJSON,
				Parameters:           map[string]any{"riskOn": "SPY"},
				Benchmark:            "SPY",
				Status:               portfolio.StatusReady,
				RunRetention:         2,
			}},
		}
		ss := &fakeStrategyStore{
			row: strategy.Strategy{
				ShortCode:    "adm",
				IsOfficial:   true,
				InstalledVer: &registryVer,
				DescribeJSON: newDescribeJSON,
			},
		}
		h := portfolio.NewHandler(st, ss, nil, nil, nil, nil, strategy.EphemeralOptions{})
		a := fiber.New()
		a.Use(func(c fiber.Ctx) error {
			c.Locals(types.AuthSubjectKey{}, ownerSub)
			return c.Next()
		})
		a.Post("/portfolios/:slug/upgrade", h.Upgrade)

		// POST with empty body — expect 409 parameters_incompatible.
		status, body := doUpgrade(a, slug, ownerSub, nil)

		Expect(status).To(Equal(409))
		Expect(body["error"]).To(Equal("parameters_incompatible"))
		Expect(body["from_version"]).To(Equal("v1.0.0"))
		Expect(body["to_version"]).To(Equal("v2.0.0"))

		incompat, ok := body["incompatibilities"].(map[string]any)
		Expect(ok).To(BeTrue())
		removed, ok := incompat["removed"].([]any)
		Expect(ok).To(BeTrue())
		Expect(removed).To(ContainElement("riskOn"))
		addedNoDefault, ok := incompat["added_without_default"].([]any)
		Expect(ok).To(BeTrue())
		Expect(addedNoDefault).To(ContainElement("riskOff"))

		Expect(st.ApplyUpgradeCalls).To(BeEmpty())
	})

	It("accepts a resubmit with valid parameters", func() {
		// Same incompatible seed: old=riskOn, new=riskOff (no default).
		portfolioVer1 := "v1.0.0"
		registryVer := "v2.0.0"
		portID := uuid.Must(uuid.NewV7())
		oldDescribeJSON := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"riskOn","type":"universe"}],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`)
		newDescribeJSON := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"riskOff","type":"universe"}],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`)

		st := &fakeStore{
			rows: []portfolio.Portfolio{{
				ID:                   portID,
				OwnerSub:             ownerSub,
				Slug:                 slug,
				Name:                 "ADM standard",
				StrategyCode:         "adm",
				StrategyVer:          &portfolioVer1,
				StrategyDescribeJSON: oldDescribeJSON,
				Parameters:           map[string]any{"riskOn": "SPY"},
				Benchmark:            "SPY",
				Status:               portfolio.StatusReady,
				RunRetention:         2,
			}},
		}
		ss := &fakeStrategyStore{
			row: strategy.Strategy{
				ShortCode:    "adm",
				IsOfficial:   true,
				InstalledVer: &registryVer,
				DescribeJSON: newDescribeJSON,
			},
		}
		knownRunID := uuid.Must(uuid.NewV7())
		disp := &countingDispatcher{runID: knownRunID}
		h := portfolio.NewHandler(st, ss, nil, disp, nil, nil, strategy.EphemeralOptions{})
		a := fiber.New()
		a.Use(func(c fiber.Ctx) error {
			c.Locals(types.AuthSubjectKey{}, ownerSub)
			return c.Next()
		})
		a.Post("/portfolios/:slug/upgrade", h.Upgrade)

		// POST with valid parameters for the new describe.
		status, body := doUpgrade(a, slug, ownerSub, map[string]any{"parameters": map[string]any{"riskOff": "QQQ"}})

		Expect(status).To(Equal(200))
		Expect(body["status"]).To(Equal("upgraded"))
		Expect(st.ApplyUpgradeCalls).To(HaveLen(1))
		Expect(st.ApplyUpgradeCalls[0].PortfolioID).To(Equal(portID))
		var gotParams map[string]any
		Expect(sonic.Unmarshal(st.ApplyUpgradeCalls[0].NewParams, &gotParams)).To(Succeed())
		Expect(gotParams).To(HaveKeyWithValue("riskOff", "QQQ"))
		Expect(disp.SubmitCalls).To(HaveLen(1))
		Expect(disp.SubmitCalls[0]).To(Equal(portID))
	})

	It("rejects a resubmit with invalid parameters as 400", func() {
		// Same incompatible seed: old=riskOn, new=riskOff (no default).
		portfolioVer1 := "v1.0.0"
		registryVer := "v2.0.0"
		portID := uuid.Must(uuid.NewV7())
		oldDescribeJSON := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"riskOn","type":"universe"}],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`)
		newDescribeJSON := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"riskOff","type":"universe"}],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`)

		st := &fakeStore{
			rows: []portfolio.Portfolio{{
				ID:                   portID,
				OwnerSub:             ownerSub,
				Slug:                 slug,
				Name:                 "ADM standard",
				StrategyCode:         "adm",
				StrategyVer:          &portfolioVer1,
				StrategyDescribeJSON: oldDescribeJSON,
				Parameters:           map[string]any{"riskOn": "SPY"},
				Benchmark:            "SPY",
				Status:               portfolio.StatusReady,
				RunRetention:         2,
			}},
		}
		ss := &fakeStrategyStore{
			row: strategy.Strategy{
				ShortCode:    "adm",
				IsOfficial:   true,
				InstalledVer: &registryVer,
				DescribeJSON: newDescribeJSON,
			},
		}
		h := portfolio.NewHandler(st, ss, nil, nil, nil, nil, strategy.EphemeralOptions{})
		a := fiber.New()
		a.Use(func(c fiber.Ctx) error {
			c.Locals(types.AuthSubjectKey{}, ownerSub)
			return c.Next()
		})
		a.Post("/portfolios/:slug/upgrade", h.Upgrade)

		// POST with unknown parameter.
		status, body := doUpgrade(a, slug, ownerSub, map[string]any{"parameters": map[string]any{"unknown": "X"}})

		Expect(status).To(Equal(400))
		detail, _ := body["detail"].(string)
		Expect(detail).To(ContainSubstring("unknown parameter"))
		Expect(st.ApplyUpgradeCalls).To(BeEmpty())
	})

	It("rejects a resubmit missing a required parameter as 400", func() {
		// Same incompatible seed: old=riskOn, new=riskOff (no default).
		portfolioVer1 := "v1.0.0"
		registryVer := "v2.0.0"
		portID := uuid.Must(uuid.NewV7())
		oldDescribeJSON := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"riskOn","type":"universe"}],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`)
		newDescribeJSON := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"riskOff","type":"universe"}],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`)

		st := &fakeStore{
			rows: []portfolio.Portfolio{{
				ID:                   portID,
				OwnerSub:             ownerSub,
				Slug:                 slug,
				Name:                 "ADM standard",
				StrategyCode:         "adm",
				StrategyVer:          &portfolioVer1,
				StrategyDescribeJSON: oldDescribeJSON,
				Parameters:           map[string]any{"riskOn": "SPY"},
				Benchmark:            "SPY",
				Status:               portfolio.StatusReady,
				RunRetention:         2,
			}},
		}
		ss := &fakeStrategyStore{
			row: strategy.Strategy{
				ShortCode:    "adm",
				IsOfficial:   true,
				InstalledVer: &registryVer,
				DescribeJSON: newDescribeJSON,
			},
		}
		h := portfolio.NewHandler(st, ss, nil, nil, nil, nil, strategy.EphemeralOptions{})
		a := fiber.New()
		a.Use(func(c fiber.Ctx) error {
			c.Locals(types.AuthSubjectKey{}, ownerSub)
			return c.Next()
		})
		a.Post("/portfolios/:slug/upgrade", h.Upgrade)

		// POST with empty parameters — riskOff is required and missing.
		status, body := doUpgrade(a, slug, ownerSub, map[string]any{"parameters": map[string]any{}})

		Expect(status).To(Equal(400))
		detail, _ := body["detail"].(string)
		Expect(detail).To(ContainSubstring("missing required parameter"))
		Expect(st.ApplyUpgradeCalls).To(BeEmpty())
	})

	It("auto-fills added-with-default parameters on a compatible upgrade", func() {
		// Portfolio at v1.0.0 with only param "a"; new describe adds "b" with default "y".
		portfolioVer1 := "v1.0.0"
		registryVer := "v2.0.0"
		portID := uuid.Must(uuid.NewV7())
		oldDescribe := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"a","type":"string"}],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`)
		newDescribe := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"a","type":"string"},{"name":"b","type":"string","default":"y"}],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`)

		st := &fakeStore{
			rows: []portfolio.Portfolio{{
				ID:                   portID,
				OwnerSub:             ownerSub,
				Slug:                 slug,
				Name:                 "ADM standard",
				StrategyCode:         "adm",
				StrategyVer:          &portfolioVer1,
				StrategyDescribeJSON: oldDescribe,
				Parameters:           map[string]any{"a": "x"},
				Benchmark:            "SPY",
				Status:               portfolio.StatusReady,
				RunRetention:         2,
			}},
		}
		ss := &fakeStrategyStore{
			row: strategy.Strategy{
				ShortCode:    "adm",
				IsOfficial:   true,
				InstalledVer: &registryVer,
				DescribeJSON: newDescribe,
			},
		}
		disp := &countingDispatcher{runID: uuid.Must(uuid.NewV7())}
		h := portfolio.NewHandler(st, ss, nil, disp, nil, nil, strategy.EphemeralOptions{})
		a := fiber.New()
		a.Use(func(c fiber.Ctx) error {
			c.Locals(types.AuthSubjectKey{}, ownerSub)
			return c.Next()
		})
		a.Post("/portfolios/:slug/upgrade", h.Upgrade)

		status, body := doUpgrade(a, slug, ownerSub, nil /* empty body */)

		Expect(status).To(Equal(200))
		Expect(body["status"]).To(Equal("upgraded"))
		Expect(st.ApplyUpgradeCalls).To(HaveLen(1))
		var gotParams map[string]any
		Expect(sonic.Unmarshal(st.ApplyUpgradeCalls[0].NewParams, &gotParams)).To(Succeed())
		Expect(gotParams).To(HaveKeyWithValue("a", "x"))
		Expect(gotParams).To(HaveKeyWithValue("b", "y"))
	})
})
