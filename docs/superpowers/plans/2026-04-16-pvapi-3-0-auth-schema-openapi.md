# pvapi 3.0 Auth + Schema + OpenAPI Wiring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the Auth0 JWT middleware, the real `1_init` database schema, the OpenAPI contract (copied from `frontend-ng` and extended with the portfolio/strategy lifecycle endpoints), and stub handlers for every endpoint returning `501` problem+json. After this plan, every downstream plan (portfolio slice, strategy registry, runners) just needs to flesh out handler bodies — the route surface, auth, error handling, and data model are already in place.

**Architecture:**
- `openapi/openapi.yaml` — single source of truth for the API contract. `frontend-ng` consumes this going forward.
- `openapi/openapi.gen.go` — oapi-codegen-generated request/response types only. Server glue is hand-written in `api/` so we are not tied to oapi-codegen's Fiber-specific generators.
- `api/auth.go` — Fiber v3 Bearer-only JWT middleware backed by `lestrrat-go/jwx/v3` JWK cache.
- `api/errors.go` — Error → RFC 7807 `application/problem+json` mapping with sentinel errors exported from domain packages.
- `api/portfolios.go`, `api/strategies.go` — stub handler files. Every method returns 501 with a problem+json body. Real bodies land in later plans.
- `sql/migrations/1_init.{up,down}.sql` — real tables: `strategies`, `portfolios`, `backtest_runs`, plus four enums. Postgres 18 `uuidv7()` for PKs.
- Ginkgo suite generates an RS256 keypair at `BeforeSuite` time and hosts an in-process JWKS server the middleware talks to. Nothing committed to the repo.

**Tech Stack:** Adds `github.com/oapi-codegen/runtime`, `github.com/lestrrat-go/jwx/v3`, `github.com/lestrrat-go/httprc/v3`, and `github.com/oapi-codegen/oapi-codegen/v2` (build-time only, via `go:generate`). Retains Go 1.25+, Fiber v3, Postgres 18, `bytedance/sonic`, Ginkgo/Gomega, zerolog.

**Reference spec:** `docs/superpowers/specs/2026-04-16-pvapi-3-0-design.md`

**Branch / worktree note for executors:**

Plan 1 (Foundation Reset) landed on the `pvapi-3-foundation` branch at commit `fee6259` and has not been merged to `main`. This plan **depends on** that code (Fiber v3 scaffold, cmd wiring, fresh migration harness). Create the Plan 2 worktree by branching from `pvapi-3-foundation`, not from `main`:

```bash
cd /Users/jdf/Developer/penny-vault/pv-api
git worktree add .worktrees/pvapi-3-auth-schema -b pvapi-3-auth-schema pvapi-3-foundation
cd .worktrees/pvapi-3-auth-schema
```

All Plan 2 commits land on the `pvapi-3-auth-schema` branch. Merge order at end-of-rewrite is foundation → auth-schema → (subsequent plans).

---

## File overview

**Created**
```
openapi/openapi.yaml                      source of truth for contract
openapi/oapi-codegen.yaml                 generator config
openapi/gen.go                            //go:generate directive
openapi/openapi.gen.go                    generated types (committed output)
api/errors.go                             WriteProblem + sentinel errors
api/errors_test.go                        RFC 7807 shape tests
api/auth.go                               Auth0 JWT middleware
api/auth_test.go                          valid + failure-mode tests
api/jwks_testing.go                       RS256 keypair + JWKS HTTP server for tests (build tag `testing`? no — see Task 6)
api/portfolios.go                         portfolio stub handlers (GET list, GET one, POST, PATCH, DELETE, GET measurements, POST/GET runs)
api/portfolios_test.go                    each returns 501, body is problem+json, method wired
api/strategies.go                         strategy stub handlers (GET list, GET one, POST)
api/strategies_test.go                    same shape as portfolios_test.go
```

**Modified**
```
sql/migrations/1_init.up.sql              placeholder → real DDL
sql/migrations/1_init.down.sql            placeholder → real DROPs
api/server.go                             register routes + auth group, accept JWKS URL
cmd/config.go                             add auth0Conf
cmd/server.go                             add auth0 flags
pvapi.toml                                add [auth0] section
types/types.go                            add AuthSubjectKey
api/api_suite_test.go                     spin up test JWKS server in BeforeSuite
go.mod / go.sum                           new deps
Makefile                                  add `gen` target
.gitignore                                (verify openapi.gen.go is NOT ignored)
```

**Kept unchanged**
```
api/server_test.go                        (gets new specs added later; no rewrite)
api/middleware.go / middleware_test.go    (no change)
api/health.go                             (stays unauthenticated)
sql/pool.go / sql/migrate.go              (scaffolding reused as-is)
cmd/root.go / cmd/log.go / cmd/version.go / cmd/viper.go
main.go, pkginfo/, Makefile lint/build/test targets
```

---

## Task 1: Real `1_init` migration with all tables

**Purpose:** Replace the `SELECT 1;` placeholder with the full Postgres 18 schema from the design spec: `strategies`, `portfolios`, `backtest_runs`, and their four enum types. `uuidv7()` is used for primary keys (Postgres 18 built-in; no `pgcrypto` extension needed). Derived-summary columns and JSONB blobs live on `portfolios` directly (no separate summary table).

**Files:**
- Modify: `sql/migrations/1_init.up.sql`
- Modify: `sql/migrations/1_init.down.sql`

- [ ] **Step 1: Rewrite `1_init.up.sql`**

Replace the entire contents of `sql/migrations/1_init.up.sql` with:

