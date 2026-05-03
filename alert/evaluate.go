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
	"time"

	"github.com/penny-vault/pv-api/alert/tradingdays"
)

func isDue(a Alert, now time.Time) bool {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	switch a.Frequency {
	case FrequencyScheduledRun:
		return true
	case FrequencyDaily:
		if a.LastSentAt == nil {
			return true
		}
		last := time.Date(a.LastSentAt.Year(), a.LastSentAt.Month(), a.LastSentAt.Day(), 0, 0, 0, 0, time.UTC)
		return today.After(last)
	case FrequencyWeekly:
		if !tradingdays.IsLastTradingDayOfWeek(now) {
			return false
		}
		if a.LastSentAt == nil {
			return true
		}
		last := time.Date(a.LastSentAt.Year(), a.LastSentAt.Month(), a.LastSentAt.Day(), 0, 0, 0, 0, time.UTC)
		return today.After(last)
	case FrequencyMonthly:
		if !tradingdays.IsLastTradingDayOfMonth(now) {
			return false
		}
		if a.LastSentAt == nil {
			return true
		}
		last := time.Date(a.LastSentAt.Year(), a.LastSentAt.Month(), a.LastSentAt.Day(), 0, 0, 0, 0, time.UTC)
		return today.After(last)
	default:
		return false
	}
}
