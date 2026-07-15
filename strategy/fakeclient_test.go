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
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// writeStdoutFrame writes p to buf using Docker's log multiplex framing
// (8-byte header: stream type 1 = stdout, three zero bytes, big-endian
// payload length) so stdcopy.StdCopy demuxes it as stdout.
func writeStdoutFrame(buf *bytes.Buffer, p []byte) {
	hdr := [8]byte{1}
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(p)))
	buf.Write(hdr[:])
	buf.Write(p)
}

// fakeDocker is a tiny stub implementing dockercli.Client enough for our
// strategy-package tests. Configure via the public fields; default behavior
// is "succeed with a canned describe payload".
type fakeDocker struct {
	mu sync.Mutex

	ImageBuildErr  error
	ImageBuildResp string // JSON stream body

	DescribeStdout []byte
	ContainerExit  int64

	CreatedImages []string
	RemovedImages []string
	CreatedCmds   [][]string
	CreatedHosts  []*container.HostConfig
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		ImageBuildResp: `{"stream":"Step 1/1 : FROM scratch\n"}
{"stream":"Successfully built abc123\n"}
`,
		DescribeStdout: []byte(`{"shortcode":"fake","name":"Fake","parameters":[],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`),
	}
}

func (f *fakeDocker) ImageBuild(_ context.Context, _ io.Reader, opts client.ImageBuildOptions) (client.ImageBuildResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ImageBuildErr != nil {
		return client.ImageBuildResult{}, f.ImageBuildErr
	}
	f.CreatedImages = append(f.CreatedImages, opts.Tags...)
	return client.ImageBuildResult{
		Body: io.NopCloser(bytes.NewBufferString(f.ImageBuildResp)),
	}, nil
}

func (f *fakeDocker) ImageRemove(_ context.Context, id string, _ client.ImageRemoveOptions) (client.ImageRemoveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RemovedImages = append(f.RemovedImages, id)
	return client.ImageRemoveResult{}, nil
}

func (f *fakeDocker) ContainerCreate(_ context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CreatedCmds = append(f.CreatedCmds, opts.Config.Cmd)
	f.CreatedHosts = append(f.CreatedHosts, opts.HostConfig)
	return client.ContainerCreateResult{ID: fmt.Sprintf("ctr-%d", len(f.CreatedCmds))}, nil
}

func (f *fakeDocker) ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error) {
	return client.ContainerStartResult{}, nil
}

func (f *fakeDocker) ContainerLogs(_ context.Context, _ string, _ client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var buf bytes.Buffer
	writeStdoutFrame(&buf, f.DescribeStdout)
	return io.NopCloser(&buf), nil
}

func (f *fakeDocker) ContainerWait(_ context.Context, _ string, _ client.ContainerWaitOptions) client.ContainerWaitResult {
	wait := make(chan container.WaitResponse, 1)
	errCh := make(chan error, 1)
	wait <- container.WaitResponse{StatusCode: f.ContainerExit}
	return client.ContainerWaitResult{Result: wait, Error: errCh}
}

func (f *fakeDocker) ContainerKill(context.Context, string, client.ContainerKillOptions) (client.ContainerKillResult, error) {
	return client.ContainerKillResult{}, nil
}
