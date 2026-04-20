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

package portfolio

import (
	"time"

	"github.com/google/uuid"
)

// Mode mirrors the portfolio_mode enum.
type Mode string

const (
	ModeOneShot    Mode = "one_shot"
	ModeContinuous Mode = "continuous"
	ModeLive       Mode = "live"
)

// Status mirrors the portfolio_status enum.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusReady   Status = "ready"
	StatusFailed  Status = "failed"
)

// Portfolio is the internal representation of a portfolios row — config
// fields only. Derived summary columns (`current_value`, `ytd_return`,
// JSONB blobs) are not exposed here; Plan 5 adds a separate derived-row
// shape when the runner starts populating those columns.
type Portfolio struct {
	ID           uuid.UUID
	OwnerSub     string
	Slug         string
	Name         string
	StrategyCode string
	StrategyVer  string
	Parameters   map[string]any
	PresetName   *string
	Benchmark    string
	Mode         Mode
	Schedule     *string
	Status       Status
	LastRunAt    *time.Time
	NextRunAt    *time.Time
	LastError    *string
	SnapshotPath *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CreateRequest is what the POST /portfolios handler hands off to the
// domain layer. Mirrors the OpenAPI PortfolioCreateRequest.
type CreateRequest struct {
	Name         string
	StrategyCode string
	StrategyVer  string // empty → use strategy's installed_ver
	Parameters   map[string]any
	Benchmark    string // empty → use strategy's describe.benchmark
	Mode         Mode
	Schedule     string // required iff Mode == ModeContinuous
	RunNow       bool   // accepted but no-op in Plan 4
}

// UpdateRequest is what PATCH /portfolios/{slug} hands off. Name-only.
type UpdateRequest struct {
	Name string
}
