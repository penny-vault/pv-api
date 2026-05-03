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

package alert_test

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/penny-vault/pv-api/alert"
	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/types"
)

// stubPortfolio implements alert.PortfolioReader for tests.
type stubPortfolio struct {
	p   portfolio.Portfolio
	err error
}

func (s stubPortfolio) Get(_ context.Context, _, _ string) (portfolio.Portfolio, error) {
	return s.p, s.err
}

// stubAlertStore implements alert.Store with panics for unused methods.
type stubAlertStore struct{}

func (s stubAlertStore) Create(_ context.Context, _ uuid.UUID, _ string, _ []string) (alert.Alert, error) {
	panic("unexpected call")
}
func (s stubAlertStore) List(_ context.Context, _ uuid.UUID) ([]alert.Alert, error) {
	panic("unexpected call")
}
func (s stubAlertStore) Get(_ context.Context, _ uuid.UUID) (alert.Alert, error) {
	panic("unexpected call")
}
func (s stubAlertStore) Update(_ context.Context, _ uuid.UUID, _ string, _ []string) (alert.Alert, error) {
	panic("unexpected call")
}
func (s stubAlertStore) Delete(_ context.Context, _ uuid.UUID) error { panic("unexpected call") }
func (s stubAlertStore) MarkSent(_ context.Context, _ uuid.UUID, _ time.Time, _ float64) error {
	panic("unexpected call")
}
func (s stubAlertStore) RemoveRecipient(_ context.Context, _ uuid.UUID, _ string) error {
	panic("unexpected call")
}

// stubSummarizer implements alert.EmailSummarizer for tests.
type stubSummarizer struct{ err error }

func (s stubSummarizer) SendSummary(_ context.Context, _ uuid.UUID, _ string) error { return s.err }

// newTestApp wires h into a minimal fiber app with auth subject pre-set.
func newTestApp(h *alert.AlertHandler) *fiber.App {
	app := fiber.New(fiber.Config{})
	app.Use(func(c fiber.Ctx) error {
		c.Locals(types.AuthSubjectKey{}, "user-1")
		return c.Next()
	})
	app.Post("/portfolios/:slug/email-summary", h.SendSummary)
	return app
}

func TestSendSummaryNilChecker(t *testing.T) {
	h := alert.NewAlertHandlerWithChecker(stubPortfolio{}, stubAlertStore{}, nil, "")
	app := newTestApp(h)

	req := httptest.NewRequest("POST", "/portfolios/my-port/email-summary",
		bytes.NewBufferString(`{"recipient":"a@b.com"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
}

func TestSendSummaryMissingRecipient(t *testing.T) {
	port := portfolio.Portfolio{ID: uuid.New(), OwnerSub: "user-1", Slug: "my-port", Status: portfolio.StatusReady}
	h := alert.NewAlertHandlerWithChecker(stubPortfolio{p: port}, stubAlertStore{}, stubSummarizer{}, "")
	app := newTestApp(h)

	req := httptest.NewRequest("POST", "/portfolios/my-port/email-summary",
		bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", resp.StatusCode)
	}
}

func TestSendSummaryInvalidJSON(t *testing.T) {
	port := portfolio.Portfolio{ID: uuid.New(), OwnerSub: "user-1", Slug: "my-port", Status: portfolio.StatusReady}
	h := alert.NewAlertHandlerWithChecker(stubPortfolio{p: port}, stubAlertStore{}, stubSummarizer{}, "")
	app := newTestApp(h)

	req := httptest.NewRequest("POST", "/portfolios/my-port/email-summary",
		bytes.NewBufferString(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", resp.StatusCode)
	}
}

func TestSendSummarySuccess(t *testing.T) {
	port := portfolio.Portfolio{ID: uuid.New(), OwnerSub: "user-1", Slug: "my-port", Status: portfolio.StatusReady}
	h := alert.NewAlertHandlerWithChecker(stubPortfolio{p: port}, stubAlertStore{}, stubSummarizer{err: nil}, "")
	app := newTestApp(h)

	req := httptest.NewRequest("POST", "/portfolios/my-port/email-summary",
		bytes.NewBufferString(`{"recipient":"a@b.com"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}
}

func TestSendSummaryEmailNotConfigured(t *testing.T) {
	port := portfolio.Portfolio{ID: uuid.New(), OwnerSub: "user-1", Slug: "my-port", Status: portfolio.StatusReady}
	h := alert.NewAlertHandlerWithChecker(stubPortfolio{p: port}, stubAlertStore{}, stubSummarizer{err: alert.ErrEmailNotConfigured}, "")
	app := newTestApp(h)

	req := httptest.NewRequest("POST", "/portfolios/my-port/email-summary",
		bytes.NewBufferString(`{"recipient":"a@b.com"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
}
