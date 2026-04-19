# pvapi 3.0 Portfolio CRUD Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the first user-facing portfolio endpoints: create, list, get (config-only), patch-name, delete. Build the new `portfolio/` package with pure-function slug generation and parameter validation against the strategy registry's `describe_json`. All derived-data endpoints (summary, drawdowns, metrics, trailing-returns, holdings, measurements, runs) stay 501 until Plan 5.

**Architecture:**
- New `portfolio/` package mirrors `strategy/`'s layout: `db.go` (pgxpool CRUD, ownership-scoped), `store.go` (PoolStore adapter), `slug.go` (FNV-1a + kebab-case preset match, pure), `validate.go` (create-request validation against strategy describe, pure), `handler.go` (fiber handlers).
- `portfolio.Create` is a single atomic step: it validates, computes the slug, and inserts — no orchestration spread across layers.
- PATCH accepts `name` only; anything else is a `422`. Slug stays immutable.
- Ownership is enforced at the SQL level: every read / mutation carries `WHERE owner_sub = $1`.
- Tests follow the strategy-package pattern: fake in-memory `Store` for handler specs, pure-function tests for `slug` and `validate`, no live DB.
- Wiring follows the same pool-optional pattern Plans 2+3 established: `api.NewApp` takes an optional `*pgxpool.Pool`; when non-nil, real portfolio handlers mount.

**Tech Stack:** Go 1.25+, Fiber v3, `github.com/jackc/pgx/v5/pgxpool`, `github.com/bytedance/sonic`, `hash/fnv` + `encoding/base32` from stdlib, Ginkgo/Gomega.

**Reference spec:** `docs/superpowers/specs/2026-04-16-pvapi-3-0-design.md`

**Worktree:** branch from `main`.

```bash
cd /Users/jdf/Developer/penny-vault/pv-api
git worktree add .worktrees/pvapi-3-portfolio-crud -b pvapi-3-portfolio-crud main
cd .worktrees/pvapi-3-portfolio-crud
```

---

## File overview

**Created**

```
sql/migrations/4_add_live_mode.up.sql       -- ALTER TYPE portfolio_mode ADD VALUE 'live'
sql/migrations/4_add_live_mode.down.sql     -- no-op (Postgres cannot drop enum values)
portfolio/                                  -- new domain package
  doc.go                                    -- package doc
  types.go                                  -- Portfolio, CreateRequest, UpdateRequest domain types
  db.go                                     -- pgxpool reads/writes scoped by owner_sub
  store.go                                  -- Store interface + PoolStore adapter
  slug.go                                   -- Slug(CreateRequest, describe) (pure)
  slug_test.go                              -- preset match + FNV-1a hash determinism
  validate.go                               -- Validate(req, strategy) (pure)
  validate_test.go                          -- every rule from the spec
  handler.go                                -- POST/GET list/GET one/PATCH/DELETE
  handler_test.go                           -- fake-store specs, owner_sub scoping
  portfolio_suite_test.go                   -- ginkgo suite runner
```

**Modified**

```
openapi/openapi.yaml                        -- PortfolioMode enum adds `live`;
                                               Portfolio schema strips derived fields;
                                               PortfolioUpdateRequest narrows to name only;
                                               adds Holding/HoldingsResponse; adds new derived
                                               endpoints /summary /drawdowns /metrics
                                               /trailing-returns /holdings /holdings/{date}
openapi/openapi.gen.go                      -- regenerated via `make gen`
api/portfolios.go                           -- replace stub RegisterPortfolioRoutes body;
                                               add RegisterPortfolioRoutesWith that delegates
                                               to portfolio.Handler for real endpoints and
                                               mounts 501 stubs for every derived endpoint
api/server.go                               -- mount real portfolio handler when Pool != nil
```

**Unchanged**

```
strategy/                                   (Plan 3 complete; portfolio validates against it)
api/auth.go, api/errors.go, api/middleware.go, api/health.go
api/server_test.go                          (still passes nil pool, so stubs remain for tests)
sql/pool.go, sql/migrate.go
cmd/                                        (no new flags; portfolio is handler-only)
types/types.go
```

---

## Task 1: Migration `4_add_live_mode`

**Files:**
- Create: `sql/migrations/4_add_live_mode.up.sql`
- Create: `sql/migrations/4_add_live_mode.down.sql`

- [ ] **Step 1: Write the up migration**

Create `sql/migrations/4_add_live_mode.up.sql`:

```sql
-- 4_add_live_mode.up.sql
-- Reserves `live` in the portfolio_mode enum. The POST /portfolios handler
-- rejects mode=live with 422 for the entirety of the 3.0 rewrite; real live
-- trading is a separate future project (see design spec § Live trading).
--
-- Postgres lets us ALTER TYPE ... ADD VALUE outside a transaction so long as
-- the value has not been used yet. golang-migrate does not wrap PG migrations
-- in an implicit transaction; this single-statement file runs as-is.

ALTER TYPE portfolio_mode ADD VALUE IF NOT EXISTS 'live';
```

- [ ] **Step 2: Write the down migration**

Create `sql/migrations/4_add_live_mode.down.sql`:

```sql
-- 4_add_live_mode.down.sql
-- Postgres does not support dropping values from an enum. This migration is
-- therefore a no-op; a full rollback requires dropping and recreating the
-- portfolio_mode type and every column that references it, which is out of
-- scope for a reversible migration.

SELECT 1;
```

- [ ] **Step 3: Verify build picks up both files**

```bash
go build ./sql/...
```
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add sql/migrations/4_add_live_mode.up.sql sql/migrations/4_add_live_mode.down.sql
git commit -m "$(cat <<'EOF'
add 4_add_live_mode migration

Reserves `live` in portfolio_mode so the enum covers every mode
named in the OpenAPI PortfolioMode schema. POST /portfolios still
returns 422 for mode=live throughout the 3.0 rewrite. Down is a
no-op — Postgres does not support dropping enum values.
EOF
)"
```

---

## Task 2: OpenAPI schema updates + regen

**Files:**
- Modify: `openapi/openapi.yaml`
- Modify: `openapi/openapi.gen.go` (via `make gen`)

### Step 1: Add `live` to `PortfolioMode`

In `openapi/openapi.yaml` under `components.schemas.PortfolioMode:`, replace:

```yaml
    PortfolioMode:
      type: string
      enum: [one_shot, continuous]
```

with:

```yaml
    PortfolioMode:
      type: string
      enum: [one_shot, continuous, live]
      description: |
        Portfolio execution mode. `live` is reserved but rejected by
        POST /portfolios with 422 until a future live-trading project ships.
```

### Step 2: Strip derived fields from `Portfolio` schema

Find the `Portfolio:` schema (config + derived bundled together). Replace the entire block with:

```yaml
    Portfolio:
      type: object
      description: Portfolio configuration + status. Derived backtest output lives on separate endpoints.
      required:
        - slug
        - name
        - strategyCode
        - strategyVer
        - parameters
        - benchmark
        - mode
        - status
        - createdAt
        - updatedAt
      properties:
        slug:
          type: string
          example: 'adm-aggressive-gm59'
        name:
          type: string
          example: 'ADM aggressive'
        strategyCode:
          type: string
        strategyVer:
          type: string
        parameters:
          type: object
          additionalProperties: true
        presetName:
          type: string
          nullable: true
          description: Name of the matched strategy preset, or null when parameters did not match a preset.
        benchmark:
          type: string
          example: 'SPY'
        mode:
          $ref: '#/components/schemas/PortfolioMode'
        schedule:
          type: string
          nullable: true
          description: tradecron string; null when mode=one_shot.
        status:
          $ref: '#/components/schemas/PortfolioStatus'
        createdAt:
          type: string
          format: date-time
        updatedAt:
          type: string
          format: date-time
        lastRunAt:
          type: string
          format: date-time
          nullable: true
        lastError:
          type: string
          nullable: true
```

### Step 3: Narrow `PortfolioUpdateRequest`

Replace the existing `PortfolioUpdateRequest:` block with:

```yaml
    PortfolioUpdateRequest:
      type: object
      description: PATCH body. Only `name` is mutable — changing parameters, schedule, benchmark, or mode would break the slug invariant, so those fields are rejected with 422.
      required: [name]
      properties:
        name:
          type: string
      additionalProperties: false
```

### Step 4: Update `PortfolioListItem` to mark derived KPIs optional

Find `PortfolioListItem:` in `components.schemas`. Replace its `required` list so derived KPIs are optional (pending portfolios appear in lists with just config + status):

```yaml
    PortfolioListItem:
      type: object
      description: Row in the portfolios list. Derived KPIs are populated when the portfolio has completed at least one successful run; absent for pending portfolios.
      required: [slug, name, strategyCode, mode, status, createdAt]
      properties:
        slug:
          type: string
        name:
          type: string
        strategyCode:
          type: string
        mode:
          $ref: '#/components/schemas/PortfolioMode'
        status:
          $ref: '#/components/schemas/PortfolioStatus'
        createdAt:
          type: string
          format: date-time
        updatedAt:
          type: string
          format: date-time
        benchmark:
          type: string
        inceptionDate:
          type: string
          format: date
          nullable: true
        currentValue:
          type: number
          format: double
          nullable: true
        ytdReturn:
          type: number
          format: double
          nullable: true
        maxDrawDown:
          type: number
          format: double
          nullable: true
        lastUpdated:
          type: string
          format: date-time
          nullable: true
