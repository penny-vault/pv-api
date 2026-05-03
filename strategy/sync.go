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

package strategy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Syncer-related sentinel errors.
var (
	ErrRunInterval = errors.New("Syncer.Run requires SyncerOptions.Interval > 0")
	ErrNoTagsFound = errors.New("no tags found")
)

// StatsRunner is the interface the Syncer uses to trigger a post-install stats
// computation. The narrow interface avoids importing stats.go's concrete type.
type StatsRunner interface {
	RunOne(ctx context.Context, shortCode string) error
}

// Store is the subset of strategy.db operations the Syncer needs. The
// production implementation wraps `*pgxpool.Pool`; tests pass an in-memory
// fake.
type Store interface {
	List(ctx context.Context) ([]Strategy, error)
	Get(ctx context.Context, shortCode string) (Strategy, error)
	GetByCloneURL(ctx context.Context, cloneURL string) (Strategy, error)
	Upsert(ctx context.Context, s Strategy) error
	MarkSuccess(ctx context.Context, shortCode, version, kind, ref string, describe []byte) error
	MarkFailure(ctx context.Context, shortCode, version, errText string) error
	LookupArtifact(ctx context.Context, cloneURL, ver string) (string, error)
}

// DiscoveryFunc returns the current set of listings from GitHub.
type DiscoveryFunc func(ctx context.Context) ([]Listing, error)

// ResolveVerFunc returns the remote latest version (git tag or SHA) for a
// clone URL. Production passes a `git ls-remote`-backed implementation;
// tests pass a canned function.
type ResolveVerFunc func(ctx context.Context, cloneURL string) (string, error)

// InstallerFunc executes a single install. Production passes strategy.Install;
// tests pass a stub.
type InstallerFunc func(ctx context.Context, req InstallRequest) (*InstallResult, error)

// SyncerOptions configures NewSyncer.
type SyncerOptions struct {
	Discovery       DiscoveryFunc
	ResolveVer      ResolveVerFunc
	Installer       InstallerFunc // host-mode installer
	DockerInstaller InstallerFunc // docker-mode installer; required when RunnerMode == "docker"
	RunnerMode      string        // "host" (default) | "docker"
	OfficialDir     string
	Concurrency     int
	Interval        time.Duration // 0 = Tick-only; Run reuses this as its period
	Stats           StatsRunner   // optional; if set, RunOne is called after each successful install
}

// expectedArtifactKind returns the artifact_kind string the current runner
// mode produces. Unknown modes treat as "binary" (host default).
func expectedArtifactKind(mode string) string {
	if mode == "docker" {
		return artifactKindImage
	}
	return artifactKindBinary
}

// Syncer orchestrates periodic registry reconciliation.
type Syncer struct {
	store Store
	opts  SyncerOptions
}

// NewSyncer constructs a Syncer. Install concurrency of 0 is replaced with 1.
func NewSyncer(store Store, opts SyncerOptions) *Syncer {
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	return &Syncer{store: store, opts: opts}
}

// Run ticks on the configured Interval until ctx is cancelled. The first
// tick fires immediately (non-blocking — HTTP server starts before Run).
func (s *Syncer) Run(ctx context.Context) error {
	if s.opts.Interval <= 0 {
		return ErrRunInterval
	}
	if err := s.Tick(ctx); err != nil {
		log.Warn().Err(err).Msg("strategy sync tick failed")
	}
	ticker := time.NewTicker(s.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.Tick(ctx); err != nil {
				log.Warn().Err(err).Msg("strategy sync tick failed")
			}
		}
	}
}

// Tick runs one reconciliation cycle.
func (s *Syncer) Tick(ctx context.Context) error {
	listings, err := s.opts.Discovery(ctx)
	if err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	log.Info().Int("count", len(listings)).Msg("strategy sync tick: discovered listings")

	type job struct {
		listing Listing
		version string
		dest    string
	}
	var jobs []job

	for _, l := range listings {
		// Look up by clone URL so we find the row by its repository identity,
		// not by an assumed short code. ErrNotFound is expected for new listings.
		existing, err := s.store.GetByCloneURL(ctx, l.CloneURL)
		if err != nil && !errors.Is(err, ErrNotFound) {
			log.Warn().Err(err).Str("clone_url", l.CloneURL).Msg("get strategy row failed")
			continue
		}

		remote, err := s.opts.ResolveVer(ctx, l.CloneURL)
		if err != nil {
			log.Warn().Err(err).Str("clone_url", l.CloneURL).Msg("resolve remote version failed")
			continue
		}

		if existing.LastAttemptedVer != nil && *existing.LastAttemptedVer == remote {
			// ArtifactKind nil means the row pre-dates kind tracking; treat as
			// matching so we don't re-install rows that were never stamped.
			kindMatches := existing.ArtifactKind == nil || *existing.ArtifactKind == expectedArtifactKind(s.opts.RunnerMode)
			if kindMatches {
				log.Debug().
					Str("repo", l.Owner+"/"+l.Name).
					Str("version", remote).
					Msg("strategy up to date; skipping")
				continue
			}
			log.Info().
				Str("short_code", existing.ShortCode).
				Str("existing_kind", ptrStr(existing.ArtifactKind)).
				Str("expected_kind", expectedArtifactKind(s.opts.RunnerMode)).
				Msg("artifact kind mismatch; scheduling reinstall")
		}

		log.Info().
			Str("repo", l.Owner+"/"+l.Name).
			Str("version", remote).
			Msg("strategy queued for install")
		dest := filepath.Join(s.opts.OfficialDir, l.Owner, l.Name, remote)
		jobs = append(jobs, job{listing: l, version: remote, dest: dest})
	}

	log.Info().Int("queued", len(jobs)).Msg("strategy sync tick: install queue ready")

	sem := make(chan struct{}, s.opts.Concurrency)
	var wg sync.WaitGroup
	for _, j := range jobs {
		j := j
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			s.runInstall(ctx, j.listing, j.version, j.dest)
		}()
	}
	wg.Wait()
	return nil
}

