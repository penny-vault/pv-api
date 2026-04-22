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
	"encoding/json"
	"fmt"
	"hash/fnv"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"github.com/penny-vault/pv-api/strategy"
)

// base32 alphabet matching RFC 4648 lowercase. 32 chars -> 5 bits per char.
const base32Alphabet = "abcdefghijklmnopqrstuvwxyz234567"

// Slug returns the deterministic slug for a create request:
//
//	<short_code>-<preset_or_custom>-<4char>
//
// The preset segment is the matched preset's name (kebab-case normalized)
// when req.Parameters deeply equals a preset's parameters. Otherwise it's
// the literal "custom". The 4-char suffix is the lower 20 bits of an
// FNV-1a 32-bit hash over (canonical parameters JSON || mode || schedule
// || benchmark), encoded in base32 lowercase.
func Slug(req CreateRequest, d strategy.Describe) (string, error) {
	preset := "custom"
	for _, p := range d.Presets {
		if presetParametersEqual(p.Parameters, req.Parameters) {
			preset = kebabCase(p.Name)
			break
		}
	}

	canon, err := canonicalJSON(req.Parameters)
	if err != nil {
		return "", fmt.Errorf("canonicalizing parameters: %w", err)
	}

	// hash.Hash.Write is documented never to return an error, so the
	// writes below are safe to ignore.
	h := fnv.New32a()
	_, _ = h.Write(canon)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(req.Benchmark))

	sum := h.Sum32() & 0xFFFFF // low 20 bits

	suffix := make([]byte, 4)
	for i := 3; i >= 0; i-- {
		suffix[i] = base32Alphabet[sum&0x1F]
		sum >>= 5
	}

	return fmt.Sprintf("%s-%s-%s", req.StrategyCode, preset, suffix), nil
}

// presetParametersEqual reports whether two parameter maps deep-equal,
// treating them as canonical JSON (so key order does not matter and
// numeric types like int vs float64 compare equal when they encode the
// same).
func presetParametersEqual(a, b map[string]any) bool {
	aj, err := canonicalJSON(a)
	if err != nil {
		return false
	}
	bj, err := canonicalJSON(b)
	if err != nil {
		return false
	}
	if string(aj) != string(bj) {
		return false
	}
	var ad, bd any
	if err := json.Unmarshal(aj, &ad); err != nil {
		return false
	}
	if err := json.Unmarshal(bj, &bd); err != nil {
		return false
	}
	return reflect.DeepEqual(ad, bd)
}

// canonicalJSON marshals v with object keys sorted at every depth so that
// the output is stable regardless of map iteration order.
func canonicalJSON(v any) ([]byte, error) {
	normalized := canonicalize(v)
	return json.Marshal(normalized)
}

// canonicalize returns an equivalent tree where every map has been
// replaced by a structure with keys sorted.
func canonicalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(x))
		for _, k := range keys {
			out[k] = canonicalize(x[k])
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = canonicalize(e)
		}
		return out
	default:
		return v
	}
}

// kebabCase lowercases s, replaces any run of non-alphanumeric runes with a
// single `-`, and trims leading/trailing `-`.
func kebabCase(s string) string {
	var b strings.Builder
	lastHyphen := true
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			lastHyphen = false
		default:
			if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