```

### Step 5: Add `Holding` and `HoldingsResponse` schemas

Under `components.schemas:`, append (alphabetical — place after `HoldingsResponse` if it exists, or near `Drawdown`):

```yaml
    Holding:
      type: object
      required: [ticker, quantity, avgCost, marketValue]
      properties:
        ticker:
          type: string
          example: 'VTI'
        figi:
          type: string
          nullable: true
        quantity:
          type: number
          format: double
        avgCost:
          type: number
          format: double
        marketValue:
          type: number
          format: double
        dayChange:
          type: number
          format: double
          nullable: true
          description: Decimal percent since previous close.

    HoldingsResponse:
      type: object
      required: [date, items, totalMarketValue]
      properties:
        date:
          type: string
          format: date
        items:
          type: array
          items:
            $ref: '#/components/schemas/Holding'
        totalMarketValue:
          type: number
          format: double
```

### Step 6: Add the new derived-data endpoints

Under `paths:`, append (just before or after the existing `/portfolios/{slug}/runs` block — keep all `/portfolios/{slug}/...` paths together):

```yaml
  /portfolios/{slug}/summary:
    get:
      tags: [Portfolios]
      operationId: getPortfolioSummary
      summary: Top-line KPIs from the latest successful backtest
      parameters:
        - $ref: '#/components/parameters/PortfolioSlug'
      responses:
        '200':
          description: Summary
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/PortfolioSummary'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
        '500':
          $ref: '#/components/responses/ServerError'

  /portfolios/{slug}/drawdowns:
    get:
      tags: [Portfolios]
      operationId: getPortfolioDrawdowns
      summary: Drawdown list ordered by depth (deepest first)
      parameters:
        - $ref: '#/components/parameters/PortfolioSlug'
      responses:
        '200':
          description: Drawdowns
          content:
            application/json:
              schema:
                type: array
                items:
                  $ref: '#/components/schemas/Drawdown'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
        '500':
          $ref: '#/components/responses/ServerError'

  /portfolios/{slug}/metrics:
    get:
      tags: [Portfolios]
      operationId: getPortfolioMetrics
      summary: Generic label/value/format metric rows
      parameters:
        - $ref: '#/components/parameters/PortfolioSlug'
      responses:
        '200':
          description: Metrics
          content:
            application/json:
              schema:
                type: array
                items:
                  $ref: '#/components/schemas/PortfolioMetric'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
        '500':
          $ref: '#/components/responses/ServerError'

  /portfolios/{slug}/trailing-returns:
    get:
      tags: [Portfolios]
      operationId: getPortfolioTrailingReturns
      summary: Trailing-returns rows (portfolio, benchmark, portfolio-tax, benchmark-tax)
      parameters:
        - $ref: '#/components/parameters/PortfolioSlug'
      responses:
        '200':
          description: Trailing returns
          content:
            application/json:
              schema:
                type: array
                items:
                  $ref: '#/components/schemas/TrailingReturnRow'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
        '500':
          $ref: '#/components/responses/ServerError'

  /portfolios/{slug}/holdings:
    get:
      tags: [Portfolios]
      operationId: getPortfolioHoldings
      summary: Latest holdings
      parameters:
        - $ref: '#/components/parameters/PortfolioSlug'
      responses:
        '200':
          description: Holdings
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/HoldingsResponse'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
        '500':
          $ref: '#/components/responses/ServerError'

  /portfolios/{slug}/holdings/{date}:
    get:
      tags: [Portfolios]
      operationId: getPortfolioHoldingsAsOf
      summary: Historical holdings as of a given date
      parameters:
        - $ref: '#/components/parameters/PortfolioSlug'
        - name: date
          in: path
          required: true
          schema:
            type: string
            format: date
      responses:
        '200':
          description: Holdings as of the given date
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/HoldingsResponse'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
        '500':
          $ref: '#/components/responses/ServerError'
```

### Step 7: Remove the derived properties from `AllocationRow` / drop entirely

Check whether `AllocationRow` still has any referrers. If `Portfolio` no longer references it (after Step 2) and no endpoint response references it, delete the `AllocationRow:` schema block.

To confirm: `grep -n 'AllocationRow' openapi/openapi.yaml` should show only the schema definition line if all references are gone. If any path still references it, that path is in an old response shape and should be fixed.

If removing, delete the block.

### Step 8: Regenerate types

```bash
make gen
```
Expected: `openapi/openapi.gen.go` is rewritten. No stderr errors.

### Step 9: Verify build and existing tests

```bash
go build ./...
ginkgo run -r
```
Expected: build clean; 42 specs still pass (30 api + 12 strategy — portfolio package doesn't exist yet).

### Step 10: Commit

```bash
git add openapi/openapi.yaml openapi/openapi.gen.go
git commit -m "$(cat <<'EOF'
flatten portfolio OpenAPI surface, narrow update body, add live mode

PortfolioMode enum gains `live`. Portfolio schema strips derived
fields and becomes config+status only. PortfolioUpdateRequest is
narrowed to a required `name` field with additionalProperties=false.
PortfolioListItem marks derived KPIs as optional.

Adds /summary /drawdowns /metrics /trailing-returns /holdings
/holdings/{date} endpoints (stubs until Plan 5). New Holding +
HoldingsResponse schemas; AllocationRow removed as /holdings
supersedes it. dayChange carries on the Holding schema.
EOF
)"
```

---

## Task 3: `portfolio` package scaffold

**Files:**
- Create: `portfolio/doc.go`
- Create: `portfolio/types.go`
- Create: `portfolio/portfolio_suite_test.go`

- [ ] **Step 1: Package doc**

Create `portfolio/doc.go`:

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

// Package portfolio implements the pvapi 3.0 portfolio CRUD slice:
// create / list / get config / patch name / delete. Slug generation
// and create-request validation live here; derived-data endpoints
// (summary / drawdowns / metrics / holdings / measurements / runs)
// stay as 501 stubs in api/ until the backtest runner arrives in
// Plan 5.
package portfolio
```

- [ ] **Step 2: Domain types**

Create `portfolio/types.go`:

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

package portfolio

import (
	"time"

	"github.com/google/uuid"
)

// Mode mirrors the portfolio_mode enum.
type Mode string

const (
	ModeOneShot    Mode = "one_shot"
	ModeContinuous Mode = "continuous"
	ModeLive       Mode = "live"
)

// Status mirrors the portfolio_status enum.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusReady   Status = "ready"
	StatusFailed  Status = "failed"
)