func (s *Syncer) runInstall(ctx context.Context, l Listing, version, dest string) {
	installer := s.opts.Installer
	kind := artifactKindBinary
	if s.opts.RunnerMode == "docker" {
		installer = s.opts.DockerInstaller
		kind = artifactKindImage
	}

	// failureKey is a best-effort key used only for failure rows where we could
	// not determine the real short code from the binary.
	failureKey := l.Name

	recordFailure := func(errText string) {
		failRow := Strategy{
			ShortCode:   failureKey,
			RepoOwner:   l.Owner,
			RepoName:    l.Name,
			CloneURL:    l.CloneURL,
			IsOfficial:  true,
			Description: strPtr(l.Description),
			Categories:  l.Categories,
			Stars:       intPtr(l.Stars),
		}
		_ = s.store.Upsert(ctx, failRow)
		_ = s.store.MarkFailure(ctx, failureKey, version, errText)
	}

	if installer == nil {
		log.Error().
			Str("repo", l.Owner+"/"+l.Name).
			Str("runner_mode", s.opts.RunnerMode).
			Msg("no installer wired for runner mode")
		recordFailure("no installer wired for runner mode " + s.opts.RunnerMode)
		return
	}

	log.Info().
		Str("repo", l.Owner+"/"+l.Name).
		Str("version", version).
		Str("dest", dest).
		Msg("cloning and building strategy")

	result, err := installer(ctx, InstallRequest{
		CloneURL: l.CloneURL, Version: version, DestDir: dest,
	})
	if err != nil {
		log.Warn().
			Err(err).
			Str("repo", l.Owner+"/"+l.Name).
			Str("version", version).
			Msg("strategy install failed")
		recordFailure(err.Error())
		return
	}

	// Use the short code reported by the binary; fall back to repo name only
	// if the installer did not populate it (e.g. incomplete mocks in tests).
	shortCode := result.ShortCode
	if shortCode == "" {
		shortCode = l.Name
	}

	log.Info().
		Str("short_code", shortCode).
		Str("repo", l.Owner+"/"+l.Name).
		Str("version", version).
		Str("artifact_ref", result.ArtifactRef).
		Msg("strategy installed successfully")

	row := Strategy{
		ShortCode:   shortCode,
		RepoOwner:   l.Owner,
		RepoName:    l.Name,
		CloneURL:    l.CloneURL,
		IsOfficial:  true,
		Description: strPtr(l.Description),
		Categories:  l.Categories,
		Stars:       intPtr(l.Stars),
	}
	if err := s.store.Upsert(ctx, row); err != nil {
		log.Warn().Err(err).Str("short_code", shortCode).Msg("upsert after install failed")
		return
	}
	if err := s.store.MarkSuccess(ctx, shortCode, version, kind, result.ArtifactRef, result.DescribeJSON); err != nil {
		log.Warn().Err(err).Str("short_code", shortCode).Msg("mark success failed")
		return
	}
	if s.opts.Stats != nil {
		sc := shortCode
		// Detach from the install request's lifetime so a finished request
		// doesn't cancel the post-install stats refresh. Values (logger,
		// trace ids) on ctx are preserved.
		statsCtx := context.WithoutCancel(ctx)
		go func() {
			if err := s.opts.Stats.RunOne(statsCtx, sc); err != nil {
				log.Warn().Err(err).Str("short_code", sc).Msg("post-install stats failed")
			}
		}()
	}
}

// ResolveVerWithGit uses `git ls-remote` to discover the most-recent
// annotated tag on the given clone URL. Falls back to the default-branch
// HEAD SHA when no tags are present. Intended as the production
// implementation of ResolveVerFunc. cloneURL comes from GitHub Search
// results, not direct user input.
func ResolveVerWithGit(ctx context.Context, cloneURL string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--tags", "--sort=-v:refname", cloneURL)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git ls-remote --tags %s: %w\n%s", cloneURL, err, out.String())
	}
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "<sha>\trefs/tags/<tag>" or "<sha>\trefs/tags/<tag>^{}"
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		ref := fields[1]
		ref = strings.TrimSuffix(ref, "^{}")
		if !strings.HasPrefix(ref, "refs/tags/") {
			continue
		}
		return strings.TrimPrefix(ref, "refs/tags/"), nil
	}
	return "", fmt.Errorf("%w on %s", ErrNoTagsFound, cloneURL)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func intPtr(i int) *int {
	return &i
}

func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
