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

package api_test

import (
	"net/http/httptest"

	"github.com/gofiber/fiber/v3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/api"
)

var _ = Describe("Portfolio handlers", func() {
	var app *fiber.App

	BeforeEach(func() {
		app = fiber.New()
		api.RegisterPortfolioRoutes(app)
	})

	DescribeTable("stub endpoints return 501 problem+json",
		func(method, path string) {
			req := httptest.NewRequest(method, path, nil)
			resp, err := app.Test(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(fiber.StatusNotImplemented))
			Expect(resp.Header.Get("Content-Type")).To(Equal("application/problem+json"))
		},
		Entry("list portfolios", "GET", "/portfolios"),
		Entry("create portfolio", "POST", "/portfolios"),
		Entry("get portfolio", "GET", "/portfolios/adm-standard-aq35"),
		Entry("update portfolio", "PATCH", "/portfolios/adm-standard-aq35"),
		Entry("delete portfolio", "DELETE", "/portfolios/adm-standard-aq35"),
		Entry("get statistics", "GET", "/portfolios/adm-standard-aq35/statistics"),
		Entry("get performance", "GET", "/portfolios/adm-standard-aq35/performance"),
		Entry("get transactions", "GET", "/portfolios/adm-standard-aq35/transactions"),
		Entry("get holdings history", "GET", "/portfolios/adm-standard-aq35/holdings/history"),
		Entry("trigger run", "POST", "/portfolios/adm-standard-aq35/runs"),
		Entry("list runs", "GET", "/portfolios/adm-standard-aq35/runs"),
		Entry("get run", "GET", "/portfolios/adm-standard-aq35/runs/019d9a15-54cc-7db7-84cc-a5b6875bf27d"),
	)
})
