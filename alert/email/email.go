package email

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type Config struct {
	Domain      string
	APIKey      string
	FromAddress string
}

// Send delivers an email via the Mailgun HTTP API.
// Returns nil without sending if cfg.APIKey is empty.
func Send(ctx context.Context, cfg Config, recipients []string, subject, htmlBody, textBody string) error {
	if cfg.APIKey == "" {
		return nil
	}
	endpoint := fmt.Sprintf("https://api.mailgun.net/v3/%s/messages", url.PathEscape(cfg.Domain))

	form := url.Values{}
	form.Set("from", cfg.FromAddress)
	form.Set("subject", subject)
	form.Set("html", htmlBody)
	form.Set("text", textBody)
	for _, r := range recipients {
		form.Add("to", r)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("mailgun: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("api:"+cfg.APIKey)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("mailgun: send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mailgun: unexpected status %d", resp.StatusCode)
	}
	return nil
}
