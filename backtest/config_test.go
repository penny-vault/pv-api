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
	"runtime"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
)

var _ = Describe("Config", func() {
	Describe("ApplyDefaults", func() {
		It("sets MaxConcurrency to runtime.NumCPU when zero", func() {
			c := backtest.Config{}
			c.ApplyDefaults()
			Expect(c.MaxConcurrency).To(Equal(runtime.NumCPU()))
		})

		It("preserves a non-zero MaxConcurrency", func() {
			c := backtest.Config{MaxConcurrency: 3}
			c.ApplyDefaults()
			Expect(c.MaxConcurrency).To(Equal(3))
		})

		It("sets Timeout to 15 minutes when zero", func() {
			c := backtest.Config{}
			c.ApplyDefaults()
			Expect(c.Timeout).To(Equal(15 * time.Minute))
		})
	})

	Describe("Validate", func() {
		It("rejects empty SnapshotsDir", func() {
			c := backtest.Config{RunnerMode: "host"}
			Expect(c.Validate()).To(MatchError(ContainSubstring("snapshots_dir")))
		})

		It("accepts docker mode with a snapshots dir", func() {
			c := backtest.Config{SnapshotsDir: "/tmp/snaps", RunnerMode: "docker"}
			Expect(c.Validate()).To(Succeed())
		})

		It("rejects kubernetes mode", func() {
			c := backtest.Config{SnapshotsDir: "/tmp/snaps", RunnerMode: "kubernetes"}
			Expect(c.Validate()).To(MatchError(ContainSubstring("runner.mode")))
		})

		It("accepts host mode with a snapshots dir", func() {
			c := backtest.Config{SnapshotsDir: "/tmp/snaps", RunnerMode: "host"}
			Expect(c.Validate()).To(Succeed())
		})

		It("rejects negative MaxConcurrency", func() {
			c := backtest.Config{SnapshotsDir: "/tmp/snaps", RunnerMode: "host", MaxConcurrency: -1}
			Expect(c.Validate()).To(MatchError(ContainSubstring("max_concurrency")))
		})
	})
})
