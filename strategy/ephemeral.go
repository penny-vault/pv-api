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

// Package strategy — ephemeral build support for unofficial strategies.
package strategy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

const defaultEphemeralTimeout = 60 * time.Second

// EphemeralOptions configures a single EphemeralBuild call.
type EphemeralOptions struct {
	CloneURL          string        // required
	Ver               string        // optional; empty = default branch HEAD
	Dir               string        // parent dir for mkdtemp; empty = os.TempDir()
	Timeout           time.Duration // 0 = 60 s
	SkipURLValidation bool          // tests may relax the allowlist
}

// EphemeralBuild clones CloneURL into a mkdtemp(Dir, "build-*") directory,
// runs `go build .`, and returns (binPath, cleanup, nil). cleanup is
// idempotent. On any error before a successful return EphemeralBuild removes
// the tempdir itself and returns ("", nil, err).
func EphemeralBuild(ctx context.Context, opts EphemeralOptions) (string, func(), error) {
	if !opts.SkipURLValidation {
		if err := ValidateCloneURL(opts.CloneURL); err != nil {
			return "", nil, err
		}
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = defaultEphemeralTimeout
	}

	parent := opts.Dir
	if parent == "" {
		parent = os.TempDir()
	}
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return "", nil, fmt.Errorf("ephemeral: create parent dir: %w", err)
	}

	buildDir, err := os.MkdirTemp(parent, "build-*")
	if err != nil {
		return "", nil, fmt.Errorf("ephemeral: mkdtemp: %w", err)
	}

	// removeOnce guards idempotent cleanup.
	var (
		removeMu   sync.Mutex
		removed    bool
	)
	removeDir := func() {
		removeMu.Lock()
		defer removeMu.Unlock()
		if !removed {
			removed = true
			os.RemoveAll(buildDir) //nolint:errcheck // best-effort cleanup
		}
	}

	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Clone.
	cloneArgs := []string{"clone", "--depth=1"}
	if opts.Ver != "" {
		cloneArgs = append(cloneArgs, "--branch", opts.Ver)
	}
	cloneArgs = append(cloneArgs, opts.CloneURL, buildDir)
	cloneCmd := exec.CommandContext(tctx, "git", cloneArgs...) //nolint:gosec // URL is validated or explicitly skipped; buildDir is internal
	var cloneOut bytes.Buffer
	cloneCmd.Stdout = &cloneOut
	cloneCmd.Stderr = &cloneOut
	if err := cloneCmd.Run(); err != nil {
		removeDir()
		return "", nil, fmt.Errorf("ephemeral: git clone %s: %w\n%s", opts.CloneURL, err, cloneOut.String())
	}

	// Build.
	binPath := filepath.Join(buildDir, "strategy.bin")
	buildCmd := exec.CommandContext(tctx, "go", "build", "-o", binPath, ".") //nolint:gosec // binPath and buildDir are internal paths
	buildCmd.Dir = buildDir
	var buildOut bytes.Buffer
	buildCmd.Stdout = &buildOut
	buildCmd.Stderr = &buildOut
	if err := buildCmd.Run(); err != nil {
		removeDir()
		return "", nil, fmt.Errorf("ephemeral: go build: %w\n%s", err, buildOut.String())
	}

	return binPath, removeDir, nil
}

var (
	ErrInvalidCloneURL = errors.New("cloneUrl must be an https GitHub URL of the form https://github.com/<owner>/<repo>(.git)?")

	cloneURLRe = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(\.git)?$`)
)

// ValidateCloneURL returns nil if cloneURL matches the allowlist, else
// ErrInvalidCloneURL.
func ValidateCloneURL(cloneURL string) error {
	if cloneURL == "" || !cloneURLRe.MatchString(cloneURL) {
		return ErrInvalidCloneURL
	}
	return nil
}