// Portfolio is the internal representation of a portfolios row — config
// fields only. Derived summary columns (`current_value`, `ytd_return`,
// JSONB blobs) are not exposed here; Plan 5 adds a separate derived-row
// shape when the runner starts populating those columns.
type Portfolio struct {
	ID           uuid.UUID
	OwnerSub     string
	Slug         string
	Name         string
	StrategyCode string
	StrategyVer  string
	Parameters   map[string]any
	PresetName   *string
	Benchmark    string
	Mode         Mode
	Schedule     *string
	Status       Status
	LastRunAt    *time.Time
	LastError    *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CreateRequest is what the POST /portfolios handler hands off to the
// domain layer. Mirrors the OpenAPI PortfolioCreateRequest.
type CreateRequest struct {
	Name         string
	StrategyCode string
	StrategyVer  string // empty → use strategy's installed_ver
	Parameters   map[string]any
	Benchmark    string // empty → use strategy's describe.benchmark
	Mode         Mode
	Schedule     string // required iff Mode == ModeContinuous
	RunNow       bool   // accepted but no-op in Plan 4
}

// UpdateRequest is what PATCH /portfolios/{slug} hands off. Name-only.
type UpdateRequest struct {
	Name string
}
```

- [ ] **Step 3: Ginkgo suite runner**

Create `portfolio/portfolio_suite_test.go`:

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

package portfolio_test

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPortfolio(t *testing.T) {
	prev := log.Logger
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: GinkgoWriter})
	defer func() { log.Logger = prev }()

	RegisterFailHandler(Fail)
	RunSpecs(t, "Portfolio Suite")
}
```

- [ ] **Step 4: Verify build**

```bash
go build ./portfolio/...
ginkgo run ./portfolio
```
Expected: build clean; `Ran 0 of 0 Specs` — correct red state, specs added in later tasks.

- [ ] **Step 5: Commit**

```bash
git add portfolio/doc.go portfolio/types.go portfolio/portfolio_suite_test.go
git commit -m "$(cat <<'EOF'
scaffold portfolio package with domain types

Adds Portfolio, Mode (one_shot/continuous/live), Status, CreateRequest,
UpdateRequest — the shapes consumed by slug, validate, db, and handler
in later tasks.
EOF
)"
```

---

## Task 4: `portfolio/slug.go` + tests

**Files:**
- Create: `portfolio/slug.go`
- Create: `portfolio/slug_test.go`

- [ ] **Step 1: Write failing tests**

Create `portfolio/slug_test.go`:

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

package portfolio_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("Slug", func() {
	admDescribe := strategy.Describe{
		ShortCode: "adm",
		Presets: []strategy.DescribePreset{
			{Name: "standard", Parameters: map[string]any{"riskOn": "VFINX,PRIDX,QQQ"}},
			{Name: "aggressive", Parameters: map[string]any{"riskOn": "SPY,GLD,VWO"}},
		},
	}

	It("uses the matched preset name for matching params", func() {
		slug, err := portfolio.Slug(portfolio.CreateRequest{
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY,GLD,VWO"},
			Mode:         portfolio.ModeOneShot,
			Benchmark:    "SPY",
		}, admDescribe)
		Expect(err).NotTo(HaveOccurred())
		Expect(slug).To(HavePrefix("adm-aggressive-"))
		Expect(slug).To(HaveLen(len("adm-aggressive-") + 4))
	})

	It("uses `custom` when no preset matches", func() {
		slug, err := portfolio.Slug(portfolio.CreateRequest{
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "NVDA,AMD"},
			Mode:         portfolio.ModeOneShot,
			Benchmark:    "SPY",
		}, admDescribe)
		Expect(err).NotTo(HaveOccurred())
		Expect(slug).To(HavePrefix("adm-custom-"))
	})

	It("is deterministic: identical configs produce identical slugs", func() {
		req := portfolio.CreateRequest{
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY,GLD,VWO"},
			Mode:         portfolio.ModeContinuous,
			Schedule:     "@monthend",
			Benchmark:    "SPY",
		}
		a, err := portfolio.Slug(req, admDescribe)
		Expect(err).NotTo(HaveOccurred())
		b, err := portfolio.Slug(req, admDescribe)
		Expect(err).NotTo(HaveOccurred())
		Expect(a).To(Equal(b))
	})

	It("differs when schedule differs even with the same preset", func() {
		base := portfolio.CreateRequest{
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY,GLD,VWO"},
			Mode:         portfolio.ModeContinuous,
			Benchmark:    "SPY",
		}
		base.Schedule = "@monthend"
		a, _ := portfolio.Slug(base, admDescribe)
		base.Schedule = "@daily"
		b, _ := portfolio.Slug(base, admDescribe)
		Expect(a).NotTo(Equal(b))
	})

	It("canonicalizes parameter key order (same map order-independent)", func() {
		d := strategy.Describe{ShortCode: "x"}
		a, err := portfolio.Slug(portfolio.CreateRequest{
			StrategyCode: "x", Mode: portfolio.ModeOneShot, Benchmark: "SPY",
			Parameters: map[string]any{"a": 1, "b": 2},
		}, d)
		Expect(err).NotTo(HaveOccurred())
		b, err := portfolio.Slug(portfolio.CreateRequest{
			StrategyCode: "x", Mode: portfolio.ModeOneShot, Benchmark: "SPY",
			Parameters: map[string]any{"b": 2, "a": 1},
		}, d)
		Expect(err).NotTo(HaveOccurred())
		Expect(a).To(Equal(b))
	})

	It("sanitizes preset names into kebab-case in the slug", func() {
		d := strategy.Describe{
			ShortCode: "foo",
			Presets: []strategy.DescribePreset{
				{Name: "Really Aggressive!", Parameters: map[string]any{"k": "v"}},
			},
		}
		slug, err := portfolio.Slug(portfolio.CreateRequest{
			StrategyCode: "foo",
			Parameters:   map[string]any{"k": "v"},
			Mode:         portfolio.ModeOneShot,
			Benchmark:    "SPY",
		}, d)
		Expect(err).NotTo(HaveOccurred())
		Expect(slug).To(HavePrefix("foo-really-aggressive-"))
	})
})
```

- [ ] **Step 2: Run to confirm red**

```bash
ginkgo run ./portfolio
```
Expected: FAIL — `portfolio.Slug` undefined.

- [ ] **Step 3: Implement `portfolio/slug.go`**

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

	h := fnv.New32a()
	h.Write(canon)
	h.Write([]byte{0})
	h.Write([]byte(string(req.Mode)))
	h.Write([]byte{0})
	h.Write([]byte(req.Schedule))
	h.Write([]byte{0})
	h.Write([]byte(req.Benchmark))

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
	// Defensive: if canonical JSON matches, decode back and reflect-equal.
	// Catches any odd case where a subtle difference survives JSON.
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
	// json.Marshal does not sort map keys for map[string]any; but it DOES
	// sort them for map[string]SomeKnownType when the type has explicit
	// fields. To guarantee a stable order for nested maps, re-encode via
	// a canonicalization walk.
	normalized := canonicalize(v)
	return json.Marshal(normalized)
}

// canonicalize returns an equivalent tree where every map has been
// replaced by a structure with keys sorted. Since Go's json.Marshal sorts
// map[string]any keys alphabetically (documented behavior as of Go 1.12),
// simply re-marshaling is sufficient for our case — but we still walk the
// tree defensively to make this robust against non-map containers.
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
```

- [ ] **Step 4: Run to confirm green**

```bash
ginkgo run ./portfolio
```
Expected: 6 passing specs.

- [ ] **Step 5: Commit**

```bash
git add portfolio/slug.go portfolio/slug_test.go
git commit -m "$(cat <<'EOF'
add portfolio.Slug: preset-aware FNV-1a slug generation

Deterministic <short_code>-<preset_or_custom>-<4char> slug. Preset
segment is the matched preset name (kebab-case normalized) or
`custom` when no preset's parameters match the request. 4-char
suffix is an FNV-1a 32-bit hash's low 20 bits, base32 lowercase.
Canonical-JSON walk sorts parameter keys so map iteration order
does not perturb the hash.
EOF
)"
```

---

## Task 5: `portfolio/validate.go` + tests

**Files:**
- Create: `portfolio/validate.go`
- Create: `portfolio/validate_test.go`

- [ ] **Step 1: Write failing tests**

Create `portfolio/validate_test.go`:

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

