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
	"fmt"
	"io"
	"sync"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

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

func (f *fakeDocker) ImageBuild(_ context.Context, _ io.Reader, opts build.ImageBuildOptions) (build.ImageBuildResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ImageBuildErr != nil {
		return build.ImageBuildResponse{}, f.ImageBuildErr
	}
	f.CreatedImages = append(f.CreatedImages, opts.Tags...)
	return build.ImageBuildResponse{
		Body: io.NopCloser(bytes.NewBufferString(f.ImageBuildResp)),
	}, nil
}

func (f *fakeDocker) ImageRemove(_ context.Context, id string, _ image.RemoveOptions) ([]image.DeleteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RemovedImages = append(f.RemovedImages, id)
	return nil, nil
}

func (f *fakeDocker) ContainerCreate(_ context.Context, cfg *container.Config, hostCfg *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CreatedCmds = append(f.CreatedCmds, cfg.Cmd)
	f.CreatedHosts = append(f.CreatedHosts, hostCfg)
	return container.CreateResponse{ID: fmt.Sprintf("ctr-%d", len(f.CreatedCmds))}, nil
}

func (f *fakeDocker) ContainerStart(context.Context, string, container.StartOptions) error {
	return nil
}

func (f *fakeDocker) ContainerLogs(_ context.Context, _ string, _ container.LogsOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Emit the describe stdout through the Docker log multiplex header
	// (stream=1 = stdout) so stdcopy.StdCopy demuxes it correctly.
	var buf bytes.Buffer
	w := stdcopy.NewStdWriter(&buf, stdcopy.Stdout)
	_, _ = w.Write(f.DescribeStdout)
	return io.NopCloser(&buf), nil
}

func (f *fakeDocker) ContainerWait(_ context.Context, _ string, _ container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	wait := make(chan container.WaitResponse, 1)
	errCh := make(chan error, 1)
	wait <- container.WaitResponse{StatusCode: f.ContainerExit}
	return wait, errCh
}

func (f *fakeDocker) ContainerKill(context.Context, string, string) error { return nil }
func (f *fakeDocker) ContainerRemove(context.Context, string, container.RemoveOptions) error {
	return nil
}
