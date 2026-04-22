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

package backtest

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// BuildArgs converts a portfolio's parameter map, benchmark, and optional date
// window into the strategy-binary CLI flags. Returns a flat []string suitable
// for appending to the "backtest --output <path>" base command.
//
// Order: parameter keys sorted ascending; --start (if set); --end (if set);
// --benchmark last (if non-empty).
func BuildArgs(params map[string]any, benchmark string, startDate, endDate *time.Time) []string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]string, 0, 2*len(keys)+6)
	for _, k := range keys {
		out = append(out, "--"+toKebab(k), stringify(params[k]))
	}
	if startDate != nil {
		out = append(out, "--start", startDate.Format("2006-01-02"))
	}
	if endDate != nil {
		out = append(out, "--end", endDate.Format("2006-01-02"))
	}
	if benchmark != "" {
		out = append(out, "--benchmark", benchmark)
	}
	return out
}

// toKebab converts camelCase or snake_case to kebab-case.
func toKebab(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == '_':
			b.WriteRune('-')
		case unicode.IsUpper(r):
			if i > 0 {
				b.WriteRune('-')
			}
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func stringify(v any) string {
	switch vv := v.(type) {
	case string:
		return vv
	case []any:
		parts := make([]string, len(vv))
		for i, e := range vv {
			parts[i] = stringify(e)
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprintf("%v", vv)
	}
}
