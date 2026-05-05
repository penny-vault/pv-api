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

var _ = Describe("Summary", func() {
	It("returns KPIs from metadata + metrics + perf_data", func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		s, err := r.Summary(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(s.CurrentValue).To(BeNumerically("~", 103000, 0.01))
		Expect(s.Sharpe).NotTo(BeNil())
		Expect(*s.Sharpe).To(BeNumerically("~", 1.23, 0.001))
		Expect(s.Sortino).NotTo(BeNil())
		Expect(*s.Sortino).To(BeNumerically("~", 1.80, 0.001))
		Expect(s.Beta).NotTo(BeNil())
		Expect(*s.Beta).To(BeNumerically("~", 0.95, 0.001))
		Expect(s.MaxDrawDown).NotTo(BeNil())
		Expect(*s.MaxDrawDown).To(BeNumerically("~", -0.00495, 0.001))
		// Return-style metrics come straight from pvbt's metrics table.
		Expect(s.YtdReturn).NotTo(BeNil())
		Expect(*s.YtdReturn).To(BeNumerically("~", 0.03, 1e-9))
		Expect(s.CagrSinceInception).NotTo(BeNil())
		Expect(*s.CagrSinceInception).To(BeNumerically("~", 6.7, 1e-9))
	})
})
