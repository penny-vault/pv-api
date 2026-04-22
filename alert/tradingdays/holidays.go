package tradingdays

import "time"

var nyseHolidays = []time.Time{
	// 2025
	d(2025, 1, 1),
	d(2025, 1, 20),
	d(2025, 2, 17),
	d(2025, 4, 18),
	d(2025, 5, 26),
	d(2025, 6, 19),
	d(2025, 7, 4),
	d(2025, 9, 1),
	d(2025, 11, 27),
	d(2025, 12, 25),
	// 2026
	d(2026, 1, 1),
	d(2026, 1, 19),
	d(2026, 2, 16),
	d(2026, 4, 3),
	d(2026, 5, 25),
	d(2026, 6, 19),
	d(2026, 7, 3),
	d(2026, 9, 7),
	d(2026, 11, 26),
	d(2026, 12, 25),
}

func d(y, m, day int) time.Time {
	return time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC)
}
