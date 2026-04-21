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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
	"github.com/google/uuid"

	"github.com/penny-vault/pv-api/dockercli"
)

// DockerEphemeralOptions configures a single EphemeralImageBuild call.
type DockerEphemeralOptions struct {
	CloneURL          string        // required
	Ver               string        // optional; empty = default branch HEAD
	Dir               string        // parent for mkdtemp; empty = os.TempDir()
	Timeout           time.Duration // 0 = defaultEphemeralTimeout
	SkipURLValidation bool          // tests may relax the allowlist
	Client            dockercli.Client
	ImagePrefix       string // default "pvapi-strategy"
}

// EphemeralImageBuild clones CloneURL into mkdtemp(Dir, "build-*"), renders
// the generated Dockerfile, and builds a disposable image tagged
// "<ImagePrefix>/ephemeral/<uuid>:latest". Returns (imageRef, cleanup, nil).
// cleanup is idempotent, calls ImageRemove(imageRef, force=true), and
// removes the tempdir. On any error before a successful return the tempdir
// is removed internally and ("", nil, err) is returned.
func EphemeralImageBuild(ctx context.Context, opts DockerEphemeralOptions) (string, func(), error) {
	if !opts.SkipURLValidation {
		if err := ValidateCloneURL(opts.CloneURL); err != nil {
			return "", nil, err
		}
	}
	if opts.Client == nil {
		return "", nil, fmt.Errorf("EphemeralImageBuild: nil Client")
	}
	if opts.ImagePrefix == "" {
		opts.ImagePrefix = "pvapi-strategy"
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
		return "", nil, fmt.Errorf("ephemeral-image: parent dir: %w", err)
	}
	buildDir, err := os.MkdirTemp(parent, "build-*")
	if err != nil {
		return "", nil, fmt.Errorf("ephemeral-image: mkdtemp: %w", err)
	}

	tag := opts.ImagePrefix + "/ephemeral/" + uuid.NewString() + ":latest"
	var (
		removeMu sync.Mutex
		removed  bool
	)
	cleanup := func() {
		removeMu.Lock()
		defer removeMu.Unlock()
		if removed {
			return
		}
		removed = true
		_, _ = opts.Client.ImageRemove(context.Background(), tag, image.RemoveOptions{Force: true, PruneChildren: true})
		_ = os.RemoveAll(buildDir)
	}

	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Clone.
	cloneArgs := []string{"clone", "--depth=1"}
	if opts.Ver != "" {
		cloneArgs = append(cloneArgs, "--branch", opts.Ver)
	}
	cloneArgs = append(cloneArgs, opts.CloneURL, buildDir)
	cloneCmd := exec.CommandContext(tctx, "git", cloneArgs...) //nolint:gosec // URL validated or explicitly skipped
	var cloneOut bytes.Buffer
	cloneCmd.Stdout = &cloneOut
	cloneCmd.Stderr = &cloneOut
	if err := cloneCmd.Run(); err != nil {
		_ = os.RemoveAll(buildDir)
		return "", nil, fmt.Errorf("ephemeral-image: git clone: %w\n%s", err, cloneOut.String())
	}

	// Render Dockerfile.
	goVer, _ := ParseGoVersion(buildDir)
	if err := os.WriteFile(filepath.Join(buildDir, "Dockerfile"), RenderDockerfile(goVer), 0o600); err != nil {
		_ = os.RemoveAll(buildDir)
		return "", nil, fmt.Errorf("ephemeral-image: write Dockerfile: %w", err)
	}

	// Build.
	buildCtx, err := tarDir(buildDir)
	if err != nil {
		_ = os.RemoveAll(buildDir)
		return "", nil, fmt.Errorf("ephemeral-image: tar: %w", err)
	}
	resp, err := opts.Client.ImageBuild(tctx, buildCtx, types.ImageBuildOptions{
		Dockerfile: "Dockerfile",
		Tags:       []string{tag},
		Remove:     true,
		PullParent: true,
	})
	if err != nil {
		_ = os.RemoveAll(buildDir)
		return "", nil, fmt.Errorf("%w: %w", ErrDockerBuildFailed, err)
	}
	defer resp.Body.Close()
	if _, bErr := drainBuildStream(resp.Body); bErr != nil {
		_ = os.RemoveAll(buildDir)
		return "", nil, fmt.Errorf("%w: %w", ErrDockerBuildFailed, bErr)
	}

	return tag, cleanup, nil
}
