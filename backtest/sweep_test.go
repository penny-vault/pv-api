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

var _ = Describe("StartupSweep", func() {
	It("removes .tmp files older than 1h", func() {
		dir := GinkgoT().TempDir()
		old := filepath.Join(dir, "abc.sqlite.tmp")
		Expect(os.WriteFile(old, []byte("x"), 0o644)).To(Succeed())
		past := time.Now().Add(-2 * time.Hour)
		Expect(os.Chtimes(old, past, past)).To(Succeed())

		recent := filepath.Join(dir, "def.sqlite.tmp")
		Expect(os.WriteFile(recent, []byte("x"), 0o644)).To(Succeed())

		Expect(backtest.StartupSweep(context.Background(), dir, nil)).To(Succeed())
		_, oErr := os.Stat(old)
		Expect(os.IsNotExist(oErr)).To(BeTrue())
		_, rErr := os.Stat(recent)
		Expect(rErr).NotTo(HaveOccurred())
	})
})
