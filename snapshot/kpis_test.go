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

var _ = Describe("Kpis", func() {
	It("computes current value, CAGR, and pulls risk metrics", func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		k, err := r.Kpis(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(k.CurrentValue).To(BeNumerically("~", 103000, 0.01))
		Expect(k.Sharpe).NotTo(BeNil())
		Expect(*k.Sharpe).To(BeNumerically("~", 1.23, 0.001))
		Expect(k.MaxDrawdown).NotTo(BeNil())
		Expect(*k.MaxDrawdown).To(BeNumerically("~", -0.00495, 0.001))
		Expect(k.InceptionDate.Format("2006-01-02")).To(Equal("2024-01-02"))
		// pvbt-emitted return metrics flow through Kpis verbatim.
		Expect(k.YtdReturn).NotTo(BeNil())
		Expect(*k.YtdReturn).To(BeNumerically("~", 0.03, 1e-9))
		Expect(k.Cagr).NotTo(BeNil())
		Expect(*k.Cagr).To(BeNumerically("~", 6.7, 1e-9))
		Expect(k.BenchmarkYtdReturn).NotTo(BeNil())
		Expect(*k.BenchmarkYtdReturn).To(BeNumerically("~", 0.02, 1e-9))
		// 1yr is not in the 5-day fixture, so the cell stays nil.
		Expect(k.OneYearReturn).To(BeNil())
	})
})
