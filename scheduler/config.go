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

package scheduler

import (
	"errors"
	"time"
)

// Config controls the scheduler tick loop.
type Config struct {
	// TickInterval is the cadence at which the scheduler polls for due
	// continuous portfolios. Defaults to 60s.
	TickInterval time.Duration
	// BatchSize is the maximum number of portfolios claimed per tick.
	// Defaults to 32.
	BatchSize int
}

// ApplyDefaults fills zero-valued fields with their documented defaults.
func (c *Config) ApplyDefaults() {
	if c.TickInterval == 0 {
		c.TickInterval = 60 * time.Second
	}
	if c.BatchSize == 0 {
		c.BatchSize = 32
	}
}

// Validate reports invalid configuration.
func (c Config) Validate() error {
	if c.TickInterval < 0 {
		return ErrInvalidTickInterval
	}
	if c.BatchSize < 0 {
		return ErrInvalidBatchSize
	}
	return nil
}

var (
	// ErrInvalidTickInterval is returned by Config.Validate when TickInterval < 0.
	ErrInvalidTickInterval = errors.New("scheduler: tick_interval must be >= 0")
	// ErrInvalidBatchSize is returned by Config.Validate when BatchSize < 0.
	ErrInvalidBatchSize = errors.New("scheduler: batch_size must be >= 0")
)
