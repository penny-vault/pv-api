package email_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/jarcoal/httpmock"

	"github.com/penny-vault/pv-api/alert/email"
)

func TestSend(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()

	httpmock.RegisterResponder(http.MethodPost,
		"https://api.mailgun.net/v3/mg.pennyvault.com/messages",
		httpmock.NewStringResponder(200, `{"id":"<abc>","message":"Queued"}`),
	)

	cfg := email.Config{
		Domain:      "mg.pennyvault.com",
		APIKey:      "key-test",
		FromAddress: "Penny Vault <no-reply@mg.pennyvault.com>",
	}
	err := email.Send(context.Background(), cfg,
		[]string{"user@example.com"},
		"Test Subject", "<p>hello</p>", "hello",
	)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if count := httpmock.GetTotalCallCount(); count != 1 {
		t.Errorf("expected 1 HTTP call, got %d", count)
	}
}

func TestSendSkipsWhenNoAPIKey(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()

	cfg := email.Config{Domain: "mg.pennyvault.com", APIKey: ""}
	err := email.Send(context.Background(), cfg, []string{"user@example.com"}, "subj", "<p>h</p>", "h")
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if count := httpmock.GetTotalCallCount(); count != 0 {
		t.Errorf("expected 0 HTTP calls, got %d", count)
	}
}
