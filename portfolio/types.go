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

// Status mirrors the portfolio_status enum.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusReady   Status = "ready"
	StatusFailed  Status = "failed"
)

// Portfolio is the internal representation of a portfolios row.
type Portfolio struct {
	ID                   uuid.UUID
	OwnerSub             string
	Slug                 string
	Name                 string
	StrategyCode         string
	StrategyVer          *string
	StrategyCloneURL     string
	StrategyDescribeJSON []byte
	Parameters           map[string]any
	PresetName           *string
	Benchmark            string
	StartDate            *time.Time
	EndDate              *time.Time
	Status               Status
	LastRunAt            *time.Time
	LastError            *string
	SnapshotPath         *string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	RunRetention         int `json:"run_retention"`
}

// CreateRequest is what the POST /portfolios handler passes to the domain layer.
type CreateRequest struct {
	Name             string
	StrategyCode     string
	StrategyVer      string
	StrategyCloneURL string
	Parameters       map[string]any
	Benchmark        string
	StartDate        *time.Time
	EndDate          *time.Time
	RunRetention     *int
}

// UpdateRequest is what PATCH /portfolios/{slug} passes to the domain layer.
type UpdateRequest struct {
	Name      string
	StartDate *time.Time
	EndDate   *time.Time
}
