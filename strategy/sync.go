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

// Store is the subset of strategy.db operations the Syncer needs. The
// production implementation wraps `*pgxpool.Pool`; tests pass an in-memory
// fake.
type Store interface {
	List(ctx context.Context) ([]Strategy, error)
	Get(ctx context.Context, shortCode string) (Strategy, error)
	Upsert(ctx context.Context, s Strategy) error
	MarkAttempt(ctx context.Context, shortCode, version string) error
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
	Discovery   DiscoveryFunc
	ResolveVer  ResolveVerFunc
	Installer   InstallerFunc
	OfficialDir string
	Concurrency int
	Interval    time.Duration // 0 = Tick-only; Run reuses this as its period
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

	type job struct {
		shortCode string
		cloneURL  string
		version   string
		dest      string
	}
	var jobs []job

	for _, l := range listings {
		shortCode := l.Name

		// Read existing row before upsert so install-tracking fields are
		// available for the skip-if-unchanged check below. ErrNotFound is
		// expected for brand-new listings and is not an error.
		existing, err := s.store.Get(ctx, shortCode)
		if err != nil && !errors.Is(err, ErrNotFound) {
			log.Warn().Err(err).Str("short_code", shortCode).Msg("get strategy row failed")
			continue
		}

		row := Strategy{
			ShortCode:   shortCode,
			RepoOwner:   l.Owner,
			RepoName:    l.Name,
			CloneURL:    l.CloneURL,
			IsOfficial:  true,
			Description: strPtr(l.Description),
			Categories:  l.Categories,
			Stars:       intPtr(l.Stars),
			// Preserve install-tracking fields so an in-memory Store (which
			// does a full row replace on Upsert) behaves like the DB (which
			// only updates metadata columns via ON CONFLICT DO UPDATE).
			InstalledVer:     existing.InstalledVer,
			InstalledAt:      existing.InstalledAt,
			LastAttemptedVer: existing.LastAttemptedVer,
			InstallError:     existing.InstallError,
			ArtifactKind:     existing.ArtifactKind,
			ArtifactRef:      existing.ArtifactRef,
			DescribeJSON:     existing.DescribeJSON,
		}
		if err := s.store.Upsert(ctx, row); err != nil {
			log.Warn().Err(err).Str("short_code", shortCode).Msg("upsert failed")
			continue
		}

		remote, err := s.opts.ResolveVer(ctx, l.CloneURL)
		if err != nil {
			log.Warn().Err(err).Str("short_code", shortCode).Msg("resolve remote version failed")
			continue
		}

		if existing.LastAttemptedVer != nil && *existing.LastAttemptedVer == remote {
			continue
		}

		dest := filepath.Join(s.opts.OfficialDir, l.Owner, l.Name, remote)
		jobs = append(jobs, job{shortCode: shortCode, cloneURL: l.CloneURL, version: remote, dest: dest})
	}

	sem := make(chan struct{}, s.opts.Concurrency)
	var wg sync.WaitGroup
	for _, j := range jobs {
		j := j
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			s.runInstall(ctx, j.shortCode, j.cloneURL, j.version, j.dest)
		}()
	}
	wg.Wait()
	return nil
}

func (s *Syncer) runInstall(ctx context.Context, shortCode, cloneURL, version, dest string) {
	if err := s.store.MarkAttempt(ctx, shortCode, version); err != nil {
		log.Warn().Err(err).Str("short_code", shortCode).Msg("mark attempt failed")
		return
	}

	result, err := s.opts.Installer(ctx, InstallRequest{
		ShortCode: shortCode, CloneURL: cloneURL, Version: version, DestDir: dest,
	})
	if err != nil {
		_ = s.store.MarkFailure(ctx, shortCode, version, err.Error())
		return
	}
	_ = s.store.MarkSuccess(ctx, shortCode, version, "binary", result.BinPath, result.DescribeJSON)
}

// ResolveVerWithGit uses `git ls-remote` to discover the most-recent
// annotated tag on the given clone URL. Falls back to the default-branch
// HEAD SHA when no tags are present. Intended as the production
// implementation of ResolveVerFunc. cloneURL comes from GitHub Search
// results, not direct user input.
func ResolveVerWithGit(ctx context.Context, cloneURL string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--tags", "--sort=-v:refname", cloneURL) //nolint:gosec // cloneURL sourced from GitHub Search
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
