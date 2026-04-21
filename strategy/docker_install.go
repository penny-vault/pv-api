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
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/penny-vault/pv-api/dockercli"
)

// ErrDockerBuildFailed wraps errors returned from ImageBuild.
var ErrDockerBuildFailed = errors.New("strategy: docker image build failed")

// ErrDockerClientNil is returned when InstallDocker or EphemeralImageBuild
// receive a nil dockercli.Client.
var ErrDockerClientNil = errors.New("strategy: docker client is nil")

// ErrDescribeNonZeroExit is returned when the describe container exits with a
// non-zero status code.
var ErrDescribeNonZeroExit = errors.New("strategy: describe container exited non-zero")

// DockerInstallDeps configures InstallDocker.
type DockerInstallDeps struct {
	Client       dockercli.Client
	ImagePrefix  string
	BuildTimeout time.Duration
}

// ImageTag returns "<prefix>/<owner>/<repo>:<ver>" for a canonical
// https://github.com/<owner>/<repo>(.git)? URL. Non-GitHub URLs (only
// reachable when the caller has set SkipURLValidation) fall through to
// "<prefix>/unknown/<slugified host+path>:<ver>".
func ImageTag(prefix, cloneURL, ver string) string {
	u, err := url.Parse(cloneURL)
	if err == nil && u.Host == "github.com" {
		path := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 2 {
			return prefix + "/" + parts[0] + "/" + parts[1] + ":" + ver
		}
	}
	slug := cloneURL
	if err == nil && u.Host != "" {
		slug = u.Host + u.Path
	}
	slug = strings.ReplaceAll(slug, "/", "-")
	slug = strings.ReplaceAll(slug, ":", "-")
	return prefix + "/unknown/" + slug + ":" + ver
}

