// Copyright 2021-2024
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
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/penny-vault/pv-api/types"
	"github.com/rs/zerolog/log"
)

var (
	jwkCache *jwk.Cache
	jwksUrl  string
)

func configureJWKCache(ctx context.Context, conf Config) {
	// Get JWK and keep it up-to-date
	// life-cycle of cache depends upon ctx
	var err error

	jwkCache, err = jwk.NewCache(ctx, httprc.NewClient())
	if err != nil {
		log.Panic().Err(err).Msg("failed to create JWK cache")
	}

	jwksUrl = conf.JwksUrl

	if jwksUrl == "" {
		log.Panic().Msg("jwks url must not be blank")
	}

	// Tell *jwk.Cache that we only want to refresh this JWKS periodically.
	if err := jwkCache.Register(ctx, conf.JwksUrl); err != nil {
		log.Panic().Err(err).Str("jwks_url", conf.JwksUrl).Msg("failed to register jwks url with jwk caching service")
	}
}

// hasRole checks if the user has the specified role; if it does not then return
// a 403 error
func hasRole(role string) func(*fiber.Ctx) error {
	return func(c *fiber.Ctx) error {
		subject := c.Locals(types.UserKey{}).(string)
		token := c.Locals(types.JwtKey{}).(string)
		userInfo := LookupUserInfo(subject, token)

		if _, containsRole := userInfo.Roles[role]; containsRole {
			return c.Next()
		}

		return c.Status(fiber.StatusForbidden).SendString("User does not possess required role")
	}
}

// hasAuth extracts a JWT token from the request and verifies its contents
// tokens may be specified via the Authorization: Bearer ... header, a cookie,
// or a query param
func hasAuth() fiber.Handler {
	// Return middleware handler
	return func(c *fiber.Ctx) error {
		var err error

		authToken := jwtFromHeader(c, fiber.HeaderAuthorization, "Bearer")

		if authToken == "" {
			authToken = jwtFromQuery(c, "token")
		}

		if authToken == "" {
			authToken = jwtFromCookie(c, "token")
		}

		// if token remains unset return an error
		if authToken == "" {
			return c.Status(fiber.StatusUnauthorized).SendString("Missing or malformed JWT")
		}

		ctx, cancel := context.WithCancel(c.UserContext())
		defer cancel()

		keySet, err := jwkCache.Lookup(ctx, jwksUrl)
		if err != nil {
			log.Panic().Err(err).Str("jwks_url", jwksUrl).Msg("failed to lookup jwks url")
		}

		token, err := jwt.Parse([]byte(authToken), jwt.WithKeySet(keySet))
		if err == nil {
			// Store user information in the locals context
			c.Locals(types.JwtKey{}, authToken)

			subject, _ := token.Subject()
			c.Locals(types.UserKey{}, subject)

			return c.Next()
		}

		return c.Status(fiber.StatusForbidden).SendString("Invalid or expired JWT")
	}
}

// jwtFromHeader returns a JWT from the specified header and auth scheme
func jwtFromHeader(c *fiber.Ctx, header string, authScheme string) string {
	auth := c.Get(header)
	l := len(authScheme)

	if len(auth) > l+1 && strings.EqualFold(auth[:l], authScheme) {
		return auth[l+1:]
	}

	return ""
}

// jwtFromQuery extracts a JWT from the request query using the given key
func jwtFromQuery(c *fiber.Ctx, key string) string {
	token := c.Query(key)

	if token == "" {
		return ""
	}

	return token
}

// jwtFromCookie extracts a JWT token from the given cookie
func jwtFromCookie(c *fiber.Ctx, name string) string {
	token := c.Cookies(name)

	if token == "" {
		return ""
	}

	return token
}