```sql
-- pvapi 3.0 initial schema.

-- Enum types used across the schema.
CREATE TYPE artifact_kind AS ENUM ('binary', 'image');
CREATE TYPE portfolio_mode AS ENUM ('one_shot', 'continuous');
CREATE TYPE portfolio_status AS ENUM ('pending', 'running', 'ready', 'failed');
CREATE TYPE run_status AS ENUM ('queued', 'running', 'success', 'failed');

-- Registry of every strategy pvapi knows about.
-- Official strategies are discovered from github.com/penny-vault; unofficial
-- strategies are user-registered and scoped to a single owner.
CREATE TABLE strategies (
    short_code      TEXT PRIMARY KEY,
    repo_owner      TEXT NOT NULL,
    repo_name       TEXT NOT NULL,
    clone_url       TEXT NOT NULL,
    is_official     BOOLEAN NOT NULL DEFAULT FALSE,
    owner_sub       TEXT,
    description     TEXT,
    categories      TEXT[],
    stars           INTEGER,
    installed_ver   TEXT,
    installed_at    TIMESTAMPTZ,
    artifact_kind   artifact_kind,
    artifact_ref    TEXT,
    describe_json   JSONB,
    cagr            DOUBLE PRECISION,
    max_drawdown    DOUBLE PRECISION,
    sharpe          DOUBLE PRECISION,
    stats_as_of     TIMESTAMPTZ,
    discovered_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((is_official AND owner_sub IS NULL) OR (NOT is_official AND owner_sub IS NOT NULL))
);
CREATE INDEX idx_strategies_official ON strategies (is_official);
CREATE INDEX idx_strategies_owner ON strategies (owner_sub) WHERE owner_sub IS NOT NULL;

-- Portfolios are the unit of user-facing configuration + cached results.
-- The runner writes derived summary columns and JSONB blobs on each successful
-- backtest; scalar columns exist so the list endpoint can sort/filter without
-- parsing JSON.
CREATE TABLE portfolios (
    id                    UUID PRIMARY KEY DEFAULT uuidv7(),
    owner_sub             TEXT NOT NULL,
    slug                  TEXT NOT NULL,
    name                  TEXT NOT NULL,
    strategy_code         TEXT NOT NULL REFERENCES strategies(short_code),
    strategy_ver          TEXT NOT NULL,
    parameters            JSONB NOT NULL,
    preset_name           TEXT,
    benchmark             TEXT NOT NULL DEFAULT 'SPY',
    mode                  portfolio_mode NOT NULL,
    schedule              TEXT,
    status                portfolio_status NOT NULL DEFAULT 'pending',
    inception_date        DATE,
    snapshot_path         TEXT,
    last_run_at           TIMESTAMPTZ,
    next_run_at           TIMESTAMPTZ,
    last_error            TEXT,
    current_value         DOUBLE PRECISION,
    ytd_return            DOUBLE PRECISION,
    max_drawdown          DOUBLE PRECISION,
    sharpe                DOUBLE PRECISION,
    cagr_since_inception  DOUBLE PRECISION,
    summary_json          JSONB,
    drawdowns_json        JSONB,
    metrics_json          JSONB,
    trailing_json         JSONB,
    allocation_json       JSONB,
    current_assets        TEXT[],
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (owner_sub, slug)
);
CREATE INDEX idx_portfolios_owner ON portfolios (owner_sub);
CREATE INDEX idx_portfolios_due ON portfolios (next_run_at)
    WHERE mode = 'continuous' AND status IN ('ready', 'failed');

-- One row per Run invocation. Kept for history; live status lives on portfolios.
CREATE TABLE backtest_runs (
    id              UUID PRIMARY KEY DEFAULT uuidv7(),
    portfolio_id    UUID NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
    status          run_status NOT NULL,
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    duration_ms     INTEGER,
    error           TEXT,
    snapshot_path   TEXT
);
CREATE INDEX idx_runs_portfolio ON backtest_runs (portfolio_id, started_at DESC);
```

- [ ] **Step 2: Rewrite `1_init.down.sql`**

Replace the entire contents of `sql/migrations/1_init.down.sql` with:

```sql
-- Reverses the 1_init.up.sql schema. Drops in reverse dependency order.

DROP TABLE IF EXISTS backtest_runs;
DROP TABLE IF EXISTS portfolios;
DROP TABLE IF EXISTS strategies;

DROP TYPE IF EXISTS run_status;
DROP TYPE IF EXISTS portfolio_status;
DROP TYPE IF EXISTS portfolio_mode;
DROP TYPE IF EXISTS artifact_kind;
```

- [ ] **Step 3: Verify embed still picks up both files**

Run: `go build ./sql/...`
Expected: no errors.

There are no unit tests for this file (policy: no live-DB tests). Correctness is validated by later tasks that cause `Instance()` to run the migration at server startup.

- [ ] **Step 4: Commit**

```bash
git add sql/migrations/1_init.up.sql sql/migrations/1_init.down.sql
git commit -m "$(cat <<'EOF'
add 1_init schema: strategies, portfolios, backtest_runs

Introduces the pvapi 3.0 Postgres schema per the design spec. Uses
Postgres 18's built-in uuidv7() for PKs; no pgcrypto extension.
Derived-summary columns and JSONB blobs live on portfolios directly.
Unofficial strategies are scoped to a user via owner_sub with a CHECK
constraint that forbids NULL owner on unofficial rows.
EOF
)"
```

---

## Task 2: Copy OpenAPI spec from `frontend-ng` and rename `{id}` → `{slug}`

