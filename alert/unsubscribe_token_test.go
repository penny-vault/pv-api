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
