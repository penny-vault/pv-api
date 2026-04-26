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

package snapshot_test

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/openapi"
	"github.com/penny-vault/pv-api/snapshot"
)

var _ = Describe("ShortTermReturns", func() {
	It("computes day, WTD, and MTD from perf_data", func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		ret, err := r.ShortTermReturns(context.Background())
		Expect(err).NotTo(HaveOccurred())
		// Last date is 2024-01-08 (value 103000), prev trading day 2024-01-05 (value 102000)
		// Day = (103000-102000)/102000 ≈ 0.0098
		Expect(ret.Day).To(BeNumerically("~", 0.0098, 0.001))
		// WTD: 2024-01-08 is Monday; first row on/after that Monday is 2024-01-08 itself → 0
		Expect(ret.WTD).To(BeNumerically("~", 0.0, 0.001))
		// MTD: first row on/after 2024-01-01 is 2024-01-02 (100000)
		// MTD = (103000-100000)/100000 = 0.03
		Expect(ret.MTD).To(BeNumerically("~", 0.03, 0.001))
	})
})

var _ = Describe("TrailingReturns", func() {
	It("emits portfolio and benchmark rows with since-inception populated", func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		rows, err := r.TrailingReturns(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(2))

		var portfolioRow openapi.TrailingReturnRow
		for _, row := range rows {
			if row.Kind == openapi.ReturnRowKindPortfolio {
				portfolioRow = row
			}
		}
		Expect(portfolioRow.Title).NotTo(BeEmpty())
		Expect(portfolioRow.SinceInception).To(BeNumerically(">", 0))
	})
})