**Purpose:** Bring the OpenAPI source of truth in-repo. Adjust the existing three endpoints to use the slug path parameter (per the design spec's "portfolios use a memorable slug instead of a UUID" decision).

**Files:**
- Create: `openapi/openapi.yaml`

- [ ] **Step 1: Copy the file**

Run:

```bash
mkdir -p openapi
cp /Users/jdf/Developer/penny-vault/frontend-ng/api/openapi.yaml openapi/openapi.yaml
```

- [ ] **Step 2: Rename every `{id}` path parameter to `{slug}`**

The current spec references `id` in path segments, parameter names, and the `PortfolioId` parameter ref. We are renaming to `slug` throughout.

Edit `openapi/openapi.yaml`:

Replace `/portfolios/{id}` with `/portfolios/{slug}` everywhere (there are two path entries: `/portfolios/{id}` and `/portfolios/{id}/measurements`).

Find and replace the shared parameter definition:

```yaml
  parameters:
    PortfolioId:
      name: id
      in: path
      required: true
      description: Portfolio UUID.
      schema:
        type: string
        format: uuid
```

Replace with:

```yaml
  parameters:
    PortfolioSlug:
      name: slug
      in: path
      required: true
      description: Portfolio slug, e.g. `adm-aggressive-gm59`.
      schema:
        type: string
        pattern: "^[a-z0-9]+(-[a-z0-9]+)*$"
```

Replace every `$ref: '#/components/parameters/PortfolioId'` with `$ref: '#/components/parameters/PortfolioSlug'`.

In the `PortfolioMeasurements` schema, rename the `portfolioId` field to `portfolioSlug` and change its format from `uuid` to a plain string:

```yaml
    PortfolioMeasurements:
      type: object
      required: [portfolioSlug, from, to, points]
      properties:
        portfolioSlug:
          type: string
```

- [ ] **Step 3: Verify YAML parses**

Run:

```bash
go run github.com/getkin/kin-openapi/openapi3@latest - < openapi/openapi.yaml >/dev/null 2>&1 || \
  (python3 -c "import yaml,sys; yaml.safe_load(open('openapi/openapi.yaml'))" && echo "yaml parses")
```

If both commands fail because neither tool is available, just confirm the file parses visually. `openapi.yaml` will be validated end-to-end by oapi-codegen in Task 4.

- [ ] **Step 4: Commit**

```bash
git add openapi/openapi.yaml
git commit -m "$(cat <<'EOF'
copy OpenAPI contract from frontend-ng, rename id -> slug

pvapi 3.0 becomes the source of truth for the API contract. Path
parameters on /portfolios/{id} and /portfolios/{id}/measurements
become /portfolios/{slug}; the shared PortfolioId parameter is
replaced with PortfolioSlug. PortfolioMeasurements.portfolioId
becomes portfolioSlug.
EOF
)"
```

---

## Task 3: Extend OpenAPI with portfolio-lifecycle and strategy endpoints

**Purpose:** The frontend-ng spec only defines three read endpoints. Add the mutations, the runs collection, and the strategies surface described in the design doc. Schemas are minimal but complete (not `{}` placeholders) so oapi-codegen generates usable types.

**Files:**
- Modify: `openapi/openapi.yaml`

- [ ] **Step 1: Add the portfolio mutation endpoints**

Under `paths:`, after the existing `/portfolios/{slug}/measurements` block, append:

```yaml
  /portfolios/{slug}/runs:
    get:
      tags: [Portfolios]
      operationId: listPortfolioRuns
      summary: Run history for a portfolio
      parameters:
        - $ref: '#/components/parameters/PortfolioSlug'
      responses:
        '200':
          description: Array of runs
          content:
            application/json:
              schema:
                type: array
                items:
                  $ref: '#/components/schemas/BacktestRun'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
        '500':
          $ref: '#/components/responses/ServerError'
    post:
      tags: [Portfolios]
      operationId: triggerPortfolioRun
      summary: Trigger a one-shot backtest for the portfolio
      parameters:
        - $ref: '#/components/parameters/PortfolioSlug'
      responses:
        '202':
          description: Run accepted
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/BacktestRun'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
        '500':
          $ref: '#/components/responses/ServerError'

  /portfolios/{slug}/runs/{runId}:
    get:
      tags: [Portfolios]
      operationId: getPortfolioRun
      summary: Detail for a single run
      parameters:
        - $ref: '#/components/parameters/PortfolioSlug'
        - name: runId
          in: path
          required: true
          description: Run UUID.
          schema:
            type: string
            format: uuid
      responses:
        '200':
          description: Run detail
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/BacktestRun'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
        '500':
          $ref: '#/components/responses/ServerError'
```

Under the existing `/portfolios:` block (`get`), add a sibling `post`:

```yaml
    post:
      tags: [Portfolios]
      operationId: createPortfolio
      summary: Create a new portfolio
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/PortfolioCreateRequest'
      responses:
        '201':
          description: Portfolio created
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Portfolio'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '409':
          $ref: '#/components/responses/Conflict'
        '422':
          $ref: '#/components/responses/UnprocessableEntity'
        '500':
          $ref: '#/components/responses/ServerError'
```

Under `/portfolios/{slug}:`, add `patch` and `delete` siblings to the existing `get`:

```yaml
    patch:
      tags: [Portfolios]
      operationId: updatePortfolio
      summary: Update portfolio name, schedule, or parameters
      parameters:
        - $ref: '#/components/parameters/PortfolioSlug'
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/PortfolioUpdateRequest'
      responses:
        '200':
          description: Portfolio updated
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Portfolio'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
        '422':
          $ref: '#/components/responses/UnprocessableEntity'
        '500':
          $ref: '#/components/responses/ServerError'
    delete:
      tags: [Portfolios]
      operationId: deletePortfolio
      summary: Delete a portfolio and its snapshot
      parameters:
        - $ref: '#/components/parameters/PortfolioSlug'
      responses:
        '204':
          description: Deleted
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
        '500':
          $ref: '#/components/responses/ServerError'
```

- [ ] **Step 2: Add the strategies endpoints**

Under `paths:`, append:

```yaml
  /strategies:
    get:
      tags: [Strategies]
      operationId: listStrategies
      summary: List strategies in the registry
      parameters:
        - name: include
          in: query
          required: false
          description: Set to `unofficial` to include the caller's registered unofficial strategies.
          schema:
            type: string
            enum: [unofficial]
      responses:
        '200':
          description: Array of strategies
          content:
            application/json:
              schema:
                type: array
                items:
                  $ref: '#/components/schemas/Strategy'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '500':
          $ref: '#/components/responses/ServerError'
    post:
      tags: [Strategies]
      operationId: registerUnofficialStrategy
      summary: Register an unofficial strategy by GitHub clone URL
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/StrategyRegisterRequest'
      responses:
        '201':
          description: Registered
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Strategy'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '409':
          $ref: '#/components/responses/Conflict'
        '422':
          $ref: '#/components/responses/UnprocessableEntity'
        '500':
          $ref: '#/components/responses/ServerError'

  /strategies/{shortCode}:
    get:
      tags: [Strategies]
      operationId: getStrategy
      summary: Strategy detail
      parameters:
        - name: shortCode
          in: path
          required: true
          schema:
            type: string
      responses:
        '200':
          description: Strategy detail
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Strategy'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
        '500':
          $ref: '#/components/responses/ServerError'
```

Add a tag entry near the top of the file (next to the existing `Portfolios` tag):

```yaml
  - name: Strategies
    description: Strategy registry and unofficial strategy registration
```

- [ ] **Step 3: Add the new response shortcuts**

Under `components.responses`, append the `Conflict` and `UnprocessableEntity` shortcuts:

```yaml
    Conflict:
      description: The target resource already exists or conflicts with an existing row.
      content:
        application/problem+json:
          schema:
            $ref: '#/components/schemas/Problem'
    UnprocessableEntity:
      description: The request body was well-formed but failed validation.
      content:
        application/problem+json:
          schema:
            $ref: '#/components/schemas/Problem'
```

- [ ] **Step 4: Add the new request/response schemas**

Under `components.schemas`, append:

```yaml
    PortfolioMode:
      type: string
      enum: [one_shot, continuous]

    PortfolioStatus:
      type: string
      enum: [pending, running, ready, failed]

    RunStatus:
      type: string
      enum: [queued, running, success, failed]

    PortfolioCreateRequest:
      type: object
      required: [name, strategyCode, parameters, mode]
      properties:
        name:
          type: string
          description: User-visible display name.
        strategyCode:
          type: string
          description: Strategy short code (from `describe` output).
        strategyVer:
          type: string
          description: Specific version to pin; omit for latest installed.
        parameters:
          type: object
          additionalProperties: true
          description: Values validated against the strategy's declared parameters.
        benchmark:
          type: string
          default: 'SPY'
        mode:
          $ref: '#/components/schemas/PortfolioMode'
        schedule:
          type: string
          description: tradecron string, required iff mode=continuous (e.g. `@monthend`).
        runNow:
          type: boolean
          description: If true, kick the first run immediately after create.
          default: false

    PortfolioUpdateRequest:
      type: object
      description: All fields optional; only provided fields are updated.
      properties:
        name:
          type: string
        schedule:
          type: string
        parameters:
          type: object
          additionalProperties: true

    BacktestRun:
      type: object
      required: [id, portfolioSlug, status]
      properties:
        id:
          type: string
          format: uuid
        portfolioSlug:
          type: string
        status:
          $ref: '#/components/schemas/RunStatus'
        startedAt:
          type: string
          format: date-time
          nullable: true
        finishedAt:
          type: string
          format: date-time
          nullable: true
        durationMs:
          type: integer
          nullable: true
        error:
          type: string
          nullable: true

    StrategyRegisterRequest:
      type: object
      required: [cloneUrl]
      properties:
        cloneUrl:
          type: string
          format: uri
          description: HTTPS clone URL for the strategy repository.

    Strategy:
      type: object
      required: [shortCode, repoOwner, repoName, isOfficial]
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
        installedVer:
          type: string
          nullable: true
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

- [ ] **Step 5: Commit**

```bash
git add openapi/openapi.yaml
git commit -m "$(cat <<'EOF'
extend OpenAPI with portfolio lifecycle + strategies endpoints

Adds POST/PATCH/DELETE on portfolios, POST/GET on the runs
collection, and full /strategies + /strategies/{shortCode}
surface. Introduces PortfolioCreateRequest / PortfolioUpdateRequest
/ BacktestRun / Strategy / StrategyRegisterRequest schemas plus
Conflict and UnprocessableEntity response shortcuts.
EOF
)"
```

---

## Task 4: Set up oapi-codegen for types generation

**Purpose:** Generate Go request/response types from `openapi.yaml`. We use types-only generation — server glue is hand-written in Fiber, so we don't depend on oapi-codegen's framework support for Fiber v3.

**Files:**
- Create: `openapi/oapi-codegen.yaml`
- Create: `openapi/gen.go`
- Create: `openapi/openapi.gen.go` (generator output; committed)
- Modify: `Makefile`
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Write the oapi-codegen config**

Create `openapi/oapi-codegen.yaml`:

```yaml
package: openapi
generate:
  models: true
  embedded-spec: false
