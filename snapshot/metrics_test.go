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

	"github.com/penny-vault/pv-api/snapshot"
)

var _ = Describe("Metrics", func() {
	var (
		r   *snapshot.Reader
		ctx = context.Background()
	)

	BeforeEach(func() {
		path := filepath.Join(GinkgoT().TempDir(), "m.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		var err error
		r, err = snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(r.Close)
	})

	It("returns metrics for since_inception by default", func() {
		result, err := r.Metrics(ctx, []string{"since_inception"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Windows).To(Equal([]string{"since_inception"}))

		Expect(result.Summary).NotTo(BeNil())
		sharpe := (*result.Summary)["Sharpe"]
		Expect(sharpe).To(HaveLen(1))
		Expect(*sharpe[0]).To(BeNumerically("~", 1.55, 0.001))
	})

	It("returns values for multiple windows, nil where window is missing", func() {
		result, err := r.Metrics(ctx, []string{"since_inception", "1yr"}, []string{"Sharpe", "UpsideCaptureRatio"})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Windows).To(Equal([]string{"since_inception", "1yr"}))

		sharpe := (*result.Summary)["Sharpe"]
		Expect(sharpe).To(HaveLen(2))
		Expect(*sharpe[0]).To(BeNumerically("~", 1.55, 0.001)) // since_inception
		Expect(*sharpe[1]).To(BeNumerically("~", 1.20, 0.001)) // 1yr

		// UpsideCaptureRatio has since_inception but not 1yr
		upCapture := (*result.Risk)["UpsideCaptureRatio"]
		Expect(upCapture).To(HaveLen(2))
		Expect(*upCapture[0]).To(BeNumerically("~", 1.05, 0.001))
		Expect(upCapture[1]).To(BeNil())
	})

	It("filters to requested metric names", func() {
		result, err := r.Metrics(ctx, []string{"since_inception"}, []string{"WinRate"})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Summary).To(BeNil())
		Expect(result.Risk).To(BeNil())
		Expect(result.Trade).NotTo(BeNil())
		Expect(*result.Trade).To(HaveKey("WinRate"))
		Expect(result.Advanced).To(BeNil())
	})

	It("places metrics in correct categories", func() {
		result, err := r.Metrics(ctx, []string{"since_inception"}, []string{"Beta", "TaxCostRatio", "Sortino"})
		Expect(err).NotTo(HaveOccurred())
		Expect((*result.Risk)).To(HaveKey("Beta"))
		Expect((*result.Tax)).To(HaveKey("TaxCostRatio"))
		Expect((*result.Summary)).To(HaveKey("Sortino"))
	})

	It("silently drops unknown metric names", func() {
		result, err := r.Metrics(ctx, []string{"since_inception"}, []string{"Sharpe", "NotAMetric"})
		Expect(err).NotTo(HaveOccurred())
		Expect((*result.Summary)).To(HaveKey("Sharpe"))
		Expect((*result.Summary)).NotTo(HaveKey("NotAMetric"))
	})

	It("silently drops unknown window names", func() {
		result, err := r.Metrics(ctx, []string{"bogus", "since_inception"}, []string{"Sharpe"})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Windows).To(Equal([]string{"since_inception"}))
	})

	It("omits metrics that have no rows in the snapshot", func() {
		result, err := r.Metrics(ctx, []string{"since_inception"}, []string{"CVaR"})
		Expect(err).NotTo(HaveOccurred())
		// CVaR has no rows in fixture — advanced should be nil or empty
		if result.Advanced != nil {
			Expect(*result.Advanced).NotTo(HaveKey("CVaR"))
		}
	})
})
