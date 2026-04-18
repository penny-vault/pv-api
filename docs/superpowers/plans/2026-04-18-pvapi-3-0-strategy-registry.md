# pvapi 3.0 Strategy Registry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the full pvapi 3.0 strategy registry: discover official strategies from GitHub via `pvbt/library.Search`, version-pinned clone+build+describe install lifecycle with a background sync goroutine, retry-on-upstream-change failure handling, real `GET /strategies` and `GET /strategies/{shortCode}` endpoints, all behind Auth0 JWT.

**Architecture:**
- `pvbt/library.Search` handles GitHub discovery + its own file cache; pvapi filters results to `Owner == "penny-vault"` and feeds them to a reconciler.
- A version-pinned install routine (written in this plan, not borrowed from `pvbt/library.Install`) does `git clone --branch <ver> --depth 1 + go build . + <bin> describe --json`, then atomically flips `strategies.installed_ver` / `artifact_ref` / `describe_json` on success.
- A sync goroutine started by `api.NewApp` runs on `strategy.registry_sync_interval` (default 1h). First tick fires immediately after server start (not blocking). Installs run on a bounded worker pool (default 2).
- Install failure records `install_error` + `last_attempted_ver`. Next sync tick compares remote HEAD to `last_attempted_ver`; no change = skip (broken repo stops hammering on its own).
- Tests use a bare git repo built from fixture files in `testdata/` at test-time; `httpmock` stubs GitHub Search. No `//go:build integration` tag — `git` and `go` are already on PATH in CI.
- `POST /strategies` stays 501 throughout Plan 3 (unofficial strategies are Plan 7).

**Tech Stack:** Go 1.25+, Fiber v3, `github.com/penny-vault/pvbt` (new direct dep), `github.com/jackc/pgx/v5/pgxpool`, `github.com/bytedance/sonic`, `github.com/jarcoal/httpmock` (new test dep), Ginkgo/Gomega.

**Reference spec:** `docs/superpowers/specs/2026-04-16-pvapi-3-0-design.md`

**Worktree:** branch from `main`.

```bash
cd /Users/jdf/Developer/penny-vault/pv-api
git worktree add .worktrees/pvapi-3-strategy-registry -b pvapi-3-strategy-registry main
cd .worktrees/pvapi-3-strategy-registry
```

---

## File overview

**Created**

```
sql/migrations/3_install_tracking.up.sql    -- adds last_attempted_ver + install_error
sql/migrations/3_install_tracking.down.sql
strategy/                                   -- new domain package
  doc.go                                    -- package doc comment
  types.go                                  -- Strategy, Listing, InstallState domain types
  db.go                                     -- pgxpool reads/writes for strategies table
  github.go                                 -- thin wrapper over pvbt/library.Search + penny-vault filter
  install.go                                -- Install(req) pure-ish function (clone/build/describe/validate)
  sync.go                                   -- Syncer type: background goroutine orchestration
  handler.go                                -- implements GET /strategies + GET /strategies/{shortCode}
  strategy_suite_test.go                    -- ginkgo suite runner
  install_test.go                           -- Install against fixture-backed bare repo
  github_test.go                            -- Search + filter via httpmock
  sync_test.go                              -- Tick happy-path + failure-retry behavior
  handler_test.go                           -- response shape + auth integration
  testdata/
    fake-strategy-src/                      -- source files copied into a bare repo by BeforeSuite
      go.mod
      main.go
      describe.json                         -- golden describe output the main.go writes on --json
    github_search_response.json             -- canned GitHub Search API response for httpmock
```

**Modified**

```
go.mod, go.sum                              -- add pvbt, httpmock
cmd/config.go                               -- add githubConf, strategyConf
cmd/server.go                               -- add --github-token, --strategy-* flags
pvapi.toml                                  -- add [github], [strategy] sections
api/server.go                               -- start strategy.Syncer from NewApp; mount real handlers
api/strategies.go                           -- stub replaced by delegations into strategy.Handler
openapi/openapi.yaml                        -- Strategy schema expansion + new sub-schemas
openapi/openapi.gen.go                      -- regenerated
```

**Unchanged**

```
api/auth.go, api/errors.go, api/middleware.go, api/health.go
api/portfolios.go                           -- still 501 stubs; Plan 4 owns
cmd/root.go, cmd/log.go, cmd/version.go, cmd/viper.go
sql/pool.go, sql/migrate.go
types/types.go, pkginfo/
```

---

## Task 1: Add `pvbt` and `httpmock` dependencies

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add pvbt and httpmock**

```bash
go get github.com/penny-vault/pvbt@latest
go get github.com/jarcoal/httpmock@latest
go mod tidy
```

If `pvbt@latest` cannot be fetched (e.g., module is private and the worktree doesn't have credentials), try:

```bash
go get github.com/penny-vault/pvbt@main
```

If that also fails, STOP and report BLOCKED — network setup is a precondition for this plan.

- [ ] **Step 2: Verify build**

```bash
go build ./...
```
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "$(cat <<'EOF'
add pvbt + httpmock direct dependencies

pvbt/library is used for GitHub strategy discovery. httpmock stubs
GitHub Search responses in Plan 3's test suite.
EOF
)"
```

---

## Task 2: Migration `3_install_tracking`

**Files:**
- Create: `sql/migrations/3_install_tracking.up.sql`
- Create: `sql/migrations/3_install_tracking.down.sql`

- [ ] **Step 1: Write the up migration**

Create `sql/migrations/3_install_tracking.up.sql`:

```sql
-- 3_install_tracking.up.sql
-- Adds install-state tracking to the strategies registry. See
-- docs/superpowers/specs/2026-04-16-pvapi-3-0-design.md "Strategy lifecycle".

ALTER TABLE strategies ADD COLUMN last_attempted_ver TEXT;
ALTER TABLE strategies ADD COLUMN install_error TEXT;
```

- [ ] **Step 2: Write the down migration**

Create `sql/migrations/3_install_tracking.down.sql`:

```sql
ALTER TABLE strategies DROP COLUMN IF EXISTS install_error;
ALTER TABLE strategies DROP COLUMN IF EXISTS last_attempted_ver;
```

- [ ] **Step 3: Verify embed picks up the new files**

```bash
go build ./sql/...
```
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add sql/migrations/3_install_tracking.up.sql sql/migrations/3_install_tracking.down.sql
git commit -m "$(cat <<'EOF'
add 3_install_tracking migration

Two new columns on strategies: last_attempted_ver (drives the
"upstream changed" detection on every sync tick) and install_error
(populated on failed installs; cleared on success).
EOF
)"
```

---

## Task 3: Configuration additions

**Files:**
- Modify: `cmd/config.go`
- Modify: `cmd/server.go`
- Modify: `pvapi.toml` (gitignored; local-dev only)

- [ ] **Step 1: Add new config structs**

Read `cmd/config.go` first so you know its current layout. Then replace its contents with:

```go
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

package cmd

import "time"

// Config is the top-level pvapi configuration shape. New sections are added
// as later plans land (runner, scheduler, ...).
type Config struct {
	Log      logConf
	Server   serverConf
	Auth0    auth0Conf
	GitHub   githubConf
	Strategy strategyConf
}

// serverConf holds HTTP server settings.
type serverConf struct {
	Port         int
	AllowOrigins string `mapstructure:"allow_origins"`
}

// auth0Conf configures the JWT-verification middleware.
type auth0Conf struct {
	JWKSURL  string `mapstructure:"jwks_url"`
	Audience string
	Issuer   string
}

// githubConf holds optional GitHub credentials.
type githubConf struct {
	Token string
}

// strategyConf controls the registry sync and install coordinator.
type strategyConf struct {
	RegistrySyncInterval time.Duration `mapstructure:"registry_sync_interval"`
	InstallConcurrency   int           `mapstructure:"install_concurrency"`
	OfficialDir          string        `mapstructure:"official_dir"`
	GithubQuery          string        `mapstructure:"github_query"`
}

var conf Config
```

- [ ] **Step 2: Register flags on `serverCmd`**

Read `cmd/server.go`. Replace the `init()` function with:

```go
func init() {
	rootCmd.AddCommand(serverCmd)

	serverCmd.Flags().Int("server-port", 3000, "port to bind the HTTP server to")
	serverCmd.Flags().String("server-allow-origins", "http://localhost:9000", "single CORS origin to allow; empty disables CORS")
	serverCmd.Flags().String("auth0-jwks-url", "", "Auth0 JWKS URL for JWT verification")
	serverCmd.Flags().String("auth0-audience", "", "Auth0 API audience")
	serverCmd.Flags().String("auth0-issuer", "", "Auth0 issuer URL")
	serverCmd.Flags().String("github-token", "", "GitHub API token; empty uses unauthenticated Search")
	serverCmd.Flags().Duration("strategy-registry-sync-interval", time.Hour, "how often to poll GitHub for strategy updates")
	serverCmd.Flags().Int("strategy-install-concurrency", 2, "maximum concurrent strategy installs")
	serverCmd.Flags().String("strategy-official-dir", "/var/lib/pvapi/strategies/official", "where installed official strategy binaries live")
	serverCmd.Flags().String("strategy-github-query", "owner:penny-vault topic:pvbt-strategy", "GitHub search query for official strategies (owner filter applied client-side)")
	bindPFlagsToViper(serverCmd)
}
```