package portfolio_test

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("ValidateCreate", func() {
	installed := "v1.0.0"
	admDescribe := []byte(`{"shortCode":"adm","name":"ADM","parameters":[{"name":"riskOn","type":"universe"}],"schedule":"@monthend","benchmark":"SPY"}`)

	makeStrategy := func() strategy.Strategy {
		return strategy.Strategy{
			ShortCode:    "adm",
			RepoOwner:    "penny-vault",
			RepoName:     "adm",
			CloneURL:     "https://github.com/penny-vault/adm.git",
			IsOfficial:   true,
			InstalledVer: &installed,
			DescribeJSON: admDescribe,
		}
	}

	It("accepts a valid one_shot request", func() {
		req := portfolio.CreateRequest{
			Name:         "foo",
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY"},
			Mode:         portfolio.ModeOneShot,
		}
		norm, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(err).NotTo(HaveOccurred())
		Expect(norm.StrategyVer).To(Equal("v1.0.0"))
		Expect(norm.Benchmark).To(Equal("SPY"))
	})

	It("defaults benchmark to strategy describe benchmark when blank", func() {
		req := portfolio.CreateRequest{
			Name:         "foo",
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY"},
			Mode:         portfolio.ModeOneShot,
		}
		norm, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(err).NotTo(HaveOccurred())
		Expect(norm.Benchmark).To(Equal("SPY"))
	})

	It("rejects mode=live with ErrLiveNotSupported", func() {
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			Parameters: map[string]any{"riskOn": "SPY"},
			Mode:       portfolio.ModeLive,
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrLiveNotSupported)).To(BeTrue())
	})

	It("rejects mode=continuous without a schedule", func() {
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			Parameters: map[string]any{"riskOn": "SPY"},
			Mode:       portfolio.ModeContinuous,
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrScheduleRequired)).To(BeTrue())
	})

	It("rejects mode=one_shot with a schedule", func() {
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			Parameters: map[string]any{"riskOn": "SPY"},
			Mode:       portfolio.ModeOneShot,
			Schedule:   "@monthend",
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrScheduleForbidden)).To(BeTrue())
	})

	It("rejects a strategy version that does not match installed_ver", func() {
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			StrategyVer: "v9.9.9",
			Parameters:  map[string]any{"riskOn": "SPY"},
			Mode:        portfolio.ModeOneShot,
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrStrategyVersionMismatch)).To(BeTrue())
	})

	It("rejects when the strategy is not installed", func() {
		s := makeStrategy()
		s.InstalledVer = nil
		s.DescribeJSON = nil
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			Parameters: map[string]any{"riskOn": "SPY"},
			Mode:       portfolio.ModeOneShot,
		}
		_, err := portfolio.ValidateCreate(req, s)
		Expect(errors.Is(err, portfolio.ErrStrategyNotReady)).To(BeTrue())
	})

	It("rejects unknown parameter keys", func() {
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			Parameters: map[string]any{"riskOn": "SPY", "bogus": 42},
			Mode:       portfolio.ModeOneShot,
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrUnknownParameter)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("bogus"))
	})

	It("rejects missing required parameters", func() {
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			Parameters: map[string]any{},
			Mode:       portfolio.ModeOneShot,
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrMissingParameter)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("riskOn"))
	})
})
```

- [ ] **Step 2: Run to confirm red**

```bash
ginkgo run ./portfolio
```
Expected: FAIL — `portfolio.ValidateCreate`, `portfolio.ErrLiveNotSupported`, etc. undefined.

- [ ] **Step 3: Implement `portfolio/validate.go`**

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

package portfolio

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/penny-vault/pv-api/strategy"
)

// Validation sentinels. Each maps to a distinct error message via
// fmt.Errorf("%w: ...") so callers get a 422 detail while retaining
// errors.Is behavior.
var (
	ErrLiveNotSupported        = errors.New("live mode unavailable")
	ErrScheduleRequired        = errors.New("schedule required for continuous mode")
	ErrScheduleForbidden       = errors.New("schedule forbidden for non-continuous mode")
	ErrStrategyNotReady        = errors.New("strategy not installed")
	ErrStrategyVersionMismatch = errors.New("strategy version not installed")
	ErrUnknownParameter        = errors.New("unknown parameter")
	ErrMissingParameter        = errors.New("missing required parameter")
	ErrInvalidStrategyDescribe = errors.New("strategy describe JSON is malformed")
)

// ValidateCreate runs every check from the spec's "Create-portfolio
// validation" subsection against req + the caller-supplied strategy row.
// On success, it returns a normalized CreateRequest with
// StrategyVer and Benchmark filled from the strategy's describe output
// when the request left them blank.
func ValidateCreate(req CreateRequest, s strategy.Strategy) (CreateRequest, error) {
	norm := req

	// 4. mode=live unsupported
	if norm.Mode == ModeLive {
		return norm, fmt.Errorf("%w: live trading is not yet supported", ErrLiveNotSupported)
	}

	// 5. schedule consistency
	switch norm.Mode {
	case ModeContinuous:
		if norm.Schedule == "" {
			return norm, fmt.Errorf("%w", ErrScheduleRequired)
		}
	case ModeOneShot:
		if norm.Schedule != "" {
			return norm, fmt.Errorf("%w", ErrScheduleForbidden)
		}
	case ModeLive:
		// already handled
	default:
		return norm, fmt.Errorf("unsupported mode %q", norm.Mode)
	}

	// 2. strategy installed
	if s.InstalledVer == nil || len(s.DescribeJSON) == 0 {
		return norm, fmt.Errorf("%w: %s is still installing — try again shortly", ErrStrategyNotReady, s.ShortCode)
	}

	// 3. strategy version matches installed
	if norm.StrategyVer != "" && norm.StrategyVer != *s.InstalledVer {
		return norm, fmt.Errorf("%w: want %s, installed is %s", ErrStrategyVersionMismatch, norm.StrategyVer, *s.InstalledVer)
	}
	norm.StrategyVer = *s.InstalledVer

	// 6. parameters validate against describe
	var d strategy.Describe
	if err := json.Unmarshal(s.DescribeJSON, &d); err != nil {
		return norm, fmt.Errorf("%w: %v", ErrInvalidStrategyDescribe, err)
	}
	declared := make(map[string]struct{}, len(d.Parameters))
	for _, p := range d.Parameters {
		declared[p.Name] = struct{}{}
	}
	for k := range norm.Parameters {
		if _, ok := declared[k]; !ok {
			return norm, fmt.Errorf("%w: %s", ErrUnknownParameter, k)
		}
	}
	for _, p := range d.Parameters {
		if _, present := norm.Parameters[p.Name]; !present {
			return norm, fmt.Errorf("%w: %s", ErrMissingParameter, p.Name)
		}
	}

	// default benchmark
	if norm.Benchmark == "" {
		norm.Benchmark = d.Benchmark
	}

	return norm, nil
}
```

- [ ] **Step 4: Run to confirm green**

```bash
ginkgo run ./portfolio
```
Expected: 15 passing specs (6 slug + 9 validate).

- [ ] **Step 5: Commit**

```bash
git add portfolio/validate.go portfolio/validate_test.go
git commit -m "$(cat <<'EOF'
add portfolio.ValidateCreate: spec-driven create-request checks

Pure function that runs every check from the design spec's
"Create-portfolio validation" subsection: live mode rejected,
schedule consistency enforced, strategy installed and version
matched, parameters validated against describe_json (no unknown
keys, all declared parameters present). Returns a normalized
CreateRequest with StrategyVer and Benchmark filled from the
strategy's describe when blank. Every failure path has a sentinel
so the handler can map them cleanly to 422.
EOF
)"
```

---

## Task 6: `portfolio/db.go`

**Files:**
- Create: `portfolio/db.go`

- [ ] **Step 1: Write `portfolio/db.go`**

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

package portfolio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a portfolio lookup does not match any row
// owned by the calling user.
var ErrNotFound = errors.New("portfolio not found")

// ErrDuplicateSlug is returned on (owner_sub, slug) unique-constraint hits.
var ErrDuplicateSlug = errors.New("duplicate portfolio slug")

const portfolioColumns = `
	id, owner_sub, slug, name, strategy_code, strategy_ver, parameters,
	preset_name, benchmark, mode, schedule, status, last_run_at,
	last_error, created_at, updated_at
`

