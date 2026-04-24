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
	"database/sql"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	_ "modernc.org/sqlite"

	"github.com/penny-vault/pv-api/openapi"
	"github.com/penny-vault/pv-api/snapshot"
)

// execAll runs a sequence of SQL statements against the file at path. Used by
// tests that need to mutate a fixture snapshot before reopening.
func execAll(path string, stmts []string) error {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

var _ = Describe("Reader.HoldingsImpact", func() {
	var path string
	var r *snapshot.Reader

	BeforeEach(func() {
		path = filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		var err error
		r, err = snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
	})
	AfterEach(func() {
		if r != nil {
			_ = r.Close()
		}
	})

	findPeriod := func(resp *openapi.HoldingsImpactResponse, id openapi.HoldingsImpactPeriodPeriod) *openapi.HoldingsImpactPeriod {
		for i := range resp.Periods {
			if resp.Periods[i].Period == id {
				return &resp.Periods[i]
			}
		}
		return nil
	}

	findItem := func(p *openapi.HoldingsImpactPeriod, ticker string) *openapi.HoldingsImpactItem {
		for i := range p.Items {
			if p.Items[i].Ticker == ticker {
				return &p.Items[i]
			}
		}
		return nil
	}

	It("sums items + rest to cumulativeReturn per period (two-ticker happy path)", func() {
		resp, err := r.HoldingsImpact(context.Background(), "acme", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.PortfolioSlug).To(Equal("acme"))
		Expect(resp.Currency).To(Equal("USD"))
		Expect(resp.AsOf.Format("2006-01-02")).To(Equal("2024-01-08"))

		inception := findPeriod(resp, openapi.HoldingsImpactPeriodPeriodInception)
		Expect(inception).NotTo(BeNil())
		Expect(inception.Label).To(Equal("Since inception"))
		Expect(inception.StartDate.Format("2006-01-02")).To(Equal("2024-01-02"))
		Expect(inception.EndDate.Format("2006-01-02")).To(Equal("2024-01-08"))
		Expect(inception.CumulativeReturn).To(BeNumerically("~", 0.03, 1e-6))

		// Both VTI and $CASH present.
		Expect(inception.Items).To(HaveLen(2))
		vti := findItem(inception, "VTI")
		cash := findItem(inception, "$CASH")
		Expect(vti).NotTo(BeNil())
		Expect(cash).NotTo(BeNil())
		Expect(vti.Figi).NotTo(BeNil())
		Expect(*vti.Figi).To(Equal("BBG000BDTBL9"))
		Expect(cash.Figi).To(BeNil())

		// items + rest must sum to cumulativeReturn exactly (pre-round tolerance 1e-6).
		sum := inception.Rest.Contribution
		for _, it := range inception.Items {
			sum += it.Contribution
		}
		Expect(sum).To(BeNumerically("~", inception.CumulativeReturn, 1e-6))

		// With topN=10 both tickers are in items; rest is empty.
		Expect(inception.Rest.Count).To(Equal(int64(0)))
		Expect(inception.Rest.Contribution).To(BeNumerically("~", 0.0, 1e-12))
	})

	It("folds extras into rest when top=1", func() {
		resp, err := r.HoldingsImpact(context.Background(), "acme", 1)
		Expect(err).NotTo(HaveOccurred())
		inception := findPeriod(resp, openapi.HoldingsImpactPeriodPeriodInception)
		Expect(inception).NotTo(BeNil())
		Expect(inception.Items).To(HaveLen(1))
		Expect(inception.Rest.Count).To(Equal(int64(1)))

		// In the standard fixture, $CASH has the larger absolute contribution
		// (pnl_CASH=2674.50 vs pnl_VTI=325.50), so $CASH should be the sole item.
		Expect(inception.Items[0].Ticker).To(Equal("$CASH"))

		// Identity still holds: items.contribution + rest.contribution == cumulativeReturn.
		sum := inception.Rest.Contribution + inception.Items[0].Contribution
		Expect(sum).To(BeNumerically("~", inception.CumulativeReturn, 1e-6))
	})

	It("omits 5y/3y/1y when history is too short", func() {
		resp, err := r.HoldingsImpact(context.Background(), "acme", 10)
		Expect(err).NotTo(HaveOccurred())

		Expect(findPeriod(resp, openapi.HoldingsImpactPeriodPeriodN5y)).To(BeNil())
		Expect(findPeriod(resp, openapi.HoldingsImpactPeriodPeriodN3y)).To(BeNil())
		Expect(findPeriod(resp, openapi.HoldingsImpactPeriodPeriodN1y)).To(BeNil())
		Expect(findPeriod(resp, openapi.HoldingsImpactPeriodPeriodInception)).NotTo(BeNil())
		Expect(findPeriod(resp, openapi.HoldingsImpactPeriodPeriodYtd)).NotTo(BeNil())
	})

	It("credits dividends to the paying ticker", func() {
		resp, err := r.HoldingsImpact(context.Background(), "acme", 10)
		Expect(err).NotTo(HaveOccurred())
		inception := findPeriod(resp, openapi.HoldingsImpactPeriodPeriodInception)
		Expect(inception).NotTo(BeNil())
		vti := findItem(inception, "VTI")
		Expect(vti).NotTo(BeNil())

		// VTI mv moved 10000 -> 10300 (+300), and the 25.50 dividend flowed out
		// to $CASH. Attribution: pnl_VTI = (mv1-mv0) - flowSum = 300 - (-25.50) = 325.50.
		// contribution = 325.50 / V0(100000) = 0.003255.
		Expect(vti.Contribution).To(BeNumerically("~", 0.003255, 1e-6))

		// Without the dividend credit VTI would only show 0.003 (= 300/100000),
		// so seeing 0.003255 confirms the dividend was attributed to VTI.
		Expect(vti.Contribution).NotTo(BeNumerically("~", 0.003, 1e-6))
	})

	It("reflects partial-period hold for a mid-period buy (custom fixture)", func() {
		// Close the default reader; we're building a custom fixture.
		_ = r.Close()
		r = nil

		path2 := filepath.Join(GinkgoT().TempDir(), "mid.sqlite")
		Expect(snapshot.BuildTestSnapshot(path2)).To(Succeed())
		// Mutate: replace the 2024-01-02 VTI positions_daily row so VTI
		// shows up only from 2024-01-03 onward (mid-period entry). Keep
		// the portfolio equity identity intact by moving the pre-buy
		// market value into $CASH for that date.
		Expect(execAll(path2, []string{
			`DELETE FROM positions_daily WHERE date='2024-01-02' AND ticker='VTI'`,
			`UPDATE positions_daily SET market_value=100000, quantity=100000 WHERE date='2024-01-02' AND ticker='$CASH'`,
			`DELETE FROM transactions WHERE date='2024-01-02'`,
			`INSERT INTO transactions VALUES (1, '2024-01-03', 'buy', 'VTI', 'BBG000BDTBL9', 100, 100, 10000, 0, 'mid buy')`,
		})).To(Succeed())

		r2, err := snapshot.Open(path2)
		Expect(err).NotTo(HaveOccurred())
		defer r2.Close()

		resp, err := r2.HoldingsImpact(context.Background(), "acme", 10)
		Expect(err).NotTo(HaveOccurred())
		inception := findPeriod(resp, openapi.HoldingsImpactPeriodPeriodInception)
		Expect(inception).NotTo(BeNil())

		vti := findItem(inception, "VTI")
		Expect(vti).NotTo(BeNil())
		// VTI held 4 of 5 days (01-03..01-08), so holdingDays must be less than
		// the full window length (5 days).
		Expect(vti.HoldingDays).To(Equal(int64(4)))

		// Hold the full window would give avgWeight = mean of (mv_k / total_v)
		// across 5 days. With a missing day on 01-02 the mean is reduced.
		// Sanity bound: avgWeight must be strictly less than 0.1 (full hold
		// would be ~0.0976; partial is ~0.078).
		Expect(vti.AvgWeight).To(BeNumerically("<", 0.09))
		Expect(vti.AvgWeight).To(BeNumerically(">", 0))
	})

	It("errors when positions do not balance perf_data (residual guard)", func() {
		_ = r.Close()
		r = nil

		path2 := filepath.Join(GinkgoT().TempDir(), "bad.sqlite")
		Expect(snapshot.BuildTestSnapshot(path2)).To(Succeed())
		// Inject a residual: bump VTI mv on 01-08 without touching perf_data.
		Expect(execAll(path2, []string{
			`UPDATE positions_daily SET market_value=99999 WHERE date='2024-01-08' AND ticker='VTI'`,
		})).To(Succeed())

		r2, err := snapshot.Open(path2)
		Expect(err).NotTo(HaveOccurred())
		defer r2.Close()

		_, err = r2.HoldingsImpact(context.Background(), "acme", 10)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("residual"))
		// The error must identify the period id (inception is the first/longest
		// window that fails).
		Expect(err.Error()).To(ContainSubstring("inception"))
	})
})
