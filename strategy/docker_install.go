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

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/rs/zerolog/log"

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
//  4. docker run --rm <image> describe --json  (short code is authoritative from the image)
func InstallDocker(ctx context.Context, req InstallRequest, deps DockerInstallDeps) (*InstallResult, error) {
	if err := validateDockerInstallInputs(req, &deps); err != nil {
		return nil, err
	}

	bctx, cancel := context.WithTimeout(ctx, deps.BuildTimeout)
	defer cancel()

	log.Info().Str("clone_url", req.CloneURL).Str("version", req.Version).Msg("cloning strategy repository")

	if err := dockerInstallClone(bctx, req); err != nil {
		return nil, err
	}
	log.Info().Str("clone_url", req.CloneURL).Msg("clone complete; writing Dockerfile")

	if err := writeDockerfileIntoDir(req.DestDir); err != nil {
		return nil, err
	}

	tag := ImageTag(deps.ImagePrefix, req.CloneURL, req.Version)
	log.Info().Str("clone_url", req.CloneURL).Str("image_tag", tag).Msg("building Docker image")
	if err := buildDockerImage(bctx, deps.Client, req.DestDir, tag); err != nil {
		return nil, err
	}
	log.Info().Str("image_tag", tag).Msg("Docker image built; running describe")

	describeJSON, parsed, err := describeDockerImage(bctx, deps.Client, tag)
	if err != nil {
		return nil, err
	}

	log.Info().
		Str("image_tag", tag).
		Str("short_code", parsed.ShortCode).
		Msg("Docker image ready")

	return &InstallResult{
		BinPath:      "",
		ArtifactRef:  tag,
		DescribeJSON: describeJSON,
		ShortCode:    parsed.ShortCode,
	}, nil
}

// validateDockerInstallInputs checks the request fields and applies defaults
// to deps. Returns one of the package's sentinel errors or nil.
func validateDockerInstallInputs(req InstallRequest, deps *DockerInstallDeps) error {
	if req.CloneURL == "" || req.Version == "" || req.DestDir == "" {
		return ErrInstallMissingFields
	}
	if deps.Client == nil {
		return ErrDockerClientNil
	}
	if deps.ImagePrefix == "" {
		deps.ImagePrefix = "pvapi-strategy"
	}
	if deps.BuildTimeout <= 0 {
		deps.BuildTimeout = 10 * time.Minute
	}
	return nil
}

// dockerInstallClone runs `git clone --depth=1 --branch <Version> <CloneURL> <DestDir>`,
// surfacing combined stdout/stderr in the wrapped error on failure.
func dockerInstallClone(ctx context.Context, req InstallRequest) error {
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1",
		"--branch", req.Version, req.CloneURL, req.DestDir)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone %s@%s: %w\n%s", req.CloneURL, req.Version, err, out.String())
	}
	return nil
}

// writeDockerfileIntoDir resolves the strategy's Go version (or the default)
// and renders the matching Dockerfile into <dir>/Dockerfile.
func writeDockerfileIntoDir(dir string) error {
	goVer, _ := ParseGoVersion(dir)
	dfPath := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(dfPath, RenderDockerfile(goVer), 0o600); err != nil {
		return fmt.Errorf("write Dockerfile: %w", err)
	}
	return nil
}

// buildDockerImage tars the build context directory and submits an ImageBuild
// request, draining the streamed build output and surfacing failure messages.
func buildDockerImage(ctx context.Context, c dockercli.Client, dir, tag string) error {
	buildCtx, err := tarDir(dir)
	if err != nil {
		return fmt.Errorf("tar build context: %w", err)
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
			log.Warn().Err(cerr).Str("image_tag", tag).Msg("docker install: build response close failed")
		}
	}()
	buildOut, bErr := drainBuildStream(resp.Body)
	if bErr != nil {
		return fmt.Errorf("%w: %w\n%s", ErrDockerBuildFailed, bErr, buildOut)
	}
	return nil
}

// describeDockerImage runs `<image> describe --json` in a disposable
// container and returns the raw JSON plus the parsed Describe struct.
func describeDockerImage(ctx context.Context, c dockercli.Client, tag string) ([]byte, Describe, error) {
	describeJSON, err := runDescribeInContainer(ctx, c, tag)
	if err != nil {
		return nil, Describe{}, fmt.Errorf("describe after build: %w", err)
	}
	var parsed Describe
	if err := json.Unmarshal(describeJSON, &parsed); err != nil {
		return nil, Describe{}, fmt.Errorf("parsing describe output: %w", err)
	}
	return describeJSON, parsed, nil
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
			f, oerr := os.Open(path)
			if oerr != nil {
				return oerr
			}
			_, cerr := io.Copy(tw, f)
			if closeErr := f.Close(); closeErr != nil {
				log.Warn().Err(closeErr).Str("path", path).Msg("docker install: tar source file close failed")
			}
			if cerr != nil {
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
	defer func() {
		if cerr := logs.Close(); cerr != nil {
			log.Warn().Err(cerr).Str("container_id", resp.ID).Msg("docker install: log stream close failed")
		}
	}()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, logs); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("demux logs: %w", err)
	}
	return bytes.TrimSpace(stdout.Bytes()), nil
}
