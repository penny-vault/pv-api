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

package strategy_test

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/strategy"
)

// fakeStore implements strategy.Store for unit-testing the sync loop
// without a live Postgres. Mirrors pool-backed behavior in memory.
type fakeStore struct {
	rows         map[string]strategy.Strategy
	upserts      []string
	attempts     []attemptCall
	successes    []successCall
	failures     []failureCall
	statsUpdates []statsUpdateCall
	statsErrors  []statsErrorCall
}

type attemptCall struct{ shortCode, version string }
type successCall struct {
	shortCode, version, kind, ref string
	describeLen                   int
}
type failureCall struct{ shortCode, version, err string }
type statsUpdateCall struct {
	shortCode string
	result    strategy.StatsResult
}
type statsErrorCall struct{ shortCode, err string }

func newFakeStore() *fakeStore {
	return &fakeStore{rows: make(map[string]strategy.Strategy)}
}

func (f *fakeStore) List(_ context.Context) ([]strategy.Strategy, error) {
	out := make([]strategy.Strategy, 0, len(f.rows))
	for _, v := range f.rows {
		out = append(out, v)
	}
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, sc string) (strategy.Strategy, error) {
	v, ok := f.rows[sc]
	if !ok {
		return strategy.Strategy{}, strategy.ErrNotFound
	}
	return v, nil
}

func (f *fakeStore) GetByCloneURL(_ context.Context, cloneURL string) (strategy.Strategy, error) {
	for _, v := range f.rows {
		if v.CloneURL == cloneURL {
			return v, nil
		}
	}
	return strategy.Strategy{}, strategy.ErrNotFound
}

func (f *fakeStore) Upsert(_ context.Context, s strategy.Strategy) error {
	f.upserts = append(f.upserts, s.ShortCode)
	// Preserve install-tracking fields to mirror ON CONFLICT DO UPDATE behavior
	// in the real DB, which only updates metadata columns.
	if existing, ok := f.rows[s.ShortCode]; ok {
		s.InstalledVer = existing.InstalledVer
		s.InstalledAt = existing.InstalledAt
		s.LastAttemptedVer = existing.LastAttemptedVer
		s.InstallError = existing.InstallError
		s.ArtifactKind = existing.ArtifactKind
		s.ArtifactRef = existing.ArtifactRef
		s.DescribeJSON = existing.DescribeJSON
		s.DiscoveredAt = existing.DiscoveredAt
	}
	f.rows[s.ShortCode] = s
	return nil
}

func (f *fakeStore) MarkAttempt(_ context.Context, sc, ver string) error {
	f.attempts = append(f.attempts, attemptCall{sc, ver})
	r := f.rows[sc]
	r.LastAttemptedVer = &ver
	r.InstallError = nil
	f.rows[sc] = r
	return nil
}

func (f *fakeStore) MarkSuccess(_ context.Context, sc, ver, kind, ref string, describe []byte) error {
	f.successes = append(f.successes, successCall{sc, ver, kind, ref, len(describe)})
	r := f.rows[sc]
	r.InstalledVer = &ver
	r.LastAttemptedVer = &ver
	now := time.Now()
	r.InstalledAt = &now
	k := kind
	r.ArtifactKind = &k
	rf := ref
	r.ArtifactRef = &rf
	r.DescribeJSON = append([]byte(nil), describe...)
	r.InstallError = nil
	f.rows[sc] = r
	return nil
}

func (f *fakeStore) MarkFailure(_ context.Context, sc, ver, errText string) error {
	f.failures = append(f.failures, failureCall{sc, ver, errText})
	r := f.rows[sc]
	r.LastAttemptedVer = &ver
	r.InstallError = &errText
	f.rows[sc] = r
	return nil
}

func (f *fakeStore) LookupArtifact(_ context.Context, cloneURL, ver string) (string, error) {
	for _, row := range f.rows {
		if row.CloneURL == cloneURL &&
			row.InstalledVer != nil && *row.InstalledVer == ver &&
			row.InstallError == nil &&
			row.ArtifactRef != nil {
			return *row.ArtifactRef, nil
		}
	}
	return "", strategy.ErrNotFound
}