At the top of `cmd/server.go`, add `"time"` to the imports if it's not already present.

- [ ] **Step 3: Update `pvapi.toml` (gitignored)**

Read `pvapi.toml`. Append the following two sections after `[auth0]` if they don't already exist:

```toml
[github]
token = ""

[strategy]
registry_sync_interval = "1h"
install_concurrency    = 2
official_dir           = "/var/lib/pvapi/strategies/official"
github_query           = "owner:penny-vault topic:pvbt-strategy"
```

`pvapi.toml` is in `.gitignore` — nothing is committed for this step.

- [ ] **Step 4: Verify build**

```bash
go build ./...
```
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add cmd/config.go cmd/server.go
git commit -m "$(cat <<'EOF'
add [github] and [strategy] config sections

Adds optional GitHub token plus the registry sync interval, install
concurrency, official install directory, and GitHub query for the
upcoming strategy registry.
EOF
)"
```

---

## Task 4: OpenAPI Strategy schema expansion

**Files:**
- Modify: `openapi/openapi.yaml`
- Modify: `openapi/openapi.gen.go` (via `make gen`)

- [ ] **Step 1: Expand the `Strategy` schema**

Read `openapi/openapi.yaml`. Find the existing `Strategy:` schema under `components.schemas:`. Replace it with:

```yaml
    Strategy:
      type: object
      required: [shortCode, repoOwner, repoName, isOfficial, installState]
      properties:
        shortCode:
          type: string
        repoOwner:
          type: string
        repoName:
          type: string
        cloneUrl:
          type: string
          format: uri
        isOfficial:
          type: boolean
        ownerSub:
          type: string
          nullable: true
          description: Auth0 sub of the registering user; NULL for official strategies.
        description:
          type: string
          nullable: true
        categories:
          type: array
          items:
            type: string
        stars:
          type: integer
          nullable: true
        installState:
          type: string
          enum: [pending, installing, ready, failed]
        installedVer:
          type: string
          nullable: true
        lastAttemptedVer:
          type: string
          nullable: true
        installError:
          type: string
          nullable: true
        installedAt:
          type: string
          format: date-time
          nullable: true
        describe:
          nullable: true
          allOf:
            - $ref: '#/components/schemas/StrategyDescribe'
        cagr:
          type: number
          format: double
          nullable: true
        maxDrawDown:
          type: number
          format: double
          nullable: true
        sharpe:
          type: number
          format: double
          nullable: true
```

- [ ] **Step 2: Add the three new sub-schemas**

Still inside `components.schemas:`, add (alphabetical order — after `StrategyRegisterRequest`):

```yaml
    StrategyDescribe:
      type: object
      required: [shortCode, name, parameters, schedule, benchmark]
      properties:
        shortCode:
          type: string
        name:
          type: string
        description:
          type: string
          nullable: true
        parameters:
          type: array
          items:
            $ref: '#/components/schemas/StrategyParameter'
        presets:
          type: array
          items:
            $ref: '#/components/schemas/StrategyPreset'
        schedule:
          type: string
        benchmark:
          type: string

    StrategyParameter:
      type: object
      required: [name, type]
      properties:
        name:
          type: string
        type:
          type: string
        default:
          description: Default value — may be any JSON-serializable type.
        description:
          type: string
          nullable: true

    StrategyPreset:
      type: object
      required: [name, parameters]
      properties:
        name:
          type: string
        parameters:
          type: object
          additionalProperties: true
```

- [ ] **Step 3: Regenerate types**

```bash
make gen
```
Expected: `openapi/openapi.gen.go` is rewritten with the new types. No stderr errors.

- [ ] **Step 4: Verify build and existing tests**

```bash
go build ./...
ginkgo run -r
```
Expected: build clean; all existing specs pass (portfolios + strategies stubs still return 501 — unchanged).

- [ ] **Step 5: Commit**

```bash
git add openapi/openapi.yaml openapi/openapi.gen.go
git commit -m "$(cat <<'EOF'
expand Strategy schema with install state and describe output

Strategy now carries installState (pending/installing/ready/failed),
installedVer / lastAttemptedVer / installError / installedAt, and a
nullable describe sub-object populated once install completes.
StrategyDescribe / StrategyParameter / StrategyPreset schemas added.
EOF
)"
```

---

## Task 5: `strategy` package — doc + domain types

**Files:**
- Create: `strategy/doc.go`
- Create: `strategy/types.go`
- Create: `strategy/strategy_suite_test.go`

- [ ] **Step 1: Package doc**

Create `strategy/doc.go`:

```go
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

// Package strategy implements the pvapi 3.0 strategy registry: GitHub
// discovery (via pvbt/library), version-pinned install (clone + build +
// describe), and the background sync goroutine that reconciles remote
// state into the strategies table.
package strategy
```

- [ ] **Step 2: Domain types**

Create `strategy/types.go`:

```go
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

import "time"

// InstallState captures where a strategy row sits in the install lifecycle.
type InstallState string

const (
	InstallStatePending    InstallState = "pending"
	InstallStateInstalling InstallState = "installing"
	InstallStateReady      InstallState = "ready"
	InstallStateFailed     InstallState = "failed"
)

// Strategy is pvapi's internal representation of a `strategies` row.
type Strategy struct {
	ShortCode        string
	RepoOwner        string
	RepoName         string
	CloneURL         string
	IsOfficial       bool
	OwnerSub         *string
	Description      *string
	Categories       []string
	Stars            *int
	InstalledVer     *string
	InstalledAt      *time.Time
	LastAttemptedVer *string
	InstallError     *string
	ArtifactKind     *string // "binary" | "image"
	ArtifactRef      *string
	DescribeJSON     []byte // raw describe output; parsed on demand
	CAGR             *float64
	MaxDrawdown      *float64
	Sharpe           *float64
	StatsAsOf        *time.Time
	DiscoveredAt     time.Time
	UpdatedAt        time.Time
}

// DeriveInstallState reports the lifecycle state implied by the row's
// install-tracking columns. Has no side effects.
func (s Strategy) DeriveInstallState() InstallState {
	switch {
	case s.InstalledVer != nil && s.InstallError == nil:
		return InstallStateReady
	case s.InstallError != nil:
		return InstallStateFailed
	case s.LastAttemptedVer != nil:
		return InstallStateInstalling
	default:
		return InstallStatePending
	}
}

// Listing is the shape returned by the GitHub discovery layer, after
// filtering to official (penny-vault) strategies.
type Listing struct {
	Name        string
	Owner       string
	Description string
	Categories  []string
	CloneURL    string
	Stars       int
	UpdatedAt   time.Time
}

// Describe mirrors pvbt's `describe --json` output, parsed from the raw
// bytes stored in Strategy.DescribeJSON.
type Describe struct {
	ShortCode   string              `json:"shortCode"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Parameters  []DescribeParameter `json:"parameters"`
	Presets     []DescribePreset    `json:"presets"`
	Schedule    string              `json:"schedule"`
	Benchmark   string              `json:"benchmark"`
}

// DescribeParameter is one declared parameter in the describe output.
type DescribeParameter struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	Default     interface{} `json:"default,omitempty"`
	Description string      `json:"description,omitempty"`
}

// DescribePreset is one named parameter set from the describe output.
type DescribePreset struct {
	Name       string                 `json:"name"`
	Parameters map[string]interface{} `json:"parameters"`
}
```

- [ ] **Step 3: Ginkgo suite runner**

Create `strategy/strategy_suite_test.go`:

```go
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

package strategy_test

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestStrategy(t *testing.T) {
	prev := log.Logger
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: GinkgoWriter})
	defer func() { log.Logger = prev }()

	RegisterFailHandler(Fail)
	RunSpecs(t, "Strategy Suite")
}
```

- [ ] **Step 4: Verify build**

```bash
go build ./strategy/...
ginkgo run ./strategy
```
Expected: build clean; ginkgo runs the suite with zero specs (prints `Ran 0 of 0 Specs`). That's the correct red state — subsequent tasks add specs.

- [ ] **Step 5: Commit**

```bash
git add strategy/doc.go strategy/types.go strategy/strategy_suite_test.go
git commit -m "$(cat <<'EOF'
scaffold strategy package with domain types

Adds Strategy, InstallState, Listing, Describe/Parameter/Preset —
the shapes consumed by the install coordinator, sync goroutine, and
handlers that later tasks introduce.
EOF
)"
```

---

## Task 6: `strategy` package — database access

**Files:**
- Create: `strategy/db.go`

- [ ] **Step 1: Write the DB access layer**

Create `strategy/db.go`:

```go
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
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a requested strategy row does not exist.
var ErrNotFound = errors.New("strategy not found")

const strategyColumns = `
	short_code, repo_owner, repo_name, clone_url, is_official, owner_sub,
	description, categories, stars,
	installed_ver, installed_at, last_attempted_ver, install_error,
	artifact_kind, artifact_ref, describe_json,
	cagr, max_drawdown, sharpe, stats_as_of,
	discovered_at, updated_at
`

// List returns every strategy row. Caller applies filtering.
func List(ctx context.Context, pool *pgxpool.Pool) ([]Strategy, error) {
	rows, err := pool.Query(ctx, `SELECT `+strategyColumns+` FROM strategies ORDER BY short_code`)
	if err != nil {
		return nil, fmt.Errorf("querying strategies: %w", err)
	}
	defer rows.Close()

	var out []Strategy
	for rows.Next() {
		s, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating strategies: %w", err)
	}
	return out, nil
}

// Get returns one strategy by short code.
func Get(ctx context.Context, pool *pgxpool.Pool, shortCode string) (Strategy, error) {
	row := pool.QueryRow(ctx,
		`SELECT `+strategyColumns+` FROM strategies WHERE short_code = $1`,
		shortCode,
	)
	s, err := scan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Strategy{}, ErrNotFound
	}
	return s, err
}

// Upsert inserts or updates a strategy row based on the scanner's shortCode.
// Used by the sync loop to reconcile discovered listings.
func Upsert(ctx context.Context, pool *pgxpool.Pool, s Strategy) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO strategies (
			short_code, repo_owner, repo_name, clone_url, is_official, owner_sub,
			description, categories, stars
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (short_code) DO UPDATE SET
			repo_owner = EXCLUDED.repo_owner,
			repo_name  = EXCLUDED.repo_name,
			clone_url  = EXCLUDED.clone_url,
			description = EXCLUDED.description,
			categories = EXCLUDED.categories,
			stars      = EXCLUDED.stars,
			updated_at = NOW()
	`, s.ShortCode, s.RepoOwner, s.RepoName, s.CloneURL, s.IsOfficial, s.OwnerSub,
		s.Description, s.Categories, s.Stars)
	if err != nil {
		return fmt.Errorf("upsert strategy %s: %w", s.ShortCode, err)
	}
	return nil
}

