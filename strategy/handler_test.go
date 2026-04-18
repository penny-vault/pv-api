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

package strategy_test

import (
	"io"
	"net/http/httptest"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("strategy.Handler", func() {
	var store *fakeStore

	BeforeEach(func() {
		store = newFakeStore()
		// One ready strategy
		ver := "v1.0.0"
		at := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
		store.rows["adm"] = strategy.Strategy{
			ShortCode:        "adm",
			RepoOwner:        "penny-vault",
			RepoName:         "adm",
			CloneURL:         "https://github.com/penny-vault/adm.git",
			IsOfficial:       true,
			InstalledVer:     &ver,
			LastAttemptedVer: &ver,
			InstalledAt:      &at,
			DescribeJSON:     []byte(`{"shortCode":"adm","name":"ADM","parameters":[],"schedule":"@monthend","benchmark":"SPY"}`),
			DiscoveredAt:     at,
			UpdatedAt:        at,
		}
		// One pending strategy (listing-only)
		store.rows["bogus"] = strategy.Strategy{
			ShortCode:    "bogus",
			RepoOwner:    "penny-vault",
			RepoName:     "bogus",
			IsOfficial:   true,
			DiscoveredAt: at,
			UpdatedAt:    at,
		}
	})

	run := func(method, path string) (int, []byte, string) {
		app := fiber.New()
		h := strategy.NewHandler(store)
		app.Get("/strategies", h.List)
		app.Get("/strategies/:shortCode", h.Get)

		resp, err := app.Test(httptest.NewRequest(method, path, nil))
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		return resp.StatusCode, body, resp.Header.Get("Content-Type")
	}

	It("lists strategies", func() {
		status, body, _ := run("GET", "/strategies")
		Expect(status).To(Equal(200))

		var out []map[string]any
		Expect(sonic.Unmarshal(body, &out)).To(Succeed())
		Expect(out).To(HaveLen(2))
	})

	It("returns install state on a ready strategy", func() {
		status, body, _ := run("GET", "/strategies/adm")
		Expect(status).To(Equal(200))

		var out map[string]any
		Expect(sonic.Unmarshal(body, &out)).To(Succeed())
		Expect(out["installState"]).To(Equal("ready"))
		Expect(out["installedVer"]).To(Equal("v1.0.0"))
		Expect(out["describe"]).NotTo(BeNil())
	})

	It("returns install state on a pending strategy with no describe", func() {
		status, body, _ := run("GET", "/strategies/bogus")
		Expect(status).To(Equal(200))

		var out map[string]any
		Expect(sonic.Unmarshal(body, &out)).To(Succeed())
		Expect(out["installState"]).To(Equal("pending"))
		Expect(out["describe"]).To(BeNil())
	})

	It("returns 404 problem+json on unknown short_code", func() {
		status, _, ct := run("GET", "/strategies/nope")
		Expect(status).To(Equal(404))
		Expect(ct).To(Equal("application/problem+json"))
	})
})
