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
	"github.com/google/uuid"

	"github.com/penny-vault/pv-api/progress"
)

// Type aliases so existing backtest callers don't need to change their import.
type ProgressMessage = progress.ProgressMessage
type TerminalEvent = progress.TerminalEvent
type Event = progress.Event
type ProgressHub = progress.Hub

// NewProgressHub returns a ready-to-use ProgressHub.
func NewProgressHub() *ProgressHub { return progress.NewHub() }

// NewProgressLineWriter returns a writer that parses strategy stdout JSON lines
// and publishes progress events to hub.
func NewProgressLineWriter(hub *ProgressHub, runID uuid.UUID) *progress.LineWriter {
	return progress.NewLineWriter(hub, runID)
}