// MarkAttempt records that a given version was attempted for a strategy.
// Called by the install coordinator at the start of every install attempt;
// also clears any previous install_error so a fresh attempt starts clean.
func MarkAttempt(ctx context.Context, pool *pgxpool.Pool, shortCode, version string) error {
	_, err := pool.Exec(ctx, `
		UPDATE strategies
		   SET last_attempted_ver = $2,
		       install_error      = NULL,
		       updated_at         = NOW()
		 WHERE short_code = $1
	`, shortCode, version)
	if err != nil {
		return fmt.Errorf("mark attempt %s@%s: %w", shortCode, version, err)
	}
	return nil
}

// MarkSuccess records a successful install. Sets installed_ver, installed_at,
// artifact_kind, artifact_ref, describe_json. Clears install_error.
func MarkSuccess(ctx context.Context, pool *pgxpool.Pool,
	shortCode, version, artifactKind, artifactRef string, describeJSON []byte,
) error {
	_, err := pool.Exec(ctx, `
		UPDATE strategies
		   SET installed_ver  = $2,
		       installed_at   = NOW(),
		       artifact_kind  = $3,
		       artifact_ref   = $4,
		       describe_json  = $5,
		       install_error  = NULL,
		       updated_at     = NOW()
		 WHERE short_code = $1
	`, shortCode, version, artifactKind, artifactRef, describeJSON)
	if err != nil {
		return fmt.Errorf("mark success %s@%s: %w", shortCode, version, err)
	}
	return nil
}

// MarkFailure records a failed install. Sets install_error; leaves
// installed_ver and artifact_* alone so an older working version stays live.
func MarkFailure(ctx context.Context, pool *pgxpool.Pool, shortCode, version, errText string) error {
	_, err := pool.Exec(ctx, `
		UPDATE strategies
		   SET last_attempted_ver = $2,
		       install_error      = $3,
		       updated_at         = NOW()
		 WHERE short_code = $1
	`, shortCode, version, errText)
	if err != nil {
		return fmt.Errorf("mark failure %s@%s: %w", shortCode, version, err)
	}
	return nil
}

// scanner is the subset of pgx.Rows / pgx.Row used by scan.
type scanner interface {
	Scan(dest ...any) error
}

func scan(r scanner) (Strategy, error) {
	var s Strategy
	err := r.Scan(
		&s.ShortCode, &s.RepoOwner, &s.RepoName, &s.CloneURL, &s.IsOfficial, &s.OwnerSub,
		&s.Description, &s.Categories, &s.Stars,
		&s.InstalledVer, &s.InstalledAt, &s.LastAttemptedVer, &s.InstallError,
		&s.ArtifactKind, &s.ArtifactRef, &s.DescribeJSON,
		&s.CAGR, &s.MaxDrawdown, &s.Sharpe, &s.StatsAsOf,
		&s.DiscoveredAt, &s.UpdatedAt,
	)
	if err != nil {
		return Strategy{}, fmt.Errorf("scanning strategy row: %w", err)
	}
	return s, nil
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./strategy/...
```
Expected: no errors.

No tests here — policy: no live-DB tests and no DB-mocking library. Correctness is validated by the end-to-end smoke.

- [ ] **Step 3: Commit**

```bash
git add strategy/db.go
git commit -m "$(cat <<'EOF'
add strategy/db.go: pgxpool CRUD for strategies

Exposes List, Get, Upsert, MarkAttempt, MarkSuccess, MarkFailure —
the minimum surface the sync loop and install coordinator need.
ErrNotFound for 404 mapping via api.WriteProblem.
EOF
)"
```

---

## Task 7: `strategy` package — GitHub discovery wrapper

**Files:**
- Create: `strategy/github.go`
- Create: `strategy/testdata/github_search_response.json`
- Create: `strategy/github_test.go`

- [ ] **Step 1: Write the failing test**

Create `strategy/testdata/github_search_response.json`:

```json
{
  "total_count": 2,
  "items": [
    {
      "name": "adm",
      "description": "Accelerating Dual Momentum",
      "clone_url": "https://github.com/penny-vault/adm.git",
      "stargazers_count": 42,
      "updated_at": "2026-04-01T00:00:00Z",
      "topics": ["pvbt-strategy", "momentum"],
      "owner": { "login": "penny-vault" }
    },
    {
      "name": "bogus",
      "description": "An outsider strategy that happens to share the topic",
      "clone_url": "https://github.com/random-user/bogus.git",
      "stargazers_count": 3,
      "updated_at": "2026-04-02T00:00:00Z",
      "topics": ["pvbt-strategy"],
      "owner": { "login": "random-user" }
    }
  ]
}
```

Create `strategy/github_test.go`:

```go
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

package strategy_test

import (
	"context"
	"net/http"
	"os"

	"github.com/jarcoal/httpmock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("GitHub discovery", func() {
	BeforeEach(func() {
		httpmock.Activate()
		httpmock.ActivateNonDefault(http.DefaultClient)

		body, err := os.ReadFile("testdata/github_search_response.json")
		Expect(err).NotTo(HaveOccurred())

		httpmock.RegisterResponder("GET", `=~^https://api\.github\.com/search/repositories.*`,
			httpmock.NewBytesResponder(200, body))
	})

	AfterEach(func() {
		httpmock.DeactivateAndReset()
	})

	It("returns only penny-vault listings", func() {
		listings, err := strategy.DiscoverOfficial(context.Background(), strategy.DiscoverOptions{
			CacheDir:    GinkgoT().TempDir(),
			ExpectOwner: "penny-vault",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(listings).To(HaveLen(1))
		Expect(listings[0].Owner).To(Equal("penny-vault"))
		Expect(listings[0].Name).To(Equal("adm"))
		Expect(listings[0].Stars).To(Equal(42))
	})
})
```

- [ ] **Step 2: Run to confirm red**

```bash
ginkgo run ./strategy
```
Expected: FAIL — `strategy.DiscoverOfficial` and `strategy.DiscoverOptions` undefined.

- [ ] **Step 3: Implement `strategy/github.go`**

Create `strategy/github.go`:

```go
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
```

- [ ] **Step 4: Run to confirm green**

```bash
ginkgo run ./strategy
```
Expected: 1 passing spec.

- [ ] **Step 5: Commit**

```bash
git add strategy/github.go strategy/github_test.go strategy/testdata/github_search_response.json
git commit -m "$(cat <<'EOF'
add GitHub discovery wrapper around pvbt/library.Search

DiscoverOfficial filters listings to a configured Owner.Login
(penny-vault in prod). An optional Token is pushed through
GITHUB_TOKEN so pvbt/library's own auth resolution picks it up.
Tests stub the underlying HTTP call via httpmock against a checked-in
GitHub Search response fixture.
EOF
)"
```

---

## Task 8: `strategy` package — version-pinned install

**Files:**
- Create: `strategy/install.go`
- Create: `strategy/testdata/fake-strategy-src/go.mod`
- Create: `strategy/testdata/fake-strategy-src/main.go`
- Create: `strategy/install_test.go`

- [ ] **Step 1: Check in the fake strategy source**

Create `strategy/testdata/fake-strategy-src/go.mod`:

```
module github.com/example/fake-strategy

go 1.25
```

Create `strategy/testdata/fake-strategy-src/main.go`:

```go
// A minimal strategy that behaves like a pvbt-compiled strategy binary for
// tests: supports `describe --json` and exits 0. Not a real backtest; this
// exists only so strategy.Install can exercise its clone + build + describe
// path end-to-end.
package main

