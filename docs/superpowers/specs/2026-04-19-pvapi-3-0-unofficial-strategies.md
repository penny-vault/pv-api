# pvapi 3.0 — unofficial strategies (plan 7)

**Status:** draft for review
**Date:** 2026-04-19
**Author:** Jeremy Fergason (with Claude)
**Supersedes:** the "Unofficial strategies" subsection of
`2026-04-16-pvapi-3-0-design.md` (§ Strategy lifecycle / Unofficial
strategies). The parent spec assumed `POST /strategies` would create
owner-scoped registry rows with `is_official = FALSE` and that
`?include=unofficial` would filter list output. This spec replaces that
model: there is no per-user registry of unofficial strategies — the
clone URL lives on the portfolio.

## Summary

Plan 7 lets users create portfolios from arbitrary GitHub strategy
repositories without pre-registering them. Every portfolio (official or
otherwise) carries the strategy clone URL and a frozen copy of the
strategy's describe output. At backtest time the runner consults a
build cache keyed by `(clone_url, strategy_ver)`; cache hits use a
pre-built binary, cache misses clone+build into a tempdir, run, and
clean up.

The `strategies` table stops being a registration target and becomes
exclusively a build cache populated by the official sync goroutine. No
new "unofficial registry" surface ships.

## Goals

- A user can paste a GitHub clone URL into the UI, get back the
  strategy's parameter list, fill in parameters, and create a working
  portfolio — without anyone publishing the strategy first.
- A single backtest code path serves both official and unofficial
  portfolios.
- No new long-lived state for unofficials (no rows in `strategies`, no
  per-user registry).
- Same security posture as the rest of plan 7's runner story: host
  runner is unsandboxed by design; sandboxing arrives with the Docker
  (plan 8) and Kubernetes (plan 9) runners.

## Non-goals

- Caching of unofficial builds. Every backtest of an unofficial
  portfolio re-clones and rebuilds. Performance-as-a-follow-up.
- Pinning unofficial portfolios to a specific tag/SHA. If a user wants
  reproducibility they fork the repo and paste the fork URL.
- Sandboxing the toolchain (build/run). Tracked under plans 8–9.
- Per-user listing of "strategies I've used." Derivable client-side
  from the portfolio list.
- Supporting non-GitHub hosts. The URL allowlist is `github.com` only.

## Storage

### Migration `6_unofficial_strategies`

```sql
ALTER TABLE portfolios
    ADD COLUMN strategy_clone_url   TEXT,
    ADD COLUMN strategy_describe_json JSONB;

UPDATE portfolios p
   SET strategy_clone_url    = s.clone_url,
       strategy_describe_json = s.describe_json
  FROM strategies s
 WHERE p.strategy_code = s.short_code;

ALTER TABLE portfolios
    ALTER COLUMN strategy_clone_url    SET NOT NULL,
    ALTER COLUMN strategy_describe_json SET NOT NULL,
    ALTER COLUMN strategy_ver          DROP NOT NULL,
    DROP CONSTRAINT portfolios_strategy_code_fkey;

CREATE INDEX idx_strategies_clone_ver
    ON strategies(clone_url, installed_ver)
 WHERE install_error IS NULL;
```

Down migration drops the new columns + index, restores NOT NULL on
`strategy_ver`, and re-adds the FK.

Notes:

- `strategy_code` stays NOT NULL but no longer FKs anywhere. For an
  official portfolio it remains the strategy's `short_code`; for an
  unofficial portfolio it is `describe.shortCode` from the inline
  describe call. Used for display and for slug generation. An
  unofficial whose describe declares the same `shortCode` as an
  official is allowed — the `(owner_sub, slug)` uniqueness check
  still distinguishes them via the FNV hash of
  `(params, mode, schedule, benchmark)`.
- `strategies.is_official` and `strategies.owner_sub` stay in the
  schema (vestigial — every row will be `is_official = TRUE`,
  `owner_sub = NULL` going forward). Dropping them is not worth a
  migration; the existing CHECK constraint stays trivially satisfied.

### Backfill

The migration backfills `strategy_clone_url` and `strategy_describe_json`
for every existing portfolio row from its referenced strategy row before
the columns flip to NOT NULL. Any portfolio whose `strategy_code` does
not resolve to a strategy row would block the migration — at the time
of writing there are no production deployments, so this is acceptable
sharp-edge behavior. If one ever appears it is a data-quality bug that
deserves to surface loudly.

## API

### `GET /strategies/describe`

Replaces `POST /strategies`. The OpenAPI patch:

- Remove the `post:` block under `/strategies`.
- Remove the `StrategyRegisterRequest` schema.
- Add a new path `/strategies/describe`:

