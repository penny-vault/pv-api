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

// nyseLoc is the timezone used for alert calendar arithmetic. Daily runs fire
// at 8 PM America/New_York (see portfolio.ClaimDue), so dedup must also be in
// that zone — otherwise the UTC date has already rolled over and weekly/monthly
// checks land on the wrong day.
var nyseLoc = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}()

func isDue(a Alert, now time.Time) bool {
	nowET := now.In(nyseLoc)
	today := time.Date(nowET.Year(), nowET.Month(), nowET.Day(), 0, 0, 0, 0, nyseLoc)
	lastET := func() time.Time {
		l := a.LastSentAt.In(nyseLoc)
		return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, nyseLoc)
	}
	switch a.Frequency {
	case FrequencyScheduledRun:
		return true
	case FrequencyDaily:
		if !tradingdays.IsTrading(nowET) {
			return false
		}
		if a.LastSentAt == nil {
			return true
		}
		return today.After(lastET())
	case FrequencyWeekly:
		if !tradingdays.IsLastTradingDayOfWeek(nowET) {
			return false
		}
		if a.LastSentAt == nil {
			return true
		}
		return today.After(lastET())
	case FrequencyMonthly:
		if !tradingdays.IsLastTradingDayOfMonth(nowET) {
			return false
		}
		if a.LastSentAt == nil {
			return true
		}
		return today.After(lastET())
	default:
		return false
	}
}