output: openapi.gen.go
output-options:
  skip-fmt: false
```

- [ ] **Step 2: Add the `go:generate` directive**

Create `openapi/gen.go`:

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

// Package openapi holds the pvapi 3.0 OpenAPI contract and the
// oapi-codegen-generated request/response types. Server glue is
// implemented by hand in the api package.
package openapi

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config=oapi-codegen.yaml openapi.yaml
```

- [ ] **Step 3: Add the runtime dep and the codegen tool dep**

Run:

```bash
go get github.com/oapi-codegen/runtime@latest
go get -tool github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
```

(`-tool` requires Go 1.24+. If your toolchain is older, fall back to `go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest` and add a note in the commit message.)

- [ ] **Step 4: Add a `gen` target to the Makefile**

Read `Makefile`, then append:

```makefile
gen:
	go generate ./openapi/...
```

- [ ] **Step 5: Run the generator**

Run: `make gen`
Expected: `openapi/openapi.gen.go` is created with generated types (several hundred lines). No stderr errors.

- [ ] **Step 6: Verify the build picks up the generated file**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 7: Add the generated file to lint exclusions**

Read `.golangci.yml`. Inside `linters.exclusions`, alongside the existing `rules:` list, add:

```yaml
    paths:
      - openapi/openapi.gen.go
```

Re-run `golangci-lint run` — expected `0 issues.`.

- [ ] **Step 8: Commit**

```bash
git add openapi/ Makefile go.mod go.sum .golangci.yml
git commit -m "$(cat <<'EOF'
wire up oapi-codegen for request/response types

Generates openapi/openapi.gen.go from openapi.yaml on `make gen`.
Types-only generation — handlers are hand-written against Fiber v3
so pvapi is not coupled to oapi-codegen's framework support.
Generated file is excluded from golangci-lint.
EOF
)"
```

---

## Task 5: Error-to-problem+json mapping

**Purpose:** Single helper that translates sentinel domain errors into RFC 7807 `application/problem+json` bodies. Handlers call `api.WriteProblem(c, err)`; the mapper picks the right status code and title.

**Files:**
- Create: `api/errors.go`
- Create: `api/errors_test.go`

- [ ] **Step 1: Write the failing test**

Create `api/errors_test.go`:

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

package api_test

import (
	"errors"
	"io"
	"net/http/httptest"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/api"
)

var _ = Describe("WriteProblem", func() {
	type problem struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}

	run := func(h fiber.Handler) problem {
		app := fiber.New()
		app.Get("/t", h)
		resp, err := app.Test(httptest.NewRequest("GET", "/t", nil))
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Header.Get("Content-Type")).To(Equal("application/problem+json"))
		var p problem
		Expect(sonic.Unmarshal(body, &p)).To(Succeed())
		Expect(p.Status).To(Equal(resp.StatusCode))
		return p
	}

	It("returns 404 for ErrNotFound", func() {
		p := run(func(c fiber.Ctx) error {
			return api.WriteProblem(c, api.ErrNotFound)
		})
		Expect(p.Status).To(Equal(404))
		Expect(p.Title).To(Equal("Not Found"))
	})

	It("returns 409 for ErrConflict", func() {
		p := run(func(c fiber.Ctx) error {
			return api.WriteProblem(c, api.ErrConflict)
		})
		Expect(p.Status).To(Equal(409))
	})

	It("returns 422 for ErrInvalidParams", func() {
		p := run(func(c fiber.Ctx) error {
			return api.WriteProblem(c, api.ErrInvalidParams)
		})
		Expect(p.Status).To(Equal(422))
	})

	It("returns 501 for ErrNotImplemented", func() {
		p := run(func(c fiber.Ctx) error {
			return api.WriteProblem(c, api.ErrNotImplemented)
		})
		Expect(p.Status).To(Equal(501))
	})

	It("returns 500 for unknown errors", func() {
		p := run(func(c fiber.Ctx) error {
			return api.WriteProblem(c, errors.New("boom"))
		})
		Expect(p.Status).To(Equal(500))
		Expect(p.Title).To(Equal("Internal Server Error"))
	})
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `ginkgo run ./api`
Expected: FAIL — `api.WriteProblem`, `api.ErrNotFound`, etc. are undefined.

- [ ] **Step 3: Implement `api/errors.go`**

Create `api/errors.go`:

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
	"errors"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog/log"
)

// Sentinel domain errors. Handlers return these; WriteProblem maps them
// to HTTP status codes. Domain packages export their own errors that wrap
// these sentinels so `errors.Is` matches.
var (
	ErrNotFound       = errors.New("resource not found")
	ErrConflict       = errors.New("resource conflict")
	ErrInvalidParams  = errors.New("invalid parameters")
	ErrNotImplemented = errors.New("not implemented")
)

// Problem is the RFC 7807 body pvapi emits on every error.
type Problem struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// WriteProblem maps err to a status + problem+json body and writes it to c.
// Unknown errors yield 500 with the error string logged (not exposed).
func WriteProblem(c fiber.Ctx, err error) error {
	status, title := classify(err)

	p := Problem{
		Type:     "about:blank",
		Title:    title,
		Status:   status,
		Detail:   err.Error(),
		Instance: c.Path(),
	}

	if status == fiber.StatusInternalServerError {
		log.Error().Err(err).Str("path", c.Path()).Msg("unexpected handler error")
		p.Detail = ""
	}

	body, marshalErr := sonic.Marshal(p)
	if marshalErr != nil {
		log.Error().Err(marshalErr).Msg("problem marshal failed")
		return c.SendStatus(fiber.StatusInternalServerError)
	}

	c.Set(fiber.HeaderContentType, "application/problem+json")
	return c.Status(status).Send(body)
}

func classify(err error) (int, string) {
	switch {
	case errors.Is(err, ErrNotFound):
		return fiber.StatusNotFound, "Not Found"
	case errors.Is(err, ErrConflict):
		return fiber.StatusConflict, "Conflict"
	case errors.Is(err, ErrInvalidParams):
		return fiber.StatusUnprocessableEntity, "Unprocessable Entity"
	case errors.Is(err, ErrNotImplemented):
		return fiber.StatusNotImplemented, "Not Implemented"
	default:
		return fiber.StatusInternalServerError, "Internal Server Error"
	}
}
```

- [ ] **Step 4: Run tests**

Run: `ginkgo run ./api`
Expected: PASS. All five WriteProblem specs green, plus the earlier api specs still pass.

- [ ] **Step 5: Commit**

```bash
git add api/errors.go api/errors_test.go
git commit -m "$(cat <<'EOF'
add api/errors.go: RFC 7807 problem+json mapping

