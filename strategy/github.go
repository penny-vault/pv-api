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

package strategy

import (
	"context"
	"fmt"
	"os"
	"time"

	pvbtlib "github.com/penny-vault/pvbt/library"
)

// DiscoverOptions configures DiscoverOfficial.
type DiscoverOptions struct {
	// CacheDir is where pvbt/library caches the GitHub response.
	// Pass GinkgoT().TempDir() in tests; a pvapi-managed directory in prod.
	CacheDir string
	// ExpectOwner keeps only listings whose Owner.Login matches. For the
	// official registry this is "penny-vault".
	ExpectOwner string
	// Token is an optional GitHub API token. If empty, pvbt/library's own
	// GITHUB_TOKEN env lookup falls back to unauthenticated.
	Token string
	// ForceRefresh bypasses pvbt/library's 1h file cache.
	ForceRefresh bool
}

// DiscoverOfficial calls pvbt/library.Search and filters to the expected
// owner. The token, if any, is exported as GITHUB_TOKEN for the duration of
// the call (pvbt/library resolves it from that env var).
func DiscoverOfficial(ctx context.Context, opts DiscoverOptions) ([]Listing, error) {
	if opts.Token != "" {
		prev, had := os.LookupEnv("GITHUB_TOKEN")
		if err := os.Setenv("GITHUB_TOKEN", opts.Token); err != nil {
			return nil, fmt.Errorf("setting GITHUB_TOKEN: %w", err)
		}
		defer func() {
			if had {
				_ = os.Setenv("GITHUB_TOKEN", prev)
			} else {
				_ = os.Unsetenv("GITHUB_TOKEN")
			}
		}()
	}

	raw, err := pvbtlib.Search(ctx, pvbtlib.SearchOptions{
		CacheDir:     opts.CacheDir,
		BaseURL:      "https://api.github.com",
		ForceRefresh: opts.ForceRefresh,
	})
	if err != nil {
		return nil, fmt.Errorf("pvbt search: %w", err)
	}

	out := make([]Listing, 0, len(raw))
	for _, r := range raw {
		if opts.ExpectOwner != "" && r.Owner != opts.ExpectOwner {
			continue
		}
		updated, _ := time.Parse(time.RFC3339, r.UpdatedAt)
		out = append(out, Listing{
			Name:        r.Name,
			Owner:       r.Owner,
			Description: r.Description,
			Categories:  r.Categories,
			CloneURL:    r.CloneURL,
			Stars:       r.Stars,
			UpdatedAt:   updated,
		})
	}
	return out, nil
}
