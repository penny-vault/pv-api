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
	"context"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("AutoUpgrader", func() {
	const shortCode = "adm"
	describeJSON := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"riskOn","type":"universe"}],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`)

	// newSetup wires a Handler + AutoUpgrader around a pre-seeded fakeStore
	// and fakeStrategyStore.
	newSetup := func(installedVer string, portfolioRows []portfolio.Portfolio) (*fakeStore, *countingDispatcher, *portfolio.AutoUpgrader) {
		st := &fakeStore{rows: portfolioRows}
		ss := &fakeStrategyStore{row: strategy.Strategy{
			ShortCode:    shortCode,
			IsOfficial:   true,
			InstalledVer: &installedVer,
			DescribeJSON: describeJSON,
		}}
		disp := &countingDispatcher{runID: uuid.Must(uuid.NewV7())}
		h := portfolio.NewHandler(st, ss, nil, disp, nil, nil, strategy.EphemeralOptions{})
		au := portfolio.NewAutoUpgrader(h, ss)
		return st, disp, au
	}

	seedPortfolio := func(ver string) portfolio.Portfolio {
		v := ver
		return portfolio.Portfolio{
			ID:                   uuid.Must(uuid.NewV7()),
			OwnerSub:             "auth0|owner",
			Slug:                 "p-" + uuid.NewString()[:8],
			Name:                 "p",
			StrategyCode:         shortCode,
			StrategyVer:          &v,
			StrategyDescribeJSON: describeJSON,
			Parameters:           map[string]any{"riskOn": "SPY"},
			Benchmark:            "SPY",
			Status:               portfolio.StatusReady,
			RunRetention:         2,
		}
	}

	ctx := context.Background()

	It("upgrades a portfolio when the new version is a patch bump", func() {
		st, disp, au := newSetup("v0.2.2", []portfolio.Portfolio{seedPortfolio("v0.2.1")})
		au.AutoUpgradeAfterInstall(ctx, shortCode, "v0.2.2")
		Expect(st.ApplyUpgradeCalls).To(HaveLen(1))
		Expect(st.ApplyUpgradeCalls[0].NewVer).To(Equal("v0.2.2"))
		Expect(disp.SubmitCalls).To(HaveLen(1))
	})

	It("skips a minor bump", func() {
		st, disp, au := newSetup("v0.3.0", []portfolio.Portfolio{seedPortfolio("v0.2.1")})
		au.AutoUpgradeAfterInstall(ctx, shortCode, "v0.3.0")
		Expect(st.ApplyUpgradeCalls).To(BeEmpty())
		Expect(disp.SubmitCalls).To(BeEmpty())
	})

	It("skips a major bump", func() {
		st, disp, au := newSetup("v1.0.0", []portfolio.Portfolio{seedPortfolio("v0.2.1")})
		au.AutoUpgradeAfterInstall(ctx, shortCode, "v1.0.0")
		Expect(st.ApplyUpgradeCalls).To(BeEmpty())
		Expect(disp.SubmitCalls).To(BeEmpty())
	})

	It("skips a portfolio whose status is running", func() {
		p := seedPortfolio("v0.2.1")
		p.Status = portfolio.StatusRunning
		st, disp, au := newSetup("v0.2.2", []portfolio.Portfolio{p})
		au.AutoUpgradeAfterInstall(ctx, shortCode, "v0.2.2")
		Expect(st.ApplyUpgradeCalls).To(BeEmpty())
		Expect(disp.SubmitCalls).To(BeEmpty())
	})

	It("skips a portfolio with an unparseable version", func() {
		p := seedPortfolio("not-a-version")
		st, disp, au := newSetup("v0.2.2", []portfolio.Portfolio{p})
		au.AutoUpgradeAfterInstall(ctx, shortCode, "v0.2.2")
		Expect(st.ApplyUpgradeCalls).To(BeEmpty())
		Expect(disp.SubmitCalls).To(BeEmpty())
	})

	It("skips when the new version itself is unparseable", func() {
		st, disp, au := newSetup("garbage", []portfolio.Portfolio{seedPortfolio("v0.2.1")})
		au.AutoUpgradeAfterInstall(ctx, shortCode, "garbage")
		Expect(st.ApplyUpgradeCalls).To(BeEmpty())
		Expect(disp.SubmitCalls).To(BeEmpty())
	})

	It("skips when the param diff is incompatible (manual upgrade required)", func() {
		// Portfolio uses an old describe with riskOn; registry now requires
		// riskOff with no default. Even though it's a patch bump, this needs
		// manual reconciliation.
		oldDescribe := []byte(`{"shortCode":"adm","name":"ADM","parameters":[{"name":"riskOn","type":"universe"}],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`)
		newDescribe := []byte(`{"shortCode":"adm","name":"ADM","parameters":[{"name":"riskOff","type":"universe"}],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`)
		p := seedPortfolio("v0.2.1")
		p.StrategyDescribeJSON = oldDescribe

		st := &fakeStore{rows: []portfolio.Portfolio{p}}
		ver := "v0.2.2"
		ss := &fakeStrategyStore{row: strategy.Strategy{
			ShortCode:    shortCode,
			InstalledVer: &ver,
			DescribeJSON: newDescribe,
		}}
		disp := &countingDispatcher{runID: uuid.Must(uuid.NewV7())}
		h := portfolio.NewHandler(st, ss, nil, disp, nil, nil, strategy.EphemeralOptions{})
		au := portfolio.NewAutoUpgrader(h, ss)

		au.AutoUpgradeAfterInstall(ctx, shortCode, "v0.2.2")

		Expect(st.ApplyUpgradeCalls).To(BeEmpty())
		Expect(disp.SubmitCalls).To(BeEmpty())
	})

	It("is a no-op when there are no portfolios for the strategy", func() {
		st, disp, au := newSetup("v0.2.2", nil)
		au.AutoUpgradeAfterInstall(ctx, shortCode, "v0.2.2")
		Expect(st.ApplyUpgradeCalls).To(BeEmpty())
		Expect(disp.SubmitCalls).To(BeEmpty())
	})

	It("upgrades only the eligible portfolios from a mixed set", func() {
		eligible := seedPortfolio("v0.2.1")      // patch bump → upgrade
		alreadyLatest := seedPortfolio("v0.2.2") // same as new → already at latest
		minorBehind := seedPortfolio("v0.1.9")   // minor bump → skip
		running := seedPortfolio("v0.2.1")
		running.Status = portfolio.StatusRunning // patch bump but running → skip

		st, disp, au := newSetup("v0.2.2",
			[]portfolio.Portfolio{eligible, alreadyLatest, minorBehind, running})

		au.AutoUpgradeAfterInstall(ctx, shortCode, "v0.2.2")

		Expect(st.ApplyUpgradeCalls).To(HaveLen(1))
		Expect(st.ApplyUpgradeCalls[0].PortfolioID).To(Equal(eligible.ID))
		Expect(disp.SubmitCalls).To(HaveLen(1))
		Expect(disp.SubmitCalls[0]).To(Equal(eligible.ID))
	})

	It("upgrades for portfolios across owners", func() {
		p1 := seedPortfolio("v0.2.1")
		p1.OwnerSub = "auth0|alice"
		p2 := seedPortfolio("v0.2.1")
		p2.OwnerSub = "auth0|bob"

		st, disp, au := newSetup("v0.2.2", []portfolio.Portfolio{p1, p2})
		au.AutoUpgradeAfterInstall(ctx, shortCode, "v0.2.2")
		Expect(st.ApplyUpgradeCalls).To(HaveLen(2))
		Expect(disp.SubmitCalls).To(HaveLen(2))
	})

	It("commits the upgrade even when the dispatcher refuses with ErrQueueFull", func() {
		st := &fakeStore{rows: []portfolio.Portfolio{seedPortfolio("v0.2.1")}}
		ver := "v0.2.2"
		ss := &fakeStrategyStore{row: strategy.Strategy{
			ShortCode:    shortCode,
			InstalledVer: &ver,
			DescribeJSON: describeJSON,
		}}
		disp := &countingDispatcher{err: portfolio.ErrQueueFull}
		h := portfolio.NewHandler(st, ss, nil, disp, nil, nil, strategy.EphemeralOptions{})
		au := portfolio.NewAutoUpgrader(h, ss)

		au.AutoUpgradeAfterInstall(ctx, shortCode, "v0.2.2")

		Expect(st.ApplyUpgradeCalls).To(HaveLen(1), "upgrade is committed even when dispatch fails")
	})
})