Exports ErrNotFound / ErrConflict / ErrInvalidParams /
ErrNotImplemented sentinels and a WriteProblem helper that maps
wrapped sentinel errors to their HTTP status + problem+json body.
Unknown errors collapse to 500; detail is dropped in that case and
the full error is logged with the request path.
EOF
)"
```

---

## Task 6: Test-side JWKS harness

**Purpose:** Give the auth-middleware tests a real RS256 JWKS to verify against without committing a private key. A helper boots an in-process HTTP server in `BeforeSuite`, exposes its JWKS URL, and mints signed JWTs on demand.

**Files:**
- Create: `api/jwks_testing.go`
- Modify: `api/api_suite_test.go`

- [ ] **Step 1: Add the test-only JWKS helper**

`api/jwks_testing.go` is gated behind the `_test.go` naming convention by convention, but we want it callable from multiple test files. The canonical pattern for "test-only helper shared across tests" in Go is a file named `*_test.go` in the same package with package-scoped exports, OR a file in `package apitesting` under `api/apitesting/`. We use the latter so tests in this and later plans can import it cleanly.

Create `api/apitesting/jwks.go`:

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

// Package apitesting provides test-only helpers for exercising the api
// package's authentication stack: an RS256 keypair generated at BeforeSuite
// time and an in-process JWKS HTTP server the middleware can talk to.
package apitesting

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http/httptest"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

const (
	// Issuer is the iss claim the suite's JWKS represents.
	Issuer = "https://test.pvapi.local/"

	// Audience is the aud claim valid tokens must carry.
	Audience = "https://api.pvapi.local"
)

// JWKS is the test harness: a fresh RSA keypair, the matching jwk.Set
// served over HTTP, and a Mint method to produce signed tokens.
type JWKS struct {
	Server *httptest.Server
	URL    string

	priv jwk.Key
	set  jwk.Set
}

// NewJWKS boots a fresh JWKS harness. Call Close when done.
func NewJWKS() (*JWKS, error) {
	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generating RSA key: %w", err)
	}

	priv, err := jwk.Import(pk)
	if err != nil {
		return nil, fmt.Errorf("importing private key: %w", err)
	}
	if err := priv.Set(jwk.KeyIDKey, "test-key-1"); err != nil {
		return nil, fmt.Errorf("setting kid: %w", err)
	}
	if err := priv.Set(jwk.AlgorithmKey, jwa.RS256()); err != nil {
		return nil, fmt.Errorf("setting alg: %w", err)
	}

	pub, err := jwk.PublicKeyOf(priv)
	if err != nil {
		return nil, fmt.Errorf("deriving public key: %w", err)
	}

	set := jwk.NewSet()
	if err := set.AddKey(pub); err != nil {
		return nil, fmt.Errorf("adding key to set: %w", err)
	}

	body, err := jwk.Marshal(set)
	if err != nil {
		return nil, fmt.Errorf("marshaling set: %w", err)
	}

	server := httptest.NewServer(jwksHandler(body))

	return &JWKS{
		Server: server,
		URL:    server.URL,
		priv:   priv,
		set:    set,
	}, nil
}

// Close shuts the JWKS HTTP server down.
func (j *JWKS) Close() {
	j.Server.Close()
}

// Mint produces an RS256 JWT with the given subject, Audience, and Issuer
// set from the package defaults. ttl is how long from now the token should
// remain valid.
func (j *JWKS) Mint(subject string, ttl time.Duration) (string, error) {
	return j.MintWith(subject, Audience, Issuer, ttl)
}

// MintWith lets tests override audience / issuer to exercise failure cases.
// A negative ttl produces an already-expired token.
func (j *JWKS) MintWith(subject, audience, issuer string, ttl time.Duration) (string, error) {
	tok, err := jwt.NewBuilder().
		Issuer(issuer).
		Audience([]string{audience}).
		Subject(subject).
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(ttl)).
		Build()
	if err != nil {
		return "", fmt.Errorf("building token: %w", err)
	}

	payload, err := jwt.NewSerializer().Sign(jwt.WithKey(jwa.RS256(), j.priv)).Serialize(tok)
	if err != nil {
		return "", fmt.Errorf("signing token: %w", err)
	}
	return string(payload), nil
}

// SignRaw signs an arbitrary payload with the suite's key. Used only to
// test cases where the payload is not a valid JWT claim set.
func (j *JWKS) SignRaw(payload []byte) ([]byte, error) {
	return jws.Sign(payload, jws.WithKey(jwa.RS256(), j.priv))
}

// jwksHandler returns an http.HandlerFunc that serves the provided JWKS
// body at every request path.
func jwksHandler(body []byte) func(w interface{ WriteHeader(int); Write([]byte) (int, error); Header() map[string][]string }, _ interface{}) {
	// Use real http.Handler signature via httptest — this nested interface
	// is only here to sidestep the import ordering; rewrite to plain
	// http.Handler in implementation.
	return nil
}
```

**Implementer note:** the `jwksHandler` sketch above is placeholder — the generated form needs the real `net/http` types. Replace the `jwksHandler` definition with:

```go
// place near the top of jwks_testing.go, NOT in the ghosted signature above
import "net/http"

func jwksHandler(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/jwk-set+json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(body)
	}
}
```

And adjust `httptest.NewServer(jwksHandler(body))` accordingly. Remove the placeholder nested-interface signature before compiling.

- [ ] **Step 2: Wire the harness into `api_suite_test.go`**

Read `api/api_suite_test.go`. Replace its contents with:

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

package api_test

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/api/apitesting"
)

var testJWKS *apitesting.JWKS

func TestApi(t *testing.T) {
	prev := log.Logger
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: GinkgoWriter})
	defer func() { log.Logger = prev }()

	RegisterFailHandler(Fail)
	RunSpecs(t, "Api Suite")
}

var _ = BeforeSuite(func() {
	j, err := apitesting.NewJWKS()
	Expect(err).NotTo(HaveOccurred())
	testJWKS = j
})

var _ = AfterSuite(func() {
	if testJWKS != nil {
		testJWKS.Close()
	}
})
```

- [ ] **Step 3: Add the jwx dependencies**

Run:

```bash
go get github.com/lestrrat-go/jwx/v3@latest
go get github.com/lestrrat-go/httprc/v3@latest
```

- [ ] **Step 4: Build and run existing tests**

```bash
go build ./...
ginkgo run ./api
```

Expected: no compile errors. The five existing specs (NewApp + 4 middleware) still pass. The new `apitesting` package compiles even without any test using it yet.

- [ ] **Step 5: Commit**

```bash
git add api/apitesting go.mod go.sum api/api_suite_test.go
git commit -m "$(cat <<'EOF'
add apitesting package with RS256 JWKS harness

