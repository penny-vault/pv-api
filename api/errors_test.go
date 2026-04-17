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
	"errors"
	"io"
	"net/http/httptest"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/api"
)

var _ = Describe("WriteProblem", func() {
	type problem struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}

	run := func(h fiber.Handler) problem {
		app := fiber.New()
		app.Get("/t", h)
		resp, err := app.Test(httptest.NewRequest("GET", "/t", nil))
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Header.Get("Content-Type")).To(Equal("application/problem+json"))
		var p problem
		Expect(sonic.Unmarshal(body, &p)).To(Succeed())
		Expect(p.Status).To(Equal(resp.StatusCode))
		return p
	}

	It("returns 404 for ErrNotFound", func() {
		p := run(func(c fiber.Ctx) error {
			return api.WriteProblem(c, api.ErrNotFound)
		})
		Expect(p.Status).To(Equal(404))
		Expect(p.Title).To(Equal("Not Found"))
	})

	It("returns 409 for ErrConflict", func() {
		p := run(func(c fiber.Ctx) error {
			return api.WriteProblem(c, api.ErrConflict)
		})
		Expect(p.Status).To(Equal(409))
	})

	It("returns 422 for ErrInvalidParams", func() {
		p := run(func(c fiber.Ctx) error {
			return api.WriteProblem(c, api.ErrInvalidParams)
		})
		Expect(p.Status).To(Equal(422))
	})

	It("returns 501 for ErrNotImplemented", func() {
		p := run(func(c fiber.Ctx) error {
			return api.WriteProblem(c, api.ErrNotImplemented)
		})
		Expect(p.Status).To(Equal(501))
	})

	It("returns 500 for unknown errors", func() {
		p := run(func(c fiber.Ctx) error {
			return api.WriteProblem(c, errors.New("boom"))
		})
		Expect(p.Status).To(Equal(500))
		Expect(p.Title).To(Equal("Internal Server Error"))
	})
})
