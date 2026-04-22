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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
)

var _ = Describe("BuildArgs", func() {
	It("emits --kebab-case flags for camelCase keys with --benchmark appended", func() {
		params := map[string]any{
			"momentumWindow": 90,
			"riskProfile":    "aggressive",
			"useTax":         true,
		}
		args := backtest.BuildArgs(params, "SPY", nil, nil)
		Expect(args).To(ContainElement("--momentum-window"))
		Expect(args).To(ContainElement("90"))
		Expect(args).To(ContainElement("--risk-profile"))
		Expect(args).To(ContainElement("aggressive"))
		Expect(args).To(ContainElement("--use-tax"))
		Expect(args).To(ContainElement("true"))
		Expect(args).To(ContainElement("--benchmark"))
		Expect(args).To(ContainElement("SPY"))
	})

	It("serializes arrays as comma-joined strings", func() {
		params := map[string]any{
			"tickers": []any{"VTI", "BND"},
		}
		args := backtest.BuildArgs(params, "", nil, nil)
		Expect(args).To(ContainElement("--tickers"))
		Expect(args).To(ContainElement("VTI,BND"))
	})

	It("omits --benchmark when blank", func() {
		args := backtest.BuildArgs(map[string]any{}, "", nil, nil)
		Expect(args).NotTo(ContainElement("--benchmark"))
	})

	It("produces deterministic order", func() {
		params := map[string]any{"z": 1, "a": 2}
		a := backtest.BuildArgs(params, "", nil, nil)
		b := backtest.BuildArgs(params, "", nil, nil)
		Expect(a).To(Equal(b))
	})

	It("appends --start and --end when both are provided", func() {
		start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		end := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
		args := backtest.BuildArgs(map[string]any{}, "", &start, &end)
		Expect(args).To(ContainElements("--start", "2020-01-01", "--end", "2024-12-31"))
	})

	It("omits --start and --end when nil", func() {
		args := backtest.BuildArgs(map[string]any{}, "", nil, nil)
		Expect(args).NotTo(ContainElement("--start"))
		Expect(args).NotTo(ContainElement("--end"))
	})

	It("appends --start before --benchmark", func() {
		start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		args := backtest.BuildArgs(map[string]any{}, "SPY", &start, nil)
		startIdx := -1
		benchIdx := -1
		for i, a := range args {
			if a == "--start" {
				startIdx = i
			}
			if a == "--benchmark" {
				benchIdx = i
			}
		}
		Expect(startIdx).To(BeNumerically("<", benchIdx))
	})
})
