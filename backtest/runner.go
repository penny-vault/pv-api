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
	"context"
	"time"

	"github.com/google/uuid"
)

// Runner executes a strategy artifact and produces a SQLite snapshot at
// RunRequest.OutPath. Implementations: HostRunner (binary), DockerRunner (image).
type Runner interface {
	Run(ctx context.Context, req RunRequest) error
}

// ArtifactKind selects how the Runner should interpret RunRequest.Artifact.
type ArtifactKind int

const (
	// ArtifactBinary means Artifact is an absolute filesystem path to an
	// executable. Consumed by HostRunner.
	ArtifactBinary ArtifactKind = iota
	// ArtifactImage means Artifact is a Docker image reference
	// ("repo/name:tag"). Consumed by DockerRunner.
	ArtifactImage
)

// RunRequest carries everything a Runner needs to produce one snapshot.
type RunRequest struct {
	RunID        uuid.UUID     // optional; used for container naming / log correlation
	Artifact     string        // binary path for host; image ref for docker
	ArtifactKind ArtifactKind  // must match the runner
	Args         []string      // strategy-specific CLI flags (parameters + benchmark)
	OutPath      string        // absolute path where the snapshot must be written
	Timeout      time.Duration // 0 means use Config.Timeout default
}
