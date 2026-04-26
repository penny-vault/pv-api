package email

import (
	"bytes"
	"encoding/base64"
	_ "embed"
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
	Ticker      string
	Action      string
	ActionColor string
	Shares      string
	Value       string
}

type HoldingRow struct {
	Ticker    string
	Shares    string // formatted with commas; "—" for $CASH
	WeightPct string
	Value     string
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

	Benchmark         string
	BenchmarkDeltaPct string
	RelativeDelta     string
	RelativeColor     string

	DayChangePct   string
	DayChangeColor string
	WtdPct         string
	WtdColor       string
	MtdPct         string
	MtdColor       string
	YtdPct         string
	YtdColor       string
	OneYearPct     string
	OneYearColor   string

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

func FormatDelta(currentValue, previousValue float64, lastSentAt time.Time) (deltaPct, deltaAbs, color, since string, hasDelta bool) {
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
	since = lastSentAt.Format("Monday")
	if time.Since(lastSentAt) > 7*24*time.Hour {
		since = lastSentAt.Format("Jan 2")
	}
	return deltaPct, deltaAbs, color, since, true
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

func buildPlaintext(p Payload) string {
	var b strings.Builder
	b.WriteString(p.PortfolioName + " — " + p.RunDate + "\n\n")
	if !p.Success {
		b.WriteString("ERROR: " + p.ErrorMessage + "\n")
		return b.String()
	}
	b.WriteString("Portfolio Value: " + p.CurrentValue + "\n")
	if p.DayChangePct != "" {
		fmt.Fprintf(&b, "Day: %s  WTD: %s  MTD: %s  YTD: %s  1Y: %s\n\n",
			p.DayChangePct, p.WtdPct, p.MtdPct, p.YtdPct, p.OneYearPct)
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
