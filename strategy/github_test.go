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
	"context"
	"net/http"
	"os"

	"github.com/jarcoal/httpmock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("GitHub discovery", func() {
	BeforeEach(func() {
		httpmock.Activate()
		httpmock.ActivateNonDefault(http.DefaultClient)

		body, err := os.ReadFile("testdata/github_search_response.json")
		Expect(err).NotTo(HaveOccurred())

		httpmock.RegisterResponder("GET", `=~^https://api\.github\.com/search/repositories.*`,
			httpmock.NewBytesResponder(200, body))
	})

	AfterEach(func() {
		httpmock.DeactivateAndReset()
	})

	It("returns only penny-vault listings", func() {
		listings, err := strategy.DiscoverOfficial(context.Background(), strategy.DiscoverOptions{
			CacheDir:    GinkgoT().TempDir(),
			ExpectOwner: "penny-vault",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(listings).To(HaveLen(1))
		Expect(listings[0].Owner).To(Equal("penny-vault"))
		Expect(listings[0].Name).To(Equal("adm"))
		Expect(listings[0].Stars).To(Equal(42))
	})
})
