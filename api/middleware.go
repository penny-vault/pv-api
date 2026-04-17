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
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/penny-vault/pv-api/types"
	"github.com/rs/zerolog/log"
)

const requestIDHeader = "X-Request-Id"

// requestIDMiddleware stores a UUIDv7 (or inbound header value) on the
// fiber context locals and mirrors it on the response.
func requestIDMiddleware() fiber.Handler {
	return func(c fiber.Ctx) error {
		rid := c.Get(requestIDHeader)
		if rid == "" {
			rid = uuid.Must(uuid.NewV7()).String()
		}

		c.Locals(types.RequestIDKey{}, rid)
		c.Set(requestIDHeader, rid)

		return c.Next()
	}
}

// timerMiddleware records the handler duration on a Server-Timing header.
// Per RFC 8942, the `dur` parameter is a bare decimal in milliseconds.
func timerMiddleware() fiber.Handler {
	return func(c fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		elapsed := time.Since(start)
		ms := float64(elapsed.Nanoseconds()) / 1e6
		c.Append("Server-Timing", fmt.Sprintf("app;dur=%.3f", ms))
		return err
	}
}

// loggerMiddleware emits one zerolog line per request, annotated with
// the request id, status, method, path, and handler duration.
func loggerMiddleware() fiber.Handler {
	return func(c fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		elapsed := time.Since(start)

		rid, _ := c.Locals(types.RequestIDKey{}).(string)
		status := c.Response().StatusCode()

		entry := log.With().
			Str("request_id", rid).
			Int("status", status).
			Str("method", c.Method()).
			Str("path", c.Path()).
			Dur("duration", elapsed).
			Logger()

		switch {
		case status >= fiber.StatusInternalServerError:
			entry.Error().Msg("request")
		case status >= fiber.StatusBadRequest:
			entry.Warn().Msg("request")
		default:
			entry.Info().Msg("request")
		}

		return err
	}
}
