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
	"math"
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
	It("emits portfolio, benchmark, portfolio-tax, and benchmark-tax rows", func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		rows, err := r.TrailingReturns(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(4))

		var portfolioRow, benchRow, portfolioTaxRow, benchTaxRow openapi.TrailingReturnRow
		for _, row := range rows {
			switch row.Kind {
			case openapi.ReturnRowKindPortfolio:
				portfolioRow = row
			case openapi.ReturnRowKindBenchmark:
				benchRow = row
			case openapi.ReturnRowKindPortfolioTax:
				portfolioTaxRow = row
			case openapi.ReturnRowKindBenchmarkTax:
				benchTaxRow = row
			}
		}

		// Portfolio cells come straight from the fixture's metrics rows:
		// TWRR/ytd=0.03, TWRR/since_inception=0.03, CAGR/since_inception=6.7.
		Expect(portfolioRow.Title).To(Equal("Portfolio"))
		Expect(portfolioRow.Ytd).NotTo(BeNil())
		Expect(*portfolioRow.Ytd).To(BeNumerically("~", 0.03, 1e-9))
		Expect(portfolioRow.SinceInception).NotTo(BeNil())
		Expect(*portfolioRow.SinceInception).To(BeNumerically("~", 6.7, 1e-9))
		// Windows pvbt did not write must surface as null — no fallback.
		Expect(portfolioRow.OneYear).To(BeNil())
		Expect(portfolioRow.ThreeYear).To(BeNil())
		Expect(portfolioRow.FiveYear).To(BeNil())
		Expect(portfolioRow.TenYear).To(BeNil())

		// Benchmark cells: cumulative return from latest=102000 against the
		// earliest on/after Jan 1 2024 (=100000) is 0.02 for both ytd and
		// since_inception. The 5-day fixture cannot satisfy multi-year
		// windows, so those cells must be null (no fallback to earliest).
		Expect(benchRow.Title).To(Equal("Benchmark"))
		Expect(benchRow.Ytd).NotTo(BeNil())
		Expect(*benchRow.Ytd).To(BeNumerically("~", 0.02, 1e-9))
		Expect(benchRow.SinceInception).NotTo(BeNil())
		Expect(benchRow.OneYear).To(BeNil())
		Expect(benchRow.ThreeYear).To(BeNil())
		Expect(benchRow.FiveYear).To(BeNil())
		Expect(benchRow.TenYear).To(BeNil())

		// Portfolio-tax cells: TWRR * (1 - TaxDrag) cumulatively, then
		// re-annualized over the actual portfolio span (2024-01-02 →
		// 2024-01-08 = 6 days) for since_inception.
		Expect(portfolioTaxRow.Title).To(Equal("Portfolio (after tax)"))
		Expect(portfolioTaxRow.Ytd).NotTo(BeNil())
		Expect(*portfolioTaxRow.Ytd).To(BeNumerically("~", 0.027, 1e-9))
		Expect(portfolioTaxRow.SinceInception).NotTo(BeNil())
		spanYears := 6.0 / 365.25
		expectedPortfolioSI := math.Pow(1.027, 1.0/spanYears) - 1
		Expect(*portfolioTaxRow.SinceInception).To(BeNumerically("~", expectedPortfolioSI, 1e-6))
		Expect(portfolioTaxRow.OneYear).To(BeNil())
		Expect(portfolioTaxRow.ThreeYear).To(BeNil())
		Expect(portfolioTaxRow.FiveYear).To(BeNil())
		Expect(portfolioTaxRow.TenYear).To(BeNil())

		// Benchmark-tax cells: cumulative gain * 0.85, then re-annualized
		// for since_inception over the same 6-day span.
		Expect(benchTaxRow.Title).To(Equal("Benchmark (after tax)"))
		Expect(benchTaxRow.Ytd).NotTo(BeNil())
		Expect(*benchTaxRow.Ytd).To(BeNumerically("~", 0.02*0.85, 1e-9))
		Expect(benchTaxRow.SinceInception).NotTo(BeNil())
		expectedBenchSI := math.Pow(1+0.02*0.85, 1.0/spanYears) - 1
		Expect(*benchTaxRow.SinceInception).To(BeNumerically("~", expectedBenchSI, 1e-6))
		Expect(benchTaxRow.OneYear).To(BeNil())
		Expect(benchTaxRow.ThreeYear).To(BeNil())
		Expect(benchTaxRow.FiveYear).To(BeNil())
		Expect(benchTaxRow.TenYear).To(BeNil())
	})
})