// List returns every portfolio owned by ownerSub, sorted newest-first.
func List(ctx context.Context, pool *pgxpool.Pool, ownerSub string) ([]Portfolio, error) {
	rows, err := pool.Query(ctx,
		`SELECT `+portfolioColumns+` FROM portfolios WHERE owner_sub = $1 ORDER BY created_at DESC`,
		ownerSub,
	)
	if err != nil {
		return nil, fmt.Errorf("querying portfolios: %w", err)
	}
	defer rows.Close()

	var out []Portfolio
	for rows.Next() {
		p, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get returns one portfolio by (ownerSub, slug). ErrNotFound if no row.
func Get(ctx context.Context, pool *pgxpool.Pool, ownerSub, slug string) (Portfolio, error) {
	row := pool.QueryRow(ctx,
		`SELECT `+portfolioColumns+` FROM portfolios WHERE owner_sub = $1 AND slug = $2`,
		ownerSub, slug,
	)
	p, err := scan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Portfolio{}, ErrNotFound
	}
	return p, err
}

// Insert writes a new portfolio row. The caller must have populated every
// field on p (slug, strategy_ver, parameters, benchmark, mode, status,
// etc.) before calling. Returns ErrDuplicateSlug on a
// (owner_sub, slug) UNIQUE violation.
func Insert(ctx context.Context, pool *pgxpool.Pool, p Portfolio) error {
	paramsJSON, err := json.Marshal(p.Parameters)
	if err != nil {
		return fmt.Errorf("marshaling parameters: %w", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO portfolios (
			owner_sub, slug, name, strategy_code, strategy_ver, parameters,
			preset_name, benchmark, mode, schedule, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, p.OwnerSub, p.Slug, p.Name, p.StrategyCode, p.StrategyVer, paramsJSON,
		p.PresetName, p.Benchmark, string(p.Mode), p.Schedule, string(p.Status))
	if err != nil {
		if pgErr := uniqueViolation(err); pgErr {
			return ErrDuplicateSlug
		}
		return fmt.Errorf("inserting portfolio: %w", err)
	}
	return nil
}

// UpdateName updates a portfolio's display name. Returns ErrNotFound if
// the (ownerSub, slug) pair does not match any row.
func UpdateName(ctx context.Context, pool *pgxpool.Pool, ownerSub, slug, name string) error {
	tag, err := pool.Exec(ctx, `
		UPDATE portfolios
		   SET name = $3, updated_at = NOW()
		 WHERE owner_sub = $1 AND slug = $2
	`, ownerSub, slug, name)
	if err != nil {
		return fmt.Errorf("updating portfolio name: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a portfolio by (ownerSub, slug). Returns ErrNotFound if
// no row was deleted.
func Delete(ctx context.Context, pool *pgxpool.Pool, ownerSub, slug string) error {
	tag, err := pool.Exec(ctx,
		`DELETE FROM portfolios WHERE owner_sub = $1 AND slug = $2`,
		ownerSub, slug,
	)
	if err != nil {
		return fmt.Errorf("deleting portfolio: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(r scanner) (Portfolio, error) {
	var (
		p          Portfolio
		modeStr    string
		statusStr  string
		paramsJSON []byte
	)
	err := r.Scan(
		&p.ID, &p.OwnerSub, &p.Slug, &p.Name, &p.StrategyCode, &p.StrategyVer,
		&paramsJSON, &p.PresetName, &p.Benchmark, &modeStr, &p.Schedule,
		&statusStr, &p.LastRunAt, &p.LastError, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return Portfolio{}, err
	}
	p.Mode = Mode(modeStr)
	p.Status = Status(statusStr)
	if len(paramsJSON) > 0 {
		if err := json.Unmarshal(paramsJSON, &p.Parameters); err != nil {
			return Portfolio{}, fmt.Errorf("unmarshaling parameters: %w", err)
		}
	}
	return p, nil
}

// uniqueViolation reports whether err is a Postgres 23505 (unique_violation).
func uniqueViolation(err error) bool {
	var pgErr *pgx.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return true
	}
	// jackc/pgx/v5 exposes its own PgError alias under pgconn.PgError; check
	// via error string as a fallback when the typed path does not match.
	return err != nil && containsPostgresErrorCode(err, "23505")
}

func containsPostgresErrorCode(err error, code string) bool {
	s := err.Error()
	needle := "SQLSTATE " + code
	for i := 0; i <= len(s)-len(needle); i++ {
		if s[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./portfolio/...
```
Expected: no errors. The `pgx.PgError` typed path may not compile depending on pgx v5's exact types; if so, drop the typed branch from `uniqueViolation` and rely on `containsPostgresErrorCode` alone:

```go
func uniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return containsPostgresErrorCode(err, "23505")
}
```

Pick the form that compiles, document the adaptation in the commit.

- [ ] **Step 3: Commit**

```bash
git add portfolio/db.go
git commit -m "$(cat <<'EOF'
add portfolio/db.go: pgxpool CRUD scoped by owner_sub

List / Get / Insert / UpdateName / Delete. Every query carries
WHERE owner_sub = $1 so a user's portfolios are never visible to
another user at the SQL layer. ErrNotFound when the pair does not
match; ErrDuplicateSlug on 23505 unique violation (InsertPortfolio
is the call site the handler maps to 409).
EOF
)"
```

---

## Task 7: `portfolio/store.go` (Store interface + PoolStore adapter)

**Files:**
- Create: `portfolio/store.go`

- [ ] **Step 1: Write `portfolio/store.go`**

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

package portfolio

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the subset of db operations the handler needs.
type Store interface {
	List(ctx context.Context, ownerSub string) ([]Portfolio, error)
	Get(ctx context.Context, ownerSub, slug string) (Portfolio, error)
	Insert(ctx context.Context, p Portfolio) error
	UpdateName(ctx context.Context, ownerSub, slug, name string) error
	Delete(ctx context.Context, ownerSub, slug string) error
}

// PoolStore adapts *pgxpool.Pool to the Store interface.
type PoolStore struct {
	Pool *pgxpool.Pool
}

func (p PoolStore) List(ctx context.Context, ownerSub string) ([]Portfolio, error) {
	return List(ctx, p.Pool, ownerSub)
}

func (p PoolStore) Get(ctx context.Context, ownerSub, slug string) (Portfolio, error) {
	return Get(ctx, p.Pool, ownerSub, slug)
}

func (p PoolStore) Insert(ctx context.Context, port Portfolio) error {
	return Insert(ctx, p.Pool, port)
}

func (p PoolStore) UpdateName(ctx context.Context, ownerSub, slug, name string) error {
	return UpdateName(ctx, p.Pool, ownerSub, slug, name)
}

func (p PoolStore) Delete(ctx context.Context, ownerSub, slug string) error {
	return Delete(ctx, p.Pool, ownerSub, slug)
}

// StrategyReader is the subset of the strategy package that the portfolio
// handler needs to validate create requests. Production uses strategy.PoolStore.
type StrategyReader interface {
	Get(ctx context.Context, shortCode string) (any, error)
}
```

Note: `StrategyReader` above returns `any` only as a placeholder; the handler actually imports the concrete `strategy.Strategy` type via an adapter. Delete the `StrategyReader` interface definition — handler uses `strategy.ReadStore` directly. Final file should not have `StrategyReader`. Re-save without it:

```go
// (remove the StrategyReader interface and its import if it was pulled in
// for it — the handler in Task 8 references strategy.ReadStore directly.)
```

- [ ] **Step 2: Verify build**

```bash
go build ./portfolio/...
```
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add portfolio/store.go
git commit -m "$(cat <<'EOF'
add portfolio.Store interface + PoolStore adapter

Store is the subset of db operations the handler needs (List, Get,
Insert, UpdateName, Delete). PoolStore wraps *pgxpool.Pool for
production; tests pass an in-memory fake.
EOF
)"
```

---

## Task 8: `portfolio/handler.go` + tests

**Files:**
- Create: `portfolio/handler.go`
- Create: `portfolio/handler_test.go`

- [ ] **Step 1: Write failing tests**

Create `portfolio/handler_test.go`:

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

package portfolio_test

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/strategy"
	"github.com/penny-vault/pv-api/types"
)

// fakeStore is a trivial in-memory implementation of portfolio.Store.
type fakeStore struct {
	rows []portfolio.Portfolio
}

func (f *fakeStore) List(_ context.Context, ownerSub string) ([]portfolio.Portfolio, error) {
	out := make([]portfolio.Portfolio, 0)
	for _, p := range f.rows {
		if p.OwnerSub == ownerSub {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, ownerSub, slug string) (portfolio.Portfolio, error) {
	for _, p := range f.rows {
		if p.OwnerSub == ownerSub && p.Slug == slug {
			return p, nil
		}
	}
	return portfolio.Portfolio{}, portfolio.ErrNotFound
}

func (f *fakeStore) Insert(_ context.Context, p portfolio.Portfolio) error {
	for _, existing := range f.rows {
		if existing.OwnerSub == p.OwnerSub && existing.Slug == p.Slug {
			return portfolio.ErrDuplicateSlug
		}
	}
	p.ID = uuid.Must(uuid.NewV7())
	p.CreatedAt = time.Now().UTC()
	p.UpdatedAt = p.CreatedAt
	f.rows = append(f.rows, p)
	return nil
}

func (f *fakeStore) UpdateName(_ context.Context, ownerSub, slug, name string) error {
	for i, p := range f.rows {
		if p.OwnerSub == ownerSub && p.Slug == slug {
			f.rows[i].Name = name
			f.rows[i].UpdatedAt = time.Now().UTC()
			return nil
		}
	}
	return portfolio.ErrNotFound
}

func (f *fakeStore) Delete(_ context.Context, ownerSub, slug string) error {
	for i, p := range f.rows {
		if p.OwnerSub == ownerSub && p.Slug == slug {
			f.rows = append(f.rows[:i], f.rows[i+1:]...)
			return nil
		}
	}
	return portfolio.ErrNotFound
}

// fakeStrategyStore implements strategy.ReadStore. Returns one configured
// strategy; anything else is ErrNotFound.
type fakeStrategyStore struct {
	row strategy.Strategy
}

func (f *fakeStrategyStore) List(_ context.Context) ([]strategy.Strategy, error) {
	return []strategy.Strategy{f.row}, nil
}

func (f *fakeStrategyStore) Get(_ context.Context, shortCode string) (strategy.Strategy, error) {
	if shortCode == f.row.ShortCode {
		return f.row, nil
	}
	return strategy.Strategy{}, strategy.ErrNotFound
}

var _ = Describe("portfolio.Handler", func() {
	var (
		store    *fakeStore
		strategies *fakeStrategyStore
		app      *fiber.App
	)

	const (
		sub1 = "auth0|user-1"
		sub2 = "auth0|user-2"
	)

	installed := "v1.0.0"
	admDescribeJSON := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"riskOn","type":"universe"}],"presets":[{"name":"standard","parameters":{"riskOn":"VFINX,PRIDX,QQQ"}}],"schedule":"@monthend","benchmark":"SPY"}`)

	BeforeEach(func() {
		store = &fakeStore{}
		strategies = &fakeStrategyStore{
			row: strategy.Strategy{
				ShortCode:    "adm",
				IsOfficial:   true,
				InstalledVer: &installed,
				DescribeJSON: admDescribeJSON,
			},
		}
		h := portfolio.NewHandler(store, strategies)

		app = fiber.New()
		app.Use(func(c fiber.Ctx) error {
			sub := c.Get("X-Test-Sub")
			if sub == "" {
				sub = sub1
			}
			c.Locals(types.AuthSubjectKey{}, sub)
			return c.Next()
		})
		app.Get("/portfolios", h.List)
		app.Post("/portfolios", h.Create)
		app.Get("/portfolios/:slug", h.Get)
		app.Patch("/portfolios/:slug", h.Patch)
		app.Delete("/portfolios/:slug", h.Delete)
	})

	request := func(method, path, sub string, body any) (int, []byte, string) {
		var reader io.Reader
		if body != nil {
			b, err := sonic.Marshal(body)
			Expect(err).NotTo(HaveOccurred())
			reader = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, reader)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if sub != "" {
			req.Header.Set("X-Test-Sub", sub)
		}
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		rb, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		return resp.StatusCode, rb, resp.Header.Get("Content-Type")
	}

	It("creates a portfolio and returns 201 with the slug", func() {
		status, body, _ := request("POST", "/portfolios", sub1, map[string]any{
			"name":         "ADM standard",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "VFINX,PRIDX,QQQ"},
			"mode":         "one_shot",
		})
		Expect(status).To(Equal(201))
		var out map[string]any
		Expect(sonic.Unmarshal(body, &out)).To(Succeed())
		Expect(out["slug"]).To(MatchRegexp(`^adm-standard-[a-z2-7]{4}$`))
		Expect(out["presetName"]).To(Equal("standard"))
		Expect(out["strategyVer"]).To(Equal("v1.0.0"))
		Expect(out["benchmark"]).To(Equal("SPY"))
	})

	It("rejects mode=live with 422 problem+json", func() {
		status, _, ct := request("POST", "/portfolios", sub1, map[string]any{
			"name":         "live",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "SPY"},
			"mode":         "live",
		})
		Expect(status).To(Equal(422))
		Expect(ct).To(Equal("application/problem+json"))
	})

	It("rejects an unknown strategy code with 422", func() {
		status, _, _ := request("POST", "/portfolios", sub1, map[string]any{
			"name":         "x",
			"strategyCode": "nope",
			"parameters":   map[string]any{"riskOn": "SPY"},
			"mode":         "one_shot",
		})
		Expect(status).To(Equal(422))
	})

	It("returns 409 when the same user creates the same config twice", func() {
		body := map[string]any{
			"name":         "ADM standard",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "VFINX,PRIDX,QQQ"},
			"mode":         "one_shot",
		}
		status, _, _ := request("POST", "/portfolios", sub1, body)
		Expect(status).To(Equal(201))
		status, _, _ = request("POST", "/portfolios", sub1, body)
		Expect(status).To(Equal(409))
	})

	It("lets two different users create the same config", func() {
		body := map[string]any{
			"name":         "ADM standard",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "VFINX,PRIDX,QQQ"},
			"mode":         "one_shot",
		}
		s1, _, _ := request("POST", "/portfolios", sub1, body)
		s2, _, _ := request("POST", "/portfolios", sub2, body)
		Expect(s1).To(Equal(201))
		Expect(s2).To(Equal(201))
	})

	It("scopes list to the caller's portfolios only", func() {
		body := map[string]any{
			"name":         "mine",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "SPY"},
			"mode":         "one_shot",
		}
		_, _, _ = request("POST", "/portfolios", sub1, body)
		_, _, _ = request("POST", "/portfolios", sub2, body)

		status, listBody, _ := request("GET", "/portfolios", sub1, nil)
		Expect(status).To(Equal(200))

		var list []map[string]any
		Expect(sonic.Unmarshal(listBody, &list)).To(Succeed())
		Expect(list).To(HaveLen(1))
	})

	It("returns 404 when another user reads your portfolio", func() {
		body := map[string]any{
			"name":         "mine",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "VFINX,PRIDX,QQQ"},
			"mode":         "one_shot",
		}
		_, createdBody, _ := request("POST", "/portfolios", sub1, body)
		var created map[string]any
		Expect(sonic.Unmarshal(createdBody, &created)).To(Succeed())
		slug := created["slug"].(string)

		status, _, _ := request("GET", "/portfolios/"+slug, sub2, nil)
		Expect(status).To(Equal(404))
	})

	It("patches the name and returns the updated portfolio", func() {
		_, createdBody, _ := request("POST", "/portfolios", sub1, map[string]any{
			"name":         "before",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "SPY"},
			"mode":         "one_shot",
		})
		var created map[string]any
		Expect(sonic.Unmarshal(createdBody, &created)).To(Succeed())
		slug := created["slug"].(string)

		status, body, _ := request("PATCH", "/portfolios/"+slug, sub1, map[string]any{"name": "after"})
		Expect(status).To(Equal(200))
		var out map[string]any
		Expect(sonic.Unmarshal(body, &out)).To(Succeed())
		Expect(out["name"]).To(Equal("after"))
	})

	It("rejects PATCH with fields other than name", func() {
		_, createdBody, _ := request("POST", "/portfolios", sub1, map[string]any{
			"name":         "x",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "SPY"},
			"mode":         "one_shot",
		})
		var created map[string]any
		Expect(sonic.Unmarshal(createdBody, &created)).To(Succeed())
		slug := created["slug"].(string)

		status, _, _ := request("PATCH", "/portfolios/"+slug, sub1, map[string]any{
			"name":     "new",
			"benchmark": "QQQ",
		})
		Expect(status).To(Equal(422))
	})

	It("deletes the portfolio and subsequent GET is 404", func() {
		_, createdBody, _ := request("POST", "/portfolios", sub1, map[string]any{
			"name":         "goner",
			"strategyCode": "adm",
			"parameters":   map[string]any{"riskOn": "SPY"},
			"mode":         "one_shot",
		})
		var created map[string]any
		Expect(sonic.Unmarshal(createdBody, &created)).To(Succeed())
		slug := created["slug"].(string)

		status, _, _ := request("DELETE", "/portfolios/"+slug, sub1, nil)
		Expect(status).To(Equal(204))

		status, _, _ = request("GET", "/portfolios/"+slug, sub1, nil)
		Expect(status).To(Equal(404))
	})
})
```

- [ ] **Step 2: Run to confirm red**

```bash
ginkgo run ./portfolio
```
Expected: FAIL — `portfolio.NewHandler` undefined.

- [ ] **Step 3: Implement `portfolio/handler.go`**

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

package portfolio

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"

	"github.com/penny-vault/pv-api/strategy"
	"github.com/penny-vault/pv-api/types"
)

// Handler serves the POST/GET/PATCH/DELETE endpoints of /portfolios and
// /portfolios/{slug}.
type Handler struct {
	store      Store
	strategies strategy.ReadStore
}

// NewHandler constructs a handler. strategies is used to validate the
// referenced strategy at create time.
func NewHandler(store Store, strategies strategy.ReadStore) *Handler {
	return &Handler{store: store, strategies: strategies}
}

// List implements GET /portfolios.
func (h *Handler) List(c fiber.Ctx) error {
	ownerSub, err := subject(c)
	if err != nil {
		return writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
	}
	rows, err := h.store.List(c.Context(), ownerSub)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	out := make([]portfolioView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toView(r))
	}
	return writeJSON(c, fiber.StatusOK, out)
}

// Get implements GET /portfolios/{slug} (config only).
func (h *Handler) Get(c fiber.Ctx) error {
	ownerSub, err := subject(c)
	if err != nil {
		return writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
	}
	slug := c.Params("slug")
	p, err := h.store.Get(c.Context(), ownerSub, slug)
	if errors.Is(err, ErrNotFound) {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "portfolio not found: "+slug)
	}
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return writeJSON(c, fiber.StatusOK, toView(p))
}

// Create implements POST /portfolios.
func (h *Handler) Create(c fiber.Ctx) error {
	ownerSub, err := subject(c)
	if err != nil {
		return writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
	}

	var body createBody
	if err := sonic.Unmarshal(c.Body(), &body); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", fmt.Sprintf("body is not valid JSON: %v", err))
	}
	req := body.toRequest()

	s, err := h.strategies.Get(c.Context(), req.StrategyCode)
	if errors.Is(err, strategy.ErrNotFound) {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unknown strategy", "no registered strategy with short_code="+req.StrategyCode)
	}
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	norm, err := ValidateCreate(req, s)
	if err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Invalid portfolio", err.Error())
	}

	var describe strategy.Describe
	if err := json.Unmarshal(s.DescribeJSON, &describe); err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", "strategy describe is malformed")
	}
	slug, err := Slug(norm, describe)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	presetName := presetMatch(norm.Parameters, describe)

	p := Portfolio{
		OwnerSub:     ownerSub,
		Slug:         slug,
		Name:         norm.Name,
		StrategyCode: norm.StrategyCode,
		StrategyVer:  norm.StrategyVer,
		Parameters:   norm.Parameters,
		PresetName:   presetName,
		Benchmark:    norm.Benchmark,
		Mode:         norm.Mode,
		Status:       StatusPending,
	}
	if norm.Schedule != "" {
		sch := norm.Schedule
		p.Schedule = &sch
	}

	if err := h.store.Insert(c.Context(), p); err != nil {
		if errors.Is(err, ErrDuplicateSlug) {
			return writeProblem(c, fiber.StatusConflict, "Conflict", "portfolio with slug "+slug+" already exists for this user")
		}
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	// Re-read so CreatedAt / UpdatedAt / ID reflect the DB row. Falls back
	// to an in-memory view if the read fails.
	stored, err := h.store.Get(c.Context(), ownerSub, slug)
	if err == nil {
		return writeJSON(c, fiber.StatusCreated, toView(stored))
	}
	return writeJSON(c, fiber.StatusCreated, toView(p))
}

// Patch implements PATCH /portfolios/{slug} (name-only).
func (h *Handler) Patch(c fiber.Ctx) error {
	ownerSub, err := subject(c)
	if err != nil {
		return writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
	}
	slug := c.Params("slug")

	// Strict decode: only `name` is allowed. Any other field is a 422.
	var raw map[string]json.RawMessage
	if err := sonic.Unmarshal(c.Body(), &raw); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", fmt.Sprintf("body is not valid JSON: %v", err))
	}
	for k := range raw {
		if k != "name" {
			return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", "only `name` may be updated; rejected field: "+k)
		}
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := sonic.Unmarshal(c.Body(), &body); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", fmt.Sprintf("body is not valid JSON: %v", err))
	}
	if body.Name == "" {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", "`name` must be non-empty")
	}

	if err := h.store.UpdateName(c.Context(), ownerSub, slug, body.Name); err != nil {
		if errors.Is(err, ErrNotFound) {
			return writeProblem(c, fiber.StatusNotFound, "Not Found", "portfolio not found: "+slug)
		}
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	p, err := h.store.Get(c.Context(), ownerSub, slug)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return writeJSON(c, fiber.StatusOK, toView(p))
}

// Delete implements DELETE /portfolios/{slug}.
func (h *Handler) Delete(c fiber.Ctx) error {
	ownerSub, err := subject(c)
	if err != nil {
		return writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
	}
	slug := c.Params("slug")

	if err := h.store.Delete(c.Context(), ownerSub, slug); err != nil {
		if errors.Is(err, ErrNotFound) {
			return writeProblem(c, fiber.StatusNotFound, "Not Found", "portfolio not found: "+slug)
		}
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// createBody mirrors the OpenAPI PortfolioCreateRequest shape. A separate
// type keeps JSON-tag details out of CreateRequest (which is the domain
// type used elsewhere).
type createBody struct {
	Name         string         `json:"name"`
	StrategyCode string         `json:"strategyCode"`
	StrategyVer  string         `json:"strategyVer,omitempty"`
	Parameters   map[string]any `json:"parameters"`
	Benchmark    string         `json:"benchmark,omitempty"`
	Mode         string         `json:"mode"`
	Schedule     string         `json:"schedule,omitempty"`
	RunNow       bool           `json:"runNow,omitempty"`
}

func (b createBody) toRequest() CreateRequest {
	return CreateRequest{
		Name:         b.Name,
		StrategyCode: b.StrategyCode,
		StrategyVer:  b.StrategyVer,
		Parameters:   b.Parameters,
		Benchmark:    b.Benchmark,
		Mode:         Mode(b.Mode),
		Schedule:     b.Schedule,
		RunNow:       b.RunNow,
	}
}

// portfolioView mirrors the OpenAPI Portfolio schema (config only).
type portfolioView struct {
	Slug         string         `json:"slug"`
	Name         string         `json:"name"`
	StrategyCode string         `json:"strategyCode"`
	StrategyVer  string         `json:"strategyVer"`
	Parameters   map[string]any `json:"parameters"`
	PresetName   *string        `json:"presetName"`
	Benchmark    string         `json:"benchmark"`
	Mode         string         `json:"mode"`
	Schedule     *string        `json:"schedule"`
	Status       string         `json:"status"`
	CreatedAt    string         `json:"createdAt"`
	UpdatedAt    string         `json:"updatedAt"`
	LastRunAt    *string        `json:"lastRunAt"`
	LastError    *string        `json:"lastError"`
}

func toView(p Portfolio) portfolioView {
	v := portfolioView{
		Slug:         p.Slug,
		Name:         p.Name,
		StrategyCode: p.StrategyCode,
		StrategyVer:  p.StrategyVer,
		Parameters:   p.Parameters,
		PresetName:   p.PresetName,
		Benchmark:    p.Benchmark,
		Mode:         string(p.Mode),
		Schedule:     p.Schedule,
		Status:       string(p.Status),
		CreatedAt:    p.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:    p.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		LastError:    p.LastError,
	}
	if p.LastRunAt != nil {
		t := p.LastRunAt.UTC().Format("2006-01-02T15:04:05Z")
		v.LastRunAt = &t
	}
	return v
}

// presetMatch returns the preset name (stored form, not kebab-cased) whose
// parameters deep-equal params, or nil.
func presetMatch(params map[string]any, d strategy.Describe) *string {
	for _, p := range d.Presets {
		if presetParametersEqual(p.Parameters, params) {
			name := p.Name
			return &name
		}
	}
	return nil
}

// subject extracts the Auth0 sub from fiber locals; returns an error if
// missing (should be unreachable in production since auth middleware
// always sets it).
func subject(c fiber.Ctx) (string, error) {
	sub, ok := c.Locals(types.AuthSubjectKey{}).(string)
	if !ok || sub == "" {
		return "", errors.New("missing authenticated subject")
	}
	return sub, nil
}

func writeJSON(c fiber.Ctx, status int, v any) error {
	body, err := sonic.Marshal(v)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	c.Set(fiber.HeaderContentType, "application/json")
	return c.Status(status).Send(body)
}

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

- [ ] **Step 4: Run to confirm green**

```bash
ginkgo run ./portfolio
```
Expected: 25 passing specs (6 slug + 9 validate + 10 handler).

- [ ] **Step 5: Commit**

```bash
git add portfolio/handler.go portfolio/handler_test.go
git commit -m "$(cat <<'EOF'
add portfolio.Handler: real POST/GET/PATCH/DELETE handlers

