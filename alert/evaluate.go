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