import (
	"fmt"
	"os"
)

const describeJSON = `{
  "shortCode": "fake",
  "name": "Fake Strategy",
  "description": "Test fixture for pvapi install tests",
  "parameters": [
    {"name": "riskOn", "type": "universe", "default": "SPY", "description": "risk-on universe"}
  ],
  "presets": [
    {"name": "standard", "parameters": {"riskOn": "SPY"}}
  ],
  "schedule": "@monthend",
  "benchmark": "SPY"
}`

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "describe" {
		fmt.Print(describeJSON)
		return
	}
	fmt.Fprintln(os.Stderr, "usage: fake-strategy describe [--json]")
	os.Exit(2)
}
```

- [ ] **Step 2: Write the failing install test**

Create `strategy/install_test.go`:

```go
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

package strategy_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/strategy"
)

// materializeFakeRepo copies strategy/testdata/fake-strategy-src/* into a
// fresh tempdir, `git init`s it, commits, tags, and returns the directory
// path. Callers can clone from `file://<path>`.
func materializeFakeRepo(tag string) string {
	srcDir, err := filepath.Abs("testdata/fake-strategy-src")
	Expect(err).NotTo(HaveOccurred())

	dst := GinkgoT().TempDir()

	// Copy source tree
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	Expect(err).NotTo(HaveOccurred())

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dst
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
	}

	run("init", "-q", "-b", "main")
	run("add", ".")
	run("commit", "-q", "-m", "initial")
	run("tag", tag)

	return dst
}

