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
	"testing"
	"time"

	"github.com/google/uuid"
)

func ptrTime(t time.Time) *time.Time { return &t }

func TestIsDue(t *testing.T) {
	tuesday := time.Date(2025, 4, 22, 15, 0, 0, 0, time.UTC)
	friday := time.Date(2025, 4, 25, 15, 0, 0, 0, time.UTC)
	lastOfApril := time.Date(2025, 4, 30, 15, 0, 0, 0, time.UTC)

	yesterday := ptrTime(tuesday.AddDate(0, 0, -1))
	sameDay := ptrTime(tuesday)

	tests := []struct {
		name string
		a    Alert
		now  time.Time
		want bool
	}{
		{"scheduled_run/no prior", Alert{ID: uuid.New(), Frequency: FrequencyScheduledRun}, tuesday, true},
		{"scheduled_run/sent today", Alert{ID: uuid.New(), Frequency: FrequencyScheduledRun, LastSentAt: sameDay}, tuesday, true},
		{"daily/no prior", Alert{ID: uuid.New(), Frequency: FrequencyDaily}, tuesday, true},
		{"daily/sent yesterday", Alert{ID: uuid.New(), Frequency: FrequencyDaily, LastSentAt: yesterday}, tuesday, true},
		{"daily/sent today", Alert{ID: uuid.New(), Frequency: FrequencyDaily, LastSentAt: sameDay}, tuesday, false},
		{"weekly/not last day", Alert{ID: uuid.New(), Frequency: FrequencyWeekly}, tuesday, false},
		{"weekly/last day no prior", Alert{ID: uuid.New(), Frequency: FrequencyWeekly}, friday, true},
		{"weekly/last day sent today", Alert{ID: uuid.New(), Frequency: FrequencyWeekly, LastSentAt: ptrTime(friday)}, friday, false},
		{"weekly/last day sent prior week", Alert{ID: uuid.New(), Frequency: FrequencyWeekly, LastSentAt: ptrTime(friday.AddDate(0, 0, -7))}, friday, true},
		{"monthly/not last day", Alert{ID: uuid.New(), Frequency: FrequencyMonthly}, tuesday, false},
		{"monthly/last day no prior", Alert{ID: uuid.New(), Frequency: FrequencyMonthly}, lastOfApril, true},
		{"monthly/last day sent today", Alert{ID: uuid.New(), Frequency: FrequencyMonthly, LastSentAt: ptrTime(lastOfApril)}, lastOfApril, false},
		{"monthly/last day sent last month", Alert{ID: uuid.New(), Frequency: FrequencyMonthly, LastSentAt: ptrTime(lastOfApril.AddDate(0, -1, 0))}, lastOfApril, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDue(tc.a, tc.now); got != tc.want {
				t.Errorf("isDue() = %v; want %v", got, tc.want)
			}
		})
	}
}
