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
