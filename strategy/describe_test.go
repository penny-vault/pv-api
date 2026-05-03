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
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/gofiber/fiber/v3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("RunDescribe", func() {
	It("returns the raw describe JSON", func() {
		bin := buildFakeStrategy()
		defer removeAll(filepath.Dir(bin))

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		raw, err := strategy.RunDescribe(ctx, bin)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).To(ContainSubstring(`"shortcode": "fake"`))
	})
})

var _ = Describe("DescribeHandler", func() {
	It("200s with the describe on a valid clone url", func() {
		app := fiber.New()
		app.Get("/strategies/describe", (&strategy.DescribeHandler{
			Builder: func(ctx context.Context, _ strategy.EphemeralOptions) (string, func(), error) {
				bin := buildFakeStrategy()
				return bin, func() { removeAll(filepath.Dir(bin)) }, nil
			},
			URLValidator:  func(string) error { return nil },
			EphemeralOpts: strategy.EphemeralOptions{Timeout: 10 * time.Second},
		}).Describe)

		req := httptest.NewRequest("GET", "/strategies/describe?cloneUrl=https%3A%2F%2Fgithub.com%2Ffoo%2Fbar", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(200))
		body, _ := io.ReadAll(resp.Body)
		Expect(string(body)).To(ContainSubstring(`"shortcode":"fake"`))
	})

	It("422s when the builder fails", func() {
		app := fiber.New()
		app.Get("/strategies/describe", (&strategy.DescribeHandler{
			Builder: func(context.Context, strategy.EphemeralOptions) (string, func(), error) {
				return "", nil, fmt.Errorf("build failed: exit 1")
			},
			URLValidator: func(string) error { return nil },
		}).Describe)

		u := "/strategies/describe?cloneUrl=" + url.QueryEscape("https://github.com/foo/bar")
		resp, err := app.Test(httptest.NewRequest("GET", u, nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(422))
	})

	It("400s on an invalid cloneUrl", func() {
		app := fiber.New()
		app.Get("/strategies/describe", (&strategy.DescribeHandler{
			Builder: func(context.Context, strategy.EphemeralOptions) (string, func(), error) {
				Fail("builder should not be called")
				return "", nil, nil
			},
			URLValidator: strategy.ValidateCloneURL,
		}).Describe)

		u := "/strategies/describe?cloneUrl=" + url.QueryEscape("ssh://git@github.com/foo/bar")
		resp, err := app.Test(httptest.NewRequest("GET", u, nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(400))
	})
})

// buildFakeStrategy compiles testdata/fake-strategy-src into a tempdir
// and returns the binary path. Caller os.RemoveAll's filepath.Dir(bin).
func buildFakeStrategy() string {
	src, err := filepath.Abs("testdata/fake-strategy-src")
	Expect(err).NotTo(HaveOccurred())
	dir, err := os.MkdirTemp("", "fakebin-*")
	Expect(err).NotTo(HaveOccurred())
	bin := filepath.Join(dir, "strategy.bin")
	var buf bytes.Buffer
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = src
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	Expect(cmd.Run()).To(Succeed(), buf.String())
	return bin
}

func removeAll(p string) { _ = os.RemoveAll(p) }
