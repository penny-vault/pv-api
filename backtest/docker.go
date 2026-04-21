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

package backtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pv-api/dockercli"
)

// DockerRunner executes a strategy image inside a one-shot Docker container
// and produces a SQLite snapshot at RunRequest.OutPath. The snapshots host
// dir is bind-mounted into the container at the same in-container path used
// by pvapi, so OutPath is written to a single filesystem the host can read.
type DockerRunner struct {
	Client           dockercli.Client
	Network          string
	NanoCPUs         int64
	MemoryBytes      int64
	SnapshotsHostDir string // host path used as bind Source
	SnapshotsDir     string // matching target path inside the strategy container
}

// Run implements Runner.
func (r *DockerRunner) Run(ctx context.Context, req RunRequest) error {
	if req.ArtifactKind != ArtifactImage {
		return fmt.Errorf("%w: DockerRunner requires ArtifactImage, got %d", ErrArtifactKindMismatch, req.ArtifactKind)
	}

	timeoutCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		timeoutCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	cmdLine := append([]string{"backtest", "--output", req.OutPath}, req.Args...)
	cfg := &container.Config{
		Image: req.Artifact,
		Cmd:   cmdLine,
		Tty:   false,
	}
	hostCfg := &container.HostConfig{
		AutoRemove:  true,
		NetworkMode: container.NetworkMode(r.Network),
		Resources: container.Resources{
			NanoCPUs: r.NanoCPUs,
			Memory:   r.MemoryBytes,
		},
		Mounts: []mount.Mount{{
			Type:   mount.TypeBind,
			Source: r.SnapshotsHostDir,
			Target: r.SnapshotsDir,
		}},
		Tmpfs: map[string]string{"/tmp": "size=256m"},
	}

	resp, err := r.Client.ContainerCreate(timeoutCtx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return fmt.Errorf("%w: container create: %w", ErrRunnerFailed, err)
	}
	log.Debug().Str("container_id", truncID(resp.ID)).Str("image", req.Artifact).Msg("container created")

	if err := r.Client.ContainerStart(timeoutCtx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("%w: container start: %w", ErrRunnerFailed, err)
	}

	logs, lerr := r.Client.ContainerLogs(timeoutCtx, resp.ID, container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: true,
	})
	var stderrTail bytes.Buffer
	if lerr == nil {
		go streamContainerLogs(logs, &stderrTail)
	}

	waitCh, errCh := r.Client.ContainerWait(timeoutCtx, resp.ID, container.WaitConditionNotRunning)
	select {
	case werr := <-errCh:
		if errors.Is(werr, context.DeadlineExceeded) || errors.Is(werr, context.Canceled) {
			_ = r.Client.ContainerKill(context.Background(), resp.ID, "SIGKILL")
			return fmt.Errorf("%w: %s", ErrTimedOut, firstNBytes(stderrTail.String(), 2048))
		}
		return fmt.Errorf("%w: wait: %w", ErrRunnerFailed, werr)
	case st := <-waitCh:
		if st.StatusCode != 0 {
			return fmt.Errorf("%w: exit=%d: %s", ErrRunnerFailed, st.StatusCode, firstNBytes(stderrTail.String(), 2048))
		}
		return nil
	case <-timeoutCtx.Done():
		_ = r.Client.ContainerKill(context.Background(), resp.ID, "SIGKILL")
		return fmt.Errorf("%w: %s", ErrTimedOut, firstNBytes(stderrTail.String(), 2048))
	}
}

// streamContainerLogs demultiplexes Docker's framed log stream into two
// zerolog log-writer sinks and keeps a bounded tail of stderr for error
// messages.
func streamContainerLogs(r io.ReadCloser, stderrTail *bytes.Buffer) {
	defer r.Close() //nolint:errcheck // log stream close is best-effort
	stdout := newLogWriter("strategy-stdout")
	stderr := newLogWriter("strategy-stderr")
	tail := &tailWriter{buf: stderrTail, max: 2048}
	_, _ = stdcopy.StdCopy(stdout, io.MultiWriter(stderr, tail), r)
}

// tailWriter accumulates the last `max` bytes written to it.
type tailWriter struct {
	buf *bytes.Buffer
	max int
}

func (t *tailWriter) Write(p []byte) (int, error) {
	remaining := t.max - t.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = t.buf.Write(p[:remaining])
		return len(p), nil
	}
	_, _ = t.buf.Write(p)
	return len(p), nil
}

func truncID(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
