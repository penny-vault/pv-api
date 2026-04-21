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

var _ = Describe("ImageTag", func() {
	DescribeTable("builds a tag from a GitHub clone URL",
		func(prefix, cloneURL, ver, want string) {
			Expect(strategy.ImageTag(prefix, cloneURL, ver)).To(Equal(want))
		},
		Entry("plain", "pvapi-strategy", "https://github.com/penny-vault/adm", "v1.2.3",
			"pvapi-strategy/penny-vault/adm:v1.2.3"),
		Entry("with .git suffix", "pvapi-strategy", "https://github.com/penny-vault/adm.git", "v1.2.3",
			"pvapi-strategy/penny-vault/adm:v1.2.3"),
		Entry("non-github fallback", "pvapi-strategy", "https://example.com/foo/bar", "abc123",
			"pvapi-strategy/unknown/example.com-foo-bar:abc123"),
	)
})