Handler fulfills the Plan 4 surface: create (with validation +
slug generation + preset match), list (scoped by owner_sub), get
config, patch-name-only (strict-decode rejects any other field),
delete. Every query carries owner_sub so a user sees only their
own portfolios. Errors map to problem+json with the right status
(401/404/409/422/500). runNow is accepted but no-op (no runner
in Plan 4). Tests use an in-memory fake store + a test-only
middleware that stashes the Auth0 sub.
EOF
)"
```

---

## Task 9: Wire into `api/portfolios.go` and `api/server.go`

**Files:**
- Modify: `api/portfolios.go`
- Modify: `api/server.go`

- [ ] **Step 1: Replace `api/portfolios.go`**

Read `api/portfolios.go` first. Replace its contents with:

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

	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/strategy"
)

// PortfolioHandler is the real-handler shim owned by api/. It delegates
// to portfolio.Handler for the CRUD endpoints. Every derived-data route
// remains 501 until Plan 5.
type PortfolioHandler struct {
	inner *portfolio.Handler
}

// NewPortfolioHandler builds a PortfolioHandler backed by a portfolio.Store
// and a strategy.ReadStore.
func NewPortfolioHandler(store portfolio.Store, strategies strategy.ReadStore) *PortfolioHandler {
	return &PortfolioHandler{inner: portfolio.NewHandler(store, strategies)}
}

// RegisterPortfolioRoutes mounts every portfolio endpoint as stubs (501).
// Kept for test harnesses that do not supply a pool.
func RegisterPortfolioRoutes(r fiber.Router) {
	r.Get("/portfolios", stubPortfolio)
	r.Post("/portfolios", stubPortfolio)
	r.Get("/portfolios/:slug", stubPortfolio)
	r.Patch("/portfolios/:slug", stubPortfolio)
	r.Delete("/portfolios/:slug", stubPortfolio)
	r.Get("/portfolios/:slug/summary", stubPortfolio)
	r.Get("/portfolios/:slug/drawdowns", stubPortfolio)
	r.Get("/portfolios/:slug/metrics", stubPortfolio)
	r.Get("/portfolios/:slug/trailing-returns", stubPortfolio)
	r.Get("/portfolios/:slug/holdings", stubPortfolio)
	r.Get("/portfolios/:slug/holdings/:date", stubPortfolio)
	r.Get("/portfolios/:slug/measurements", stubPortfolio)
	r.Post("/portfolios/:slug/runs", stubPortfolio)
	r.Get("/portfolios/:slug/runs", stubPortfolio)
	r.Get("/portfolios/:slug/runs/:runId", stubPortfolio)
}

// RegisterPortfolioRoutesWith mounts CRUD endpoints backed by h; all
// derived-data endpoints stay 501 until Plan 5 (backtest runner).
func RegisterPortfolioRoutesWith(r fiber.Router, h *PortfolioHandler) {
	r.Get("/portfolios", h.inner.List)
	r.Post("/portfolios", h.inner.Create)
	r.Get("/portfolios/:slug", h.inner.Get)
	r.Patch("/portfolios/:slug", h.inner.Patch)
	r.Delete("/portfolios/:slug", h.inner.Delete)

	// derived-data stubs — land in Plan 5
	r.Get("/portfolios/:slug/summary", stubPortfolio)
	r.Get("/portfolios/:slug/drawdowns", stubPortfolio)
	r.Get("/portfolios/:slug/metrics", stubPortfolio)
	r.Get("/portfolios/:slug/trailing-returns", stubPortfolio)
	r.Get("/portfolios/:slug/holdings", stubPortfolio)
	r.Get("/portfolios/:slug/holdings/:date", stubPortfolio)
	r.Get("/portfolios/:slug/measurements", stubPortfolio)
	r.Post("/portfolios/:slug/runs", stubPortfolio)
	r.Get("/portfolios/:slug/runs", stubPortfolio)
	r.Get("/portfolios/:slug/runs/:runId", stubPortfolio)
}

func stubPortfolio(c fiber.Ctx) error { return WriteProblem(c, ErrNotImplemented) }
```

