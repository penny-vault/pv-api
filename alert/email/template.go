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

package email

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"fmt"
	"math"
	"strings"
	"text/template"
	"time"
)

//go:embed assets/logo-80.jpg
var logoBytes []byte

var logoDataURL string

func init() {
	logoDataURL = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(logoBytes)
}

//go:embed templates/success.html
var successHTML string

//go:embed templates/failure.html
var failureHTML string

var (
	successTmpl = template.Must(template.New("success").Parse(successHTML))
	failureTmpl = template.Must(template.New("failure").Parse(failureHTML))
)

type TradeRow struct {
	Ticker        string
	Action        string
	ActionColor   string
	ActionBgColor string
	Shares        string
	Value         string
}

type HoldingRow struct {
	Ticker      string
	TickerColor string
	Shares      string // formatted with commas; "—" for $CASH
	WeightPct   string
	Value       string
}

// ReturnCell is one formatted return percentage and the color it renders in.
// Pct is "—" when the snapshot has no value for that window.
type ReturnCell struct {
	Pct   string
	Color string
}

// ReturnsRow is one labeled line of the returns comparison grid: the
// portfolio's returns, or the benchmark's, across the standard windows.
type ReturnsRow struct {
	Label   string
	Day     ReturnCell
	Wtd     ReturnCell
	Mtd     ReturnCell
	Ytd     ReturnCell
	OneYear ReturnCell
}

type Payload struct {
	PortfolioName string
	StrategyCode  string
	RunDate       string
	Success       bool

	LogoDataURL string

	CurrentValue string
	HasDelta     bool
	DeltaPct     string
	DeltaAbs     string
	SinceLabel   string
	DeltaColor   string

	// Returns is the comparison grid: row 0 is the portfolio, row 1 (when
	// present) is the benchmark. Each row spans Day/WTD/MTD/YTD/1Y.
	Returns []ReturnsRow

	Trades   []TradeRow
	Holdings []HoldingRow

	PortfolioURL   string
	UnsubscribeURL string

	ErrorMessage   string
	LastKnownValue string
}

func Render(p Payload) (string, string, error) {
	tmpl := successTmpl
	if !p.Success {
		tmpl = failureTmpl
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, p); err != nil {
		return "", "", fmt.Errorf("render email template: %w", err)
	}
	return buf.String(), buildPlaintext(p), nil
}

// FormatDelta formats a value change since lastSentAt, relative to now. Both
// times are interpreted in their own locations for the day-difference calc, so
// callers should pass them already converted to the display timezone.
func FormatDelta(currentValue, previousValue float64, lastSentAt, now time.Time) (deltaPct, deltaAbs, color, since string, hasDelta bool) {
	if previousValue <= 0 {
		return "", "", "", "", false
	}
	diff := currentValue - previousValue
	pct := diff / previousValue * 100
	sign := "+"
	if diff < 0 {
		sign = "-"
		diff = -diff
		pct = math.Abs(pct)
	}
	deltaPct = fmt.Sprintf("%s%.1f%%", sign, pct)
	deltaAbs = fmt.Sprintf("%s$%s", sign, FormatMoneyVal(diff))
	if sign == "+" {
		color = "#22c55e"
	} else {
		color = "#ef4444"
	}
	since = formatSinceLabel(lastSentAt, now)
	return deltaPct, deltaAbs, color, since, true
}

func formatSinceLabel(lastSentAt, now time.Time) string {
	sentDay := time.Date(lastSentAt.Year(), lastSentAt.Month(), lastSentAt.Day(), 0, 0, 0, 0, lastSentAt.Location())
	nowDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	diffDays := int(nowDay.Sub(sentDay).Hours() / 24)
	switch {
	case diffDays <= 0:
		return "earlier today"
	case diffDays == 1:
		return "yesterday"
	case diffDays >= 2 && diffDays <= 6 && lastSentAt.Weekday() != now.Weekday():
		return lastSentAt.Format("Monday")
	default:
		return lastSentAt.Format("Mon, Jan 2")
	}
}

func FormatMoneyVal(v float64) string {
	s := fmt.Sprintf("%.0f", v)
	if len(s) <= 3 {
		return s
	}
	var result strings.Builder
	offset := len(s) % 3
	if offset > 0 {
		result.WriteString(s[:offset])
	}
	for i := offset; i < len(s); i += 3 {
		if i > 0 {
			result.WriteByte(',')
		}
		result.WriteString(s[i : i+3])
	}
	return result.String()
}

// FormatReturnPct formats a fractional return (e.g. 0.034) as "+3.4%" and
// returns the appropriate color string for light-mode rendering. A value that
// rounds to zero renders as "0.0%" with no sign and a neutral color, so a tiny
// move isn't dressed up as a gain or a loss.
func FormatReturnPct(v float64) (pct, color string) {
	rounded := math.Round(v*1000) / 10 // percent, one decimal place
	if rounded == 0 {
		return "0.0%", "#94a3b8"
	}
	sign := "+"
	if rounded < 0 {
		sign = "-"
		rounded = -rounded
	}
	pct = fmt.Sprintf("%s%.1f%%", sign, rounded)
	if sign == "+" {
		color = "#16a34a"
	} else {
		color = "#dc2626"
	}
	return
}

// LogoDataURL returns the embedded logo as a data URL string.
func LogoDataURL() string { return logoDataURL }

func buildPlaintext(p Payload) string {
	var b strings.Builder
	b.WriteString(p.PortfolioName + " — " + p.RunDate + "\n\n")
	if !p.Success {
		b.WriteString("ERROR: " + p.ErrorMessage + "\n")
		return b.String()
	}
	b.WriteString("Portfolio Value: " + p.CurrentValue + "\n")
	if p.HasDelta {
		fmt.Fprintf(&b, "Change: %s (%s) since %s\n", p.DeltaPct, p.DeltaAbs, p.SinceLabel)
	}
	b.WriteString("\n")
	if len(p.Returns) > 0 {
		fmt.Fprintf(&b, "%-12s %7s %7s %7s %7s %7s\n", "", "DAY", "WTD", "MTD", "YTD", "1Y")
		for _, row := range p.Returns {
			fmt.Fprintf(&b, "%-12s %7s %7s %7s %7s %7s\n",
				row.Label, row.Day.Pct, row.Wtd.Pct, row.Mtd.Pct, row.Ytd.Pct, row.OneYear.Pct)
		}
		b.WriteString("\n")
	}
	if len(p.Trades) == 0 {
		b.WriteString("No trades required.\n\n")
	} else {
		b.WriteString("Trades to Execute:\n")
		for _, tr := range p.Trades {
			fmt.Fprintf(&b, "  %s %s %s shares (~%s)\n", tr.Action, tr.Ticker, tr.Shares, tr.Value)
		}
		b.WriteString("\n")
	}
	if len(p.Holdings) > 0 {
		b.WriteString("Current Holdings:\n")
		for _, h := range p.Holdings {
			fmt.Fprintf(&b, "  %s  %s shares  %s  %s%%\n", h.Ticker, h.Shares, h.Value, h.WeightPct)
		}
		b.WriteString("\n")
	}
	if p.PortfolioURL != "" {
		b.WriteString("View portfolio: " + p.PortfolioURL + "\n")
	}
	if p.UnsubscribeURL != "" {
		b.WriteString("Unsubscribe: " + p.UnsubscribeURL + "\n")
	}
	return b.String()
}