var _ = Describe("Install", func() {
	It("clones a pinned tag, builds, and parses describe", func() {
		repo := materializeFakeRepo("v1.0.0")
		dst := GinkgoT().TempDir()

		result, err := strategy.Install(context.Background(), strategy.InstallRequest{
			ShortCode: "fake",
			CloneURL:  "file://" + repo,
			Version:   "v1.0.0",
			DestDir:   dst,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.BinPath).To(BeARegularFile())
		Expect(result.ShortCode).To(Equal("fake"))

		var d strategy.Describe
		Expect(json.Unmarshal(result.DescribeJSON, &d)).To(Succeed())
		Expect(d.ShortCode).To(Equal("fake"))
		Expect(d.Presets).To(HaveLen(1))
		Expect(d.Presets[0].Name).To(Equal("standard"))
	})

	It("fails with a useful error when the tag does not exist", func() {
		repo := materializeFakeRepo("v1.0.0")

		_, err := strategy.Install(context.Background(), strategy.InstallRequest{
			ShortCode: "fake",
			CloneURL:  "file://" + repo,
			Version:   "v9.9.9",
			DestDir:   GinkgoT().TempDir(),
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("v9.9.9"))
	})

	It("fails when describe output's shortCode does not match the request", func() {
		repo := materializeFakeRepo("v1.0.0")

		_, err := strategy.Install(context.Background(), strategy.InstallRequest{
			ShortCode: "mismatch",
			CloneURL:  "file://" + repo,
			Version:   "v1.0.0",
			DestDir:   GinkgoT().TempDir(),
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("short_code"))
	})
})
```

- [ ] **Step 3: Run to confirm red**

```bash
ginkgo run ./strategy
```
Expected: FAIL — `strategy.Install`, `strategy.InstallRequest` undefined.

- [ ] **Step 4: Implement `strategy/install.go`**

Create `strategy/install.go`:

```go
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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// InstallRequest describes a single version-pinned install.
type InstallRequest struct {
	ShortCode string // expected short_code; validated against describe output
	CloneURL  string // git URL (https, ssh, or file://)
	Version   string // git tag or commit SHA to check out
	DestDir   string // absolute path to clone/build into
}

// InstallResult is what a successful install produces.
type InstallResult struct {
	BinPath      string // absolute path to the built binary
	DescribeJSON []byte // raw `<bin> describe --json` output
	ShortCode    string // parsed from the describe output
}

// Install performs a single version-pinned install:
//  1. git clone --branch <Version> --depth 1 <CloneURL> <DestDir>
//  2. go build -o <DestDir>/<ShortCode>.bin .
//  3. <binary> describe --json
//  4. validates describe.shortCode matches req.ShortCode
//
// On failure Install returns a wrapped error and leaves DestDir in whatever
// state it was in; callers are expected to treat DestDir as throwaway on
// failure.
func Install(ctx context.Context, req InstallRequest) (*InstallResult, error) {
	if req.ShortCode == "" || req.CloneURL == "" || req.Version == "" || req.DestDir == "" {
		return nil, fmt.Errorf("InstallRequest: all fields required")
	}

	// Clone at the specific tag/SHA.
	cloneCmd := exec.CommandContext(ctx, "git", "clone", "--depth=1",
		"--branch", req.Version, req.CloneURL, req.DestDir)
	var cloneOut bytes.Buffer
	cloneCmd.Stdout = &cloneOut
	cloneCmd.Stderr = &cloneOut
	if err := cloneCmd.Run(); err != nil {
		return nil, fmt.Errorf("git clone %s@%s: %w\n%s", req.CloneURL, req.Version, err, cloneOut.String())
	}

	// Build.
	binPath := filepath.Join(req.DestDir, req.ShortCode+".bin")
	buildCmd := exec.CommandContext(ctx, "go", "build", "-o", binPath, ".")
	buildCmd.Dir = req.DestDir
	var buildOut bytes.Buffer
	buildCmd.Stdout = &buildOut
	buildCmd.Stderr = &buildOut
	if err := buildCmd.Run(); err != nil {
		return nil, fmt.Errorf("go build: %w\n%s", err, buildOut.String())
	}

	// Describe.
	describeCmd := exec.CommandContext(ctx, binPath, "describe", "--json")
	var describeOut bytes.Buffer
	describeCmd.Stdout = &describeOut
	describeCmd.Stderr = os.Stderr
	if err := describeCmd.Run(); err != nil {
		return nil, fmt.Errorf("%s describe --json: %w", binPath, err)
	}

	describeBytes := describeOut.Bytes()

	var parsed Describe
	if err := json.Unmarshal(describeBytes, &parsed); err != nil {
		return nil, fmt.Errorf("parsing describe output: %w", err)
	}

	if parsed.ShortCode != req.ShortCode {
		return nil, fmt.Errorf("describe short_code mismatch: want %q, got %q", req.ShortCode, parsed.ShortCode)
	}

	return &InstallResult{
		BinPath:      binPath,
		DescribeJSON: describeBytes,
		ShortCode:    parsed.ShortCode,
	}, nil
}
```

- [ ] **Step 5: Run to confirm green**

```bash
ginkgo run ./strategy
```
Expected: 4 passing specs (1 github + 3 install).

- [ ] **Step 6: Commit**

```bash
git add strategy/install.go strategy/install_test.go strategy/testdata/fake-strategy-src
git commit -m "$(cat <<'EOF'
add strategy.Install: version-pinned clone + build + describe

Install does git clone --branch <ver> --depth 1, go build, runs the
resulting binary with describe --json, and validates the short_code.
Tests materialize a bare git repo from checked-in fixture source in
BeforeEach and clone it via file://; covers the happy path, a
missing-tag failure, and a short_code-mismatch failure.
EOF
)"
```

---

## Task 9: `strategy` package — sync goroutine

**Files:**
- Create: `strategy/sync.go`
- Create: `strategy/sync_test.go`

- [ ] **Step 1: Write the failing sync tests**

Create `strategy/sync_test.go`:

```go
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

package strategy_test

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/strategy"
)

// fakeStore implements strategy.Store for unit-testing the sync loop
// without a live Postgres. Mirrors pool-backed behavior in memory.
type fakeStore struct {
	rows      map[string]strategy.Strategy
	upserts   []string
	attempts  []attemptCall
	successes []successCall
	failures  []failureCall
}

type attemptCall struct{ shortCode, version string }
type successCall struct {
	shortCode, version, kind, ref string
	describeLen                   int
}
type failureCall struct{ shortCode, version, err string }

func newFakeStore() *fakeStore {
	return &fakeStore{rows: make(map[string]strategy.Strategy)}
}

func (f *fakeStore) List(_ context.Context) ([]strategy.Strategy, error) {
	out := make([]strategy.Strategy, 0, len(f.rows))
	for _, v := range f.rows {
		out = append(out, v)
	}
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, sc string) (strategy.Strategy, error) {
	v, ok := f.rows[sc]
	if !ok {
		return strategy.Strategy{}, strategy.ErrNotFound
	}
	return v, nil
}

func (f *fakeStore) Upsert(_ context.Context, s strategy.Strategy) error {
	f.upserts = append(f.upserts, s.ShortCode)
	f.rows[s.ShortCode] = s
	return nil
}

func (f *fakeStore) MarkAttempt(_ context.Context, sc, ver string) error {
	f.attempts = append(f.attempts, attemptCall{sc, ver})
	r := f.rows[sc]
	r.LastAttemptedVer = &ver
	r.InstallError = nil
	f.rows[sc] = r
	return nil
}

func (f *fakeStore) MarkSuccess(_ context.Context, sc, ver, kind, ref string, describe []byte) error {
	f.successes = append(f.successes, successCall{sc, ver, kind, ref, len(describe)})
	r := f.rows[sc]
	r.InstalledVer = &ver
	now := time.Now()
	r.InstalledAt = &now
	k := kind
	r.ArtifactKind = &k
	rf := ref
	r.ArtifactRef = &rf
	r.DescribeJSON = append([]byte(nil), describe...)
	r.InstallError = nil
	f.rows[sc] = r
	return nil
}

func (f *fakeStore) MarkFailure(_ context.Context, sc, ver, errText string) error {
	f.failures = append(f.failures, failureCall{sc, ver, errText})
	r := f.rows[sc]
	r.LastAttemptedVer = &ver
	r.InstallError = &errText
	f.rows[sc] = r
	return nil
}

var _ = Describe("Syncer.Tick", func() {
	It("inserts a new listing, attempts install, records success", func() {
		store := newFakeStore()

		discovery := func(_ context.Context) ([]strategy.Listing, error) {
			return []strategy.Listing{{
				Name: "fake", Owner: "penny-vault", CloneURL: "file:///tmp/fake.git",
				Stars: 1, Categories: []string{"momentum"},
			}}, nil
		}
		resolveVer := func(_ context.Context, cloneURL string) (string, error) {
			return "v1.0.0", nil
		}
		installer := func(_ context.Context, req strategy.InstallRequest) (*strategy.InstallResult, error) {
			return &strategy.InstallResult{
				BinPath:      "/var/lib/pvapi/strategies/official/fake/v1.0.0/fake.bin",
				DescribeJSON: []byte(`{"shortCode":"fake","name":"Fake","parameters":[],"schedule":"@monthend","benchmark":"SPY"}`),
				ShortCode:    "fake",
			}, nil
		}

		s := strategy.NewSyncer(store, strategy.SyncerOptions{
			Discovery:   discovery,
			ResolveVer:  resolveVer,
			Installer:   installer,
			OfficialDir: "/var/lib/pvapi/strategies/official",
			Concurrency: 1,
		})
		Expect(s.Tick(context.Background())).To(Succeed())

		Expect(store.upserts).To(ConsistOf("fake"))
		Expect(store.attempts).To(HaveLen(1))
		Expect(store.attempts[0]).To(Equal(attemptCall{"fake", "v1.0.0"}))
		Expect(store.successes).To(HaveLen(1))
		Expect(store.successes[0].ref).To(ContainSubstring("fake"))
	})

	It("records failure on install error and leaves installed_ver alone", func() {
		store := newFakeStore()
		// Pre-seed: previously successful install of v0.9.0.
		prev := "v0.9.0"
		store.rows["fake"] = strategy.Strategy{
			ShortCode:        "fake",
			IsOfficial:       true,
			InstalledVer:     &prev,
			LastAttemptedVer: &prev,
		}

		discovery := func(_ context.Context) ([]strategy.Listing, error) {
			return []strategy.Listing{{Name: "fake", Owner: "penny-vault", CloneURL: "file:///tmp/fake.git"}}, nil
		}
		resolveVer := func(_ context.Context, _ string) (string, error) { return "v1.0.0", nil }
		installer := func(_ context.Context, _ strategy.InstallRequest) (*strategy.InstallResult, error) {
			return nil, errors.New("build failed")
		}

		s := strategy.NewSyncer(store, strategy.SyncerOptions{
			Discovery: discovery, ResolveVer: resolveVer, Installer: installer,
			OfficialDir: "/tmp", Concurrency: 1,
		})
		Expect(s.Tick(context.Background())).To(Succeed())

		Expect(store.failures).To(HaveLen(1))
		Expect(store.failures[0].version).To(Equal("v1.0.0"))
		Expect(*store.rows["fake"].InstalledVer).To(Equal("v0.9.0"), "installed_ver is preserved on failure")
		Expect(*store.rows["fake"].InstallError).To(ContainSubstring("build failed"))
	})

	It("skips an install when remote version has not changed", func() {
		store := newFakeStore()
		installed := "v1.0.0"
		store.rows["fake"] = strategy.Strategy{
			ShortCode:        "fake",
			IsOfficial:       true,
			InstalledVer:     &installed,
			LastAttemptedVer: &installed,
		}

		installerCalls := 0
		discovery := func(_ context.Context) ([]strategy.Listing, error) {
			return []strategy.Listing{{Name: "fake", Owner: "penny-vault", CloneURL: "file:///tmp/fake.git"}}, nil
		}
		resolveVer := func(_ context.Context, _ string) (string, error) { return "v1.0.0", nil }
		installer := func(_ context.Context, _ strategy.InstallRequest) (*strategy.InstallResult, error) {
			installerCalls++
			return nil, errors.New("should not be called")
		}

		s := strategy.NewSyncer(store, strategy.SyncerOptions{
			Discovery: discovery, ResolveVer: resolveVer, Installer: installer,
			OfficialDir: "/tmp", Concurrency: 1,
		})
		Expect(s.Tick(context.Background())).To(Succeed())
		Expect(installerCalls).To(Equal(0))
	})

	It("skips a failed install when upstream version has not changed", func() {
		store := newFakeStore()
		attempted := "v1.0.0"
		failed := "build failed"
		store.rows["fake"] = strategy.Strategy{
			ShortCode:        "fake",
			IsOfficial:       true,
			LastAttemptedVer: &attempted,
			InstallError:     &failed,
		}

		installerCalls := 0
		discovery := func(_ context.Context) ([]strategy.Listing, error) {
			return []strategy.Listing{{Name: "fake", Owner: "penny-vault", CloneURL: "file:///tmp/fake.git"}}, nil
		}
		resolveVer := func(_ context.Context, _ string) (string, error) { return "v1.0.0", nil }
		installer := func(_ context.Context, _ strategy.InstallRequest) (*strategy.InstallResult, error) {
			installerCalls++
			return nil, errors.New("should not be called")
		}

		s := strategy.NewSyncer(store, strategy.SyncerOptions{
			Discovery: discovery, ResolveVer: resolveVer, Installer: installer,
			OfficialDir: "/tmp", Concurrency: 1,
		})
		Expect(s.Tick(context.Background())).To(Succeed())
		Expect(installerCalls).To(Equal(0))
	})
})
```

- [ ] **Step 2: Run to confirm red**

```bash
ginkgo run ./strategy
```
Expected: FAIL — `strategy.NewSyncer`, `strategy.SyncerOptions`, `strategy.Store` undefined.

- [ ] **Step 3: Implement `strategy/sync.go`**

Create `strategy/sync.go`:

```go
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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Store is the subset of strategy.db operations the Syncer needs. The
// production implementation wraps `*pgxpool.Pool`; tests pass an in-memory
// fake.
type Store interface {
	List(ctx context.Context) ([]Strategy, error)
	Get(ctx context.Context, shortCode string) (Strategy, error)
	Upsert(ctx context.Context, s Strategy) error
	MarkAttempt(ctx context.Context, shortCode, version string) error
	MarkSuccess(ctx context.Context, shortCode, version, kind, ref string, describe []byte) error
	MarkFailure(ctx context.Context, shortCode, version, errText string) error
}

// DiscoveryFunc returns the current set of listings from GitHub.
type DiscoveryFunc func(ctx context.Context) ([]Listing, error)

// ResolveVerFunc returns the remote latest version (git tag or SHA) for a
// clone URL. Production passes a `git ls-remote`-backed implementation;
// tests pass a canned function.
type ResolveVerFunc func(ctx context.Context, cloneURL string) (string, error)

// InstallerFunc executes a single install. Production passes strategy.Install;
// tests pass a stub.
type InstallerFunc func(ctx context.Context, req InstallRequest) (*InstallResult, error)

// SyncerOptions configures NewSyncer.
type SyncerOptions struct {
	Discovery   DiscoveryFunc
	ResolveVer  ResolveVerFunc
	Installer   InstallerFunc
	OfficialDir string
	Concurrency int
	Interval    time.Duration // 0 = Tick-only; Run reuses this as its period
}

// Syncer orchestrates periodic registry reconciliation.
type Syncer struct {
	store Store
	opts  SyncerOptions
}

// NewSyncer constructs a Syncer. Install concurrency of 0 is replaced with 1.
func NewSyncer(store Store, opts SyncerOptions) *Syncer {
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	return &Syncer{store: store, opts: opts}
}

// Run ticks on the configured Interval until ctx is cancelled. The first
// tick fires immediately (non-blocking — HTTP server starts before Run).
func (s *Syncer) Run(ctx context.Context) error {
	if s.opts.Interval <= 0 {
		return errors.New("Syncer.Run requires SyncerOptions.Interval > 0")
	}
	if err := s.Tick(ctx); err != nil {
		log.Warn().Err(err).Msg("strategy sync tick failed")
	}
	ticker := time.NewTicker(s.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.Tick(ctx); err != nil {
				log.Warn().Err(err).Msg("strategy sync tick failed")
			}
		}
	}
}