BeforeSuite generates a fresh RSA keypair and boots an httptest JWKS
server; Mint / MintWith produce valid or deliberately-broken tokens
for the upcoming auth middleware tests. No keys are committed.
EOF
)"
```

---

## Task 7: Auth0 JWT middleware

**Purpose:** Bearer-only Fiber v3 middleware that pulls a JWKS URL from config, verifies iss/aud/exp on every request, and stores the subject on `c.Locals` under a dedicated key. Unauth'd / invalid / expired tokens yield problem+json 401.

**Files:**
- Create: `api/auth.go`
- Create: `api/auth_test.go`
- Modify: `types/types.go`

- [ ] **Step 1: Add the subject locals key**

Read `types/types.go`, then append (inside the file, after `RequestIDKey`):

```go
// AuthSubjectKey is the Fiber locals key for the authenticated user's
// subject (Auth0 `sub` claim).
type AuthSubjectKey struct{}
```

- [ ] **Step 2: Write the failing auth tests**

Create `api/auth_test.go`:

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

package api_test

import (
	"context"
	"net/http/httptest"
	"time"

	"github.com/gofiber/fiber/v3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/api"
	"github.com/penny-vault/pv-api/api/apitesting"
	"github.com/penny-vault/pv-api/types"
)

var _ = Describe("JWT middleware", func() {
	var app *fiber.App

	BeforeEach(func() {
		ctx := context.Background()
		mw, err := api.NewAuthMiddleware(ctx, api.AuthConfig{
			JWKSURL:  testJWKS.URL,
			Audience: apitesting.Audience,
			Issuer:   apitesting.Issuer,
		})
		Expect(err).NotTo(HaveOccurred())

		app = fiber.New()
		app.Use(mw)
		app.Get("/secure", func(c fiber.Ctx) error {
			sub, _ := c.Locals(types.AuthSubjectKey{}).(string)
			return c.SendString("hello " + sub)
		})
	})

	It("rejects requests with no Authorization header", func() {
		resp, err := app.Test(httptest.NewRequest("GET", "/secure", nil))
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnauthorized))
		Expect(resp.Header.Get("Content-Type")).To(Equal("application/problem+json"))
	})

	It("rejects a malformed Authorization header", func() {
		req := httptest.NewRequest("GET", "/secure", nil)
		req.Header.Set("Authorization", "NotBearer garbage")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnauthorized))
	})

	It("rejects an expired token", func() {
		tok, err := testJWKS.Mint("user-1", -1*time.Hour)
		Expect(err).NotTo(HaveOccurred())
		req := httptest.NewRequest("GET", "/secure", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnauthorized))
	})

	It("rejects a token with the wrong audience", func() {
		tok, err := testJWKS.MintWith("user-1", "wrong-audience", apitesting.Issuer, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		req := httptest.NewRequest("GET", "/secure", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnauthorized))
	})

	It("rejects a token with the wrong issuer", func() {
		tok, err := testJWKS.MintWith("user-1", apitesting.Audience, "https://evil.example/", time.Hour)
		Expect(err).NotTo(HaveOccurred())
		req := httptest.NewRequest("GET", "/secure", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnauthorized))
	})

	It("accepts a valid token and stores sub on locals", func() {
		tok, err := testJWKS.Mint("user-42", time.Hour)
		Expect(err).NotTo(HaveOccurred())
		req := httptest.NewRequest("GET", "/secure", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))
	})
})
```

- [ ] **Step 3: Run to confirm red**

Run: `ginkgo run ./api`
Expected: FAIL — `api.NewAuthMiddleware`, `api.AuthConfig` undefined.

- [ ] **Step 4: Implement `api/auth.go`**

Create `api/auth.go`:

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
// verification. The middleware converts it to a 401 problem+json.
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
```

- [ ] **Step 5: Teach `WriteProblem` to map `ErrInvalidToken` to 401**

Read `api/errors.go`. In the `classify` switch, add the `ErrInvalidToken` arm before the default. The final switch should look like:

```go
func classify(err error) (int, string) {
	switch {
	case errors.Is(err, ErrInvalidToken):
		return fiber.StatusUnauthorized, "Unauthorized"
	case errors.Is(err, ErrNotFound):
		return fiber.StatusNotFound, "Not Found"
	case errors.Is(err, ErrConflict):
		return fiber.StatusConflict, "Conflict"
	case errors.Is(err, ErrInvalidParams):
		return fiber.StatusUnprocessableEntity, "Unprocessable Entity"
	case errors.Is(err, ErrNotImplemented):
		return fiber.StatusNotImplemented, "Not Implemented"
	default:
		return fiber.StatusInternalServerError, "Internal Server Error"
	}
}
```

- [ ] **Step 6: Run to confirm green**

Run: `ginkgo run ./api`
Expected: all specs pass (previous 5 api specs + 5 WriteProblem specs + 6 auth specs).

- [ ] **Step 7: Commit**

```bash
git add api/auth.go api/auth_test.go api/errors.go types/types.go go.mod go.sum
git commit -m "$(cat <<'EOF'
add Auth0 JWT middleware for Fiber v3

NewAuthMiddleware returns a Bearer-only handler that verifies iss/aud/exp
against a jwx JWK cache. Subject is stashed on types.AuthSubjectKey.
Missing / malformed / expired / wrong-aud / wrong-iss tokens all yield
401 problem+json. WriteProblem gains an ErrInvalidToken arm.
EOF
)"
```

---

## Task 8: Portfolio stub handlers

**Purpose:** One handler function per portfolio endpoint. Each returns `ErrNotImplemented` via `WriteProblem` — 501 with a problem+json body. Real bodies land in Plan 3.

**Files:**
- Create: `api/portfolios.go`
- Create: `api/portfolios_test.go`

- [ ] **Step 1: Write the failing tests**

Create `api/portfolios_test.go`:

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

package api_test

import (
	"net/http/httptest"

	"github.com/gofiber/fiber/v3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/api"
)

var _ = Describe("Portfolio handlers", func() {
	var app *fiber.App

	BeforeEach(func() {
		app = fiber.New()
		api.RegisterPortfolioRoutes(app)
	})

	DescribeTable("stub endpoints return 501 problem+json",
		func(method, path string) {
			req := httptest.NewRequest(method, path, nil)
			resp, err := app.Test(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(fiber.StatusNotImplemented))
			Expect(resp.Header.Get("Content-Type")).To(Equal("application/problem+json"))
		},
		Entry("list portfolios", "GET", "/portfolios"),
		Entry("create portfolio", "POST", "/portfolios"),
		Entry("get portfolio", "GET", "/portfolios/adm-standard-aq35"),
		Entry("update portfolio", "PATCH", "/portfolios/adm-standard-aq35"),
		Entry("delete portfolio", "DELETE", "/portfolios/adm-standard-aq35"),
		Entry("get measurements", "GET", "/portfolios/adm-standard-aq35/measurements"),
		Entry("trigger run", "POST", "/portfolios/adm-standard-aq35/runs"),
		Entry("list runs", "GET", "/portfolios/adm-standard-aq35/runs"),
		Entry("get run", "GET", "/portfolios/adm-standard-aq35/runs/019d9a15-54cc-7db7-84cc-a5b6875bf27d"),
	)
})
```

- [ ] **Step 2: Run to confirm red**

Run: `ginkgo run ./api`
Expected: FAIL — `api.RegisterPortfolioRoutes` undefined.

- [ ] **Step 3: Implement `api/portfolios.go`**

Create `api/portfolios.go`:

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
)

// RegisterPortfolioRoutes mounts every portfolio-related endpoint on the
// provided router. All handlers are stubs returning 501 until Plan 3 fills
// them in.
func RegisterPortfolioRoutes(r fiber.Router) {
	r.Get("/portfolios", listPortfolios)
	r.Post("/portfolios", createPortfolio)
	r.Get("/portfolios/:slug", getPortfolio)
	r.Patch("/portfolios/:slug", updatePortfolio)
	r.Delete("/portfolios/:slug", deletePortfolio)
	r.Get("/portfolios/:slug/measurements", getPortfolioMeasurements)
	r.Post("/portfolios/:slug/runs", triggerPortfolioRun)
	r.Get("/portfolios/:slug/runs", listPortfolioRuns)
	r.Get("/portfolios/:slug/runs/:runId", getPortfolioRun)
}

