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

package portfolio

import (
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v3"
)

// HoldingsImpact returns per-ticker contribution to portfolio return across
// canonical periods (YTD, 1Y, 3Y, 5Y, inception).
func (h *Handler) HoldingsImpact(c fiber.Ctx) error {
	slug := string([]byte(c.Params("slug")))
	topN := parseTopN(c.Query("top"))
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		resp, err := r.HoldingsImpact(c.Context(), slug, topN)
		if errors.Is(err, ErrSnapshotNotFound) {
			return nil, errNotFoundSentinel
		}
		return resp, err
	})
}

// parseTopN returns a clamped integer; invalid or empty input yields the default 10.
func parseTopN(raw string) int {
	if raw == "" {
		return 10
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 10
	}
	if n > 50 {
		return 50
	}
	return n
}
