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

package tradingdays

import "time"

func IsTrading(t time.Time) bool {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
		return false
	}
	for _, h := range nyseHolidays {
		if day.Equal(h) {
			return false
		}
	}
	return true
}

func IsLastTradingDayOfWeek(t time.Time) bool {
	if !IsTrading(t) {
		return false
	}
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	for next := day.AddDate(0, 0, 1); next.Weekday() != time.Saturday; next = next.AddDate(0, 0, 1) {
		if IsTrading(next) {
			return false
		}
	}
	return true
}

func IsLastTradingDayOfMonth(t time.Time) bool {
	if !IsTrading(t) {
		return false
	}
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	for next := day.AddDate(0, 0, 1); next.Month() == day.Month(); next = next.AddDate(0, 0, 1) {
		if IsTrading(next) {
			return false
		}
	}
	return true
}