func listPortfolios(c fiber.Ctx) error           { return WriteProblem(c, ErrNotImplemented) }
func createPortfolio(c fiber.Ctx) error          { return WriteProblem(c, ErrNotImplemented) }
func getPortfolio(c fiber.Ctx) error             { return WriteProblem(c, ErrNotImplemented) }
func updatePortfolio(c fiber.Ctx) error          { return WriteProblem(c, ErrNotImplemented) }
func deletePortfolio(c fiber.Ctx) error          { return WriteProblem(c, ErrNotImplemented) }
func getPortfolioMeasurements(c fiber.Ctx) error { return WriteProblem(c, ErrNotImplemented) }
func triggerPortfolioRun(c fiber.Ctx) error      { return WriteProblem(c, ErrNotImplemented) }
func listPortfolioRuns(c fiber.Ctx) error        { return WriteProblem(c, ErrNotImplemented) }
func getPortfolioRun(c fiber.Ctx) error          { return WriteProblem(c, ErrNotImplemented) }
```

- [ ] **Step 4: Run to confirm green**

Run: `ginkgo run ./api`
Expected: all specs pass (previous + 9 new portfolio entries).

- [ ] **Step 5: Commit**

```bash
git add api/portfolios.go api/portfolios_test.go
git commit -m "$(cat <<'EOF'
add portfolio route stubs returning 501

Every portfolio endpoint described in the OpenAPI contract has a
handler function and a test proving it returns 501 problem+json.
Real bodies land in the next plan (portfolio slice + host runner).
EOF
)"
```

---

## Task 9: Strategy stub handlers

**Purpose:** Same shape as portfolios — three strategy endpoints, each returns 501 via `WriteProblem(c, ErrNotImplemented)`. Real bodies land in Plan 5.

**Files:**
- Create: `api/strategies.go`
- Create: `api/strategies_test.go`

- [ ] **Step 1: Write the failing tests**

Create `api/strategies_test.go`:

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

package api_test

import (
	"net/http/httptest"

	"github.com/gofiber/fiber/v3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/api"
)

var _ = Describe("Strategy handlers", func() {
	var app *fiber.App

	BeforeEach(func() {
		app = fiber.New()
		api.RegisterStrategyRoutes(app)
	})

	DescribeTable("stub endpoints return 501 problem+json",
		func(method, path string) {
			req := httptest.NewRequest(method, path, nil)
			resp, err := app.Test(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(fiber.StatusNotImplemented))
			Expect(resp.Header.Get("Content-Type")).To(Equal("application/problem+json"))
		},
		Entry("list strategies", "GET", "/strategies"),
		Entry("register unofficial", "POST", "/strategies"),
		Entry("get strategy", "GET", "/strategies/adm"),
	)
})
```

- [ ] **Step 2: Run to confirm red**

Run: `ginkgo run ./api`
Expected: FAIL — `api.RegisterStrategyRoutes` undefined.

- [ ] **Step 3: Implement `api/strategies.go`**

Create `api/strategies.go`:

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
)

// RegisterStrategyRoutes mounts the strategy endpoints on the provided router.
// All handlers are stubs returning 501 until Plans 5/6 fill them in.
func RegisterStrategyRoutes(r fiber.Router) {
	r.Get("/strategies", listStrategies)
	r.Post("/strategies", registerUnofficialStrategy)
	r.Get("/strategies/:shortCode", getStrategy)
}

func listStrategies(c fiber.Ctx) error              { return WriteProblem(c, ErrNotImplemented) }
func registerUnofficialStrategy(c fiber.Ctx) error  { return WriteProblem(c, ErrNotImplemented) }
func getStrategy(c fiber.Ctx) error                 { return WriteProblem(c, ErrNotImplemented) }
```

- [ ] **Step 4: Run to confirm green**

Run: `ginkgo run ./api`
Expected: all specs pass.

- [ ] **Step 5: Commit**

```bash
git add api/strategies.go api/strategies_test.go
git commit -m "$(cat <<'EOF'
add strategy route stubs returning 501

Three strategy endpoints — list, register unofficial, get detail —
each served by a stub handler returning 501 problem+json. Real
bodies land in the strategy-registry plan.
EOF
)"
```

---

## Task 10: Wire auth + routes into `api.NewApp` and the server subcommand

**Purpose:** `/healthz` stays public; every other route runs under the auth middleware. Config gains an `[auth0]` section. The `pvapi server` command reads it and passes values into `api.NewApp`.

**Files:**
- Modify: `api/server.go`
- Modify: `api/server_test.go`
- Modify: `cmd/config.go`
- Modify: `cmd/server.go`
- Modify: `pvapi.toml`

- [ ] **Step 1: Add the Auth0 config block**

Read `cmd/config.go`. Replace its contents with:

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

// Config is the top-level pvapi configuration shape. New sections are added
// as later plans land (runner, strategy, scheduler, ...).
type Config struct {
	Log    logConf
	Server serverConf
	Auth0  auth0Conf
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

var conf Config
```

- [ ] **Step 2: Teach `cmd/server.go` to pass Auth0 config into `api.NewApp`**

Read `cmd/server.go`. Replace the flag registrations and the `api.NewApp` call. The full file becomes:

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

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/penny-vault/pv-api/api"
)

func init() {
	rootCmd.AddCommand(serverCmd)

	serverCmd.Flags().Int("server-port", 3000, "port to bind the HTTP server to")
	serverCmd.Flags().String("server-allow-origins", "http://localhost:9000", "single CORS origin to allow; empty disables CORS")
	serverCmd.Flags().String("auth0-jwks-url", "", "Auth0 JWKS URL for JWT verification")
	serverCmd.Flags().String("auth0-audience", "", "Auth0 API audience")
	serverCmd.Flags().String("auth0-issuer", "", "Auth0 issuer URL")
	bindPFlagsToViper(serverCmd)
}

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Run the pvapi HTTP server",
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		app, err := api.NewApp(ctx, api.Config{
			Port:         conf.Server.Port,
			AllowOrigins: conf.Server.AllowOrigins,
			Auth: api.AuthConfig{
				JWKSURL:  conf.Auth0.JWKSURL,
				Audience: conf.Auth0.Audience,
				Issuer:   conf.Auth0.Issuer,
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
}
```

- [ ] **Step 3: Update `api/server.go`: take ctx, embed AuthConfig, mount auth'd routes**

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

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
)

// Config holds HTTP-layer configuration.
type Config struct {
	Port         int
	AllowOrigins string
	Auth         AuthConfig
}

// NewApp builds a Fiber v3 app with pvapi's middleware stack and routes.
// /healthz is public; every other route is mounted under the auth middleware.
// ctx controls the JWK cache lifecycle.
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

	// Public routes
	app.Get("/healthz", Healthz)

	// Protected routes
	auth, err := NewAuthMiddleware(ctx, conf.Auth)
	if err != nil {
		return nil, fmt.Errorf("build auth middleware: %w", err)
	}
	protected := app.Group("", auth)
	RegisterPortfolioRoutes(protected)
	RegisterStrategyRoutes(protected)

	return app, nil
}
```

- [ ] **Step 4: Update `api/server_test.go` to pass a ctx + Auth block**

Read `api/server_test.go`. Replace its contents with:

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

package api_test

import (
	"context"
	"io"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/api"
	"github.com/penny-vault/pv-api/api/apitesting"
)

var _ = Describe("NewApp", func() {
	newApp := func() *api.App {
		app, err := api.NewApp(context.Background(), api.Config{
			Auth: api.AuthConfig{
				JWKSURL:  testJWKS.URL,
				Audience: apitesting.Audience,
				Issuer:   apitesting.Issuer,
			},
		})
		Expect(err).NotTo(HaveOccurred())
		return app
	}

	// Note: `api.App` alias is used here for brevity; if the code returns
	// *fiber.App directly (it does), import that and remove the alias.

	It("responds 200 on GET /healthz with body 'ok'", func() {
		app := newApp()
		resp, err := app.Test(httptest.NewRequest("GET", "/healthz", nil))
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(200))
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(Equal("ok"))
	})

	It("rejects a request to /portfolios without a JWT", func() {
		app := newApp()
		resp, err := app.Test(httptest.NewRequest("GET", "/portfolios", nil))
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(401))
	})

	It("returns 501 on /portfolios with a valid JWT", func() {
		app := newApp()
		tok, err := testJWKS.Mint("user-1", time.Hour)
		Expect(err).NotTo(HaveOccurred())
		req := httptest.NewRequest("GET", "/portfolios", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(501))
	})
})
```

**Implementer note:** `*api.App` is not defined — the function returns `*fiber.App`. Adjust the helper signature to `*fiber.App` and add the `"github.com/gofiber/fiber/v3"` import. Remove the `api.App` alias comment. Final imports block:

```go
import (
	"context"
	"io"
	"net/http/httptest"
	"time"

	"github.com/gofiber/fiber/v3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/api"
	"github.com/penny-vault/pv-api/api/apitesting"
)
```

And `newApp := func() *fiber.App { ... }`.

- [ ] **Step 5: Update `pvapi.toml` with the `[auth0]` section**

Read `pvapi.toml`. Replace its contents with:

```toml
[server]
port          = 3000
allow_origins = "http://localhost:9000"