```yaml
/strategies/describe:
  get:
    tags: [Strategies]
    operationId: describeStrategy
    summary: Clone, build, and describe a strategy repository
    parameters:
      - name: cloneUrl
        in: query
        required: true
        description: HTTPS GitHub clone URL.
        schema:
          type: string
          format: uri
    responses:
      '200':
        description: Describe output
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/StrategyDescribe'
      '400':
        $ref: '#/components/responses/BadRequest'
      '401':
        $ref: '#/components/responses/Unauthorized'
      '422':
        $ref: '#/components/responses/UnprocessableEntity'
      '500':
        $ref: '#/components/responses/ServerError'
```

Server clones the URL into `mkdtemp(strategy.ephemeral_dir)`, runs
`go build -o <dir>/bin .`, executes `<bin> describe --json`, returns
the describe JSON, and removes the tempdir on the way out (deferred,
runs on success and on every error). The whole sequence is bounded by
`strategy.ephemeral_install_timeout` (default 60s) via a single
`context.WithTimeout`.

400 covers an obviously invalid `cloneUrl` (allowlist failure, missing
param). 422 covers clone/build/describe failures with the toolchain
output in `detail`.

### `GET /strategies` and `?include=unofficial`

Drop the `?include=unofficial` query parameter from the OpenAPI. List
remains the curated catalog (officials only — there are no other rows
to surface).

`GET /strategies/{shortCode}` is unchanged.

### `POST /portfolios`

Add one optional field to the request body:

```yaml
strategyCloneUrl:
  type: string
  format: uri
  description: |
    GitHub clone URL for an unofficial strategy. Mutually exclusive
    with `strategyCode`.
```

Encoded in the schema as `oneOf`: exactly one of `strategyCode` or
`strategyCloneUrl` must be present. `strategyVer` becomes nullable in
the request and the response.

Handler behavior splits on which field is set:

- **`strategyCode`** (existing path): unchanged. Looks up the strategy
  row, copies `clone_url` and `describe_json` onto the portfolio,
  pins `strategy_ver = installed_ver`. All plan-4 validation rules
  apply.
- **`strategyCloneUrl`** (new path):
  1. Validate the URL against the allowlist (see § Security).
  2. Call `strategy.EphemeralBuild(ctx, cloneURL, "")` to get a built
     binary + a cleanup closure (the binary is needed for one
     describe call only; cleanup runs in a defer).
  3. Run `<bin> describe --json`, unmarshal to `strategy.Describe`.
  4. Validate request parameters against the describe via
     `portfolio.ValidateCreateUnofficial`.
  5. Persist the portfolio with `strategy_clone_url = <url>`,
     `strategy_describe_json = <raw bytes>`,
     `strategy_code = describe.shortCode`, `strategy_ver = NULL`.
  6. Compute the slug using the inline describe (preset matching is
     identical to the official path).
  7. If `runNow=true` or `mode=one_shot`, dispatch as today.
  8. Cleanup closure removes the tempdir before returning.

The official path keeps the existing behavior including the
`ErrStrategyNotReady` / `ErrStrategyVersionMismatch` checks. The
unofficial path skips the install-state check (there is no install
row) and skips version validation (there is no version).

### `Portfolio` config response

Add `strategyCloneUrl: string` (required for new responses; old rows
have it after backfill). `strategyVer` becomes nullable.

The remaining derived endpoints (`/summary`, `/holdings`, etc.) are
unaffected.

## Backtest resolver

The orchestrator's binary resolver changes signature in
`backtest/run.go`:

```go
// before
type BinaryResolver func(code, ver string) (string, error)

// after
type BinaryResolver func(ctx context.Context, cloneURL, ver string) (binPath string, cleanup func(), err error)
```

`cleanup` is `func() {}` for cached binaries and `os.RemoveAll(tmpdir)`
for ephemeral builds. The orchestrator calls `defer cleanup()` after
acquiring the binary, so the tempdir is removed regardless of
runner success/failure.

The closure built in `cmd/server.go` becomes:

```go
resolve := func(ctx context.Context, cloneURL, ver string) (string, func(), error) {
    if ver != "" {
        artifact, err := strategyStore.LookupArtifact(ctx, cloneURL, ver)
        if err == nil && artifact != "" {
            return artifact, func() {}, nil
        }
        if err != nil && !errors.Is(err, strategy.ErrNotFound) {
            return "", nil, err
        }
    }
    return strategy.EphemeralBuild(ctx, strategy.EphemeralOptions{
        CloneURL: cloneURL, Ver: ver,
        Dir: conf.Strategy.EphemeralDir,
        Timeout: conf.Strategy.EphemeralInstallTimeout,
    })
}
```

