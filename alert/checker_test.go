package alert

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/penny-vault/pv-api/alert/email"
)

func TestSendSummaryNoAPIKey(t *testing.T) {
	c := &Checker{emailConfig: email.Config{}}
	err := c.SendSummary(context.Background(), uuid.New(), "user@example.com")
	if !errors.Is(err, ErrEmailNotConfigured) {
		t.Fatalf("expected ErrEmailNotConfigured, got %v", err)
	}
}
