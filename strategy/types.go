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

package strategy

import (
	"time"
)

// InstallState captures where a strategy row sits in the install lifecycle.
type InstallState string

const (
	InstallStatePending    InstallState = "pending"
	InstallStateInstalling InstallState = "installing"
	InstallStateReady      InstallState = "ready"
	InstallStateFailed     InstallState = "failed"
)

// Strategy is pvapi's internal representation of a `strategies` row.
type Strategy struct {
	ShortCode        string
	RepoOwner        string
	RepoName         string
	CloneURL         string
	IsOfficial       bool
	OwnerSub         *string
	Description      *string
	Categories       []string
	Stars            *int
	InstalledVer     *string
	InstalledAt      *time.Time
	LastAttemptedVer *string
	InstallError     *string
	ArtifactKind     *string // "binary" | "image"
	ArtifactRef      *string
	DescribeJSON     []byte // raw describe output; parsed on demand
	CAGR             *float64
	MaxDrawdown      *float64
	Sharpe           *float64
	StatsAsOf        *time.Time
	// StatsError is write-only at the DB layer; not included in strategyColumns/scan.
	StatsError       *string
	DiscoveredAt     time.Time
	UpdatedAt        time.Time
}

// StatsResult holds the performance metrics written back to the strategies table.
type StatsResult struct {
	CAGR        float64
	MaxDrawdown float64
	Sharpe      float64
	AsOf        time.Time
}

// DeriveInstallState reports the lifecycle state implied by the row's
// install-tracking columns. Has no side effects.
func (s Strategy) DeriveInstallState() InstallState {
	switch {
	case s.InstalledVer != nil && s.InstallError == nil:
		return InstallStateReady
	case s.InstallError != nil:
		return InstallStateFailed
	case s.LastAttemptedVer != nil:
		return InstallStateInstalling
	default:
		return InstallStatePending
	}
}

// Listing is the shape returned by the GitHub discovery layer, after
// filtering to official (penny-vault) strategies.
type Listing struct {
	Name        string
	Owner       string
	Description string
	Categories  []string
	CloneURL    string
	Stars       int
	UpdatedAt   time.Time
}

// Describe mirrors pvbt's `describe --json` output, parsed from the raw
// bytes stored in Strategy.DescribeJSON.
type Describe struct {
	ShortCode   string              `json:"shortcode"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Parameters  []DescribeParameter `json:"parameters"`
	Presets     []DescribePreset    `json:"presets"`
	Schedule    string              `json:"schedule"`
	Benchmark   string              `json:"benchmark"`
}

// DescribeParameter is one declared parameter in the describe output.
type DescribeParameter struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	Default     interface{} `json:"default,omitempty"`
	Description string      `json:"description,omitempty"`
}

// DescribePreset is one named parameter set from the describe output.
type DescribePreset struct {
	Name       string                 `json:"name"`
	Parameters map[string]interface{} `json:"parameters"`
}
