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
	"net/http/httptest"
	"time"

	"github.com/gofiber/fiber/v3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/api"
	"github.com/penny-vault/pv-api/api/apitesting"
	"github.com/penny-vault/pv-api/types"
)

var _ = Describe("JWT middleware", func() {
	var app *fiber.App

	BeforeEach(func() {
		ctx := context.Background()
		mw, err := api.NewAuthMiddleware(ctx, api.AuthConfig{
			JWKSURL:  testJWKS.URL,
			Audience: apitesting.Audience,
			Issuer:   apitesting.Issuer,
		})
		Expect(err).NotTo(HaveOccurred())

		app = fiber.New()
		app.Use(mw)
		app.Get("/secure", func(c fiber.Ctx) error {
			sub, _ := c.Locals(types.AuthSubjectKey{}).(string)
			return c.SendString("hello " + sub)
		})
	})

	It("rejects requests with no Authorization header", func() {
		resp, err := app.Test(httptest.NewRequest("GET", "/secure", nil))
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnauthorized))
		Expect(resp.Header.Get("Content-Type")).To(Equal("application/problem+json"))
	})

	It("rejects a malformed Authorization header", func() {
		req := httptest.NewRequest("GET", "/secure", nil)
		req.Header.Set("Authorization", "NotBearer garbage")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnauthorized))
	})

	It("rejects an expired token", func() {
		tok, err := testJWKS.Mint("user-1", -1*time.Hour)
		Expect(err).NotTo(HaveOccurred())
		req := httptest.NewRequest("GET", "/secure", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnauthorized))
	})

	It("rejects a token with the wrong audience", func() {
		tok, err := testJWKS.MintWith("user-1", "wrong-audience", apitesting.Issuer, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		req := httptest.NewRequest("GET", "/secure", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnauthorized))
	})

	It("rejects a token with the wrong issuer", func() {
		tok, err := testJWKS.MintWith("user-1", apitesting.Audience, "https://evil.example/", time.Hour)
		Expect(err).NotTo(HaveOccurred())
		req := httptest.NewRequest("GET", "/secure", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnauthorized))
	})

	It("accepts a valid token", func() {
		tok, err := testJWKS.Mint("user-42", time.Hour)
		Expect(err).NotTo(HaveOccurred())
		req := httptest.NewRequest("GET", "/secure", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))
	})
})
