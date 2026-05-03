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
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/rs/zerolog/log"
)

// ErrUnexpectedStatus is returned by Send when Mailgun replies with a
// non-2xx HTTP status. The response status code is included in the wrapping
// error message produced by fmt.Errorf("...: %w", ErrUnexpectedStatus).
var ErrUnexpectedStatus = errors.New("mailgun: unexpected status")

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
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Warn().Err(err).Msg("mailgun: response body close")
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: %d", ErrUnexpectedStatus, resp.StatusCode)
	}
	return nil
}
