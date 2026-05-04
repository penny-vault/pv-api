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

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
)

type fakeOrphanStore struct {
	portfolios map[uuid.UUID]struct{}
	runs       map[uuid.UUID]struct{}
}

func (f *fakeOrphanStore) AllPortfolioIDs(_ context.Context) (map[uuid.UUID]struct{}, error) {
	return f.portfolios, nil
}

func (f *fakeOrphanStore) AllRunIDs(_ context.Context) (map[uuid.UUID]struct{}, error) {
	return f.runs, nil
}

var _ = Describe("SweepOrphans", func() {
	It("removes per-portfolio dirs whose UUID is no longer in portfolios", func() {
		dir := GinkgoT().TempDir()
		live := uuid.New()
		dead := uuid.New()
		Expect(os.MkdirAll(filepath.Join(dir, live.String()), 0o750)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(dir, dead.String()), 0o750)).To(Succeed())

		store := &fakeOrphanStore{
			portfolios: map[uuid.UUID]struct{}{live: {}},
			runs:       map[uuid.UUID]struct{}{},
		}
		n, err := backtest.SweepOrphans(context.Background(), dir, store)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(1))

		_, livErr := os.Stat(filepath.Join(dir, live.String()))
		Expect(livErr).NotTo(HaveOccurred(), "live portfolio dir must remain")
		_, deadErr := os.Stat(filepath.Join(dir, dead.String()))
		Expect(os.IsNotExist(deadErr)).To(BeTrue(), "orphan dir should be removed")
	})

	It("removes snapshot files whose run UUID is not in backtest_runs", func() {
		dir := GinkgoT().TempDir()
		port := uuid.New()
		liveRun := uuid.New()
		orphanRun := uuid.New()
		portDir := filepath.Join(dir, port.String())
		Expect(os.MkdirAll(portDir, 0o750)).To(Succeed())
		liveFile := filepath.Join(portDir, liveRun.String()+".sqlite")
		orphanFile := filepath.Join(portDir, orphanRun.String()+".sqlite")
		Expect(os.WriteFile(liveFile, []byte("a"), 0o644)).To(Succeed())
		Expect(os.WriteFile(orphanFile, []byte("b"), 0o644)).To(Succeed())

		store := &fakeOrphanStore{
			portfolios: map[uuid.UUID]struct{}{port: {}},
			runs:       map[uuid.UUID]struct{}{liveRun: {}},
		}
		n, err := backtest.SweepOrphans(context.Background(), dir, store)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(1))

		_, livErr := os.Stat(liveFile)
		Expect(livErr).NotTo(HaveOccurred(), "active snapshot must remain")
		_, orphErr := os.Stat(orphanFile)
		Expect(os.IsNotExist(orphErr)).To(BeTrue(), "orphan snapshot should be removed")
	})

	It("ignores .sqlite.tmp files (owned by stale-tmp sweep)", func() {
		dir := GinkgoT().TempDir()
		port := uuid.New()
		portDir := filepath.Join(dir, port.String())
		Expect(os.MkdirAll(portDir, 0o750)).To(Succeed())
		tmpFile := filepath.Join(portDir, uuid.New().String()+".sqlite.tmp")
		Expect(os.WriteFile(tmpFile, []byte("x"), 0o644)).To(Succeed())

		store := &fakeOrphanStore{
			portfolios: map[uuid.UUID]struct{}{port: {}},
			runs:       map[uuid.UUID]struct{}{},
		}
		n, err := backtest.SweepOrphans(context.Background(), dir, store)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(0))

		_, tErr := os.Stat(tmpFile)
		Expect(tErr).NotTo(HaveOccurred(), "tmp files are not the orphan sweep's concern")
	})

	It("ignores top-level legacy files", func() {
		dir := GinkgoT().TempDir()
		legacyPath := filepath.Join(dir, uuid.New().String()+".sqlite")
		Expect(os.WriteFile(legacyPath, []byte("x"), 0o644)).To(Succeed())

		store := &fakeOrphanStore{
			portfolios: map[uuid.UUID]struct{}{},
			runs:       map[uuid.UUID]struct{}{},
		}
		n, err := backtest.SweepOrphans(context.Background(), dir, store)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(0))

		_, lErr := os.Stat(legacyPath)
		Expect(lErr).NotTo(HaveOccurred(), "top-level legacy files are left to manual cleanup")
	})

	It("ignores files and dirs whose names aren't UUIDs", func() {
		dir := GinkgoT().TempDir()
		Expect(os.MkdirAll(filepath.Join(dir, "not-a-uuid"), 0o750)).To(Succeed())

		store := &fakeOrphanStore{}
		n, err := backtest.SweepOrphans(context.Background(), dir, store)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(0))

		_, sErr := os.Stat(filepath.Join(dir, "not-a-uuid"))
		Expect(sErr).NotTo(HaveOccurred())
	})
})