- [ ] **Step 2: Update `api/server.go` to mount the real handler**

Read `api/server.go`. Find the block under `if conf.Pool != nil {` and replace the call to `RegisterPortfolioRoutes(protected)` with:

```go
		portfolioStore := portfolio.PoolStore{Pool: conf.Pool}
		strategyStore := strategy.PoolStore{Pool: conf.Pool}
		RegisterPortfolioRoutesWith(protected, NewPortfolioHandler(portfolioStore, strategyStore))
		RegisterStrategyRoutesWith(protected, NewStrategyHandler(strategyStore))
```

So the `NewApp` function's post-auth section becomes:

```go
	protected := app.Group("", auth)

	if conf.Pool != nil {
		portfolioStore := portfolio.PoolStore{Pool: conf.Pool}
		strategyStore := strategy.PoolStore{Pool: conf.Pool}
		RegisterPortfolioRoutesWith(protected, NewPortfolioHandler(portfolioStore, strategyStore))
		RegisterStrategyRoutesWith(protected, NewStrategyHandler(strategyStore))

		if err := startRegistrySync(ctx, strategyStore, conf.Registry); err != nil {
			return nil, fmt.Errorf("start registry sync: %w", err)
		}
	} else {
		RegisterPortfolioRoutes(protected)
		RegisterStrategyRoutes(protected)
	}
```