// Tick runs one reconciliation cycle.
func (s *Syncer) Tick(ctx context.Context) error {
	listings, err := s.opts.Discovery(ctx)
	if err != nil {
		return fmt.Errorf("discovery: %w", err)
	}

	type job struct {
		shortCode string
		cloneURL  string
		version   string
		dest      string
	}
	var jobs []job

	for _, l := range listings {
		shortCode := l.Name
		row := Strategy{
			ShortCode:   shortCode,
			RepoOwner:   l.Owner,
			RepoName:    l.Name,
			CloneURL:    l.CloneURL,
			IsOfficial:  true,
			Description: strPtr(l.Description),
			Categories:  l.Categories,
			Stars:       intPtr(l.Stars),
		}
		if err := s.store.Upsert(ctx, row); err != nil {
			log.Warn().Err(err).Str("short_code", shortCode).Msg("upsert failed")
			continue
		}

		remote, err := s.opts.ResolveVer(ctx, l.CloneURL)
		if err != nil {
			log.Warn().Err(err).Str("short_code", shortCode).Msg("resolve remote version failed")
			continue
		}

		existing, err := s.store.Get(ctx, shortCode)
		if err != nil {
			log.Warn().Err(err).Str("short_code", shortCode).Msg("get strategy row failed")
			continue
		}
		if existing.LastAttemptedVer != nil && *existing.LastAttemptedVer == remote {
			continue
		}

		dest := filepath.Join(s.opts.OfficialDir, l.Owner, l.Name, remote)
		jobs = append(jobs, job{shortCode: shortCode, cloneURL: l.CloneURL, version: remote, dest: dest})
	}

	sem := make(chan struct{}, s.opts.Concurrency)
	var wg sync.WaitGroup
	for _, j := range jobs {
		j := j
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			s.runInstall(ctx, j.shortCode, j.cloneURL, j.version, j.dest)
		}()
	}
	wg.Wait()
	return nil
}

func (s *Syncer) runInstall(ctx context.Context, shortCode, cloneURL, version, dest string) {
	if err := s.store.MarkAttempt(ctx, shortCode, version); err != nil {
		log.Warn().Err(err).Str("short_code", shortCode).Msg("mark attempt failed")
		return
	}

	result, err := s.opts.Installer(ctx, InstallRequest{
		ShortCode: shortCode, CloneURL: cloneURL, Version: version, DestDir: dest,
	})
	if err != nil {
		_ = s.store.MarkFailure(ctx, shortCode, version, err.Error())
		return
	}
	_ = s.store.MarkSuccess(ctx, shortCode, version, "binary", result.BinPath, result.DescribeJSON)
}

// ResolveVerWithGit uses `git ls-remote` to discover the most-recent
// annotated tag on the given clone URL. Falls back to the default-branch
// HEAD SHA when no tags are present. Intended as the production
// implementation of ResolveVerFunc.
func ResolveVerWithGit(ctx context.Context, cloneURL string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--tags", "--sort=-v:refname", cloneURL)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git ls-remote --tags %s: %w\n%s", cloneURL, err, out.String())
	}
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "<sha>\trefs/tags/<tag>" or "<sha>\trefs/tags/<tag>^{}"
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		ref := fields[1]
		ref = strings.TrimSuffix(ref, "^{}")
		if !strings.HasPrefix(ref, "refs/tags/") {
			continue
		}
		return strings.TrimPrefix(ref, "refs/tags/"), nil
	}
	return "", fmt.Errorf("no tags found on %s", cloneURL)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func intPtr(i int) *int {
	return &i
}
```

- [ ] **Step 4: Run to confirm green**

```bash
ginkgo run ./strategy
```
Expected: 8 passing specs (4 prior + 4 new sync specs).

- [ ] **Step 5: Commit**

```bash
git add strategy/sync.go strategy/sync_test.go
git commit -m "$(cat <<'EOF'
add strategy.Syncer: reconcile listings, install on upstream change

NewSyncer takes an in-memory Store interface (fake in tests,
pgxpool-backed in prod) plus function-valued Discovery/ResolveVer/
Installer dependencies so the loop is fully testable. Tick runs one
reconcile cycle: upsert metadata, resolve remote version, compare to
last_attempted_ver, install only on change. Installs run through a
bounded-concurrency worker pool. Failed installs preserve the
previous installed_ver so portfolios pinning older versions keep
running.

ResolveVerWithGit invokes `git ls-remote --tags --sort=-v:refname`
as the production implementation.
EOF
)"
```

---

## Task 10: `strategy` package — real handlers

**Files:**
- Create: `strategy/handler.go`
- Create: `strategy/handler_test.go`
- Modify: `api/strategies.go`

- [ ] **Step 1: Write the failing handler tests**

Create `strategy/handler_test.go`:

```go
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

package strategy_test

import (
	"io"
	"net/http/httptest"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("strategy.Handler", func() {
	var store *fakeStore

	BeforeEach(func() {
		store = newFakeStore()
		// One ready strategy
		ver := "v1.0.0"
		at := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
		store.rows["adm"] = strategy.Strategy{
			ShortCode:        "adm",
			RepoOwner:        "penny-vault",
			RepoName:         "adm",
			CloneURL:         "https://github.com/penny-vault/adm.git",
			IsOfficial:       true,
			InstalledVer:     &ver,
			LastAttemptedVer: &ver,
			InstalledAt:      &at,
			DescribeJSON:     []byte(`{"shortCode":"adm","name":"ADM","parameters":[],"schedule":"@monthend","benchmark":"SPY"}`),
			DiscoveredAt:     at,
			UpdatedAt:        at,
		}
		// One pending strategy (listing-only)
		store.rows["bogus"] = strategy.Strategy{
			ShortCode:    "bogus",
			RepoOwner:    "penny-vault",
			RepoName:     "bogus",
			IsOfficial:   true,
			DiscoveredAt: at,
			UpdatedAt:    at,
		}
	})

	run := func(method, path string) (int, []byte) {
		app := fiber.New()
		h := strategy.NewHandler(store)
		app.Get("/strategies", h.List)
		app.Get("/strategies/:shortCode", h.Get)

		resp, err := app.Test(httptest.NewRequest(method, path, nil))
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		return resp.StatusCode, body
	}

	It("lists strategies", func() {
		status, body := run("GET", "/strategies")
		Expect(status).To(Equal(200))

		var out []map[string]any
		Expect(sonic.Unmarshal(body, &out)).To(Succeed())
		Expect(out).To(HaveLen(2))
	})

	It("returns install state on a ready strategy", func() {
		status, body := run("GET", "/strategies/adm")
		Expect(status).To(Equal(200))

		var out map[string]any
		Expect(sonic.Unmarshal(body, &out)).To(Succeed())
		Expect(out["installState"]).To(Equal("ready"))
		Expect(out["installedVer"]).To(Equal("v1.0.0"))
		Expect(out["describe"]).NotTo(BeNil())
	})

	It("returns install state on a pending strategy with no describe", func() {
		status, body := run("GET", "/strategies/bogus")
		Expect(status).To(Equal(200))

		var out map[string]any
		Expect(sonic.Unmarshal(body, &out)).To(Succeed())
		Expect(out["installState"]).To(Equal("pending"))
		Expect(out["describe"]).To(BeNil())
	})

	It("returns 404 on unknown short_code", func() {
		status, body := run("GET", "/strategies/nope")
		Expect(status).To(Equal(404))
		Expect(string(body)).To(ContainSubstring("application/problem+json"), "response should be problem+json")
	})
})
```

- [ ] **Step 2: Run to confirm red**

```bash
ginkgo run ./strategy
```
Expected: FAIL — `strategy.NewHandler` undefined.

- [ ] **Step 3: Implement `strategy/handler.go`**

Create `strategy/handler.go`:

```go
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
	"encoding/json"
	"errors"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
)

// ReadStore is the subset of Store operations the handler uses.
type ReadStore interface {
	List(ctx context.Context) ([]Strategy, error)
	Get(ctx context.Context, shortCode string) (Strategy, error)
}

// Handler serves the GET /strategies endpoints.
type Handler struct {
	store ReadStore
}

// NewHandler constructs a handler backed by the given read-only store.
func NewHandler(store ReadStore) *Handler {
	return &Handler{store: store}
}

// List implements GET /strategies.
func (h *Handler) List(c fiber.Ctx) error {
	rows, err := h.store.List(c.Context())
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	out := make([]strategyView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toView(r))
	}
	body, err := sonic.Marshal(out)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	c.Set(fiber.HeaderContentType, "application/json")
	return c.Status(fiber.StatusOK).Send(body)
}

// Get implements GET /strategies/{shortCode}.
func (h *Handler) Get(c fiber.Ctx) error {
	shortCode := c.Params("shortCode")
	row, err := h.store.Get(c.Context(), shortCode)
	if errors.Is(err, ErrNotFound) {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "strategy not found: "+shortCode)
	}
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	body, err := sonic.Marshal(toView(row))
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	c.Set(fiber.HeaderContentType, "application/json")
	return c.Status(fiber.StatusOK).Send(body)
}

