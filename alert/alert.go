package alert

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const (
	FrequencyScheduledRun = "scheduled_run"
	FrequencyDaily        = "daily"
	FrequencyWeekly       = "weekly"
	FrequencyMonthly      = "monthly"
)

type Alert struct {
	ID            uuid.UUID
	PortfolioID   uuid.UUID
	Frequency     string
	Recipients    []string
	LastSentAt    *time.Time
	LastSentValue *float64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Notifier is called by the backtest orchestrator after each run completes.
type Notifier interface {
	NotifyRunComplete(ctx context.Context, portfolioID, runID uuid.UUID, success bool) error
}
