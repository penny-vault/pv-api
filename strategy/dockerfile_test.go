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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("ParseGoVersion", func() {
	It("reads 1.22", func() {
		v, err := strategy.ParseGoVersion("testdata/gomods/go122")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal("1.22"))
	})

	It("reads 1.24", func() {
		v, err := strategy.ParseGoVersion("testdata/gomods/go124")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal("1.24"))
	})

	It("returns empty on garbage go.mod", func() {
		v, _ := strategy.ParseGoVersion("testdata/gomods/garbage")
		Expect(v).To(Equal(""))
	})

	It("returns empty when go.mod is missing", func() {
		v, _ := strategy.ParseGoVersion("testdata/gomods/doesnotexist")
		Expect(v).To(Equal(""))
	})
})

var _ = Describe("RenderDockerfile", func() {
	It("uses the given go version in the build stage", func() {
		df := string(strategy.RenderDockerfile("1.23"))
		Expect(df).To(ContainSubstring("FROM golang:1.23-alpine AS build"))
		Expect(df).To(ContainSubstring("FROM gcr.io/distroless/static-debian12:nonroot"))
		Expect(df).To(ContainSubstring(`ENTRYPOINT ["/strategy"]`))
	})

	It("falls back to the default when goVer is empty", func() {
		df := string(strategy.RenderDockerfile(""))
		Expect(df).To(ContainSubstring("FROM golang:1.24-alpine AS build"))
	})
})
