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
	"io"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/api"
)

var _ = Describe("NewApp", func() {
	It("responds 200 on GET /healthz with body 'ok'", func() {
		app := api.NewApp(api.Config{})

		req := httptest.NewRequest("GET", "/healthz", nil)

		resp, err := app.Test(req)
		Expect(err).To(BeNil())
		defer resp.Body.Close()

		Expect(resp.StatusCode).To(Equal(200))

		body, err := io.ReadAll(resp.Body)
		Expect(err).To(BeNil())
		Expect(string(body)).To(Equal("ok"))
	})
})
