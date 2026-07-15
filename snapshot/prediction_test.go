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

	"github.com/penny-vault/pv-api/snapshot"
)

var _ = Describe("Prediction", func() {
	var path string

	BeforeEach(func() {
		path = filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
	})

	// exec runs a mutation against the fixture before it is opened
	// read-only (e.g. to simulate pre-schema-6 files).
	exec := func(stmt string) {
		db, err := sql.Open("sqlite", "file:"+path)
		Expect(err).NotTo(HaveOccurred())
		defer db.Close()
		_, err = db.Exec(stmt)
		Expect(err).NotTo(HaveOccurred())
	}

	It("returns the predicted date, transactions, and weighted holdings", func() {
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		p, err := r.Prediction(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Date.Format("2006-01-02")).To(Equal("2024-01-11"))

		Expect(p.Transactions).To(HaveLen(2))
		Expect(p.Transactions[0].Type).To(Equal("sell"))
		Expect(*p.Transactions[0].Ticker).To(Equal("VTI"))
		Expect(*p.Transactions[0].Justification).To(Equal("annual rebalance"))
		Expect(p.Transactions[1].Type).To(Equal("buy"))
		Expect(*p.Transactions[1].Ticker).To(Equal("QQQ"))
		Expect(*p.Transactions[1].Quantity).To(BeNumerically("==", 30))
		Expect(*p.Transactions[1].Price).To(BeNumerically("==", 400))
		Expect(p.Transactions[1].Justification).To(BeNil())

		Expect(p.Holdings).To(HaveLen(2))
		Expect(p.Holdings[0].Ticker).To(Equal("QQQ"))
		Expect(p.Holdings[0].MarketValue).To(BeNumerically("==", 12000))
		Expect(p.Holdings[0].Weight).To(BeNumerically("~", 0.75, 1e-9))
		Expect(p.Holdings[1].Ticker).To(Equal("SHV"))
		Expect(p.Holdings[1].Weight).To(BeNumerically("~", 0.25, 1e-9))
		Expect(p.TotalMarketValue).To(BeNumerically("==", 16000))
	})

	It("returns empty transactions when the strategy would not trade", func() {
		exec(`DELETE FROM predicted_transactions`)
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		p, err := r.Prediction(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Transactions).To(BeEmpty())
		Expect(p.Holdings).To(HaveLen(2))
	})

	It("returns ErrNotFound when no prediction was recorded", func() {
		exec(`DELETE FROM prediction`)
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		_, err = r.Prediction(context.Background())
		Expect(err).To(MatchError(snapshot.ErrNotFound))
	})

	It("returns ErrNotFound for pre-schema-6 files without prediction tables", func() {
		exec(`DROP TABLE prediction`)
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		_, err = r.Prediction(context.Background())
		Expect(err).To(MatchError(snapshot.ErrNotFound))
	})
})
