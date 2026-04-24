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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/rs/zerolog/log"
)

// Install-related sentinel errors.
var (
	ErrInstallMissingFields = errors.New("InstallRequest: all fields required")
	ErrShortCodeMismatch    = errors.New("describe short_code mismatch")
)

// InstallRequest describes a single version-pinned install.
type InstallRequest struct {
	ShortCode string // unused by Install; reserved for callers that need a hint key
	CloneURL  string // git URL (https, ssh, or file://)
	Version   string // git tag or commit SHA to check out
	DestDir   string // absolute path to clone/build into
}

// InstallResult is what a successful install produces.
type InstallResult struct {
	BinPath      string // absolute path to the built binary (host mode only; "" in docker mode)
	ArtifactRef  string // image ref in docker mode; same as BinPath in host mode
	DescribeJSON []byte // raw `<bin> describe --json` output
	ShortCode    string // parsed from the describe output
}

// Install performs a single version-pinned install:
//  1. git clone --branch <Version> --depth 1 <CloneURL> <DestDir>
//  2. go build -o <DestDir>/strategy.bin .
//  3. <binary> describe --json  (short code is authoritative from the binary)
//  4. rename binary to <DestDir>/<shortCode>.bin
//
// On failure Install returns a wrapped error and leaves DestDir in whatever
// state it was in; callers are expected to treat DestDir as throwaway on
// failure.
func Install(ctx context.Context, req InstallRequest) (*InstallResult, error) {
	if req.CloneURL == "" || req.Version == "" || req.DestDir == "" {
		return nil, ErrInstallMissingFields
	}

	log.Info().Str("clone_url", req.CloneURL).Str("version", req.Version).Msg("cloning strategy repository")

	// Clone at the specific tag/SHA. Inputs come from GitHub Search results
	// (CloneURL) + `git ls-remote` (Version) + internal config (DestDir) —
	// not direct user input.
	cloneCmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", //nolint:gosec // args sourced from trusted sync state
		"--branch", req.Version, req.CloneURL, req.DestDir)
	var cloneOut bytes.Buffer
	cloneCmd.Stdout = &cloneOut
	cloneCmd.Stderr = &cloneOut
	if err := cloneCmd.Run(); err != nil {
		return nil, fmt.Errorf("git clone %s@%s: %w\n%s", req.CloneURL, req.Version, err, cloneOut.String())
	}
	log.Info().Str("clone_url", req.CloneURL).Str("dest", req.DestDir).Msg("clone complete; building binary")

	// Build to a temp name; we rename once describe tells us the real short code.
	tmpBinPath := filepath.Join(req.DestDir, "strategy.bin")
	buildCmd := exec.CommandContext(ctx, "go", "build", "-o", tmpBinPath, ".") //nolint:gosec // tmpBinPath/DestDir are internal paths
	buildCmd.Dir = req.DestDir
	var buildOut bytes.Buffer
	buildCmd.Stdout = &buildOut
	buildCmd.Stderr = &buildOut
	if err := buildCmd.Run(); err != nil {
		return nil, fmt.Errorf("go build: %w\n%s", err, buildOut.String())
	}
	log.Info().Str("clone_url", req.CloneURL).Msg("build complete; running describe")

	// Describe. tmpBinPath is an internal path we just wrote above.
	describeCmd := exec.CommandContext(ctx, tmpBinPath, "describe", "--json") //nolint:gosec // tmpBinPath is internal
	var describeOut bytes.Buffer
	describeCmd.Stdout = &describeOut
	describeCmd.Stderr = os.Stderr
	if err := describeCmd.Run(); err != nil {
		return nil, fmt.Errorf("%s describe --json: %w", tmpBinPath, err)
	}

	describeBytes := describeOut.Bytes()

	var parsed Describe
	if err := json.Unmarshal(describeBytes, &parsed); err != nil {
		return nil, fmt.Errorf("parsing describe output: %w", err)
	}

	// Rename the binary to its canonical name derived from the describe output.
	binPath := filepath.Join(req.DestDir, parsed.ShortCode+".bin")
	if err := os.Rename(tmpBinPath, binPath); err != nil {
		return nil, fmt.Errorf("rename binary to %s: %w", binPath, err)
	}
	log.Info().
		Str("clone_url", req.CloneURL).
		Str("short_code", parsed.ShortCode).
		Str("bin_path", binPath).
		Msg("binary ready")

	return &InstallResult{
		BinPath:      binPath,
		ArtifactRef:  binPath,
		DescribeJSON: describeBytes,
		ShortCode:    parsed.ShortCode,
	}, nil
}
