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

	It("accepts a valid one_shot request", func() {
		req := portfolio.CreateRequest{
			Name:         "foo",
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY"},
			Mode:         portfolio.ModeOneShot,
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
			Mode:         portfolio.ModeOneShot,
		}
		norm, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(err).NotTo(HaveOccurred())
		Expect(norm.Benchmark).To(Equal("SPY"))
	})

	It("rejects mode=live with ErrLiveNotSupported", func() {
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			Parameters: map[string]any{"riskOn": "SPY"},
			Mode:       portfolio.ModeLive,
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrLiveNotSupported)).To(BeTrue())
	})

	It("rejects mode=continuous without a schedule", func() {
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			Parameters: map[string]any{"riskOn": "SPY"},
			Mode:       portfolio.ModeContinuous,
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrScheduleRequired)).To(BeTrue())
	})

	It("rejects mode=one_shot with a schedule", func() {
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			Parameters: map[string]any{"riskOn": "SPY"},
			Mode:       portfolio.ModeOneShot,
			Schedule:   "@monthend",
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrScheduleForbidden)).To(BeTrue())
	})

	It("rejects a strategy version that does not match installed_ver", func() {
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			StrategyVer: "v9.9.9",
			Parameters:  map[string]any{"riskOn": "SPY"},
			Mode:        portfolio.ModeOneShot,
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrStrategyVersionMismatch)).To(BeTrue())
	})

	It("rejects when the strategy is not installed", func() {
		s := makeStrategy()
		s.InstalledVer = nil
		s.DescribeJSON = nil
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			Parameters: map[string]any{"riskOn": "SPY"},
			Mode:       portfolio.ModeOneShot,
		}
		_, err := portfolio.ValidateCreate(req, s)
		Expect(errors.Is(err, portfolio.ErrStrategyNotReady)).To(BeTrue())
	})

	It("rejects unknown parameter keys", func() {
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			Parameters: map[string]any{"riskOn": "SPY", "bogus": 42},
			Mode:       portfolio.ModeOneShot,
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrUnknownParameter)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("bogus"))
	})

	It("rejects missing required parameters", func() {
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			Parameters: map[string]any{},
			Mode:       portfolio.ModeOneShot,
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrMissingParameter)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("riskOn"))
	})
})

var _ = Describe("Schedule validation", func() {
	strategyFixture := func() strategy.Strategy {
		ver := "v1.0.0"
		return strategy.Strategy{
			ShortCode:    "adm",
			InstalledVer: &ver,
			DescribeJSON: []byte(`{
				"shortCode": "adm",
				"benchmark": "SPY",
				"parameters": [],
				"presets": []
			}`),
		}
	}

	It("accepts a valid tradecron schedule on continuous", func() {
		req := portfolio.CreateRequest{
			Name: "x", StrategyCode: "adm", Parameters: map[string]any{},
			Mode: portfolio.ModeContinuous, Schedule: "@daily",
		}
		_, err := portfolio.ValidateCreate(req, strategyFixture())
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects an invalid schedule on continuous with ErrInvalidSchedule", func() {
		req := portfolio.CreateRequest{
			Name: "x", StrategyCode: "adm", Parameters: map[string]any{},
			Mode: portfolio.ModeContinuous, Schedule: "garbage",
		}
		_, err := portfolio.ValidateCreate(req, strategyFixture())
		Expect(errors.Is(err, portfolio.ErrInvalidSchedule)).To(BeTrue())
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

	It("accepts a well-formed one-shot request", func() {
		req := portfolio.CreateRequest{
			Name:       "p",
			Mode:       portfolio.ModeOneShot,
			Parameters: map[string]any{"riskOn": "SPY"},
		}
		norm, err := portfolio.ValidateCreateUnofficial(req, d)
		Expect(err).NotTo(HaveOccurred())
		Expect(norm.Benchmark).To(Equal("SPY"))
	})

	It("rejects unknown parameter", func() {
		req := portfolio.CreateRequest{
			Name:       "p",
			Mode:       portfolio.ModeOneShot,
			Parameters: map[string]any{"riskOn": "SPY", "nope": 1},
		}
		_, err := portfolio.ValidateCreateUnofficial(req, d)
		Expect(errors.Is(err, portfolio.ErrUnknownParameter)).To(BeTrue())
	})

	It("rejects missing parameter", func() {
		req := portfolio.CreateRequest{
			Name:       "p",
			Mode:       portfolio.ModeOneShot,
			Parameters: map[string]any{},
		}
		_, err := portfolio.ValidateCreateUnofficial(req, d)
		Expect(errors.Is(err, portfolio.ErrMissingParameter)).To(BeTrue())
	})

	It("rejects live mode", func() {
		req := portfolio.CreateRequest{
			Name:       "p",
			Mode:       portfolio.ModeLive,
			Parameters: map[string]any{"riskOn": "SPY"},
		}
		_, err := portfolio.ValidateCreateUnofficial(req, d)
		Expect(errors.Is(err, portfolio.ErrLiveNotSupported)).To(BeTrue())
	})

	It("requires schedule for continuous", func() {
		req := portfolio.CreateRequest{
			Name:       "p",
			Mode:       portfolio.ModeContinuous,
			Parameters: map[string]any{"riskOn": "SPY"},
		}
		_, err := portfolio.ValidateCreateUnofficial(req, d)
		Expect(errors.Is(err, portfolio.ErrScheduleRequired)).To(BeTrue())
	})
})
