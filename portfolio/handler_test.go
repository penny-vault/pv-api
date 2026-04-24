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
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/openapi"
	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/strategy"
	"github.com/penny-vault/pv-api/types"
)

// countingDispatcher is a portfolio.Dispatcher that records calls and
// returns a canned (runID, err). Pointer receiver for Submit so call
// counts persist across the interface boundary.
type countingDispatcher struct {
	calls atomic.Int64
	runID uuid.UUID
	err   error
}

func (d *countingDispatcher) Submit(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	d.calls.Add(1)
	return d.runID, d.err
}

// fakeStore is a trivial in-memory implementation of portfolio.Store.
type fakeStore struct {
	rows []portfolio.Portfolio
}

func (f *fakeStore) List(_ context.Context, ownerSub string) ([]portfolio.Portfolio, error) {
	out := make([]portfolio.Portfolio, 0)
	for _, p := range f.rows {
		if p.OwnerSub == ownerSub {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, ownerSub, slug string) (portfolio.Portfolio, error) {
	for _, p := range f.rows {
		if p.OwnerSub == ownerSub && p.Slug == slug {
			return p, nil
		}
	}
	return portfolio.Portfolio{}, portfolio.ErrNotFound
}

func (f *fakeStore) Insert(_ context.Context, p portfolio.Portfolio) error {
	for _, existing := range f.rows {
		if existing.OwnerSub == p.OwnerSub && existing.Slug == p.Slug {
			return portfolio.ErrDuplicateSlug
		}
	}
	p.ID = uuid.Must(uuid.NewV7())
	p.CreatedAt = time.Now().UTC()
	p.UpdatedAt = p.CreatedAt
	f.rows = append(f.rows, p)
	return nil
}

func (f *fakeStore) UpdateName(_ context.Context, ownerSub, slug, name string) error {
	for i, p := range f.rows {
		if p.OwnerSub == ownerSub && p.Slug == slug {
			f.rows[i].Name = name
			f.rows[i].UpdatedAt = time.Now().UTC()
			return nil
		}
	}
	return portfolio.ErrNotFound
}

func (f *fakeStore) Delete(_ context.Context, ownerSub, slug string) error {
	for i, p := range f.rows {
		if p.OwnerSub == ownerSub && p.Slug == slug {
			f.rows = append(f.rows[:i], f.rows[i+1:]...)
			return nil
		}
	}
	return portfolio.ErrNotFound
}

// RunStore stub methods — not exercised by handler tests.

func (f *fakeStore) CreateRun(_ context.Context, _ uuid.UUID, _ string) (portfolio.Run, error) {
	return portfolio.Run{}, nil
}

func (f *fakeStore) UpdateRunRunning(_ context.Context, _ uuid.UUID) error { return nil }

func (f *fakeStore) UpdateRunSuccess(_ context.Context, _ uuid.UUID, _ string, _ int32) error {
	return nil
}

func (f *fakeStore) UpdateRunFailed(_ context.Context, _ uuid.UUID, _ string, _ int32) error {
	return nil
}

func (f *fakeStore) ListRuns(_ context.Context, _ uuid.UUID) ([]portfolio.Run, error) {
	return nil, nil
}

func (f *fakeStore) GetRun(_ context.Context, _, _ uuid.UUID) (portfolio.Run, error) {
	return portfolio.Run{}, portfolio.ErrNotFound
}

// ClaimDue stub — handler tests do not exercise the scheduler path.
func (f *fakeStore) ClaimDue(_ context.Context, _ int) ([]uuid.UUID, error) {
	return nil, nil
}

func (f *fakeStore) UpdateDates(_ context.Context, ownerSub, slug string, startDate, endDate *time.Time) error {
	for i, p := range f.rows {
		if p.OwnerSub == ownerSub && p.Slug == slug {
			if startDate != nil {
				f.rows[i].StartDate = startDate
			}
			if endDate != nil {
				f.rows[i].EndDate = endDate
			}
			f.rows[i].UpdatedAt = time.Now().UTC()
			return nil
		}
	}
	return portfolio.ErrNotFound
}

// fakeStrategyStore implements strategy.ReadStore. Returns one configured
// strategy; anything else is ErrNotFound.
type fakeStrategyStore struct {
	row strategy.Strategy
}

func (f *fakeStrategyStore) List(_ context.Context) ([]strategy.Strategy, error) {
	return []strategy.Strategy{f.row}, nil
}

func (f *fakeStrategyStore) Get(_ context.Context, shortCode string) (strategy.Strategy, error) {
	if shortCode == f.row.ShortCode {
		return f.row, nil
	}
	return strategy.Strategy{}, strategy.ErrNotFound
}

var _ = Describe("portfolio.Handler", func() {
	var (
		store      *fakeStore
		strategies *fakeStrategyStore
		app        *fiber.App
	)

	const (
		sub1 = "auth0|user-1"
		sub2 = "auth0|user-2"
	)

	installed := "v1.0.0"
	admDescribeJSON := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"riskOn","type":"universe"}],"presets":[{"name":"standard","parameters":{"riskOn":"VFINX,PRIDX,QQQ"}}],"schedule":"@monthend","benchmark":"SPY"}`)

	BeforeEach(func() {
		store = &fakeStore{}
		strategies = &fakeStrategyStore{
			row: strategy.Strategy{
				ShortCode:    "adm",
				IsOfficial:   true,
				InstalledVer: &installed,
				DescribeJSON: admDescribeJSON,
			},
		}
		h := portfolio.NewHandler(store, strategies, nil, nil, nil, nil, strategy.EphemeralOptions{})

		app = fiber.New()
		app.Use(func(c fiber.Ctx) error {
			sub := c.Get("X-Test-Sub")
			if sub == "" {
				sub = sub1
			}
			c.Locals(types.AuthSubjectKey{}, sub)
			return c.Next()
		})
		app.Get("/portfolios", h.List)
		app.Post("/portfolios", h.Create)
		app.Get("/portfolios/:slug", h.Get)
		app.Patch("/portfolios/:slug", h.Patch)
		app.Delete("/portfolios/:slug", h.Delete)
	})

	request := func(method, path, sub string, body any) (int, []byte, string) {
		var reader io.Reader
		if body != nil {
			b, err := sonic.Marshal(body)
			Expect(err).NotTo(HaveOccurred())
			reader = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, reader)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if sub != "" {
			req.Header.Set("X-Test-Sub", sub)
		}
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		rb, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		return resp.StatusCode, rb, resp.Header.Get("Content-Type")
	}

	It("creates a portfolio and returns 201 with the slug", func() {
		status, body, _ := request("POST", "/portfolios", sub1, map[string]any{
			"name":         "ADM standard",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "VFINX,PRIDX,QQQ"},
		})
		Expect(status).To(Equal(201))
		var out map[string]any
		Expect(sonic.Unmarshal(body, &out)).To(Succeed())
		Expect(out["slug"]).To(MatchRegexp(`^adm-standard-[a-z2-7]{4}$`))
		Expect(out["presetName"]).To(Equal("standard"))
		Expect(out["strategyVer"]).To(Equal("v1.0.0"))
		Expect(out["benchmark"]).To(Equal("SPY"))
	})

	It("rejects an unknown strategy code with 422", func() {
		status, _, _ := request("POST", "/portfolios", sub1, map[string]any{
			"name":         "x",
			"strategyCode": "nope",
			"parameters":   map[string]any{"riskOn": "SPY"},
		})
		Expect(status).To(Equal(422))
	})

	It("returns 409 when the same user creates the same config twice", func() {
		body := map[string]any{
			"name":         "ADM standard",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "VFINX,PRIDX,QQQ"},
		}
		status, _, _ := request("POST", "/portfolios", sub1, body)
		Expect(status).To(Equal(201))
		status, _, _ = request("POST", "/portfolios", sub1, body)
		Expect(status).To(Equal(409))
	})

	It("lets two different users create the same config", func() {
		body := map[string]any{
			"name":         "ADM standard",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "VFINX,PRIDX,QQQ"},
		}
		s1, _, _ := request("POST", "/portfolios", sub1, body)
		s2, _, _ := request("POST", "/portfolios", sub2, body)
		Expect(s1).To(Equal(201))
		Expect(s2).To(Equal(201))
	})

	It("scopes list to the caller's portfolios only", func() {
		body := map[string]any{
			"name":         "mine",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "SPY"},
		}
		_, _, _ = request("POST", "/portfolios", sub1, body)
		_, _, _ = request("POST", "/portfolios", sub2, body)

		status, listBody, _ := request("GET", "/portfolios", sub1, nil)
		Expect(status).To(Equal(200))

		var list []map[string]any
		Expect(sonic.Unmarshal(listBody, &list)).To(Succeed())
		Expect(list).To(HaveLen(1))
	})

	It("returns 404 when another user reads your portfolio", func() {
		body := map[string]any{
			"name":         "mine",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "VFINX,PRIDX,QQQ"},
		}
		_, createdBody, _ := request("POST", "/portfolios", sub1, body)
		var created map[string]any
		Expect(sonic.Unmarshal(createdBody, &created)).To(Succeed())
		slug := created["slug"].(string)

		status, _, _ := request("GET", "/portfolios/"+slug, sub2, nil)
		Expect(status).To(Equal(404))
	})

	It("patches the name and returns the updated portfolio", func() {
		_, createdBody, _ := request("POST", "/portfolios", sub1, map[string]any{
			"name":         "before",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "SPY"},
		})
		var created map[string]any
		Expect(sonic.Unmarshal(createdBody, &created)).To(Succeed())
		slug := created["slug"].(string)

		status, body, _ := request("PATCH", "/portfolios/"+slug, sub1, map[string]any{"name": "after"})
		Expect(status).To(Equal(200))
		var out map[string]any
		Expect(sonic.Unmarshal(body, &out)).To(Succeed())
		Expect(out["name"]).To(Equal("after"))
	})

	It("rejects PATCH with fields other than name", func() {
		_, createdBody, _ := request("POST", "/portfolios", sub1, map[string]any{
			"name":         "x",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "SPY"},
		})
		var created map[string]any
		Expect(sonic.Unmarshal(createdBody, &created)).To(Succeed())
		slug := created["slug"].(string)

		status, _, _ := request("PATCH", "/portfolios/"+slug, sub1, map[string]any{
			"name":      "new",
			"benchmark": "QQQ",
		})
		Expect(status).To(Equal(422))
	})

	It("patches startDate and endDate and returns them in the response", func() {
		_, createdBody, _ := request("POST", "/portfolios", sub1, map[string]any{
			"name":         "dated",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "SPY"},
		})
		var created map[string]any
		Expect(sonic.Unmarshal(createdBody, &created)).To(Succeed())
		slug := created["slug"].(string)

		status, body, _ := request("PATCH", "/portfolios/"+slug, sub1, map[string]any{
			"startDate": "2020-01-01",
			"endDate":   "2024-12-31",
		})
		Expect(status).To(Equal(200))
		var out map[string]any
		Expect(sonic.Unmarshal(body, &out)).To(Succeed())
		Expect(out["startDate"]).To(Equal("2020-01-01"))
		Expect(out["endDate"]).To(Equal("2024-12-31"))
	})

	It("rejects PATCH when endDate is before startDate with 422", func() {
		_, createdBody, _ := request("POST", "/portfolios", sub1, map[string]any{
			"name":         "bad-dates",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "SPY"},
		})
		var created map[string]any
		Expect(sonic.Unmarshal(createdBody, &created)).To(Succeed())
		slug := created["slug"].(string)

		status, _, _ := request("PATCH", "/portfolios/"+slug, sub1, map[string]any{
			"startDate": "2024-01-01",
			"endDate":   "2020-01-01",
		})
		Expect(status).To(Equal(422))
	})

	It("deletes the portfolio and subsequent GET is 404", func() {
		_, createdBody, _ := request("POST", "/portfolios", sub1, map[string]any{
			"name":         "goner",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "SPY"},
		})
		var created map[string]any
		Expect(sonic.Unmarshal(createdBody, &created)).To(Succeed())
		slug := created["slug"].(string)

		status, _, _ := request("DELETE", "/portfolios/"+slug, sub1, nil)
		Expect(status).To(Equal(204))

		status, _, _ = request("GET", "/portfolios/"+slug, sub1, nil)
		Expect(status).To(Equal(404))
	})

	It("returns status in the GET /portfolios/:slug response", func() {
		store.rows = []portfolio.Portfolio{{
			ID:           uuid.Must(uuid.NewV7()),
			OwnerSub:     sub1,
			Slug:         "adm-ready-0001",
			Name:         "ADM ready",
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY"},
			Benchmark:    "SPY",
			Status:       portfolio.StatusReady,
			CreatedAt:    time.Now().UTC(),
			UpdatedAt:    time.Now().UTC(),
		}}

		httpStatus, body, _ := request("GET", "/portfolios/adm-ready-0001", sub1, nil)
		Expect(httpStatus).To(Equal(200))
		var out map[string]any
		Expect(sonic.Unmarshal(body, &out)).To(Succeed())
		Expect(out["status"]).To(Equal("ready"))
	})
})

// Minimal fakes for derived-endpoint specs.
type fakeSnapshotOpener struct {
	readers map[string]portfolio.SnapshotReader
}

func (f *fakeSnapshotOpener) Open(path string) (portfolio.SnapshotReader, error) {
	r, ok := f.readers[path]
	if !ok {
		return nil, errors.New("fake opener: unknown path " + path)
	}
	return r, nil
}

type fakeSnapshotReader struct {
	summary *openapi.PortfolioSummary
	metrics *openapi.PortfolioMetrics
}

func (f *fakeSnapshotReader) Close() error { return nil }
func (f *fakeSnapshotReader) Summary(_ context.Context) (*openapi.PortfolioSummary, error) {
	return f.summary, nil
}
func (f *fakeSnapshotReader) Drawdowns(_ context.Context) ([]openapi.Drawdown, error) {
	return nil, nil
}
func (f *fakeSnapshotReader) Statistics(_ context.Context) ([]openapi.PortfolioStatistic, error) {
	return nil, nil
}
func (f *fakeSnapshotReader) TrailingReturns(_ context.Context) ([]openapi.TrailingReturnRow, error) {
	return nil, nil
}
func (f *fakeSnapshotReader) CurrentHoldings(_ context.Context) (*openapi.HoldingsResponse, error) {
	return nil, nil
}
func (f *fakeSnapshotReader) HoldingsAsOf(_ context.Context, _ time.Time) (*openapi.HoldingsResponse, error) {
	return nil, nil
}
func (f *fakeSnapshotReader) HoldingsHistory(_ context.Context, _, _ *time.Time) (*openapi.HoldingsHistoryResponse, error) {
	return nil, nil
}
func (f *fakeSnapshotReader) Performance(_ context.Context, _ string, _, _ *time.Time) (*openapi.PortfolioPerformance, error) {
	return nil, nil
}
func (f *fakeSnapshotReader) Transactions(_ context.Context, _ portfolio.SnapshotTxFilter) (*openapi.TransactionsResponse, error) {
	return nil, nil
}
func (f *fakeSnapshotReader) Metrics(_ context.Context, _, _ []string) (*openapi.PortfolioMetrics, error) {
	return f.metrics, nil
}

var _ = Describe("Handler.Summary", func() {
	var (
		app    *fiber.App
		store  *fakeStore
		opener *fakeSnapshotOpener
		sub    = "auth0|owner"
	)

	BeforeEach(func() {
		store = &fakeStore{}
		opener = &fakeSnapshotOpener{readers: map[string]portfolio.SnapshotReader{}}
		app = fiber.New(fiber.Config{JSONEncoder: sonic.Marshal, JSONDecoder: sonic.Unmarshal})
		app.Use(func(c fiber.Ctx) error {
			c.Locals(types.AuthSubjectKey{}, sub)
			return c.Next()
		})
		h := portfolio.NewHandler(store, &fakeStrategyStore{}, opener, nil, nil, nil, strategy.EphemeralOptions{})
		app.Get("/portfolios/:slug/summary", h.Summary)
	})

	It("returns 404 with 'no successful run' when status=pending", func() {
		store.rows = []portfolio.Portfolio{{
			ID: uuid.Must(uuid.NewV7()), OwnerSub: sub, Slug: "s1",
			Status: portfolio.StatusPending, SnapshotPath: nil,
		}}

		req := httptest.NewRequest("GET", "/portfolios/s1/summary", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusNotFound))

		body, _ := io.ReadAll(resp.Body)
		Expect(string(body)).To(ContainSubstring("no successful run"))
	})

	It("returns 200 with the summary payload when the snapshot opens", func() {
		path := "/fake/snap.sqlite"
		store.rows = []portfolio.Portfolio{{
			ID: uuid.Must(uuid.NewV7()), OwnerSub: sub, Slug: "s1",
			Status: portfolio.StatusReady, SnapshotPath: &path,
		}}
		wantSummary := &openapi.PortfolioSummary{
			CurrentValue: 103000,
			Sharpe:       1.23,
		}
		opener.readers[path] = &fakeSnapshotReader{summary: wantSummary}

		req := httptest.NewRequest("GET", "/portfolios/s1/summary", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))

		body, _ := io.ReadAll(resp.Body)
		var got openapi.PortfolioSummary
		Expect(sonic.Unmarshal(body, &got)).To(Succeed())
		Expect(got.CurrentValue).To(Equal(wantSummary.CurrentValue))
	})
})

var _ = Describe("Handler.Metrics", func() {
	var (
		app    *fiber.App
		store  *fakeStore
		opener *fakeSnapshotOpener
		sub    = "auth0|owner"
	)

	BeforeEach(func() {
		store = &fakeStore{}
		opener = &fakeSnapshotOpener{readers: map[string]portfolio.SnapshotReader{}}
		app = fiber.New(fiber.Config{JSONEncoder: sonic.Marshal, JSONDecoder: sonic.Unmarshal})
		app.Use(func(c fiber.Ctx) error {
			c.Locals(types.AuthSubjectKey{}, sub)
			return c.Next()
		})
		h := portfolio.NewHandler(store, &fakeStrategyStore{}, opener, nil, nil, nil, strategy.EphemeralOptions{})
		app.Get("/portfolios/:slug/metrics", h.Metrics)
	})

	It("returns 404 when portfolio has no snapshot", func() {
		store.rows = []portfolio.Portfolio{{
			ID: uuid.Must(uuid.NewV7()), OwnerSub: sub, Slug: "s1",
			Status: portfolio.StatusPending, SnapshotPath: nil,
		}}
		req := httptest.NewRequest("GET", "/portfolios/s1/metrics", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusNotFound))
	})

	It("returns 200 with metrics payload", func() {
		path := "/fake/snap.sqlite"
		store.rows = []portfolio.Portfolio{{
			ID: uuid.Must(uuid.NewV7()), OwnerSub: sub, Slug: "s1",
			Status: portfolio.StatusReady, SnapshotPath: &path,
		}}
		sharpeVal := 1.55
		g := openapi.MetricGroup{"Sharpe": []*float64{&sharpeVal}}
		want := &openapi.PortfolioMetrics{
			Windows: []string{"since_inception"},
			Summary: &g,
		}
		opener.readers[path] = &fakeSnapshotReader{metrics: want}

		req := httptest.NewRequest("GET", "/portfolios/s1/metrics", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))

		body, _ := io.ReadAll(resp.Body)
		var got openapi.PortfolioMetrics
		Expect(sonic.Unmarshal(body, &got)).To(Succeed())
		Expect(got.Windows).To(Equal([]string{"since_inception"}))
	})

	It("passes window and metric query params to reader", func() {
		path := "/fake/snap.sqlite"
		store.rows = []portfolio.Portfolio{{
			ID: uuid.Must(uuid.NewV7()), OwnerSub: sub, Slug: "s1",
			Status: portfolio.StatusReady, SnapshotPath: &path,
		}}

		var capturedWindows, capturedMetrics []string
		capturingReader := &capturingMetricsReader{
			fakeSnapshotReader: &fakeSnapshotReader{
				metrics: &openapi.PortfolioMetrics{Windows: []string{"since_inception", "1yr"}},
			},
			onMetrics: func(w, m []string) {
				capturedWindows = w
				capturedMetrics = m
			},
		}
		opener.readers[path] = capturingReader

		req := httptest.NewRequest("GET", "/portfolios/s1/metrics?window=since_inception,1yr&metric=Sharpe,Beta", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))
		Expect(capturedWindows).To(Equal([]string{"since_inception", "1yr"}))
		Expect(capturedMetrics).To(Equal([]string{"Sharpe", "Beta"}))
	})
})

type capturingMetricsReader struct {
	*fakeSnapshotReader
	onMetrics func(windows, metrics []string)
}

func (c *capturingMetricsReader) Metrics(ctx context.Context, windows, metrics []string) (*openapi.PortfolioMetrics, error) {
	c.onMetrics(windows, metrics)
	return c.fakeSnapshotReader.Metrics(ctx, windows, metrics)
}

var _ = Describe("Create with date period", func() {
	installedVer := "v1.0.0"
	describeJSON := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"riskOn","type":"universe"}],"presets":[{"name":"standard","parameters":{"riskOn":"VFINX,PRIDX,QQQ"}}],"schedule":"@monthend","benchmark":"SPY"}`)

	newSetup := func(disp portfolio.Dispatcher) (*fakeStore, *fiber.App) {
		store := &fakeStore{}
		strategies := &fakeStrategyStore{
			row: strategy.Strategy{
				ShortCode:    "adm",
				IsOfficial:   true,
				InstalledVer: &installedVer,
				DescribeJSON: describeJSON,
			},
		}
		h := portfolio.NewHandler(store, strategies, nil, disp, nil, nil, strategy.EphemeralOptions{})
		app := fiber.New()
		app.Use(func(c fiber.Ctx) error {
			c.Locals(types.AuthSubjectKey{}, "auth0|user-1")
			return c.Next()
		})
		app.Post("/portfolios", h.Create)
		return store, app
	}

	It("always submits a run on creation", func() {
		disp := &countingDispatcher{runID: uuid.Must(uuid.NewV7())}
		_, app := newSetup(disp)

		body := `{"name":"test","strategyCode":"adm","parameters":{"riskOn":"VFINX,PRIDX,QQQ"}}`
		req := httptest.NewRequest("POST", "/portfolios", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusCreated))
		Expect(disp.calls.Load()).To(Equal(int64(1)))
	})

	It("stores startDate and endDate on the portfolio", func() {
		disp := &countingDispatcher{runID: uuid.Must(uuid.NewV7())}
		store, app := newSetup(disp)

		body := `{"name":"dated","strategyCode":"adm","parameters":{"riskOn":"VFINX,PRIDX,QQQ"},"startDate":"2020-01-01","endDate":"2024-12-31"}`
		req := httptest.NewRequest("POST", "/portfolios", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusCreated))
		Expect(store.rows).To(HaveLen(1))
		Expect(store.rows[0].StartDate).NotTo(BeNil())
		Expect(store.rows[0].StartDate.Format("2006-01-02")).To(Equal("2020-01-01"))
		Expect(store.rows[0].EndDate).NotTo(BeNil())
		Expect(store.rows[0].EndDate.Format("2006-01-02")).To(Equal("2024-12-31"))
	})

	It("returns 422 for an invalid startDate format", func() {
		disp := &countingDispatcher{runID: uuid.Must(uuid.NewV7())}
		_, app := newSetup(disp)

		body := `{"name":"x","strategyCode":"adm","parameters":{"riskOn":"VFINX,PRIDX,QQQ"},"startDate":"not-a-date"}`
		req := httptest.NewRequest("POST", "/portfolios", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnprocessableEntity))
	})

	It("returns 422 when endDate is before startDate", func() {
		disp := &countingDispatcher{runID: uuid.Must(uuid.NewV7())}
		_, app := newSetup(disp)

		body := `{"name":"x","strategyCode":"adm","parameters":{"riskOn":"VFINX,PRIDX,QQQ"},"startDate":"2024-01-01","endDate":"2020-01-01"}`
		req := httptest.NewRequest("POST", "/portfolios", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnprocessableEntity))
	})

	It("rolls back the portfolio row and returns 503 when dispatcher is full", func() {
		disp := &countingDispatcher{err: portfolio.ErrQueueFull}
		store, app := newSetup(disp)

		body := `{"name":"test","strategyCode":"adm","parameters":{"riskOn":"VFINX,PRIDX,QQQ"}}`
		req := httptest.NewRequest("POST", "/portfolios", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusServiceUnavailable))
		Expect(store.rows).To(BeEmpty())
	})
})

