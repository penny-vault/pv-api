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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/openapi"
	"github.com/penny-vault/pv-api/snapshot"
)

var _ = Describe("Transactions", func() {
	var reader *snapshot.Reader

	BeforeEach(func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		var err error
		reader, err = snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(reader.Close)
	})

	It("returns all transactions when no filter is provided", func() {
		resp, err := reader.Transactions(context.Background(), snapshot.TransactionFilter{})
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Items).To(HaveLen(4))
		Expect(resp.Items[0].Type).To(BeEquivalentTo(openapi.Buy))
		Expect(resp.Items[0].BatchId).To(Equal(int64(1)))
		Expect(resp.Items[1].Type).To(BeEquivalentTo(openapi.Dividend))
		Expect(resp.Items[1].BatchId).To(Equal(int64(2)))
		Expect(resp.Items[2].Type).To(BeEquivalentTo(openapi.Sell))
		Expect(resp.Items[2].BatchId).To(Equal(int64(4)))
		Expect(resp.Items[3].Type).To(BeEquivalentTo(openapi.Buy))
		Expect(resp.Items[3].BatchId).To(Equal(int64(4)))
	})

	It("filters by date range inclusively", func() {
		from := mustDate("2024-01-04")
		to := mustDate("2024-01-08")
		resp, err := reader.Transactions(context.Background(), snapshot.TransactionFilter{From: &from, To: &to})
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Items).To(HaveLen(1))
		Expect(resp.Items[0].Type).To(BeEquivalentTo(openapi.Dividend))
	})

	It("filters by type", func() {
		resp, err := reader.Transactions(context.Background(), snapshot.TransactionFilter{Types: []string{"buy"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Items).To(HaveLen(2))
		Expect(resp.Items[0].Type).To(BeEquivalentTo(openapi.Buy))
		Expect(resp.Items[1].Type).To(BeEquivalentTo(openapi.Buy))
	})
})

func mustDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	Expect(err).NotTo(HaveOccurred())
	return t
}
