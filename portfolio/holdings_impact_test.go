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
	"io"
	"net/http/httptest"

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

var _ = Describe("Handler.HoldingsImpact", func() {
	var (
		app    *fiber.App
		store  *fakeStore
		opener *fakeSnapshotOpener
		reader *fakeSnapshotReader
		sub    = "auth0|owner"
	)

	const (
		slug         = "demo"
		snapshotPath = "/fake/snap.sqlite"
	)

	BeforeEach(func() {
		store = &fakeStore{}
		reader = &fakeSnapshotReader{}
		opener = &fakeSnapshotOpener{readers: map[string]portfolio.SnapshotReader{
			snapshotPath: reader,
		}}
		path := snapshotPath
		store.rows = []portfolio.Portfolio{{
			ID:           uuid.Must(uuid.NewV7()),
			OwnerSub:     sub,
			Slug:         slug,
			Status:       portfolio.StatusReady,
			SnapshotPath: &path,
		}}

		app = fiber.New(fiber.Config{JSONEncoder: sonic.Marshal, JSONDecoder: sonic.Unmarshal})
		app.Use(func(c fiber.Ctx) error {
			c.Locals(types.AuthSubjectKey{}, sub)
			return c.Next()
		})
		h := portfolio.NewHandler(store, &fakeStrategyStore{}, opener, nil, nil, nil, strategy.EphemeralOptions{})
		app.Get("/portfolios/:slug/holdings-impact", h.HoldingsImpact)
	})

	It("returns 200 with a response containing the portfolio slug", func() {
		req := httptest.NewRequest("GET", "/portfolios/"+slug+"/holdings-impact", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))

		body, _ := io.ReadAll(resp.Body)
		var got openapi.HoldingsImpactResponse
		Expect(sonic.Unmarshal(body, &got)).To(Succeed())
		Expect(got.PortfolioSlug).To(Equal(slug))
	})

	It("passes the top query parameter through to the reader", func() {
		var capturedTopN int
		reader.holdingsImpactFn = func(_ context.Context, s string, topN int) (*openapi.HoldingsImpactResponse, error) {
			capturedTopN = topN
			return &openapi.HoldingsImpactResponse{PortfolioSlug: s}, nil
		}

		req := httptest.NewRequest("GET", "/portfolios/"+slug+"/holdings-impact?top=3", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))
		Expect(capturedTopN).To(Equal(3))
	})

	It("defaults topN to 10 when the top query parameter is absent", func() {
		var capturedTopN int
		reader.holdingsImpactFn = func(_ context.Context, s string, topN int) (*openapi.HoldingsImpactResponse, error) {
			capturedTopN = topN
			return &openapi.HoldingsImpactResponse{PortfolioSlug: s}, nil
		}

		req := httptest.NewRequest("GET", "/portfolios/"+slug+"/holdings-impact", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))
		Expect(capturedTopN).To(Equal(10))
	})

	It("clamps invalid or out-of-range top values", func() {
		var capturedTopN int
		reader.holdingsImpactFn = func(_ context.Context, s string, topN int) (*openapi.HoldingsImpactResponse, error) {
			capturedTopN = topN
			return &openapi.HoldingsImpactResponse{PortfolioSlug: s}, nil
		}

		// top=0 is invalid (n < 1) and falls back to the default 10.
		req := httptest.NewRequest("GET", "/portfolios/"+slug+"/holdings-impact?top=0", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))
		Expect(capturedTopN).To(Equal(10))

		// top=999 is clamped to the maximum of 50.
		req = httptest.NewRequest("GET", "/portfolios/"+slug+"/holdings-impact?top=999", nil)
		resp, err = app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))
		Expect(capturedTopN).To(Equal(50))
	})

	It("returns 404 when the portfolio is not found", func() {
		req := httptest.NewRequest("GET", "/portfolios/does-not-exist/holdings-impact", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusNotFound))
	})

	It("returns 404 when the snapshot lacks positions_daily (ErrSnapshotNotFound)", func() {
		reader.holdingsImpactFn = func(_ context.Context, _ string, _ int) (*openapi.HoldingsImpactResponse, error) {
			return nil, portfolio.ErrSnapshotNotFound
		}

		req := httptest.NewRequest("GET", "/portfolios/"+slug+"/holdings-impact", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusNotFound))
	})

	It("returns 401 when no auth subject is present", func() {
		// Build a fresh app with no middleware setting AuthSubjectKey so the
		// subject() helper returns ErrNoSubject.
		noAuthApp := fiber.New(fiber.Config{JSONEncoder: sonic.Marshal, JSONDecoder: sonic.Unmarshal})
		h := portfolio.NewHandler(store, &fakeStrategyStore{}, opener, nil, nil, nil, strategy.EphemeralOptions{})
		noAuthApp.Get("/portfolios/:slug/holdings-impact", h.HoldingsImpact)

		req := httptest.NewRequest("GET", "/portfolios/"+slug+"/holdings-impact", nil)
		resp, err := noAuthApp.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnauthorized))
	})
})
