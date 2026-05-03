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
	"sync"
	"time"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/image"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

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
	if err := normalizeEphemeralOptions(&opts); err != nil {
		return "", nil, err
	}
	buildDir, err := prepareEphemeralBuildDir(opts.Dir)
	if err != nil {
		return "", nil, err
	}

	tag := opts.ImagePrefix + "/ephemeral/" + uuid.NewString() + ":latest"
	cleanup := makeEphemeralCleanup(opts.Client, tag, buildDir)

	tctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	if err := ephemeralClone(tctx, opts.CloneURL, opts.Ver, buildDir); err != nil {
		_ = os.RemoveAll(buildDir)
		return "", nil, err
	}
	if err := writeDockerfileIntoDir(buildDir); err != nil {
		_ = os.RemoveAll(buildDir)
		return "", nil, fmt.Errorf("ephemeral-image: %w", err)
	}
	if err := ephemeralBuildImage(tctx, opts.Client, buildDir, tag); err != nil {
		_ = os.RemoveAll(buildDir)
		return "", nil, err
	}
	return tag, cleanup, nil
}

// normalizeEphemeralOptions validates and applies defaults to opts so the
// downstream stages can rely on every required field being populated.
func normalizeEphemeralOptions(opts *DockerEphemeralOptions) error {
	if !opts.SkipURLValidation {
		if err := ValidateCloneURL(opts.CloneURL); err != nil {
			return err
		}
	}
	if opts.Client == nil {
		return ErrDockerClientNil
	}
	if opts.ImagePrefix == "" {
		opts.ImagePrefix = "pvapi-strategy"
	}
	if opts.Timeout == 0 {
		opts.Timeout = defaultEphemeralTimeout
	}
	if opts.Dir == "" {
		opts.Dir = os.TempDir()
	}
	return nil
}

// prepareEphemeralBuildDir ensures parent exists, then creates a fresh
// build-* subdirectory and returns its absolute path.
func prepareEphemeralBuildDir(parent string) (string, error) {
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return "", fmt.Errorf("ephemeral-image: parent dir: %w", err)
	}
	buildDir, err := os.MkdirTemp(parent, "build-*")
	if err != nil {
		return "", fmt.Errorf("ephemeral-image: mkdtemp: %w", err)
	}
	return buildDir, nil
}

// makeEphemeralCleanup returns an idempotent cleanup function that removes
// the disposable Docker image and its on-disk build context.
func makeEphemeralCleanup(client dockercli.Client, tag, buildDir string) func() {
	var (
		mu      sync.Mutex
		removed bool
	)
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if removed {
			return
		}
		removed = true
		_, _ = client.ImageRemove(context.Background(), tag, image.RemoveOptions{Force: true, PruneChildren: true})
		_ = os.RemoveAll(buildDir)
	}
}

// ephemeralClone runs `git clone --depth=1 [--branch <ver>] <url> <dir>`
// against the ephemeral build directory.
func ephemeralClone(ctx context.Context, cloneURL, ver, buildDir string) error {
	args := []string{"clone", "--depth=1"}
	if ver != "" {
		args = append(args, "--branch", ver)
	}
	args = append(args, cloneURL, buildDir)
	cmd := exec.CommandContext(ctx, "git", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ephemeral-image: git clone: %w\n%s", err, out.String())
	}
	return nil
}

// ephemeralBuildImage tars buildDir and submits an ImageBuild request,
// draining the build output and surfacing any error frames.
func ephemeralBuildImage(ctx context.Context, c dockercli.Client, buildDir, tag string) error {
	buildCtx, err := tarDir(buildDir)
	if err != nil {
		return fmt.Errorf("ephemeral-image: tar: %w", err)
	}
	resp, err := c.ImageBuild(ctx, buildCtx, build.ImageBuildOptions{
		Dockerfile: "Dockerfile",
		Tags:       []string{tag},
		Remove:     true,
		PullParent: true,
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDockerBuildFailed, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.Warn().Err(cerr).Str("image_tag", tag).Msg("docker ephemeral: build response close failed")
		}
	}()
	if _, bErr := drainBuildStream(resp.Body); bErr != nil {
		return fmt.Errorf("%w: %w", ErrDockerBuildFailed, bErr)
	}
	return nil
}
