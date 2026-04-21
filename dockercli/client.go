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

// Package dockercli defines the narrow Docker Engine SDK surface the backtest
// runner and the strategy docker installer rely on. Lives in its own leaf
// package so neither backtest/ nor strategy/ depends on the other.
package dockercli

import (
	"context"
	"io"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Client is the subset of *docker/client.Client pvapi uses. Production wires
// a real *client.Client built with client.NewClientWithOpts; tests pass a
// fake.
type Client interface {
	ImageBuild(ctx context.Context, buildContext io.Reader, opts types.ImageBuildOptions) (types.ImageBuildResponse, error)
	ImageRemove(ctx context.Context, imageID string, opts image.RemoveOptions) ([]image.DeleteResponse, error)
	ContainerCreate(ctx context.Context, cfg *container.Config, hostCfg *container.HostConfig, netCfg *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, id string, opts container.StartOptions) error
	ContainerLogs(ctx context.Context, id string, opts container.LogsOptions) (io.ReadCloser, error)
	ContainerWait(ctx context.Context, id string, cond container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
	ContainerKill(ctx context.Context, id, signal string) error
	ContainerRemove(ctx context.Context, id string, opts container.RemoveOptions) error
}
