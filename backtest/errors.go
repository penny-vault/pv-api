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

import "errors"

var (
	// ErrRunnerFailed is returned when the strategy binary exits non-zero
	// or cannot be launched. The wrapped error holds details.
	ErrRunnerFailed = errors.New("backtest: runner failed")

	// ErrTimedOut is returned when the runner exceeded its configured timeout.
	ErrTimedOut = errors.New("backtest: runner timed out")

	// ErrAlreadyRunning is returned by Run when the portfolio's status is
	// already 'running' at the time the worker picks the task up.
	ErrAlreadyRunning = errors.New("backtest: portfolio already running")

	// ErrQueueFull is returned by Dispatcher.Submit when the task channel
	// has reached its bounded capacity.
	ErrQueueFull = errors.New("backtest: dispatcher queue full")

	// ErrStrategyNotInstalled is returned when the resolved strategy has
	// no installed binary on disk.
	ErrStrategyNotInstalled = errors.New("backtest: strategy binary not installed")

	// ErrSnapshotsDirRequired is returned by Config.Validate when SnapshotsDir is empty.
	ErrSnapshotsDirRequired = errors.New("backtest: snapshots_dir is required")

	// ErrInvalidConcurrency is returned by Config.Validate when MaxConcurrency < 0.
	ErrInvalidConcurrency = errors.New("backtest: max_concurrency must be >= 0")

	// ErrUnsupportedRunnerMode is returned by Config.Validate when RunnerMode is not "host" or "docker".
	ErrUnsupportedRunnerMode = errors.New(`backtest: runner.mode must be "host" or "docker" (kubernetes lands in plan 9)`)

	// ErrStrategyNoArtifact is returned when a strategy has no installed binary artifact.
	ErrStrategyNoArtifact = errors.New("backtest: strategy has no installed binary")

	// ErrArtifactKindMismatch is returned when a runner is handed a RunRequest
	// whose ArtifactKind does not match what the runner supports. Indicates a
	// wiring bug at startup (resolver + runner are wired together by
	// cmd/server.go).
	ErrArtifactKindMismatch = errors.New("backtest: artifact kind mismatch")
)
