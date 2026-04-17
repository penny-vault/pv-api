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
	"bytes"
	"context"
	"net/http/httptest"

	"github.com/gofiber/fiber/v3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pv-api/api"
	"github.com/penny-vault/pv-api/api/apitesting"
)

var _ = Describe("Middleware", func() {
	var app *fiber.App

	BeforeEach(func() {
		var err error
		app, err = api.NewApp(context.Background(), api.Config{
			Auth: api.AuthConfig{
				JWKSURL:  testJWKS.URL,
				Audience: apitesting.Audience,
				Issuer:   apitesting.Issuer,
			},
		})
		Expect(err).NotTo(HaveOccurred())
	})

	It("sets an X-Request-Id response header", func() {
		req := httptest.NewRequest("GET", "/healthz", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()

		Expect(resp.Header.Get("X-Request-Id")).NotTo(BeEmpty())
	})

	It("honors an inbound X-Request-Id", func() {
		req := httptest.NewRequest("GET", "/healthz", nil)
		req.Header.Set("X-Request-Id", "test-request-id-42")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()

		Expect(resp.Header.Get("X-Request-Id")).To(Equal("test-request-id-42"))
	})

	It("sets a Server-Timing header", func() {
		req := httptest.NewRequest("GET", "/healthz", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()

		Expect(resp.Header.Get("Server-Timing")).To(ContainSubstring("app;dur="))
	})

	It("writes a zerolog line containing the request_id, status, and path", func() {
		var buf bytes.Buffer
		prev := log.Logger
		log.Logger = zerolog.New(&buf)
		defer func() { log.Logger = prev }()

		req := httptest.NewRequest("GET", "/healthz", nil)
		req.Header.Set("X-Request-Id", "log-line-test")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()

		Expect(buf.String()).To(ContainSubstring(`"request_id":"log-line-test"`))
		Expect(buf.String()).To(ContainSubstring(`"status":200`))
		Expect(buf.String()).To(ContainSubstring(`"path":"/healthz"`))
	})
})
