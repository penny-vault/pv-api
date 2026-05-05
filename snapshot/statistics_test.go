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

var _ = Describe("Statistics", func() {
	It("maps metrics rows to PortfolioStatistic with labels", func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		stats, err := r.Statistics(context.Background())
		Expect(err).NotTo(HaveOccurred())

		byLabel := map[string]*float64{}
		for _, s := range stats {
			byLabel[s.Label] = s.Value
		}
		Expect(byLabel).To(HaveKey("Sharpe Ratio"))
		Expect(*byLabel["Sharpe Ratio"]).To(BeNumerically("~", 1.23, 0.001))
		Expect(byLabel).To(HaveKey("Beta"))
		Expect(*byLabel["Beta"]).To(BeNumerically("~", 0.95, 0.001))
		Expect(byLabel).To(HaveKey("Win Rate"))
		Expect(*byLabel["Win Rate"]).To(BeNumerically("~", 0.62, 0.001))

		// Metrics absent from the fixture surface as nil so the UI can render "--".
		Expect(byLabel).To(HaveKeyWithValue("Profit Factor", BeNil()))
	})
})
