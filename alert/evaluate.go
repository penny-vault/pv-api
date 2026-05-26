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
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pvbt/tradecron"
)

// nyseLoc is the timezone used for alert calendar arithmetic. Daily runs fire
// at 8 PM America/New_York, so dedup must also be in that zone -- otherwise the
// UTC date has already rolled over and the cadence checks land on the wrong day.
var nyseLoc = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}()

// The cadence checks delegate to pvbt's tradecron, using the holiday calendar
// loaded into tradecron at startup. weekEnd/monthEnd answer "last trading day
// of the week/month"; market answers "is a trading day".
var (
	calOnce  sync.Once
	weekEnd  *tradecron.TradeCron
	monthEnd *tradecron.TradeCron
	market   *tradecron.MarketStatus
)

func buildCalendar() {
	calOnce.Do(func() {
		var err error
		if weekEnd, err = tradecron.New("@weekend", tradecron.RegularHours); err != nil {
			log.Error().Err(err).Msg("alert: build @weekend schedule")
			return
		}
		if monthEnd, err = tradecron.New("@monthend", tradecron.RegularHours); err != nil {
			log.Error().Err(err).Msg("alert: build @monthend schedule")
			return
		}
		market = tradecron.NewMarketStatus(&tradecron.RegularHours)
	})
}

// calReady reports whether the trading calendar can answer cadence questions.
// The holiday data is loaded into tradecron at server startup; if it is missing
// the cadence checks report false so an alert is never sent for a day whose
// trading status cannot be verified.
func calReady() bool {
	if !tradecron.HolidaysInitialized() {
		return false
	}
	buildCalendar()
	return market != nil && weekEnd != nil && monthEnd != nil
}

func isTradingDay(t time.Time) bool { return calReady() && market.IsMarketDay(t) }

func isLastTradingDayOfWeek(t time.Time) bool { return calReady() && weekEnd.IsTradeDay(t) }

func isLastTradingDayOfMonth(t time.Time) bool { return calReady() && monthEnd.IsTradeDay(t) }

// strategyFiresOn reports whether a strategy whose rebalance schedule is spec
// rebalances on day t. An empty or unparseable schedule is treated as firing
// every trading day.
func strategyFiresOn(spec string, t time.Time) bool {
	if !calReady() {
		return false
	}
	if spec == "" {
		return market.IsMarketDay(t)
	}
	tc, err := tradecron.New(spec, tradecron.RegularHours)
	if err != nil {
		log.Warn().Err(err).Str("schedule", spec).Msg("alert: unparseable strategy schedule; treating as every trading day")
		return market.IsMarketDay(t)
	}
	return tc.IsTradeDay(t)
}

// isDue reports whether alert a should be sent for a run completing at now.
// strategySchedule is the portfolio's rebalance schedule, used by the
// scheduled_run ("every run") cadence. A second send is suppressed once an
// alert has already gone out for the current ET day.
func isDue(a Alert, strategySchedule string, now time.Time) bool {
	nowET := now.In(nyseLoc)

	switch a.Frequency {
	case FrequencyScheduledRun:
		if !strategyFiresOn(strategySchedule, nowET) {
			return false
		}
	case FrequencyDaily:
		if !isTradingDay(nowET) {
			return false
		}
	case FrequencyWeekly:
		if !isLastTradingDayOfWeek(nowET) {
			return false
		}
	case FrequencyMonthly:
		if !isLastTradingDayOfMonth(nowET) {
			return false
		}
	default:
		return false
	}

	if a.LastSentAt == nil {
		return true
	}
	today := time.Date(nowET.Year(), nowET.Month(), nowET.Day(), 0, 0, 0, 0, nyseLoc)
	last := a.LastSentAt.In(nyseLoc)
	lastDay := time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, nyseLoc)
	return today.After(lastDay)
}