[auth0]
jwks_url = ""
audience = ""
issuer   = ""

[db]
url = "postgres://jdf@localhost/pvapi"

[log]
level         = "info"
output        = "stdout"
pretty        = true
report_caller = false
```

Note: `pvapi.toml` is gitignored — this change is not committed; it's a local-dev reference only.

- [ ] **Step 6: Run the full api suite**

Run: `ginkgo run ./api`
Expected: every spec passes (existing + 3 new NewApp specs).

- [ ] **Step 7: Commit**

```bash
git add api/server.go api/server_test.go cmd/config.go cmd/server.go
git commit -m "$(cat <<'EOF'
wire auth onto the protected route group

api.NewApp now takes a ctx and an Auth block, builds the JWT
middleware, and mounts portfolios + strategies under a protected
fiber.Group. /healthz stays public. cmd/server.go gains the auth0
flags; Config carries an [auth0] section.
EOF
)"
```

---

## Task 11: End-to-end smoke

**Purpose:** Final gate before closing Plan 2. Build, lint, run the full Ginkgo suite, verify the live server enforces auth.

- [ ] **Step 1: Clean build**

Run: `make build`
Expected: `pvapi` binary produced; no errors.

- [ ] **Step 2: Lint**

Run: `make lint`
Expected: no errors.

- [ ] **Step 3: Full test run**

Run: `ginkgo run -r --race`
Expected: `Api Suite` runs all specs (NewApp + middleware + WriteProblem + Auth + Portfolio + Strategy) — roughly 25–30 It blocks. `0 Failed`.

- [ ] **Step 4: Live smoke — auth enforced**

In a shell:

```bash
./pvapi server --server-port 3030 \
  --auth0-jwks-url https://pvapi.local/.well-known/jwks.json \
  --auth0-audience test \
  --auth0-issuer https://pvapi.local/ > /tmp/pvapi-smoke.log 2>&1 &
PID=$!
sleep 1
echo "--- healthz (public) ---"
curl -s -o /dev/null -w 'healthz=%{http_code}\n' http://localhost:3030/healthz
echo "--- portfolios no-auth (expect 401) ---"
curl -s -w 'http_code=%{http_code}\n' http://localhost:3030/portfolios
echo "--- headers ---"
curl -sI http://localhost:3030/portfolios | grep -iE '^(content-type|x-request-id):'
kill -TERM $PID
wait $PID 2>/dev/null
rm -f /tmp/pvapi-smoke.log
```

Expected:
- `healthz=200`
- `/portfolios` returns `http_code=401`
- Response headers include `Content-Type: application/problem+json` and an `X-Request-Id`.
- Server exits cleanly after SIGTERM.

The server will error at startup if it cannot reach the JWKS URL — for this smoke we're only validating that the middleware refuses unauthenticated requests. If the startup fails because `httprc` cannot reach the dummy URL, substitute a valid test URL (the suite's `BeforeSuite` provides one at test time; for the live smoke any reachable JSON endpoint will do, since we're not minting tokens).

If the live JWKS fetch is blocking even the 401 response, that is a defect — file a follow-up, but for now the suite passing is the blocking gate.

- [ ] **Step 5: Push the branch**

```bash
git push -u origin pvapi-3-auth-schema
```

No commit on this task; verification only. Plan 2 is complete when every step above passes.

---

## Self-review summary

- **Spec coverage:**
  - Schema (spec § Data model) → Task 1 ✓
  - OpenAPI copy-in (spec § API surface) → Task 2 ✓
  - OpenAPI lifecycle extensions (spec § Create-portfolio request body, Conventions) → Task 3 ✓
  - oapi-codegen types (spec § API surface / "Generate Go server types") → Task 4 ✓
  - RFC 7807 error mapping (spec § Errors) → Task 5 ✓
  - Auth0 JWT verification (spec § Auth) → Tasks 6–7 ✓
  - Public /healthz + protected group (spec § Auth) → Task 10 ✓
  - Stub handlers for every endpoint → Tasks 8–9 ✓
  - Testing policy: no DB, ginkgo-only, runtime-generated keys → Tasks 5–10 ✓
  - Fiber v3, pgx v5 (carried from Plan 1) → Tasks 1, 10 ✓

  **Deliberately deferred to later plans:** parameter validation vs strategy describe output (Plan 5), slug generation logic (Plan 3), live GitHub discovery (Plan 5), continuous scheduler (Plan 4), runners (Plans 7–9). These are handler body concerns, not route-surface concerns.

- **Placeholder scan:** no `TBD`, `TODO`, or "handle edge cases" steps. Task 6's nested-interface sketch for `jwksHandler` has an explicit implementer note that walks through the fix to plain `http.Handler`; the final form is shown in that note.

- **Type consistency:**
  - `api.AuthConfig{JWKSURL, Audience, Issuer}` — used in Tasks 7 (middleware), 10 (server wiring), matches `cmd.auth0Conf` via field names.
  - `api.ErrInvalidToken / ErrNotFound / ErrConflict / ErrInvalidParams / ErrNotImplemented` — declared in Task 5, consumed in Task 7 (token) and Tasks 8–9 (NotImplemented).
  - `types.AuthSubjectKey` — introduced in Task 7, read in auth tests and later plans.
  - `apitesting.NewJWKS`, `Mint`, `MintWith`, `Audience`, `Issuer` — declared in Task 6, consumed in Task 7 and Task 10.
  - `api.RegisterPortfolioRoutes` / `api.RegisterStrategyRoutes` — declared in Tasks 8–9, consumed in Task 10.
  - `api.NewApp(ctx, Config) (*fiber.App, error)` — signature changed in Task 10; Plan 1's signature `NewApp(Config) *fiber.App` is superseded there. Both call sites (cmd/server.go, server_test.go) are updated in the same commit.
