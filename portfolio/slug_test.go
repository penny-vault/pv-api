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

package portfolio_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("Slug", func() {
	admDescribe := strategy.Describe{
		ShortCode: "adm",
		Presets: []strategy.DescribePreset{
			{Name: "standard", Parameters: map[string]any{"riskOn": "VFINX,PRIDX,QQQ"}},
			{Name: "aggressive", Parameters: map[string]any{"riskOn": "SPY,GLD,VWO"}},
		},
	}

	It("uses the matched preset name for matching params", func() {
		slug, err := portfolio.Slug(portfolio.CreateRequest{
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY,GLD,VWO"},
			Benchmark:    "SPY",
		}, admDescribe)
		Expect(err).NotTo(HaveOccurred())
		Expect(slug).To(HavePrefix("adm-aggressive-"))
		Expect(slug).To(HaveLen(len("adm-aggressive-") + 4))
	})

	It("uses `custom` when no preset matches", func() {
		slug, err := portfolio.Slug(portfolio.CreateRequest{
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "NVDA,AMD"},
			Benchmark:    "SPY",
		}, admDescribe)
		Expect(err).NotTo(HaveOccurred())
		Expect(slug).To(HavePrefix("adm-custom-"))
	})

	It("is deterministic: identical configs produce identical slugs", func() {
		req := portfolio.CreateRequest{
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY,GLD,VWO"},
			Benchmark:    "SPY",
		}
		a, err := portfolio.Slug(req, admDescribe)
		Expect(err).NotTo(HaveOccurred())
		b, err := portfolio.Slug(req, admDescribe)
		Expect(err).NotTo(HaveOccurred())
		Expect(a).To(Equal(b))
	})

	It("differs when benchmark differs", func() {
		base := portfolio.CreateRequest{
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY,GLD,VWO"},
			Benchmark:    "SPY",
		}
		a, _ := portfolio.Slug(base, admDescribe)
		base.Benchmark = "QQQ"
		b, _ := portfolio.Slug(base, admDescribe)
		Expect(a).NotTo(Equal(b))
	})

	It("differs when name differs", func() {
		base := portfolio.CreateRequest{
			Name:         "My Portfolio",
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY,GLD,VWO"},
			Benchmark:    "SPY",
		}
		a, _ := portfolio.Slug(base, admDescribe)
		base.Name = "Other Portfolio"
		b, _ := portfolio.Slug(base, admDescribe)
		Expect(a).NotTo(Equal(b))
	})

	It("differs when startDate differs", func() {
		d1 := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		d2 := time.Date(2021, 6, 15, 0, 0, 0, 0, time.UTC)
		base := portfolio.CreateRequest{
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY,GLD,VWO"},
			Benchmark:    "SPY",
			StartDate:    &d1,
		}
		a, _ := portfolio.Slug(base, admDescribe)
		base.StartDate = &d2
		b, _ := portfolio.Slug(base, admDescribe)
		Expect(a).NotTo(Equal(b))
	})

	It("differs when startDate is nil vs set", func() {
		d := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		withDate := portfolio.CreateRequest{
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY,GLD,VWO"},
			Benchmark:    "SPY",
			StartDate:    &d,
		}
		withoutDate := portfolio.CreateRequest{
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY,GLD,VWO"},
			Benchmark:    "SPY",
		}
		a, _ := portfolio.Slug(withDate, admDescribe)
		b, _ := portfolio.Slug(withoutDate, admDescribe)
		Expect(a).NotTo(Equal(b))
	})

	It("canonicalizes parameter key order (same map order-independent)", func() {
		d := strategy.Describe{ShortCode: "x"}
		a, err := portfolio.Slug(portfolio.CreateRequest{
			StrategyCode: "x", Benchmark: "SPY",
			Parameters: map[string]any{"a": 1, "b": 2},
		}, d)
		Expect(err).NotTo(HaveOccurred())
		b, err := portfolio.Slug(portfolio.CreateRequest{
			StrategyCode: "x", Benchmark: "SPY",
			Parameters: map[string]any{"b": 2, "a": 1},
		}, d)
		Expect(err).NotTo(HaveOccurred())
		Expect(a).To(Equal(b))
	})

	It("sanitizes preset names into kebab-case in the slug", func() {
		d := strategy.Describe{
			ShortCode: "foo",
			Presets: []strategy.DescribePreset{
				{Name: "Really Aggressive!", Parameters: map[string]any{"k": "v"}},
			},
		}
		slug, err := portfolio.Slug(portfolio.CreateRequest{
			StrategyCode: "foo",
			Parameters:   map[string]any{"k": "v"},
			Benchmark:    "SPY",
		}, d)
		Expect(err).NotTo(HaveOccurred())
		Expect(slug).To(HavePrefix("foo-really-aggressive-"))
	})
})
