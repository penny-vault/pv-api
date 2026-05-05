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
	It("emits portfolio, benchmark, portfolio-tax, and benchmark-tax rows from pvbt metrics", func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		rows, err := r.TrailingReturns(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(4))

		byKind := map[openapi.ReturnRowKind]openapi.TrailingReturnRow{}
		for _, row := range rows {
			byKind[row.Kind] = row
		}

		expectRow := func(kind openapi.ReturnRowKind, title string, ytd, since float64) openapi.TrailingReturnRow {
			row, ok := byKind[kind]
			Expect(ok).To(BeTrue(), "expected row for kind %s", kind)
			Expect(row.Title).To(Equal(title))
			Expect(row.Ytd).NotTo(BeNil())
			Expect(*row.Ytd).To(BeNumerically("~", ytd, 1e-9))
			Expect(row.SinceInception).NotTo(BeNil())
			Expect(*row.SinceInception).To(BeNumerically("~", since, 1e-9))
			// 5-day fixture: only ytd / since_inception have metrics rows.
			// Every other window must surface as null — no fallback.
			Expect(row.OneYear).To(BeNil())
			Expect(row.ThreeYear).To(BeNil())
			Expect(row.FiveYear).To(BeNil())
			Expect(row.TenYear).To(BeNil())
			return row
		}

		expectRow(openapi.ReturnRowKindPortfolio, "Portfolio", 0.03, 6.7)
		expectRow(openapi.ReturnRowKindBenchmark, "Benchmark", 0.02, 4.5)
		expectRow(openapi.ReturnRowKindPortfolioTax, "Portfolio (after tax)", 0.027, 5.9)
		expectRow(openapi.ReturnRowKindBenchmarkTax, "Benchmark (after tax)", 0.017, 3.8)
	})

})
