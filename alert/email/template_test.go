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

package email_test

import (
	"strings"
	"testing"
	"time"

	"github.com/penny-vault/pv-api/alert/email"
)

func successPayload() email.Payload {
	return email.Payload{
		PortfolioName:     "My Portfolio",
		StrategyCode:      "rsi-mean-reversion",
		RunDate:           "Monday, April 21, 2026",
		Success:           true,
		CurrentValue:      "$103,240",
		HasDelta:          true,
		DeltaPct:          "+12.0%",
		DeltaAbs:          "+$1,240",
		SinceLabel:        "Tuesday",
		DeltaColor:        "#22c55e",
		Benchmark:         "SPY",
		BenchmarkDeltaPct: "+10.8%",
		RelativeDelta:     "+1.2%",
		RelativeColor:     "#22c55e",
		Trades: []email.TradeRow{
			{Ticker: "VTI", Action: "Buy", ActionColor: "#22c55e", Shares: "12", Value: "$2,400"},
			{Ticker: "BND", Action: "Sell", ActionColor: "#ef4444", Shares: "5", Value: "$450"},
		},
		Holdings: []email.HoldingRow{
			{Ticker: "VTI", WeightPct: "90.0", Value: "$92,916"},
			{Ticker: "$CASH", WeightPct: "10.0", Value: "$10,324"},
		},
	}
}

func TestRenderSuccess(t *testing.T) {
	p := successPayload()
	html, text, err := email.Render(p)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	checks := []string{
		"My Portfolio",
		"$103,240",
		"+12.0%",
		"+$1,240",
		"since Tuesday",
		"SPY",
		"+10.8%",
		"VTI",
		"Buy",
		"BND",
		"Sell",
		"90.0%",
	}
	for _, want := range checks {
		if !strings.Contains(html, want) {
			t.Errorf("success HTML missing %q", want)
		}
		if !strings.Contains(text, want) {
			t.Errorf("success text missing %q", want)
		}
	}
}

func TestRenderFailure(t *testing.T) {
	p := email.Payload{
		PortfolioName:  "My Portfolio",
		StrategyCode:   "rsi-mean-reversion",
		RunDate:        "Monday, April 21, 2026",
		Success:        false,
		LastKnownValue: "$101,000",
		ErrorMessage:   "strategy binary exited with code 1",
	}
	html, text, err := email.Render(p)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{"My Portfolio", "Error", "strategy binary exited with code 1", "$101,000"} {
		if !strings.Contains(html, want) {
			t.Errorf("failure HTML missing %q", want)
		}
	}
	if !strings.Contains(text, "ERROR:") {
		t.Error("failure plaintext missing ERROR: prefix")
	}
}

func TestFormatDelta(t *testing.T) {
	lastSent := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)

	pct, abs, color, since, hasDelta := email.FormatDelta(103240, 102000, lastSent, now)
	if !hasDelta {
		t.Fatal("expected hasDelta=true")
	}
	if !strings.HasPrefix(pct, "+") {
		t.Errorf("pct = %q; want positive", pct)
	}
	if color != "#22c55e" {
		t.Errorf("color = %q; want green", color)
	}
	_ = abs
	_ = since

	pct2, _, color2, _, _ := email.FormatDelta(99000, 102000, lastSent, now)
	if !strings.HasPrefix(pct2, "-") {
		t.Errorf("pct = %q; want negative", pct2)
	}
	if color2 != "#ef4444" {
		t.Errorf("color = %q; want red", color2)
	}
}

func TestFormatDeltaSinceLabel(t *testing.T) {
	// Anchor "now" to a known weekday: 2026-04-23 is a Thursday.
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		lastSent time.Time
		want     string
	}{
		{"same day", time.Date(2026, 4, 23, 8, 0, 0, 0, time.UTC), "earlier today"},
		{"yesterday", time.Date(2026, 4, 22, 8, 0, 0, 0, time.UTC), "yesterday"},
		{"two days ago weekday", time.Date(2026, 4, 21, 8, 0, 0, 0, time.UTC), "Tuesday"},
		{"six days ago weekday", time.Date(2026, 4, 17, 8, 0, 0, 0, time.UTC), "Friday"},
		{"exactly seven days ago — same weekday, falls back to date", time.Date(2026, 4, 16, 8, 0, 0, 0, time.UTC), "Thu, Apr 16"},
		{"two weeks ago", time.Date(2026, 4, 9, 8, 0, 0, 0, time.UTC), "Thu, Apr 9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, since, _ := email.FormatDelta(103000, 100000, tc.lastSent, now)
			if since != tc.want {
				t.Errorf("since = %q; want %q", since, tc.want)
			}
		})
	}
}