// strategyView is the JSON shape returned by the handler. It mirrors the
// OpenAPI `Strategy` schema. Kept in this package to avoid pulling in the
// openapi package; Plan 4+ may consolidate once they need it too.
type strategyView struct {
	ShortCode        string    `json:"shortCode"`
	RepoOwner        string    `json:"repoOwner"`
	RepoName         string    `json:"repoName"`
	CloneURL         string    `json:"cloneUrl,omitempty"`
	IsOfficial       bool      `json:"isOfficial"`
	OwnerSub         *string   `json:"ownerSub,omitempty"`
	Description      *string   `json:"description,omitempty"`
	Categories       []string  `json:"categories,omitempty"`
	Stars            *int      `json:"stars,omitempty"`
	InstallState     string    `json:"installState"`
	InstalledVer     *string   `json:"installedVer,omitempty"`
	LastAttemptedVer *string   `json:"lastAttemptedVer,omitempty"`
	InstallError     *string   `json:"installError,omitempty"`
	InstalledAt      *string   `json:"installedAt,omitempty"`
	Describe         *Describe `json:"describe,omitempty"`
	CAGR             *float64  `json:"cagr,omitempty"`
	MaxDrawdown      *float64  `json:"maxDrawDown,omitempty"`
	Sharpe           *float64  `json:"sharpe,omitempty"`
}

func toView(s Strategy) strategyView {
	v := strategyView{
		ShortCode:        s.ShortCode,
		RepoOwner:        s.RepoOwner,
		RepoName:         s.RepoName,
		CloneURL:         s.CloneURL,
		IsOfficial:       s.IsOfficial,
		OwnerSub:         s.OwnerSub,
		Description:      s.Description,
		Categories:       s.Categories,
		Stars:            s.Stars,
		InstallState:     string(s.DeriveInstallState()),
		InstalledVer:     s.InstalledVer,
		LastAttemptedVer: s.LastAttemptedVer,
		InstallError:     s.InstallError,
		CAGR:             s.CAGR,
		MaxDrawdown:      s.MaxDrawdown,
		Sharpe:           s.Sharpe,
	}
	if s.InstalledAt != nil {
		t := s.InstalledAt.UTC().Format("2006-01-02T15:04:05Z")
		v.InstalledAt = &t
	}
	if len(s.DescribeJSON) > 0 {
		var d Describe
		if err := json.Unmarshal(s.DescribeJSON, &d); err == nil {
			v.Describe = &d
		}
	}
	return v
}

// writeProblem emits an RFC 7807 problem+json body. Mirrors the api package's
// helper but is local to strategy/ so this package doesn't import api/.
func writeProblem(c fiber.Ctx, status int, title, detail string) error {
	type problem struct {
		Type     string `json:"type"`
		Title    string `json:"title"`
		Status   int    `json:"status"`
		Detail   string `json:"detail,omitempty"`
		Instance string `json:"instance,omitempty"`
	}
	body, _ := sonic.Marshal(problem{
		Type: "about:blank", Title: title, Status: status, Detail: detail, Instance: c.Path(),
	})
	c.Set(fiber.HeaderContentType, "application/problem+json")
	return c.Status(status).Send(body)
}
```

- [ ] **Step 4: Wire into `api/strategies.go`**

Read `api/strategies.go`. Replace its contents with:

```go
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
	"github.com/gofiber/fiber/v3"

	"github.com/penny-vault/pv-api/strategy"
)

// StrategyHandler is the real-handler shim owned by api/. It delegates
// to strategy.Handler for GET endpoints; POST stays 501 until Plan 7.
type StrategyHandler struct {
	inner *strategy.Handler
}

// NewStrategyHandler builds a StrategyHandler backed by the given read store.
func NewStrategyHandler(store strategy.ReadStore) *StrategyHandler {
	return &StrategyHandler{inner: strategy.NewHandler(store)}
}

// RegisterStrategyRoutes mounts the strategy endpoints on the provided
// router. The zero-value argument keeps compatibility with Plan 2's stub
// signature; callers that want real handlers use RegisterStrategyRoutesWith.
func RegisterStrategyRoutes(r fiber.Router) {
	r.Get("/strategies", stubListStrategies)
	r.Post("/strategies", stubRegisterUnofficialStrategy)
	r.Get("/strategies/:shortCode", stubGetStrategy)
}

// RegisterStrategyRoutesWith mounts the strategy endpoints, delegating to
// the given handler for GETs. POST stays 501.
func RegisterStrategyRoutesWith(r fiber.Router, h *StrategyHandler) {
	r.Get("/strategies", h.inner.List)
	r.Post("/strategies", stubRegisterUnofficialStrategy)
	r.Get("/strategies/:shortCode", h.inner.Get)
}

func stubListStrategies(c fiber.Ctx) error             { return WriteProblem(c, ErrNotImplemented) }
func stubRegisterUnofficialStrategy(c fiber.Ctx) error { return WriteProblem(c, ErrNotImplemented) }
func stubGetStrategy(c fiber.Ctx) error                { return WriteProblem(c, ErrNotImplemented) }
```

- [ ] **Step 5: Run to confirm green**

```bash
ginkgo run -r
```
Expected: full suite passes. Strategy suite has 12 specs (previous 8 + 4 new handler). Api suite unchanged.

- [ ] **Step 6: Commit**

```bash
git add strategy/handler.go strategy/handler_test.go api/strategies.go
git commit -m "$(cat <<'EOF'
add strategy.Handler + wire into api/strategies.go

Handler serves GET /strategies and GET /strategies/{shortCode} from
any ReadStore implementation. The JSON view mirrors the OpenAPI
Strategy schema (installState derived from columns, describe parsed
from describe_json). api/strategies.go now exports both the
Plan-2-compatible RegisterStrategyRoutes (stubs only) and a new
RegisterStrategyRoutesWith that mounts the real handler.
EOF
)"
```

---

## Task 11: Wire the sync goroutine into `api.NewApp` and `cmd/server.go`

**Files:**
- Create: `strategy/store.go`
- Modify: `api/server.go`
- Modify: `cmd/server.go`

- [ ] **Step 1: Add a pgxpool-backed Store**

Create `strategy/store.go`:

```go
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

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolStore adapts *pgxpool.Pool to the Store interface.
type PoolStore struct {
	Pool *pgxpool.Pool
}

func (p PoolStore) List(ctx context.Context) ([]Strategy, error) {
	return List(ctx, p.Pool)
}

func (p PoolStore) Get(ctx context.Context, shortCode string) (Strategy, error) {
	return Get(ctx, p.Pool, shortCode)
}

func (p PoolStore) Upsert(ctx context.Context, s Strategy) error {
	return Upsert(ctx, p.Pool, s)
}

func (p PoolStore) MarkAttempt(ctx context.Context, shortCode, version string) error {
	return MarkAttempt(ctx, p.Pool, shortCode, version)
}

func (p PoolStore) MarkSuccess(ctx context.Context, shortCode, version, kind, ref string, describe []byte) error {
	return MarkSuccess(ctx, p.Pool, shortCode, version, kind, ref, describe)
}

func (p PoolStore) MarkFailure(ctx context.Context, shortCode, version, errText string) error {
	return MarkFailure(ctx, p.Pool, shortCode, version, errText)
}
```

- [ ] **Step 2: Update `api.NewApp` to accept a registry config and start the syncer**

Read `api/server.go`. Replace its contents with:

```go
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
	"fmt"
	"path/filepath"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/penny-vault/pv-api/strategy"
)

// Config holds HTTP-layer configuration.
type Config struct {
	Port         int
	AllowOrigins string
	Auth         AuthConfig
	Registry     RegistryConfig
	Pool         *pgxpool.Pool // optional: if set, real handlers mount; otherwise stubs
}

// RegistryConfig configures the strategy registry sync and its install
// coordinator.
type RegistryConfig struct {
	GitHubToken    string
	SyncInterval   time.Duration
	Concurrency    int
	OfficialDir    string
	GitHubOwner    string // "penny-vault" in prod
	CacheDir       string // GitHub Search cache directory
}

// NewApp builds a Fiber v3 app with pvapi's middleware stack and routes.
// /healthz is public; every other route is mounted under the auth middleware.
// ctx controls the JWK cache and (if the pool is non-nil) the strategy
// sync goroutine. When a non-nil pool is supplied, the registry sync is
// started in the background.
func NewApp(ctx context.Context, conf Config) (*fiber.App, error) {
	app := fiber.New(fiber.Config{
		JSONEncoder: sonic.Marshal,
		JSONDecoder: sonic.Unmarshal,
	})

	app.Use(requestIDMiddleware())
	app.Use(timerMiddleware())

	if conf.AllowOrigins != "" {
		app.Use(cors.New(cors.Config{
			AllowOrigins: []string{conf.AllowOrigins},
		}))
	}

	app.Use(loggerMiddleware())

	app.Get("/healthz", Healthz)

	auth, err := NewAuthMiddleware(ctx, conf.Auth)
	if err != nil {
		return nil, fmt.Errorf("build auth middleware: %w", err)
	}
	protected := app.Group("", auth)
	RegisterPortfolioRoutes(protected)

	if conf.Pool != nil {
		store := strategy.PoolStore{Pool: conf.Pool}
		RegisterStrategyRoutesWith(protected, NewStrategyHandler(store))

		if err := startRegistrySync(ctx, store, conf.Registry); err != nil {
			return nil, fmt.Errorf("start registry sync: %w", err)
		}
	} else {
		RegisterStrategyRoutes(protected)
	}

	return app, nil
}

