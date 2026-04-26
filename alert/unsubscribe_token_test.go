package alert_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/penny-vault/pv-api/alert"
)

func TestUnsubscribeTokenRoundtrip(t *testing.T) {
	secret := "test-secret-32-bytes-long-enough!"
	alertID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	recipient := "user@example.com"

	tok, err := alert.GenerateUnsubscribeToken(secret, alertID, recipient)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	gotID, gotRecipient, err := alert.VerifyUnsubscribeToken(secret, tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if gotID != alertID {
		t.Errorf("alert ID: got %v want %v", gotID, alertID)
	}
	if gotRecipient != recipient {
		t.Errorf("recipient: got %q want %q", gotRecipient, recipient)
	}
}

func TestUnsubscribeTokenWrongSecret(t *testing.T) {
	alertID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	tok, _ := alert.GenerateUnsubscribeToken("secret-a", alertID, "user@example.com")
	_, _, err := alert.VerifyUnsubscribeToken("secret-b", tok)
	if err == nil {
		t.Fatal("expected error for wrong secret, got nil")
	}
}