func (f *fakeStore) ListInstalled(_ context.Context) ([]strategy.Strategy, error) {
	var out []strategy.Strategy
	for _, v := range f.rows {
		if v.ArtifactRef != nil {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeStore) UpdateStats(_ context.Context, shortCode string, r strategy.StatsResult) error {
	f.statsUpdates = append(f.statsUpdates, statsUpdateCall{shortCode, r})
	row := f.rows[shortCode]
	row.CAGR = &r.CAGR
	row.MaxDrawdown = &r.MaxDrawdown
	row.Sharpe = &r.Sharpe
	now := r.AsOf
	row.StatsAsOf = &now
	f.rows[shortCode] = row
	return nil
}

func (f *fakeStore) MarkStatsError(_ context.Context, shortCode, errText string) error {
	f.statsErrors = append(f.statsErrors, statsErrorCall{shortCode, errText})
	row := f.rows[shortCode]
	row.StatsError = &errText
	f.rows[shortCode] = row
	return nil
}

var _ = Describe("Syncer.Tick", func() {
	It("inserts a new listing, attempts install, records success", func() {
		store := newFakeStore()

		discovery := func(_ context.Context) ([]strategy.Listing, error) {
			return []strategy.Listing{{
				Name: "fake", Owner: "penny-vault", CloneURL: "file:///tmp/fake.git",
				Stars: 1, Categories: []string{"momentum"},
			}}, nil
		}
		resolveVer := func(_ context.Context, cloneURL string) (string, error) {
			return "v1.0.0", nil
		}
		installer := func(_ context.Context, req strategy.InstallRequest) (*strategy.InstallResult, error) {
			return &strategy.InstallResult{
				BinPath:      "/var/lib/pvapi/strategies/official/fake/v1.0.0/fake.bin",
				ArtifactRef:  "/var/lib/pvapi/strategies/official/fake/v1.0.0/fake.bin",
				DescribeJSON: []byte(`{"shortcode":"fake","name":"Fake","parameters":[],"schedule":"@monthend","benchmark":"SPY"}`),
				ShortCode:    "fake",
			}, nil
		}

		s := strategy.NewSyncer(store, strategy.SyncerOptions{
			Discovery:   discovery,
			ResolveVer:  resolveVer,
			Installer:   installer,
			OfficialDir: "/var/lib/pvapi/strategies/official",
			Concurrency: 1,
		})
		Expect(s.Tick(context.Background())).To(Succeed())

		Expect(store.upserts).To(ConsistOf("fake"))
		Expect(store.successes).To(HaveLen(1))
		Expect(store.successes[0].ref).To(ContainSubstring("fake"))
	})

	It("records failure on install error and leaves installed_ver alone", func() {
		store := newFakeStore()
		// Pre-seed: previously successful install of v0.9.0.
		prev := "v0.9.0"
		store.rows["fake"] = strategy.Strategy{
			ShortCode:        "fake",
			CloneURL:         "file:///tmp/fake.git",
			IsOfficial:       true,
			InstalledVer:     &prev,
			LastAttemptedVer: &prev,
		}

		discovery := func(_ context.Context) ([]strategy.Listing, error) {
			return []strategy.Listing{{Name: "fake", Owner: "penny-vault", CloneURL: "file:///tmp/fake.git"}}, nil
		}
		resolveVer := func(_ context.Context, _ string) (string, error) { return "v1.0.0", nil }
		installer := func(_ context.Context, _ strategy.InstallRequest) (*strategy.InstallResult, error) {
			return nil, errors.New("build failed")
		}

		s := strategy.NewSyncer(store, strategy.SyncerOptions{
			Discovery: discovery, ResolveVer: resolveVer, Installer: installer,
			OfficialDir: "/tmp", Concurrency: 1,
		})
		Expect(s.Tick(context.Background())).To(Succeed())

		Expect(store.failures).To(HaveLen(1))
		Expect(store.failures[0].version).To(Equal("v1.0.0"))
		Expect(*store.rows["fake"].InstalledVer).To(Equal("v0.9.0"), "installed_ver is preserved on failure")
		Expect(*store.rows["fake"].InstallError).To(ContainSubstring("build failed"))
	})

	It("skips an install when remote version has not changed", func() {
		store := newFakeStore()
		installed := "v1.0.0"
		store.rows["fake"] = strategy.Strategy{
			ShortCode:        "fake",
			CloneURL:         "file:///tmp/fake.git",
			IsOfficial:       true,
			InstalledVer:     &installed,
			LastAttemptedVer: &installed,
		}

		installerCalls := 0
		discovery := func(_ context.Context) ([]strategy.Listing, error) {
			return []strategy.Listing{{Name: "fake", Owner: "penny-vault", CloneURL: "file:///tmp/fake.git"}}, nil
		}
		resolveVer := func(_ context.Context, _ string) (string, error) { return "v1.0.0", nil }
		installer := func(_ context.Context, _ strategy.InstallRequest) (*strategy.InstallResult, error) {
			installerCalls++
			return nil, errors.New("should not be called")
		}

		s := strategy.NewSyncer(store, strategy.SyncerOptions{
			Discovery: discovery, ResolveVer: resolveVer, Installer: installer,
			OfficialDir: "/tmp", Concurrency: 1,
		})
		Expect(s.Tick(context.Background())).To(Succeed())
		Expect(installerCalls).To(Equal(0))
	})

	It("skips a failed install when upstream version has not changed", func() {
		store := newFakeStore()
		attempted := "v1.0.0"
		failed := "build failed"
		store.rows["fake"] = strategy.Strategy{
			ShortCode:        "fake",
			CloneURL:         "file:///tmp/fake.git",
			IsOfficial:       true,
			LastAttemptedVer: &attempted,
			InstallError:     &failed,
		}

		installerCalls := 0
		discovery := func(_ context.Context) ([]strategy.Listing, error) {
			return []strategy.Listing{{Name: "fake", Owner: "penny-vault", CloneURL: "file:///tmp/fake.git"}}, nil
		}
		resolveVer := func(_ context.Context, _ string) (string, error) { return "v1.0.0", nil }
		installer := func(_ context.Context, _ strategy.InstallRequest) (*strategy.InstallResult, error) {
			installerCalls++
			return nil, errors.New("should not be called")
		}

		s := strategy.NewSyncer(store, strategy.SyncerOptions{
			Discovery: discovery, ResolveVer: resolveVer, Installer: installer,
			OfficialDir: "/tmp", Concurrency: 1,
		})
		Expect(s.Tick(context.Background())).To(Succeed())
		Expect(installerCalls).To(Equal(0))
	})

	It("skips install when non-nil artifact_kind matches current runner mode", func() {
		store := newFakeStore()
		kind := "binary"
		installed := "v1.0.0"
		attempted := "v1.0.0"
		store.rows["fake"] = strategy.Strategy{
			ShortCode:        "fake",
			CloneURL:         "file:///tmp/fake.git",
			IsOfficial:       true,
			InstalledVer:     &installed,
			LastAttemptedVer: &attempted,
			ArtifactKind:     &kind,
		}

		installerCalls := 0
		discovery := func(_ context.Context) ([]strategy.Listing, error) {
			return []strategy.Listing{{Name: "fake", Owner: "penny-vault", CloneURL: "file:///tmp/fake.git"}}, nil
		}
		resolveVer := func(_ context.Context, _ string) (string, error) { return "v1.0.0", nil }
		installer := func(_ context.Context, _ strategy.InstallRequest) (*strategy.InstallResult, error) {
			installerCalls++
			return nil, errors.New("should not be called")
		}

		s := strategy.NewSyncer(store, strategy.SyncerOptions{
			Discovery:   discovery,
			ResolveVer:  resolveVer,
			Installer:   installer,
			RunnerMode:  "host",
			OfficialDir: "/tmp",
			Concurrency: 1,
		})
		Expect(s.Tick(context.Background())).To(Succeed())
		Expect(installerCalls).To(Equal(0))
	})

	It("reinstalls when runner mode changes and artifact_kind no longer matches", func() {
		store := newFakeStore()
		kind := "binary"
		attempted := "v1.0.0"
		installed := "v1.0.0"
		store.rows["adm"] = strategy.Strategy{
			ShortCode:        "adm",
			RepoOwner:        "penny-vault",
			RepoName:         "adm",
			CloneURL:         "https://github.com/penny-vault/adm",
			IsOfficial:       true,
			LastAttemptedVer: &attempted,
			InstalledVer:     &installed,
			ArtifactKind:     &kind,
		}

		var dockerCalls int
		discovery := func(_ context.Context) ([]strategy.Listing, error) {
			return []strategy.Listing{{
				Name: "adm", Owner: "penny-vault",
				CloneURL: "https://github.com/penny-vault/adm",
			}}, nil
		}
		resolveVer := func(_ context.Context, _ string) (string, error) { return "v1.0.0", nil }
		dockerInstaller := func(_ context.Context, _ strategy.InstallRequest) (*strategy.InstallResult, error) {
			dockerCalls++
			return &strategy.InstallResult{
				ArtifactRef:  "pvapi-strategy/penny-vault/adm:v1.0.0",
				DescribeJSON: []byte(`{}`),
			}, nil
		}

		s := strategy.NewSyncer(store, strategy.SyncerOptions{
			Discovery:       discovery,
			ResolveVer:      resolveVer,
			DockerInstaller: dockerInstaller,
			RunnerMode:      "docker",
			OfficialDir:     "/tmp",
			Concurrency:     1,
			Interval:        time.Second,
		})
		Expect(s.Tick(context.Background())).To(Succeed())
		Expect(dockerCalls).To(Equal(1))
		Expect(store.successes).To(HaveLen(1))
		Expect(store.successes[0].kind).To(Equal("image"))
		Expect(store.successes[0].ref).To(Equal("pvapi-strategy/penny-vault/adm:v1.0.0"))
	})
})