// startRegistrySync spins off a goroutine that runs the strategy.Syncer
// on conf.SyncInterval. Runs independently of the HTTP server; errors are
// logged but never propagated.
func startRegistrySync(ctx context.Context, store strategy.Store, conf RegistryConfig) error {
	if conf.SyncInterval <= 0 {
		return fmt.Errorf("RegistryConfig.SyncInterval must be > 0")
	}
	if conf.OfficialDir == "" {
		return fmt.Errorf("RegistryConfig.OfficialDir must not be empty")
	}
	if conf.GitHubOwner == "" {
		return fmt.Errorf("RegistryConfig.GitHubOwner must not be empty")
	}
	cacheDir := conf.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(conf.OfficialDir, ".cache")
	}

	discovery := func(ctx context.Context) ([]strategy.Listing, error) {
		return strategy.DiscoverOfficial(ctx, strategy.DiscoverOptions{
			CacheDir:    cacheDir,
			ExpectOwner: conf.GitHubOwner,
			Token:       conf.GitHubToken,
		})
	}

	syncer := strategy.NewSyncer(store, strategy.SyncerOptions{
		Discovery:   discovery,
		ResolveVer:  strategy.ResolveVerWithGit,
		Installer:   strategy.Install,
		OfficialDir: conf.OfficialDir,
		Concurrency: conf.Concurrency,
		Interval:    conf.SyncInterval,
	})
	go func() {
		_ = syncer.Run(ctx)
	}()
	return nil
}
```

- [ ] **Step 3: Update `cmd/server.go` to pass the pool + RegistryConfig**

Read `cmd/server.go`. Replace the `serverCmd` `RunE` body (everything between `RunE: func(...)` and its closing `}`) with:

```go
RunE: func(_ *cobra.Command, _ []string) error {
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()

    pool := sql.Instance(ctx)

    app, err := api.NewApp(ctx, api.Config{
        Port:         conf.Server.Port,
        AllowOrigins: conf.Server.AllowOrigins,
        Auth: api.AuthConfig{
            JWKSURL:  conf.Auth0.JWKSURL,
            Audience: conf.Auth0.Audience,
            Issuer:   conf.Auth0.Issuer,
        },
        Pool: pool,
        Registry: api.RegistryConfig{
            GitHubToken:  conf.GitHub.Token,
            SyncInterval: conf.Strategy.RegistrySyncInterval,
            Concurrency:  conf.Strategy.InstallConcurrency,
            OfficialDir:  conf.Strategy.OfficialDir,
            GitHubOwner:  "penny-vault",
        },
    })
    if err != nil {
        return fmt.Errorf("build app: %w", err)
    }

    errCh := make(chan error, 1)
    addr := fmt.Sprintf(":%d", conf.Server.Port)

    go func() {
        log.Info().Str("addr", addr).Msg("server listening")
        if err := app.Listen(addr, fiber.ListenConfig{DisableStartupMessage: true}); err != nil {
            errCh <- err
        }
        close(errCh)
    }()

    select {
    case err := <-errCh:
        if err != nil {
            return err
        }
        return nil
    case <-ctx.Done():
        log.Info().Msg("shutdown signal received")
        if err := app.ShutdownWithContext(ctx); err != nil {
            return fmt.Errorf("fiber shutdown: %w", err)
        }
        return nil
    }
},
```

Add `"github.com/penny-vault/pv-api/sql"` to the imports at the top of `cmd/server.go` if not already present.

- [ ] **Step 4: Adjust `api/server_test.go` to pass a nil Pool (keep Plan 2's tests green)**

Read `api/server_test.go`. The `newApp` helper currently calls `api.NewApp(...)` with the existing Config fields. Since `Pool` now defaults to nil, no change is required — the test continues to hit stub handlers. Verify by running:

```bash
ginkgo run ./api
```
Expected: all api specs still pass (healthz + middleware + NewApp + auth + portfolio stubs + strategy stubs).

- [ ] **Step 5: Full build and test**

```bash
go build ./...
ginkgo run -r
```
Expected: all packages compile; strategy + api suites both green.

- [ ] **Step 6: Commit**

```bash
git add strategy/store.go api/server.go cmd/server.go
git commit -m "$(cat <<'EOF'
wire strategy sync into api.NewApp + cmd/server.go

NewApp now takes an optional *pgxpool.Pool and a RegistryConfig.
When Pool is non-nil, real strategy handlers mount and a background
sync goroutine starts. Passing a nil Pool (as api/server_test.go
does) preserves Plan 2 stub behavior. cmd/server.go acquires the
pool via sql.Instance and wires conf.GitHub / conf.Strategy into
the registry config.
EOF
)"
```

---

## Task 12: End-to-end smoke

**Purpose:** Build the binary, run `pvapi server` against a real Postgres with the new migration, confirm startup is non-blocking, `/strategies` returns data after the first sync completes.

- [ ] **Step 1: Clean build**

```bash
make build
```
Expected: `pvapi` binary produced. No errors.

- [ ] **Step 2: Lint**

```bash
make lint
```
Expected: `0 issues.`

- [ ] **Step 3: Full test run**

```bash
ginkgo run -r --race
```
Expected:
- `Api Suite`: all pre-existing specs green.
- `Strategy Suite`: 12 specs green (1 github + 3 install + 4 sync + 4 handler).

- [ ] **Step 4: Start a local Postgres and run migrations**

If no local Postgres instance is available, STOP and ask the user how they'd like to run the smoke. Otherwise:

```bash
# Create the target database if needed; the embedded migrations will run
# on first pool access inside `pvapi server`.
createdb pvapi 2>/dev/null || true
```

- [ ] **Step 5: Live smoke**

Ensure you have a local `pvapi.toml` with valid `[db]`, `[auth0]` stub values, and a real `[github].token` if rate limits are a concern.

```bash
mv pvapi /tmp/pvapi-smoke     # move out of cwd so viper doesn't treat it as a config file
/tmp/pvapi-smoke server \
  --server-port 3030 \
  --strategy-registry-sync-interval 5s \
  > /tmp/pvapi-smoke.log 2>&1 &
PID=$!
sleep 2
echo "--- healthz ---"
curl -s -o /dev/null -w 'healthz=%{http_code}\n' http://localhost:3030/healthz
echo "--- strategies no-auth ---"
curl -s -w 'status=%{http_code}\n' http://localhost:3030/strategies | head -c 200
echo ""
echo "--- log tail ---"
tail -20 /tmp/pvapi-smoke.log
kill -TERM $PID
wait $PID 2>/dev/null
rm -f /tmp/pvapi-smoke /tmp/pvapi-smoke.log
```

Expected:
- `healthz=200`
- `/strategies` without a JWT returns `401` (auth middleware working).
- Log shows `server listening` and at least one `strategy sync tick` entry within a few seconds of startup — even if the first tick produced zero installable strategies, the log line is the acceptance criterion.
- Clean shutdown on SIGTERM, exit code 0.

If `pvapi` cannot contact GitHub (network restricted), the smoke's acceptance criterion falls back to: `healthz=200`, `/strategies` returns 401 without a JWT, and the log shows the discovery fetch failing with a clear error — no panic, server keeps running.

- [ ] **Step 6: Push the branch**

```bash
git push -u origin pvapi-3-strategy-registry
```

No commit on this task — verification only. Plan 3 is complete when every step above passes.

---

## Self-review summary

- **Spec coverage:**
  - Strategy lifecycle / registry sync (spec § Strategy lifecycle) → Tasks 7–9, 11.
  - Install-tracking schema (spec § Data model, `last_attempted_ver` / `install_error`) → Task 2.
  - Install coordinator (spec § Install coordinator, bounded concurrency, atomic flip, retry-on-upstream-change) → Tasks 8–9.
  - OpenAPI Strategy expansion + describe types (spec § Strategy schema) → Task 4.
  - `[github]` and `[strategy]` config blocks (spec § Config) → Task 3.
  - Non-blocking startup (spec § Registry sync) → Task 11 starts sync in a detached goroutine.
  - Auth gating for `/strategies` (spec § Auth) → Task 11 mounts handlers under `auth` group.
  - Testing: bare git repo in `testdata/`, httpmock for GitHub (spec § Testing — strategy/ suite) → Tasks 7, 8, 9.
  - `POST /strategies` remains 501 → Task 10 (stub kept).

- **Placeholder scan:** no `TBD`, no "handle edge cases later", no "similar to Task N" references. Every code block is complete.

- **Type consistency:**
  - `strategy.Store` (Task 9) used by `strategy.Syncer` + adapted by `strategy.PoolStore` (Task 11).
  - `strategy.ReadStore` (Task 10) used by `strategy.Handler`; `PoolStore` (Task 11) also satisfies it because `PoolStore.List` / `.Get` match.
  - `strategy.Strategy` columns in Task 5 line up with `scan` in Task 6 and `toView` in Task 10.
  - `strategy.InstallRequest` fields match between Install (Task 8), Syncer (Task 9), and NewApp (Task 11).
  - `api.Config.Pool` and `api.RegistryConfig` names are introduced in Task 11 and consumed in `cmd/server.go` the same task; no drift.
  - Commit SHAs and commit messages follow the conventional-commit style used in Plans 1 and 2.
