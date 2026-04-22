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
