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

package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/penny-vault/pv-api/types"
)

// AuthConfig captures the Auth0 settings the JWT middleware needs.
type AuthConfig struct {
	JWKSURL  string
	Audience string
	Issuer   string
}

// ErrInvalidToken is returned when a JWT is missing, malformed, or fails
// verification. The middleware converts it to a 401 problem+json via
// WriteProblem.
var ErrInvalidToken = errors.New("invalid or expired token")

// NewAuthMiddleware builds a Fiber v3 handler that verifies the
// Authorization: Bearer <jwt> header on every request and stores the
// subject on types.AuthSubjectKey. ctx controls the JWK cache lifecycle.
func NewAuthMiddleware(ctx context.Context, conf AuthConfig) (fiber.Handler, error) {
	if conf.JWKSURL == "" {
		return nil, errors.New("AuthConfig.JWKSURL must not be empty")
	}
	if conf.Audience == "" {
		return nil, errors.New("AuthConfig.Audience must not be empty")
	}
	if conf.Issuer == "" {
		return nil, errors.New("AuthConfig.Issuer must not be empty")
	}

	cache, err := jwk.NewCache(ctx, httprc.NewClient())
	if err != nil {
		return nil, fmt.Errorf("creating JWK cache: %w", err)
	}
	if err := cache.Register(ctx, conf.JWKSURL); err != nil {
		return nil, fmt.Errorf("registering JWKS URL: %w", err)
	}

	return func(c fiber.Ctx) error {
		token := bearerToken(c)
		if token == "" {
			return WriteProblem(c, fmt.Errorf("missing bearer token: %w", ErrInvalidToken))
		}

		keyset, err := cache.Lookup(c.Context(), conf.JWKSURL)
		if err != nil {
			return WriteProblem(c, fmt.Errorf("JWKS lookup failed: %w", err))
		}

		parsed, err := jwt.Parse([]byte(token),
			jwt.WithKeySet(keyset),
			jwt.WithIssuer(conf.Issuer),
			jwt.WithAudience(conf.Audience),
			jwt.WithValidate(true),
		)
		if err != nil {
			return WriteProblem(c, fmt.Errorf("%w: %v", ErrInvalidToken, err))
		}

		sub, ok := parsed.Subject()
		if !ok || sub == "" {
			return WriteProblem(c, fmt.Errorf("missing sub claim: %w", ErrInvalidToken))
		}

		c.Locals(types.AuthSubjectKey{}, sub)
		return c.Next()
	}, nil
}

// bearerToken returns the JWT from an `Authorization: Bearer <...>` header,
// or "" if the header is absent or not a bearer scheme.
func bearerToken(c fiber.Ctx) string {
	h := c.Get(fiber.HeaderAuthorization)
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return h[len(prefix):]
}
