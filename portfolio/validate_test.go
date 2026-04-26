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
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("ValidateCreate", func() {
	installed := "v1.0.0"
	admDescribe := []byte(`{"shortCode":"adm","name":"ADM","parameters":[{"name":"riskOn","type":"universe"}],"schedule":"@monthend","benchmark":"SPY"}`)

	makeStrategy := func() strategy.Strategy {
		return strategy.Strategy{
			ShortCode:    "adm",
			RepoOwner:    "penny-vault",
			RepoName:     "adm",
			CloneURL:     "https://github.com/penny-vault/adm.git",
			IsOfficial:   true,
			InstalledVer: &installed,
			DescribeJSON: admDescribe,
		}
	}

	It("accepts a valid request and normalises StrategyVer and Benchmark", func() {
		req := portfolio.CreateRequest{
			Name:         "foo",
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY"},
		}
		norm, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(err).NotTo(HaveOccurred())
		Expect(norm.StrategyVer).To(Equal("v1.0.0"))
		Expect(norm.Benchmark).To(Equal("SPY"))
	})

	It("defaults benchmark to strategy describe benchmark when blank", func() {
		req := portfolio.CreateRequest{
			Name:         "foo",
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY"},
		}
		norm, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(err).NotTo(HaveOccurred())
		Expect(norm.Benchmark).To(Equal("SPY"))
	})

	It("rejects a strategy version that does not match installed_ver", func() {
		req := portfolio.CreateRequest{
			Name:         "foo",
			StrategyCode: "adm",
			StrategyVer:  "v9.9.9",
			Parameters:   map[string]any{"riskOn": "SPY"},
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrStrategyVersionMismatch)).To(BeTrue())
	})

	It("rejects when the strategy is not installed", func() {
		s := makeStrategy()
		s.InstalledVer = nil
		s.DescribeJSON = nil
		req := portfolio.CreateRequest{
			Name:         "foo",
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY"},
		}
		_, err := portfolio.ValidateCreate(req, s)
		Expect(errors.Is(err, portfolio.ErrStrategyNotReady)).To(BeTrue())
	})

	It("rejects unknown parameter keys", func() {
		req := portfolio.CreateRequest{
			Name:         "foo",
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY", "bogus": 42},
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrUnknownParameter)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("bogus"))
	})

	It("rejects missing required parameters", func() {
		req := portfolio.CreateRequest{
			Name:         "foo",
			StrategyCode: "adm",
			Parameters:   map[string]any{},
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrMissingParameter)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("riskOn"))
	})

	It("accepts valid startDate and endDate", func() {
		start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		end := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
		req := portfolio.CreateRequest{
			Name:         "foo",
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY"},
			StartDate:    &start,
			EndDate:      &end,
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects endDate before startDate with ErrEndBeforeStart", func() {
		start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		end := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		req := portfolio.CreateRequest{
			Name:         "foo",
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY"},
			StartDate:    &start,
			EndDate:      &end,
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrEndBeforeStart)).To(BeTrue())
	})

	It("accepts equal startDate and endDate", func() {
		d := time.Date(2022, 6, 1, 0, 0, 0, 0, time.UTC)
		req := portfolio.CreateRequest{
			Name:         "foo",
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY"},
			StartDate:    &d,
			EndDate:      &d,
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("ValidateCreateUnofficial", func() {
	d := strategy.Describe{
		ShortCode: "fake",
		Name:      "Fake",
		Parameters: []strategy.DescribeParameter{
			{Name: "riskOn", Type: "universe"},
		},
		Schedule:  "@monthend",
		Benchmark: "SPY",
	}

	It("accepts a well-formed request", func() {
		req := portfolio.CreateRequest{
			Name:       "p",
			Parameters: map[string]any{"riskOn": "SPY"},
		}
		norm, err := portfolio.ValidateCreateUnofficial(req, d)
		Expect(err).NotTo(HaveOccurred())
		Expect(norm.Benchmark).To(Equal("SPY"))
	})

	It("rejects unknown parameter", func() {
		req := portfolio.CreateRequest{
			Name:       "p",
			Parameters: map[string]any{"riskOn": "SPY", "nope": 1},
		}
		_, err := portfolio.ValidateCreateUnofficial(req, d)
		Expect(errors.Is(err, portfolio.ErrUnknownParameter)).To(BeTrue())
	})

	It("rejects missing parameter", func() {
		req := portfolio.CreateRequest{
			Name:       "p",
			Parameters: map[string]any{},
		}
		_, err := portfolio.ValidateCreateUnofficial(req, d)
		Expect(errors.Is(err, portfolio.ErrMissingParameter)).To(BeTrue())
	})

	It("rejects endDate before startDate", func() {
		start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		end := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
		req := portfolio.CreateRequest{
			Name:       "p",
			Parameters: map[string]any{"riskOn": "SPY"},
			StartDate:  &start,
			EndDate:    &end,
		}
		_, err := portfolio.ValidateCreateUnofficial(req, d)
		Expect(errors.Is(err, portfolio.ErrEndBeforeStart)).To(BeTrue())
	})
})

var _ = Describe("DiffParameters", func() {
	It("classifies an unchanged parameter as kept", func() {
		old := strategy.Describe{Parameters: []strategy.DescribeParameter{{Name: "p", Type: "int"}}}
		newDesc := strategy.Describe{Parameters: []strategy.DescribeParameter{{Name: "p", Type: "int"}}}
		d := portfolio.DiffParameters(old, newDesc)
		Expect(d.Kept).To(ConsistOf("p"))
		Expect(d.Compatible()).To(BeTrue())
	})

	It("classifies a new parameter with default as added_with_default", func() {
		old := strategy.Describe{}
		newDesc := strategy.Describe{Parameters: []strategy.DescribeParameter{
			{Name: "p", Type: "int", Default: 7},
		}}
		d := portfolio.DiffParameters(old, newDesc)
		Expect(d.AddedWithDefault).To(ConsistOf("p"))
		Expect(d.Compatible()).To(BeTrue())
	})

	It("classifies a new parameter without default as added_without_default", func() {
		old := strategy.Describe{}
		newDesc := strategy.Describe{Parameters: []strategy.DescribeParameter{{Name: "p", Type: "int"}}}
		d := portfolio.DiffParameters(old, newDesc)
		Expect(d.AddedWithoutDefault).To(ConsistOf("p"))
		Expect(d.Compatible()).To(BeFalse())
	})

	It("classifies a removed parameter", func() {
		old := strategy.Describe{Parameters: []strategy.DescribeParameter{
			{Name: "p", Type: "int"},
			{Name: "q", Type: "int"},
		}}
		newDesc := strategy.Describe{Parameters: []strategy.DescribeParameter{{Name: "p", Type: "int"}}}
		d := portfolio.DiffParameters(old, newDesc)
		Expect(d.Removed).To(ConsistOf("q"))
		Expect(d.Compatible()).To(BeFalse())
	})

	It("classifies a retyped parameter", func() {
		old := strategy.Describe{Parameters: []strategy.DescribeParameter{{Name: "p", Type: "int"}}}
		newDesc := strategy.Describe{Parameters: []strategy.DescribeParameter{{Name: "p", Type: "string"}}}
		d := portfolio.DiffParameters(old, newDesc)
		Expect(d.Retyped).To(HaveLen(1))
		Expect(d.Retyped[0].Name).To(Equal("p"))
		Expect(d.Retyped[0].From).To(Equal("int"))
		Expect(d.Retyped[0].To).To(Equal("string"))
		Expect(d.Compatible()).To(BeFalse())
	})

	It("handles a mix of all five buckets", func() {
		old := strategy.Describe{Parameters: []strategy.DescribeParameter{
			{Name: "kept", Type: "int"},
			{Name: "removed", Type: "int"},
			{Name: "retyped", Type: "int"},
		}}
		newDesc := strategy.Describe{Parameters: []strategy.DescribeParameter{
			{Name: "kept", Type: "int"},
			{Name: "added_default", Type: "int", Default: 0},
			{Name: "added_no_default", Type: "int"},
			{Name: "retyped", Type: "string"},
		}}
		d := portfolio.DiffParameters(old, newDesc)
		Expect(d.Kept).To(ConsistOf("kept"))
		Expect(d.AddedWithDefault).To(ConsistOf("added_default"))
		Expect(d.AddedWithoutDefault).To(ConsistOf("added_no_default"))
		Expect(d.Removed).To(ConsistOf("removed"))
		Expect(d.Retyped).To(HaveLen(1))
		Expect(d.Compatible()).To(BeFalse())
	})
})

var _ = Describe("MatchPresetName", func() {
	presets := []strategy.DescribePreset{
		{Name: "conservative", Parameters: map[string]any{"p": 1.0}},
		{Name: "aggressive", Parameters: map[string]any{"p": 9.0}},
	}
	desc := strategy.Describe{Presets: presets}

	It("returns the matching preset name when parameters match", func() {
		n := portfolio.MatchPresetName(map[string]any{"p": 1.0}, desc)
		Expect(n).NotTo(BeNil())
		Expect(*n).To(Equal("conservative"))
	})

	It("returns nil when no preset matches", func() {
		n := portfolio.MatchPresetName(map[string]any{"p": 5.0}, desc)
		Expect(n).To(BeNil())
	})

	It("returns nil when describe has no presets", func() {
		n := portfolio.MatchPresetName(map[string]any{"p": 1.0}, strategy.Describe{})
		Expect(n).To(BeNil())
	})
})
