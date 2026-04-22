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
	"context"
	"io"
	"net/http/httptest"
	"time"

	"github.com/gofiber/fiber/v3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/api"
	"github.com/penny-vault/pv-api/api/apitesting"
)

var _ = Describe("NewApp", func() {
	newApp := func() *fiber.App {
		app, err := api.NewApp(context.Background(), api.Config{
			Auth: api.AuthConfig{
				JWKSURL:  testJWKS.URL,
				Audience: apitesting.Audience,
				Issuer:   apitesting.Issuer,
			},
		})
		Expect(err).NotTo(HaveOccurred())
		return app
	}

	It("responds 200 on GET /healthz with body 'ok'", func() {
		app := newApp()
		resp, err := app.Test(httptest.NewRequest("GET", "/healthz", nil))
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(200))
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(Equal("ok"))
	})

	It("rejects a request to /api/v3/portfolios without a JWT", func() {
		app := newApp()
		resp, err := app.Test(httptest.NewRequest("GET", "/api/v3/portfolios", nil))
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(401))
	})

	It("returns 501 on /api/v3/portfolios with a valid JWT", func() {
		app := newApp()
		tok, err := testJWKS.Mint("user-1", time.Hour)
		Expect(err).NotTo(HaveOccurred())
		req := httptest.NewRequest("GET", "/api/v3/portfolios", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(501))
	})
})
