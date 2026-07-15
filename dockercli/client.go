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

	"github.com/moby/moby/client"
)

// Client is the subset of *moby/client.Client pvapi uses. Production wires
// a real *client.Client built with client.New; tests pass a fake.
type Client interface {
	ImageBuild(ctx context.Context, buildContext io.Reader, opts client.ImageBuildOptions) (client.ImageBuildResult, error)
	ImageRemove(ctx context.Context, imageID string, opts client.ImageRemoveOptions) (client.ImageRemoveResult, error)
	ContainerCreate(ctx context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, id string, opts client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerLogs(ctx context.Context, id string, opts client.ContainerLogsOptions) (client.ContainerLogsResult, error)
	ContainerWait(ctx context.Context, id string, opts client.ContainerWaitOptions) client.ContainerWaitResult
	ContainerKill(ctx context.Context, id string, opts client.ContainerKillOptions) (client.ContainerKillResult, error)
}
