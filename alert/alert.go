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

package alert

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const (
	FrequencyScheduledRun = "scheduled_run"
	FrequencyDaily        = "daily"
	FrequencyWeekly       = "weekly"
	FrequencyMonthly      = "monthly"
)

type Alert struct {
	ID            uuid.UUID
	PortfolioID   uuid.UUID
	Frequency     string
	Recipients    []string
	LastSentAt    *time.Time
	LastSentValue *float64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Notifier is called by the backtest orchestrator after each run completes.
type Notifier interface {
	NotifyRunComplete(ctx context.Context, portfolioID, runID uuid.UUID, success bool) error
}
