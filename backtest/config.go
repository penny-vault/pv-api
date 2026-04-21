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
	"runtime"
	"time"
)

// Config drives the backtest subsystem. Populated at startup from viper.
type Config struct {
	SnapshotsDir   string        // absolute path; required
	MaxConcurrency int           // 0 -> runtime.NumCPU()
	Timeout        time.Duration // per-run timeout; 0 -> 15 minutes
	RunnerMode     string        // "host" or "docker"; "kubernetes" lands in plan 9
}

// ApplyDefaults fills zero-valued fields with their defaults.
func (c *Config) ApplyDefaults() {
	if c.MaxConcurrency == 0 {
		c.MaxConcurrency = runtime.NumCPU()
	}
	if c.Timeout == 0 {
		c.Timeout = 15 * time.Minute
	}
}

// Validate returns an error if the config is not usable.
func (c Config) Validate() error {
	if c.SnapshotsDir == "" {
		return ErrSnapshotsDirRequired
	}
	if c.MaxConcurrency < 0 {
		return ErrInvalidConcurrency
	}
	switch c.RunnerMode {
	case "host", "docker":
		// ok
	default:
		return ErrUnsupportedRunnerMode
	}
	return nil
}