`strategy.LookupArtifact(ctx, cloneURL, installedVer)` is a new
`store.go` helper backed by:

```sql
SELECT artifact_ref
  FROM strategies
 WHERE clone_url = $1 AND installed_ver = $2 AND install_error IS NULL
 LIMIT 1
```

Returns `("", strategy.ErrNotFound)` when the row is missing.

`backtest.PortfolioRow` exposes `StrategyCloneURL string` and the
existing `StrategyVer string` (now possibly empty). The orchestrator
reads both off the portfolio and hands them to the resolver. Officials
provide a non-empty `Ver` and hit the cache; unofficials provide an
empty `Ver` and skip straight to ephemeral.

## strategy package additions

New file `strategy/ephemeral.go`:

```go
type EphemeralOptions struct {
    CloneURL string        // required
    Ver      string        // optional; empty = clone HEAD of default branch
    Dir      string        // parent dir for mkdtemp; default = /tmp/pvapi-strategies
    Timeout  time.Duration // 0 = 60s
}

// EphemeralBuild clones, builds, and returns the path to a fresh
// binary plus a cleanup closure that removes the tempdir. The cleanup
// closure is safe to call multiple times.
func EphemeralBuild(ctx context.Context, opts EphemeralOptions) (binPath string, cleanup func(), err error)

// ValidateCloneURL returns an error if cloneURL is not an HTTPS URL
// pointing at github.com/<owner>/<repo>(.git)?.
func ValidateCloneURL(cloneURL string) error
```

`EphemeralBuild` derives a `context.WithTimeout` from `opts.Timeout`,
calls `os.MkdirTemp(opts.Dir, "build-*")`, runs the same clone/build
pipeline as `Install` (but without the describe-and-validate step —
describe is the caller's job since the caller already knows whether
they need it), and returns `(<dir>/bin, removeAll, nil)`. On any
error before returning, `EphemeralBuild` removes the tempdir itself
and returns `("", nil, err)`.

The clone command shape: `git clone --depth=1 <cloneURL> <dir>` when
`Ver == ""`, else `git clone --depth=1 --branch <Ver> <cloneURL>
<dir>`. (Plan 4/5 changed `Install` to clone with `--depth=1
--branch`; ephemeral matches that pattern except the branch flag is
optional.)

New file `strategy/describe.go`:

```go
// RunDescribe executes <binPath> describe --json and returns the raw
// stdout. Lives in strategy/describe.go and is reused by the create-
// portfolio handler (which needs describe after EphemeralBuild) and by
// DescribeHandler.
func RunDescribe(ctx context.Context, binPath string) ([]byte, error)

type DescribeHandler struct {
    Builder      func(ctx context.Context, opts EphemeralOptions) (string, func(), error) // = EphemeralBuild
    URLValidator func(string) error                                                        // = ValidateCloneURL
    EphemeralOpts EphemeralOptions                                                          // Dir + Timeout passed through to Builder
}

func (h *DescribeHandler) Describe(c fiber.Ctx) error
```

`Describe` reads `cloneUrl` from the query string, validates it, calls
`Builder` (which builds, returns binary path + cleanup), `defer`s the
cleanup, runs `RunDescribe`, returns the parsed describe as JSON. All
errors route through the existing `writeProblem` helper.

New `Store` method `LookupArtifact(ctx, cloneURL, ver) (string, error)`
on `PoolStore` and on the `Store` interface (so existing tests' fake
stores extend cleanly).

## portfolio package additions

`portfolio.CreateRequest` gains `StrategyCloneURL string` (zero value
== unused). `Mode` / `Schedule` validation is unchanged.

`portfolio.ValidateCreateUnofficial(req CreateRequest, d strategy.Describe)
(CreateRequest, error)` mirrors `ValidateCreate` but takes an inline
describe instead of a `strategy.Strategy` row, and skips:

- Strategy-installed check (no install lifecycle).
- Strategy-version-matches-installed check (no installed version).

It still runs:

- Mode/schedule validation (`validateMode`).
- Parameter validation against describe (`validateParameters`).
- Default benchmark from describe.

The existing `ValidateCreate` is unchanged.

`portfolio.Handler.Create` branches once at the top:

```go
if req.StrategyCloneURL != "" && req.StrategyCode != "" {
    return writeProblem(c, fiber.StatusBadRequest, "Bad Request",
        "exactly one of strategyCode or strategyCloneUrl is required")
}
if req.StrategyCloneURL != "" {
    return h.createUnofficial(c, req)
}
return h.createOfficial(c, req) // current Create renamed
```

`createUnofficial` does URL validation, ephemeral build, describe,
parameter validation, slug generation, insert, dispatch. It uses the
same `Insert` path as the official flow — only the column values
differ.

