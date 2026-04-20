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

var _ = Describe("ValidateCloneURL", func() {
	DescribeTable("accepts canonical GitHub HTTPS URLs",
		func(url string) { Expect(strategy.ValidateCloneURL(url)).To(Succeed()) },
		Entry("owner/repo", "https://github.com/penny-vault/pvbt"),
		Entry("with .git suffix", "https://github.com/penny-vault/pvbt.git"),
		Entry("dashes", "https://github.com/some-owner/some-repo"),
		Entry("dots", "https://github.com/a.b/c.d"),
	)

	DescribeTable("rejects other shapes",
		func(url string) { Expect(strategy.ValidateCloneURL(url)).ToNot(Succeed()) },
		Entry("ssh", "git@github.com:owner/repo.git"),
		Entry("http", "http://github.com/owner/repo"),
		Entry("file", "file:///tmp/repo"),
		Entry("gitlab", "https://gitlab.com/owner/repo"),
		Entry("query string", "https://github.com/owner/repo?x=1"),
		Entry("path beyond repo", "https://github.com/owner/repo/tree/main"),
		Entry("empty", ""),
	)
})