Add `"github.com/penny-vault/pv-api/portfolio"` to the imports at the top of `api/server.go`.

- [ ] **Step 3: Full build and tests**

```bash
go build ./...
ginkgo run -r
```
Expected:
- Build clean.
- `Api Suite`: all specs pass (30+ — unchanged).
- `Strategy Suite`: 12 specs pass.
- `Portfolio Suite`: 25 specs pass.

- [ ] **Step 4: Commit**

```bash
git add api/portfolios.go api/server.go
git commit -m "$(cat <<'EOF'
wire portfolio.Handler into api.NewApp

When api.Config.Pool is non-nil, portfolio CRUD endpoints (list,
create, get, patch, delete) route to the real handler backed by
portfolio.PoolStore + strategy.PoolStore. Derived-data endpoints
(summary, drawdowns, metrics, trailing-returns, holdings,
holdings/{date}, measurements, runs) stay as 501 stubs until Plan 5.
EOF
)"
```

---

## Task 10: End-to-end smoke

**Purpose:** Final gate. Build, lint, run the full Ginkgo suite, verify the live server creates + lists + gets + patches + deletes a portfolio end-to-end against a real PG 18 instance with a hand-seeded strategy.

- [ ] **Step 1: Clean build + lint + full tests**

```bash
make build
make lint
ginkgo run -r --race
```
Expected:
- `make build`: `pvapi` binary; no errors.
- `make lint`: `0 issues.`
- `ginkgo run -r --race`: `Api Suite`, `Strategy Suite`, `Portfolio Suite` all green.

- [ ] **Step 2: Live smoke against PG 18**

Prerequisites:
- PG 18 on PATH (use `/Applications/Postgres.app/Contents/Versions/18/bin` on this box).
- `pvapi_smoke` database created (`createdb pvapi_smoke`).
- `/tmp/pvapi-jwks/jwks.json` from a local Python HTTP server on port 65535 (same pattern as Plan 3's smoke).

Shell script:

```bash
export PATH=/Applications/Postgres.app/Contents/Versions/18/bin:$PATH
rm -f pvapi
createdb pvapi_smoke 2>/dev/null || true

mkdir -p /tmp/pvapi-jwks
cat > /tmp/pvapi-jwks/jwks.json <<'EOF'
{"keys":[{"kty":"RSA","use":"sig","alg":"RS256","kid":"smoke","n":"0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw","e":"AQAB"}]}
EOF
python3 -m http.server --directory /tmp/pvapi-jwks --bind 127.0.0.1 65535 > /dev/null 2>&1 &
JWKS_PID=$!
sleep 1

go build -o /tmp/pvapi-smoke .
PVAPI_DB_URL="postgres://${USER}@localhost/pvapi_smoke" /tmp/pvapi-smoke server \
  --server-port 3030 \
  --auth0-jwks-url http://127.0.0.1:65535/jwks.json \
  --auth0-audience smoke \
  --auth0-issuer https://smoke.pvapi.local/ \
  --strategy-registry-sync-interval 5s \
  --strategy-install-concurrency 1 \
  --strategy-official-dir /tmp/pvapi-smoke-strategies \
  > /tmp/pvapi-smoke.log 2>&1 &
PID=$!
sleep 2

echo "--- healthz ---"
curl -s -o /dev/null -w 'healthz=%{http_code}\n' http://localhost:3030/healthz
echo "--- /portfolios without JWT ---"
curl -s -w 'portfolios=%{http_code}\n' http://localhost:3030/portfolios | head -c 200
echo ""
echo "--- log excerpt ---"
tail -8 /tmp/pvapi-smoke.log

kill -TERM $PID 2>/dev/null; wait $PID 2>/dev/null
kill -TERM $JWKS_PID 2>/dev/null; wait $JWKS_PID 2>/dev/null
rm -f /tmp/pvapi-smoke /tmp/pvapi-smoke.log
rm -rf /tmp/pvapi-jwks /tmp/pvapi-smoke-strategies
dropdb pvapi_smoke
```

Expected:
- `healthz=200`
- `/portfolios` without JWT returns `401` (application/problem+json).
- Log shows `server listening`, a strategy sync tick, and clean shutdown.

**A full create + list + get + patch + delete round-trip requires a valid JWT, which this smoke script does not mint.** The Ginkgo suite covers that end-to-end with the test JWKS harness; the live smoke validates that the binary starts against a real PG and routes behave.

Plan 4 is complete when every step above passes.

---

## Self-review summary

- **Spec coverage:**
  - `live` added to `portfolio_mode` enum and OpenAPI — Task 1 (migration) + Task 2 (OpenAPI).
  - Portfolio schema strip + listing optional KPIs — Task 2.
  - `PortfolioUpdateRequest` narrowed → Task 2 (OpenAPI) + Task 8 (handler strict decode).
  - Holding schemas + derived endpoints (`/summary`, `/drawdowns`, `/metrics`, `/trailing-returns`, `/holdings`, `/holdings/{date}`) — Task 2.
  - Slug generation (FNV-1a + preset match + kebab-case) — Task 4.
  - Create-portfolio validation (all 7 rules from the spec) — Task 5.
  - Pool CRUD scoped by `owner_sub` — Task 6.
  - Handler mapping to 401/404/409/422/500 problem+json — Task 8.
  - Wiring into `api.NewApp` with pool-optional pattern — Task 9.
  - `POST /portfolios/{slug}/runs` and every other derived endpoint stay 501 in Plan 4 per scope — Task 9.

- **Placeholder scan:** no "TBD", no "similar to …", no "handle edge cases" — every step has complete code.

- **Type consistency:**
  - `portfolio.Store` (Task 7) used by `portfolio.Handler` (Task 8) + satisfied by `portfolio.PoolStore` (Task 7) + fake in tests.
  - `portfolio.Handler` uses `strategy.ReadStore` (from Plan 3); `strategy.PoolStore` already satisfies it — Task 9 reuses the Plan-3 adapter.
  - `portfolio.Mode` constants match the migration / OpenAPI enum values (`one_shot`, `continuous`, `live`).
  - `types.AuthSubjectKey{}` (Plan 2) is the fiber locals key used by the test middleware in Task 8 and by the production auth middleware — no drift.
  - `WriteProblem` / `ErrNotImplemented` / `ErrNotFound` / `ErrConflict` sentinels from `api/errors.go` are referenced in Task 9's stub function; portfolio handler writes problems directly (same shape) to avoid an api → portfolio import cycle.