## Slug generation

Unchanged. `slug.go` already takes `describe.Presets` and the request
parameters; it does not care whether the describe came from a
strategy row or an inline build. `strategy_code` (which feeds the
short-code segment of the slug) is `describe.shortCode` for both
flows.

## Security and limits

Plan 7 ships unsandboxed for the host runner. Defenses:

- **URL allowlist**: `^https://github\.com/[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+(\.git)?$`.
  Rejects ssh, file://, gitlab, raw IPs, anything with a query
  string. Enforced in the describe handler and in
  `createUnofficial`. Test fixtures get a relaxed validator via
  dependency injection (so `file://` URLs work in unit tests).
- **Single ctx-bound timeout** on every clone+build+describe sequence.
  Default 60s; configurable via `strategy.ephemeral_install_timeout`.
  `exec.CommandContext` propagates SIGKILL on expiry.
- **No repo size cap, no concurrency cap** on ephemeral builds in
  plan 7. Documented as a known limit. Implicit bounds: the backtest
  dispatcher caps run-time concurrency, and request-rate caps
  describe-time concurrency.
- **Auth required** on both endpoints (existing JWT middleware).
  Anonymous users cannot trigger arbitrary code execution.

The risk is well-understood and matches the parent spec's stance.
Plans 8 and 9 close it.

## Config

```toml
[strategy]
ephemeral_dir              = "/tmp/pvapi-strategies"  # already in spec, used now
ephemeral_install_timeout  = "60s"                    # new
```

`cmd/server.go` adds matching viper bindings + flag definitions
following the existing `[strategy]` pattern.

## Testing

All new tests follow the existing Ginkgo / `*_suite_test` shape and
hit no live database. The plan-3 fixture `strategy/testdata/fakestrategy`
(a small `package main` with a `describe --json` subcommand) is reused
for every build-touching test.

- **`strategy/ephemeral_test.go`**:
  - happy path: `EphemeralBuild` against `file://testdata/fakestrategy`
    produces a runnable binary; cleanup closure removes the dir.
  - cleanup closure idempotent (callable twice).
  - context cancellation kills the build (`exec.CommandContext`
    behavior); cleanup still runs.
  - `ValidateCloneURL` accepts canonical GitHub HTTPS URLs and
    rejects ssh, file://, gitlab, raw IPs, anything with a query
    string.
- **`strategy/describe_handler_test.go`**:
  - in-process Fiber app with the handler mounted, injected
    `Builder` returning a checked-in fake binary path, asserts the
    response body equals the fake's describe output.
  - URL validator rejection -> 400.
  - builder error -> 422 with the error in detail.
- **`portfolio/validate_test.go`**: extends with `Describe`-table
  cases for `ValidateCreateUnofficial` covering happy path,
  unknown parameter, missing parameter, malformed schedule.
- **`portfolio/handler_test.go`**: a `POST /portfolios` test with
  `strategyCloneUrl` set, an injected ephemeral builder pointing at
  the fake strategy, asserts the inserted row carries
  `strategy_ver IS NULL`, `strategy_describe_json` non-empty,
  `strategy_clone_url` set; asserts mutual exclusivity error when
  both fields are sent.
- **`backtest/run_test.go`**: extends the resolver mock to test
  both branches; asserts the cleanup closure is called on success
  and on every failure point inside `Run`.

A single integration-tagged smoke test (`//go:build integration`)
exercises the real `EphemeralBuild` against `testdata/fakestrategy`
served over a `file://` URL with the validator relaxed (same approach
as plan 3's installer integration test) so CI catches regressions in
the git/go toolchain shellout without depending on GitHub.

## Plan boundaries

Inside scope:
- Migration `6_unofficial_strategies`.
- `GET /strategies/describe` endpoint.
- `POST /portfolios` accepting `strategyCloneUrl`.
- Resolver signature change + `LookupArtifact`.
- New `strategy/ephemeral.go` and `strategy/describe.go`.
- New `portfolio.ValidateCreateUnofficial` and the `createUnofficial`
  branch.
- OpenAPI patch + regenerated `openapi.gen.go`.
- Tests above.

Outside scope (deferred):
- Build-cache / GC for unofficials (follow-up plan if perf demands).
- Sandboxing (plans 8–9).
- Listing / discovery surface for "strategies I've used"
  (synthesizable client-side).
- Embedded-ref URL parsing (`github.com/foo/bar/tree/v1`).
- Live trading (already deferred at the parent-spec level).

## Open questions

None — all design decisions in this spec are fixed. Open items
that surface during implementation are normal plan-execution
detail, not brainstorming.