// buildFakeStrategyBin compiles strategy/testdata/fake-strategy-src into a
// tempdir and returns the binary path. Caller owns cleanup of filepath.Dir(bin).
func buildFakeStrategyBin() string {
	src, err := filepath.Abs("../strategy/testdata/fake-strategy-src")
	Expect(err).NotTo(HaveOccurred())
	dir, err := os.MkdirTemp("", "fakebin-*")
	Expect(err).NotTo(HaveOccurred())
	bin := filepath.Join(dir, "strategy.bin")
	cmd := exec.Command("go", "build", "-o", bin, ".") //nolint:gosec // test helper; path is internal
	cmd.Dir = src
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	Expect(cmd.Run()).To(Succeed(), buf.String())
	return bin
}

var _ = Describe("POST /portfolios with strategyCloneUrl", func() {
	const sub = "auth0|user-1"

	newApp := func(builder strategy.BuilderFunc, urlValidator strategy.URLValidatorFunc) (*fakeStore, *fiber.App) {
		st := &fakeStore{}
		h := portfolio.NewHandler(st, &fakeStrategyStore{}, nil, nil,
			builder, urlValidator, strategy.EphemeralOptions{})
		a := fiber.New()
		a.Use(func(c fiber.Ctx) error {
			c.Locals(types.AuthSubjectKey{}, sub)
			return c.Next()
		})
		a.Post("/portfolios", h.Create)
		return st, a
	}

	It("returns 201 and persists the portfolio on the happy path", func() {
		fakeBuilder := func(_ context.Context, _ strategy.EphemeralOptions) (string, func(), error) {
			bin := buildFakeStrategyBin()
			return bin, func() { os.RemoveAll(filepath.Dir(bin)) }, nil //nolint:errcheck
		}
		urlValidator := func(string) error { return nil }

		st, a := newApp(fakeBuilder, urlValidator)

		reqBody := `{"name":"u1","strategyCloneUrl":"https://github.com/foo/bar","parameters":{"riskOn":"SPY"}}`
		req := httptest.NewRequest("POST", "/portfolios", bytes.NewBufferString(reqBody))
		req.Header.Set("Content-Type", "application/json")
		resp, err := a.Test(req, fiber.TestConfig{Timeout: 30 * time.Second}) // go build may take a while
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusCreated))

		Expect(st.rows).To(HaveLen(1))
		row := st.rows[0]
		Expect(row.StrategyCloneURL).To(Equal("https://github.com/foo/bar"))
		Expect(row.StrategyVer).To(BeNil())
		Expect(row.StrategyCode).To(Equal("fake"))
		Expect(row.StrategyDescribeJSON).NotTo(BeEmpty())
	})

	It("returns 422 when both strategyCode and strategyCloneUrl are set", func() {
		_, a := newApp(nil, func(string) error { return nil })

		reqBody := `{"name":"x","strategyCode":"adm","strategyCloneUrl":"https://github.com/foo/bar","parameters":{}}`
		req := httptest.NewRequest("POST", "/portfolios", bytes.NewBufferString(reqBody))
		req.Header.Set("Content-Type", "application/json")
		resp, err := a.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnprocessableEntity))
	})
})
