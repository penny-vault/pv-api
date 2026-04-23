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
	"bufio"
	"context"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/progress"
	"github.com/penny-vault/pv-api/strategy"
	"github.com/penny-vault/pv-api/types"
)

// runStore extends fakeStore with a configurable GetRun.
type runStore struct {
	fakeStore
	run    portfolio.Run
	runErr error
}

func (s *runStore) GetRun(_ context.Context, _, _ uuid.UUID) (portfolio.Run, error) {
	if s.runErr != nil {
		return portfolio.Run{}, s.runErr
	}
	return s.run, nil
}

// newSSEApp builds a minimal Fiber app with a subject-injection middleware
// and the SSE route wired to the given handler.
func newSSEApp(store portfolio.Store, hub *progress.Hub) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		sub := c.Get("X-Test-Sub")
		if sub != "" {
			c.Locals(types.AuthSubjectKey{}, sub)
		}
		return c.Next()
	})
	h := portfolio.NewHandler(store, nil, nil, nil, nil, nil, strategy.EphemeralOptions{})
	if hub != nil {
		h.WithHub(hub)
	}
	app.Get("/portfolios/:slug/runs/:runId/progress", h.StreamRunProgress)
	return app
}

var _ = Describe("StreamRunProgress", func() {
	var (
		hub    *progress.Hub
		runID  uuid.UUID
		portID uuid.UUID
	)

	BeforeEach(func() {
		hub = progress.NewHub()
		runID = uuid.New()
		portID = uuid.New()
	})

	It("returns 501 when hub is not configured", func() {
		store := &runStore{}
		store.rows = []portfolio.Portfolio{{ID: portID, Slug: "test-slug", OwnerSub: "user1"}}
		app := newSSEApp(store, nil)

		req := httptest.NewRequest("GET", "/portfolios/test-slug/runs/"+runID.String()+"/progress", nil)
		req.Header.Set("X-Test-Sub", "user1")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusNotImplemented))
	})

	It("returns 404 when the run does not exist", func() {
		store := &runStore{runErr: portfolio.ErrNotFound}
		store.rows = []portfolio.Portfolio{{ID: portID, Slug: "test-slug", OwnerSub: "user1"}}
		app := newSSEApp(store, hub)

		req := httptest.NewRequest("GET", "/portfolios/test-slug/runs/"+runID.String()+"/progress", nil)
		req.Header.Set("X-Test-Sub", "user1")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusNotFound))
	})

	It("immediately streams a terminal done event for a completed run", func() {
		store := &runStore{run: portfolio.Run{ID: runID, Status: "success"}}
		store.rows = []portfolio.Portfolio{{ID: portID, Slug: "test-slug", OwnerSub: "user1"}}
		app := newSSEApp(store, hub)

		req := httptest.NewRequest("GET", "/portfolios/test-slug/runs/"+runID.String()+"/progress", nil)
		req.Header.Set("X-Test-Sub", "user1")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))
		Expect(resp.Header.Get("Content-Type")).To(ContainSubstring("text/event-stream"))

		var lines []string
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		Expect(strings.Join(lines, "\n")).To(ContainSubstring("event: done"))
	})

	It("streams live progress then a terminal event for an active run", func() {
		store := &runStore{run: portfolio.Run{ID: runID, Status: "running"}}
		store.rows = []portfolio.Portfolio{{ID: portID, Slug: "test-slug", OwnerSub: "user1"}}
		app := newSSEApp(store, hub)

		go func() {
			time.Sleep(20 * time.Millisecond)
			hub.Publish(runID, progress.ProgressMessage{Type: "progress", Step: 1, TotalSteps: 10, Pct: 10.0})
			time.Sleep(10 * time.Millisecond)
			hub.Complete(runID, "success", "")
		}()

		req := httptest.NewRequest("GET", "/portfolios/test-slug/runs/"+runID.String()+"/progress", nil)
		req.Header.Set("X-Test-Sub", "user1")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))

		var sb strings.Builder
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			sb.WriteString(scanner.Text() + "\n")
		}
		Expect(sb.String()).To(ContainSubstring("event: progress"))
		Expect(sb.String()).To(ContainSubstring("event: done"))
	})
})
