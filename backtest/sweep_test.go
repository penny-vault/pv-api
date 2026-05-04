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

package backtest_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
)

type fakeSweeper struct {
	called  bool
	reason  string
	ports   int
	runs    int
	returns []int
}

func (f *fakeSweeper) MarkAllRunningAsFailed(_ context.Context, reason string) (int, int, error) {
	f.called = true
	f.reason = reason
	if len(f.returns) >= 2 {
		return f.returns[0], f.returns[1], nil
	}
	return f.ports, f.runs, nil
}

var _ = Describe("StartupSweep", func() {
	It("removes .tmp files older than 1h, including those in per-portfolio subdirs", func() {
		dir := GinkgoT().TempDir()
		past := time.Now().Add(-2 * time.Hour)

		// Top-level stale tmp (legacy layout).
		oldTop := filepath.Join(dir, "abc.sqlite.tmp")
		Expect(os.WriteFile(oldTop, []byte("x"), 0o644)).To(Succeed())
		Expect(os.Chtimes(oldTop, past, past)).To(Succeed())

		// Stale tmp inside a per-portfolio subdir (current layout).
		subDir := filepath.Join(dir, "portfolio-uuid")
		Expect(os.MkdirAll(subDir, 0o750)).To(Succeed())
		oldNested := filepath.Join(subDir, "run-uuid.sqlite.tmp")
		Expect(os.WriteFile(oldNested, []byte("x"), 0o644)).To(Succeed())
		Expect(os.Chtimes(oldNested, past, past)).To(Succeed())

		// Recent tmp must survive.
		recent := filepath.Join(subDir, "fresh-run.sqlite.tmp")
		Expect(os.WriteFile(recent, []byte("x"), 0o644)).To(Succeed())

		Expect(backtest.StartupSweep(context.Background(), dir, nil)).To(Succeed())

		_, oErr := os.Stat(oldTop)
		Expect(os.IsNotExist(oErr)).To(BeTrue())
		_, nErr := os.Stat(oldNested)
		Expect(os.IsNotExist(nErr)).To(BeTrue())
		_, rErr := os.Stat(recent)
		Expect(rErr).NotTo(HaveOccurred())
	})

	It("invokes the portfolio sweeper to flip in-flight portfolios and runs", func() {
		dir := GinkgoT().TempDir()
		sweeper := &fakeSweeper{returns: []int{2, 5}}

		Expect(backtest.StartupSweep(context.Background(), dir, sweeper)).To(Succeed())

		Expect(sweeper.called).To(BeTrue())
		Expect(sweeper.reason).To(ContainSubstring("server restarted"))
	})
})
