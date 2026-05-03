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

package tradingdays_test

import (
	"testing"
	"time"

	"github.com/penny-vault/pv-api/alert/tradingdays"
)

func date(y, m, d int) time.Time {
	return time.Date(y, time.Month(m), d, 12, 0, 0, 0, time.UTC)
}

func TestIsTrading(t *testing.T) {
	tests := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"weekday", date(2025, 4, 22), true},
		{"saturday", date(2025, 4, 19), false},
		{"sunday", date(2025, 4, 20), false},
		{"new years 2025", date(2025, 1, 1), false},
		{"good friday 2025", date(2025, 4, 18), false},
		{"thanksgiving 2025", date(2025, 11, 27), false},
		{"day after thanksgiving", date(2025, 11, 28), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tradingdays.IsTrading(tc.t); got != tc.want {
				t.Errorf("IsTrading(%v) = %v; want %v", tc.t.Format("2006-01-02"), got, tc.want)
			}
		})
	}
}

func TestIsLastTradingDayOfWeek(t *testing.T) {
	tests := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"friday no holiday", date(2025, 4, 25), true},
		{"thursday before normal friday", date(2025, 4, 24), false},
		{"good friday — thursday is last", date(2025, 4, 17), true},
		{"good friday itself", date(2025, 4, 18), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tradingdays.IsLastTradingDayOfWeek(tc.t); got != tc.want {
				t.Errorf("IsLastTradingDayOfWeek(%v) = %v; want %v", tc.t.Format("2006-01-02"), got, tc.want)
			}
		})
	}
}

func TestIsLastTradingDayOfMonth(t *testing.T) {
	tests := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"last trading day april 2025", date(2025, 4, 30), true},
		{"second to last", date(2025, 4, 29), false},
		{"last trading day dec 2025", date(2025, 12, 31), true},
		{"christmas eve 2025", date(2025, 12, 24), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tradingdays.IsLastTradingDayOfMonth(tc.t); got != tc.want {
				t.Errorf("IsLastTradingDayOfMonth(%v) = %v; want %v", tc.t.Format("2006-01-02"), got, tc.want)
			}
		})
	}
}