// InstallDocker performs a single version-pinned Docker install:
//  1. git clone --depth=1 --branch <Version> <CloneURL> <DestDir>
//  2. write generated Dockerfile into <DestDir>/Dockerfile
//  3. ImageBuild, tag = ImageTag(prefix, CloneURL, Version)
//  4. docker run --rm <image> describe --json  (via the sdk)
//  5. validate describe.shortCode matches req.ShortCode
func InstallDocker(ctx context.Context, req InstallRequest, deps DockerInstallDeps) (*InstallResult, error) { //nolint:gocyclo // control flow is sequential; refactoring would obscure error handling
	if req.ShortCode == "" || req.CloneURL == "" || req.Version == "" || req.DestDir == "" {
		return nil, ErrInstallMissingFields
	}
	if deps.Client == nil {
		return nil, ErrDockerClientNil
	}
	if deps.ImagePrefix == "" {
		deps.ImagePrefix = "pvapi-strategy"
	}
	timeout := deps.BuildTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	bctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 1. clone.
	cloneCmd := exec.CommandContext(bctx, "git", "clone", "--depth=1", //nolint:gosec // sourced from trusted sync state
		"--branch", req.Version, req.CloneURL, req.DestDir)
	var cloneOut bytes.Buffer
	cloneCmd.Stdout = &cloneOut
	cloneCmd.Stderr = &cloneOut
	if err := cloneCmd.Run(); err != nil {
		return nil, fmt.Errorf("git clone %s@%s: %w\n%s", req.CloneURL, req.Version, err, cloneOut.String())
	}

	// 2. write generated Dockerfile.
	goVer, _ := ParseGoVersion(req.DestDir)
	dfPath := filepath.Join(req.DestDir, "Dockerfile")
	if err := os.WriteFile(dfPath, RenderDockerfile(goVer), 0o600); err != nil {
		return nil, fmt.Errorf("write Dockerfile: %w", err)
	}

	// 3. build.
	tag := ImageTag(deps.ImagePrefix, req.CloneURL, req.Version)
	buildCtx, err := tarDir(req.DestDir)
	if err != nil {
		return nil, fmt.Errorf("tar build context: %w", err)
	}
	resp, err := deps.Client.ImageBuild(bctx, buildCtx, types.ImageBuildOptions{
		Dockerfile: "Dockerfile",
		Tags:       []string{tag},
		Remove:     true,
		PullParent: true,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDockerBuildFailed, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close is best-effort after read
	buildOut, bErr := drainBuildStream(resp.Body)
	if bErr != nil {
		return nil, fmt.Errorf("%w: %w\n%s", ErrDockerBuildFailed, bErr, buildOut)
	}

	// 4. describe.
	describeJSON, err := runDescribeInContainer(bctx, deps.Client, tag)
	if err != nil {
		return nil, fmt.Errorf("describe after build: %w", err)
	}

	// 5. validate short code.
	var parsed Describe
	if err := json.Unmarshal(describeJSON, &parsed); err != nil {
		return nil, fmt.Errorf("parsing describe output: %w", err)
	}
	if parsed.ShortCode != req.ShortCode {
		return nil, fmt.Errorf("%w: want %q, got %q", ErrShortCodeMismatch, req.ShortCode, parsed.ShortCode)
	}

	return &InstallResult{
		BinPath:      "",
		ArtifactRef:  tag,
		DescribeJSON: describeJSON,
		ShortCode:    parsed.ShortCode,
	}, nil
}

// tarDir packs dir (recursively) into an in-memory tar suitable as an
// ImageBuild context.
func tarDir(dir string) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		hdr, herr := tar.FileInfoHeader(info, "")
		if herr != nil {
			return herr
		}
		hdr.Name = filepath.ToSlash(rel)
		if werr := tw.WriteHeader(hdr); werr != nil {
			return werr
		}
		if info.Mode().IsRegular() {
			f, oerr := os.Open(path) //nolint:gosec // path is internal
			if oerr != nil {
				return oerr
			}
			defer f.Close() //nolint:errcheck // read-only file; close error is not actionable
			if _, cerr := io.Copy(tw, f); cerr != nil {
				return cerr
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// drainBuildStream reads the newline-delimited JSON ImageBuild stream,
// surfacing any {"errorDetail":{"message":...}} frames as errors.
func drainBuildStream(r io.Reader) (string, error) {
	var out bytes.Buffer
	dec := json.NewDecoder(r)
	for dec.More() {
		var frame struct {
			Stream      string `json:"stream,omitempty"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail,omitempty"`
		}
		if err := dec.Decode(&frame); err != nil {
			return out.String(), err
		}
		out.WriteString(frame.Stream)
		if frame.ErrorDetail != nil {
			return out.String(), fmt.Errorf("%w: %s", ErrDockerBuildFailed, frame.ErrorDetail.Message)
		}
	}
	return out.String(), nil
}

// runDescribeInContainer creates a disposable container from the given image
// with cmd = ["describe", "--json"], starts it, waits for exit 0, and
// returns stdout. AutoRemove=true cleans up the container on exit.
func runDescribeInContainer(ctx context.Context, c dockercli.Client, image string) ([]byte, error) {
	resp, err := c.ContainerCreate(ctx, &container.Config{
		Image: image,
		Cmd:   []string{"describe", "--json"},
		Tty:   false,
	}, &container.HostConfig{AutoRemove: true}, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("container create: %w", err)
	}
	if err := c.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("container start: %w", err)
	}

	waitCh, errCh := c.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case werr := <-errCh:
		return nil, fmt.Errorf("container wait: %w", werr)
	case st := <-waitCh:
		if st.StatusCode != 0 {
			return nil, fmt.Errorf("%w: exit %d", ErrDescribeNonZeroExit, st.StatusCode)
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	logs, err := c.ContainerLogs(ctx, resp.ID, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return nil, fmt.Errorf("container logs: %w", err)
	}
	defer logs.Close() //nolint:errcheck // best-effort close on read-only log stream

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, logs); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("demux logs: %w", err)
	}
	return bytes.TrimSpace(stdout.Bytes()), nil
}
