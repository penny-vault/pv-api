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

package alert

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

var ErrInvalidUnsubscribeToken = errors.New("invalid unsubscribe token")

// GenerateUnsubscribeToken produces a URL-safe token encoding alertID and
// recipient, signed with HMAC-SHA256 using secret.
// Format: base64url(alertID:recipient) + "." + base64url(hmac)
func GenerateUnsubscribeToken(secret string, alertID uuid.UUID, recipient string) (string, error) {
	payload := alertID.String() + ":" + recipient
	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write([]byte(payload)); err != nil {
		return "", fmt.Errorf("generate unsubscribe token: %w", err)
	}
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	body := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return body + "." + sig, nil
}

// VerifyUnsubscribeToken validates the token and returns alertID and recipient.
func VerifyUnsubscribeToken(secret, token string) (uuid.UUID, string, error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return uuid.UUID{}, "", ErrInvalidUnsubscribeToken
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return uuid.UUID{}, "", ErrInvalidUnsubscribeToken
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return uuid.UUID{}, "", ErrInvalidUnsubscribeToken
	}
	payload := string(payloadBytes)
	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write([]byte(payload)); err != nil {
		return uuid.UUID{}, "", ErrInvalidUnsubscribeToken
	}
	expected := mac.Sum(nil)
	if !hmac.Equal(sigBytes, expected) {
		return uuid.UUID{}, "", ErrInvalidUnsubscribeToken
	}
	idx := strings.Index(payload, ":")
	if idx < 0 {
		return uuid.UUID{}, "", ErrInvalidUnsubscribeToken
	}
	id, err := uuid.Parse(payload[:idx])
	if err != nil {
		return uuid.UUID{}, "", ErrInvalidUnsubscribeToken
	}
	return id, payload[idx+1:], nil
}
