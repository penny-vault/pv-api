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

var _ = Describe("Holdings", func() {
	var reader *snapshot.Reader

	BeforeEach(func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		var err error
		reader, err = snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(reader.Close)
	})

	Describe("CurrentHoldings", func() {
		It("returns the holdings rows with totalMarketValue summed", func() {
			resp, err := reader.CurrentHoldings(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Items).To(HaveLen(2))
			Expect(resp.TotalMarketValue).To(BeNumerically("~", 103300, 0.01))
		})
	})

	Describe("HoldingsAsOf", func() {
		It("returns the replayed position on the buy date", func() {
			d := mustDate("2024-01-02")
			resp, err := reader.HoldingsAsOf(context.Background(), d)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Items).To(HaveLen(1))
			Expect(resp.Items[0].Ticker).To(Equal("VTI"))
			Expect(resp.Items[0].Quantity).To(BeNumerically("~", 100, 0.01))
		})

		It("returns ErrNotFound for a date outside the backtest window", func() {
			d := mustDate("2023-06-01")
			_, err := reader.HoldingsAsOf(context.Background(), d)
			Expect(err).To(MatchError(snapshot.ErrNotFound))
		})
	})

	Describe("HoldingsHistory", func() {
		It("emits one entry per batch with cumulative holdings and annotations", func() {
			resp, err := reader.HoldingsHistory(context.Background(), nil, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Items).To(HaveLen(3))

			Expect(resp.Items[0].BatchId).To(Equal(int64(1)))
			Expect(resp.Items[0].Items).To(HaveLen(1))
			Expect(resp.Items[0].Items[0].Ticker).To(Equal("VTI"))
			Expect(resp.Items[0].Items[0].Quantity).To(BeNumerically("~", 100, 0.01))
			Expect(*resp.Items[0].Annotations).To(HaveKeyWithValue("reason", "initial allocation"))

			Expect(resp.Items[1].BatchId).To(Equal(int64(2)))
			Expect(resp.Items[1].Items).To(HaveLen(1))
			Expect(resp.Items[1].Items[0].Quantity).To(BeNumerically("~", 100, 0.01))

			Expect(resp.Items[2].BatchId).To(Equal(int64(3)))
			Expect(resp.Items[2].Items).To(HaveLen(1))
			Expect(resp.Items[2].Items[0].Quantity).To(BeNumerically("~", 100, 0.01))
		})

		It("filters the batch range by from/to timestamps", func() {
			from := mustDate("2024-01-04")
			to := mustDate("2024-01-06")
			resp, err := reader.HoldingsHistory(context.Background(), &from, &to)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Items).To(HaveLen(1))
			Expect(resp.Items[0].BatchId).To(Equal(int64(2)))
		})
	})
})
