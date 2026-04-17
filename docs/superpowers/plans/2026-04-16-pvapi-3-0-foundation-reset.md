# pvapi 3.0 Foundation Reset Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Clear out the 2.x Plaid/account codebase and stand up an empty Fiber v3 scaffold with a working `/healthz` endpoint, fresh migrations, and green CI. No auth, no domain tables, no business logic yet — that lands in later plans.

**Architecture:** Delete the 2.x handlers, middleware, migrations, and schema tests. Keep `cmd/`, `sql/` (pool + migrate scaffolding), `pkginfo/`, `types/`, the Makefile, the CI workflow, `.golangci.yml`, and the test JWK files. Port middleware (request-id, logger, timer) to Fiber v3. Register a `server` Cobra subcommand that boots the Fiber v3 app and serves `/healthz`. Migration `1_init.sql` is a placeholder — real tables come in Plan 2.

**Tech Stack:** Go 1.23+, `github.com/gofiber/fiber/v3`, `github.com/spf13/cobra`, `github.com/spf13/viper`, `github.com/jackc/pgx/v5`, `github.com/golang-migrate/migrate/v4` (pgx v5 driver), `github.com/rs/zerolog`, `github.com/onsi/ginkgo/v2` + `github.com/onsi/gomega`, PostgreSQL 18.

**Reference spec:** `docs/superpowers/specs/2026-04-16-pvapi-3-0-design.md`

---

## File overview

**Deleted in this plan**
```
account/                                  (entire package — Plaid handlers, types, etc.)
api/auth.go                               (Fiber v2 JWT middleware — rewritten in Plan 2)
api/routes.go                             (Plaid routes)
api/server.go                             (Fiber v2 app)
api/logger.go                             (Fiber v2 middleware)
api/timer.go                              (Fiber v2 middleware)
api/userinfo.go                           (userinfo fetch — dropped entirely)
cache/                                    (tinylru — no remaining consumer)
sql/migrations/1_pvui_v0_1_0.up.sql
sql/migrations/1_pvui_v0_1_0.down.sql
sql/migrations/2_scf_2023.up.sql
sql/migrations/2_scf_2023.down.sql
sql/sql_functions_test.go                 (tests plpgsql functions that no longer exist)
sql/tx.go                                 (pvdb transaction wrapper + role machinery — pvapi 3.0 doesn't use per-user DB roles)
```

**Modified**
```
cmd/root.go                               (drop account/api/plaid imports; root command stops doing work)
cmd/config.go                             (rewrite Config struct: ServerConfig now, nothing else for now)
cmd/log.go                                (unchanged)
cmd/viper.go                              (unchanged)
cmd/version.go                            (unchanged)
sql/pool.go                               (drop TrxForUser, createUser, LogOpenTransactions — they require sql/tx.go)
sql/migrate.go                            (unchanged)
pvapi.toml                                (strip [plaid], [email], [debug]; rewrite [server]; keep [db], [log])
main.go                                   (unchanged)
go.mod / go.sum                           (drop fiber/v2, plaid-go, bytedance/sonic, goccy/go-json, lestrrat jwx, jarcoal httpmock, tinylru; add fiber/v3)
.github/workflows/ci-unit-tests.yml       (drop Plaid env vars; keep Postgres + ginkgo)
.gitignore                                (ensure pvapi binary, coverprofile.out, test-report.xml are ignored)
Makefile                                  (add `make run` convenience target — optional; lint/test/build unchanged)
types/types.go                            (keep only TraceIDKey; drop JwtKey + UserKey — Plan 2 reintroduces auth types)
```

**Created**
```
sql/migrations/1_init.up.sql              (placeholder body: `SELECT 1;`)
sql/migrations/1_init.down.sql            (placeholder body: `SELECT 1;`)
api/server.go                             (Fiber v3 app construction)
api/health.go                             (`/healthz` handler returning `"ok"`)
api/middleware.go                         (request-id, zerolog logger, server-timing timer — Fiber v3 signatures)
api/api_suite_test.go                     (Ginkgo suite runner)
api/server_test.go                        (NewApp + /healthz smoke test)
api/middleware_test.go                    (unit tests for the three middlewares)
cmd/server.go                             (Cobra `server` subcommand that runs the Fiber app + handles shutdown)
```

**Kept unchanged**
```
pkginfo/package.go
jwk-test-priv.json                        (reused by Plan 2's auth tests)
jwk-test-pub.json                         (reused by Plan 2's auth tests)
.golangci.yml
LICENSE
CHANGELOG.md
sql/migration_test.go                     (generic up/down test — still works against the placeholder migration)
sql/sql_suite_test.go                     (ginkgo suite scaffolding for sql package)
account/account_suite_test.go             (deleted with rest of account/)
```

---

## Task 1: Remove Plaid and account domain code

**Purpose:** Delete the 2.x Plaid handlers and every file in `api/` and `cache/`, then drop all references from `cmd/` so the repo compiles again. This task ends with a repo that builds but has no HTTP surface.

**Files:**
- Delete: `account/` (whole directory)
- Delete: `api/auth.go`, `api/routes.go`, `api/server.go`, `api/logger.go`, `api/timer.go`, `api/userinfo.go`
- Delete: `cache/` (whole directory)
- Modify: `cmd/root.go`
- Modify: `cmd/config.go`
- Modify: `types/types.go`
- Modify: `pvapi.toml`

- [ ] **Step 1: Delete the account package**

```bash
rm -rf account
```

- [ ] **Step 2: Delete the 2.x api/ files (leave the empty directory — files land in Task 5)**

```bash
rm -f api/auth.go api/routes.go api/server.go api/logger.go api/timer.go api/userinfo.go
rmdir api 2>/dev/null || true
```

- [ ] **Step 3: Delete the cache package**

```bash
rm -rf cache
```

- [ ] **Step 4: Strip account/api imports and flags out of `cmd/root.go`**

Replace the entire contents of `cmd/root.go` with:

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
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "pvapi",
	Short: "Penny Vault API",
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.config/pvapi.toml)")

	rootCmd.PersistentFlags().String("log-level", "info", "set logging level. one of debug, error, fatal, info, panic, trace, or warn")
	rootCmd.PersistentFlags().String("log-output", "stdout", "set log output. a filename, stdout, or stderr")
	rootCmd.PersistentFlags().Bool("log-pretty", true, "pretty print log output (default is JSON output)")
	rootCmd.PersistentFlags().Bool("log-report-caller", false, "print the filename and line number of the log statement that caused the message")

	bindPFlagsToViper(rootCmd)
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		viper.AddConfigPath("/etc/")
		viper.AddConfigPath(fmt.Sprintf("%s/.config", home))
		viper.AddConfigPath(".")
		viper.SetConfigType("toml")
		viper.SetConfigName("pvapi")
	}

	viper.SetEnvPrefix("pvapi")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		// config file is optional — env vars and flags are still honored
		log.Debug().Err(err).Msg("no config file loaded")
	}

	if err := viper.Unmarshal(&conf); err != nil {
		log.Panic().Err(err).Msg("error reading config into the config struct")
	}

	setupLogging(conf.Log)

	if file := viper.ConfigFileUsed(); file != "" {
		log.Info().Str("ConfigFile", file).Msg("loaded config file")
	}
}
```

- [ ] **Step 5: Slim `cmd/config.go` to just the log section**

Replace the entire contents of `cmd/config.go` with:

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

// Config is the top-level pvapi configuration shape. New sections are added as
// later plans land (auth0, runner, strategy, scheduler, ...).
type Config struct {
	Log    logConf
	Server serverConf
}

// serverConf holds HTTP server settings.
type serverConf struct {
	Port         int
	AllowOrigins string `mapstructure:"allow_origins"`
}

var conf Config
```

- [ ] **Step 6: Trim `types/types.go` to just the request-id key**

Replace the entire contents of `types/types.go` with:

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

package types

// RequestIDKey is the Fiber locals key used to carry a per-request UUID.
type RequestIDKey struct{}
```

- [ ] **Step 7: Rewrite `pvapi.toml` with only the sections that exist now**

Replace the entire contents of `pvapi.toml` with:

```toml
[server]
port          = 3000
allow_origins = "http://localhost:9000"

[db]
url = "postgres://jdf@localhost/pvapi"

[log]
level         = "info"
output        = "stdout"
pretty        = true
report_caller = false
```

- [ ] **Step 8: Verify compile**

Run: `go build ./...`
Expected: no errors. Module still builds; there is no HTTP server, no /api package yet.

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
remove Plaid and 2.x HTTP surface

Deletes account/, api/, and cache/ packages from the 2.x codebase and
trims cmd/ so the binary still builds. There is no HTTP server in this
state; the scaffolding returns in the following commits.
EOF
)"
```

---

## Task 2: Replace the 2.x migrations with a fresh `1_init` placeholder

**Purpose:** The old migrations describe the deleted Plaid schema. Replace them with an empty placeholder so the existing migration harness (pool auto-migrate on first access) has something to run. Real tables arrive in Plan 2.

**Files:**
- Delete: `sql/migrations/1_pvui_v0_1_0.up.sql`, `sql/migrations/1_pvui_v0_1_0.down.sql`
- Delete: `sql/migrations/2_scf_2023.up.sql`, `sql/migrations/2_scf_2023.down.sql`
- Delete: `sql/sql_functions_test.go`
- Delete: `sql/tx.go`
- Modify: `sql/pool.go`
- Create: `sql/migrations/1_init.up.sql`
- Create: `sql/migrations/1_init.down.sql`

- [ ] **Step 1: Delete obsolete migrations and schema tests**

```bash
rm -f sql/migrations/1_pvui_v0_1_0.up.sql \
      sql/migrations/1_pvui_v0_1_0.down.sql \
      sql/migrations/2_scf_2023.up.sql \
      sql/migrations/2_scf_2023.down.sql \
      sql/sql_functions_test.go \
      sql/tx.go
```

- [ ] **Step 2: Strip the role/transaction machinery from `sql/pool.go`**

Replace the entire contents of `sql/pool.go` with:

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

package sql

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

var (
	once sync.Once
	pool *pgxpool.Pool
)

// Instance returns a process-wide singleton pgxpool.Pool. First call creates
// the pool, pings the server, and runs all pending migrations.
func Instance(ctx context.Context) *pgxpool.Pool {
	once.Do(func() {
		dbURL := viper.GetString("db.url")

		var err error
		pool, err = pgxpool.New(ctx, dbURL)
		if err != nil {
			log.Panic().Err(err).Msg("could not create postgresql pool")
		}

		if err = pool.Ping(ctx); err != nil {
			log.Panic().Stack().Err(err).Msg("could not ping database server")
		}

		migrate := NewDatabaseSchema(CreateConnStrFromPool(pool))
		if err := migrate.Up(); err != nil {
			log.Panic().Err(err).Msg("could not migrate database")
		}

		log.Info().
			Str("Database", pool.Config().ConnConfig.Database).
			Str("DbHost", pool.Config().ConnConfig.Host).
			Msg("connected to database")
	})

	return pool
}

// Acquire returns a connection from the pool.
func Acquire(ctx context.Context) *pgxpool.Conn {
	conn, err := Instance(ctx).Acquire(ctx)
	if err != nil {
		log.Panic().Err(err).Msg("could not acquire postgresql connection")
	}

	return conn
}
```

- [ ] **Step 3: Create the placeholder up migration**

Create `sql/migrations/1_init.up.sql`:

```sql
-- pvapi 3.0 initial migration placeholder.
-- Real tables land in Plan 2 (auth + schema + OpenAPI wiring).
SELECT 1;
```

- [ ] **Step 4: Create the placeholder down migration**

Create `sql/migrations/1_init.down.sql`:

```sql
SELECT 1;
```

- [ ] **Step 5: Verify the sql package still builds**

Run: `go build ./sql/...`
Expected: no errors.

- [ ] **Step 6: Run the sql migration suite against Postgres**

Make sure a local Postgres is reachable. Then:

```bash
PVAPI_TEST_DB_USER=${USER} \
PVAPI_TEST_DB_HOST=localhost \
PVAPI_TEST_DB_PORT=5432 \
PVAPI_TEST_DB_ADMIN_DBNAME=postgres \
go test ./sql/... -v
```

Expected: the `migration_test.go` cases (`should migrate up without error`, `should have a version greater than 0`, `should not be dirty`) all pass against the placeholder migration. `version` returns `1`.

If no local Postgres is reachable, document that the test requires it and move on; CI will run it.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
reset sql migrations to a pvapi 3.0 placeholder

Deletes the pvui v0.1.0 and scf_2023 migrations (and their PL/pgSQL
function tests) and introduces a placeholder 1_init migration. Real
tables land in the next plan. Strips the per-user role helpers from
sql/pool.go — pvapi 3.0 does not set per-user Postgres roles.
EOF
)"
```

---

## Task 3: Tidy `go.mod` and pin Fiber v3

**Purpose:** Drop dependencies the deleted code pulled in, and add Fiber v3 so Task 4/5 can use it.

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Remove Fiber v2, Plaid SDK, and 2.x-only deps**

Run:

```bash
go get github.com/gofiber/fiber/v2@none
go get github.com/plaid/plaid-go/v31@none
go get github.com/bytedance/sonic@none
go get github.com/goccy/go-json@none
go get github.com/lestrrat-go/jwx/v3@none
go get github.com/lestrrat-go/httprc/v3@none
go get github.com/jarcoal/httpmock@none
go get github.com/tidwall/tinylru@none
```

Any dependency that returns "module not found in go.mod" is fine — it was already pruned when its caller was deleted. Proceed regardless.

- [ ] **Step 2: Add Fiber v3**

Run: `go get github.com/gofiber/fiber/v3@latest`

Expected: go.mod gains `github.com/gofiber/fiber/v3 vX.Y.Z` in its `require` block. Note the exact version line you see (we will reference it in Task 4).

- [ ] **Step 3: Tidy the module graph**

Run: `go mod tidy`
Expected: no errors. `go.sum` may shrink substantially.

- [ ] **Step 4: Verify everything still builds**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum
git commit -m "$(cat <<'EOF'
pin Fiber v3 and drop 2.x-only dependencies

Removes Fiber v2, plaid-go, bytedance/sonic, goccy/go-json, the
lestrrat JWX stack, jarcoal/httpmock, and tinylru (all no longer
imported). Adds Fiber v3 so the new HTTP layer can be built against
it. JWX and httpmock will be re-added in Plan 2 when auth lands.
EOF
)"
```

---

## Task 4: Fiber v3 `/healthz` endpoint behind Ginkgo

**Purpose:** First new code: a minimal Fiber v3 app with a `/healthz` endpoint, covered by a Ginkgo suite so the api-package test harness exists from day one.

**Files:**
- Create: `api/api_suite_test.go`
- Create: `api/server_test.go`
- Create: `api/server.go`
- Create: `api/health.go`

- [ ] **Step 1: Create the Ginkgo suite runner**

Create `api/api_suite_test.go`:

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
)

func TestApi(t *testing.T) {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: GinkgoWriter})
	RegisterFailHandler(Fail)
	RunSpecs(t, "Api Suite")
}
```

- [ ] **Step 2: Write the failing `/healthz` test**

Create `api/server_test.go`:

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
	"io"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/api"
)

var _ = Describe("NewApp", func() {
	It("responds 200 on GET /healthz with body 'ok'", func() {
		app := api.NewApp(api.Config{})

		req := httptest.NewRequest("GET", "/healthz", nil)

		resp, err := app.Test(req, -1)
		Expect(err).To(BeNil())
		defer resp.Body.Close()

		Expect(resp.StatusCode).To(Equal(200))

		body, err := io.ReadAll(resp.Body)
		Expect(err).To(BeNil())
		Expect(string(body)).To(Equal("ok"))
	})
})
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./api/... -v`
Expected: FAIL — `api.NewApp undefined` (or `api.Config undefined`). This is the expected red step.

- [ ] **Step 4: Create `api/server.go` with the minimal Fiber v3 app**

Create `api/server.go`:

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

// Config holds HTTP-layer configuration. Populated from cmd.serverConf.
type Config struct {
	Port         int
	AllowOrigins string
}

// NewApp builds a Fiber v3 app with pvapi's middleware stack and routes.
// The caller is responsible for calling app.Listen.
func NewApp(_ Config) *fiber.App {
	app := fiber.New()

	registerRoutes(app)

	return app
}

func registerRoutes(app *fiber.App) {
	app.Get("/healthz", Healthz)
}
```

- [ ] **Step 5: Create the `/healthz` handler**

Create `api/health.go`:

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

// Healthz returns 200 OK with a constant body. Used by load balancers and
// readiness probes; never emits a log line for itself.
func Healthz(c fiber.Ctx) error {
	return c.SendString("ok")
}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./api/... -v`
Expected: PASS — `NewApp responds 200 on GET /healthz with body 'ok'`.

- [ ] **Step 7: Commit**

```bash
git add api/ go.mod go.sum
git commit -m "$(cat <<'EOF'
add Fiber v3 server skeleton with /healthz

First new code: api.NewApp returns a Fiber v3 app with a single route
that returns 200 "ok". A Ginkgo suite boots for the api package so
later tasks have a place to hang middleware and handler tests.
EOF
)"
```

---

## Task 5: Request-id, zerolog logger, and server-timing middleware (Fiber v3)

**Purpose:** Port the three pieces of request-scoped middleware from the 2.x codebase to Fiber v3, wire them into `NewApp`, and cover each with a Ginkgo test.

**Files:**
- Create: `api/middleware.go`
- Create: `api/middleware_test.go`
- Modify: `api/server.go`

- [ ] **Step 1: Write the failing middleware tests**

Create `api/middleware_test.go`:

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
	"bytes"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pv-api/api"
)

var _ = Describe("Middleware", func() {
	var app = api.NewApp(api.Config{})

	It("sets an X-Request-Id response header", func() {
		req := httptest.NewRequest("GET", "/healthz", nil)
		resp, err := app.Test(req, -1)
		Expect(err).To(BeNil())
		defer resp.Body.Close()

		Expect(resp.Header.Get("X-Request-Id")).NotTo(BeEmpty())
	})

	It("honors an inbound X-Request-Id", func() {
		req := httptest.NewRequest("GET", "/healthz", nil)
		req.Header.Set("X-Request-Id", "test-request-id-42")
		resp, err := app.Test(req, -1)
		Expect(err).To(BeNil())
		defer resp.Body.Close()

		Expect(resp.Header.Get("X-Request-Id")).To(Equal("test-request-id-42"))
	})

	It("sets a Server-Timing header", func() {
		req := httptest.NewRequest("GET", "/healthz", nil)
		resp, err := app.Test(req, -1)
		Expect(err).To(BeNil())
		defer resp.Body.Close()

		Expect(resp.Header.Get("Server-Timing")).To(ContainSubstring("app;dur="))
	})

	It("writes a zerolog line containing the request_id, status, and path", func() {
		var buf bytes.Buffer
		prev := log.Logger
		log.Logger = zerolog.New(&buf)
		defer func() { log.Logger = prev }()

		req := httptest.NewRequest("GET", "/healthz", nil)
		req.Header.Set("X-Request-Id", "log-line-test")
		resp, err := app.Test(req, -1)
		Expect(err).To(BeNil())
		defer resp.Body.Close()

		Expect(buf.String()).To(ContainSubstring(`"request_id":"log-line-test"`))
		Expect(buf.String()).To(ContainSubstring(`"status":200`))
		Expect(buf.String()).To(ContainSubstring(`"path":"/healthz"`))
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./api/... -v -run TestApi`
Expected: the four new `Middleware` specs FAIL (missing headers, no log output). The `NewApp` spec from Task 4 still passes.

- [ ] **Step 3: Implement the middleware**

Create `api/middleware.go`:

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
func timerMiddleware() fiber.Handler {
	return func(c fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		elapsed := time.Since(start)
		c.Append("Server-Timing", fmt.Sprintf("app;dur=%s", elapsed))
		return err
	}
}

// loggerMiddleware emits one zerolog line per request, annotated with
// the request id, status, method, and path.
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
```

- [ ] **Step 4: Wire middleware into `NewApp` (order matters)**

Replace the body of `NewApp` in `api/server.go` with the version below (keep the file's imports; add `"github.com/gofiber/fiber/v3/middleware/cors"`):

```go
package api

import (
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
)

// Config holds HTTP-layer configuration. Populated from cmd.serverConf.
type Config struct {
	Port         int
	AllowOrigins string
}

// NewApp builds a Fiber v3 app with pvapi's middleware stack and routes.
// The caller is responsible for calling app.Listen.
func NewApp(conf Config) *fiber.App {
	app := fiber.New()

	// Order: request-id first (so all later middleware + handlers see it),
	// then timer (so it measures everything inside), then CORS, then logger.
	app.Use(requestIDMiddleware())
	app.Use(timerMiddleware())

	if conf.AllowOrigins != "" {
		app.Use(cors.New(cors.Config{
			AllowOrigins: []string{conf.AllowOrigins},
		}))
	}

	app.Use(loggerMiddleware())

	registerRoutes(app)

	return app
}

func registerRoutes(app *fiber.App) {
	app.Get("/healthz", Healthz)
}
```

- [ ] **Step 5: Run the suite to verify everything passes**

Run: `go test ./api/... -v`
Expected: all Ginkgo specs PASS (NewApp + the four Middleware specs).

- [ ] **Step 6: Commit**

```bash
git add api/
git commit -m "$(cat <<'EOF'
add request-id, timer, logger middleware for Fiber v3

Each request gets a UUIDv7 request-id (or honors an inbound
X-Request-Id), a Server-Timing header with handler duration, and a
zerolog line keyed by request_id/status/method/path/duration. CORS is
enabled conditionally from Config.AllowOrigins.
EOF
)"
```

---

## Task 6: `pvapi server` Cobra subcommand

**Purpose:** Expose the Fiber app through a `server` subcommand that honors config/flags, does graceful shutdown on SIGINT/SIGTERM, and returns a non-zero exit code on listen failure.

**Files:**
- Create: `cmd/server.go`

- [ ] **Step 1: Add the subcommand**

Create `cmd/server.go`:

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
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/penny-vault/pv-api/api"
)

func init() {
	rootCmd.AddCommand(serverCmd)

	serverCmd.Flags().Int("server-port", 3000, "port to bind the HTTP server to")
	serverCmd.Flags().String("server-allow-origins", "http://localhost:9000", "single CORS origin to allow; empty disables CORS")
	bindPFlagsToViper(serverCmd)
}

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Run the pvapi HTTP server",
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		app := api.NewApp(api.Config{
			Port:         conf.Server.Port,
			AllowOrigins: conf.Server.AllowOrigins,
		})

		errCh := make(chan error, 1)
		addr := fmt.Sprintf(":%d", conf.Server.Port)

		go func() {
			log.Info().Str("addr", addr).Msg("server listening")
			if err := app.Listen(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
			if err := app.Shutdown(); err != nil {
				return fmt.Errorf("fiber shutdown: %w", err)
			}
			return nil
		}
	},
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 3: Smoke-test the binary**

```bash
go run . server --server-port 3030 &
PID=$!
sleep 1
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:3030/healthz
curl -s -I http://localhost:3030/healthz | grep -Ei '^(server-timing|x-request-id):'
kill -TERM $PID
wait $PID 2>/dev/null
```

Expected:
- First `curl` prints `200`.
- `-I` output contains an `X-Request-Id:` header and a `Server-Timing: app;dur=...` header.
- Process exits cleanly (no stack trace).

- [ ] **Step 4: Commit**

```bash
git add cmd/server.go
git commit -m "$(cat <<'EOF'
add `pvapi server` Cobra subcommand

Runs the Fiber v3 app, reads port/allow_origins from viper, and handles
SIGINT/SIGTERM via signal.NotifyContext + app.Shutdown. Returns non-zero
on listen failure.
EOF
)"
```

---

## Task 7: CI workflow cleanup

**Purpose:** The current CI workflow exports Plaid credentials as env vars. Nothing consumes them any more; remove them. Also add lint and build steps so the pipeline fails early on basic breakage.

**Files:**
- Modify: `.github/workflows/ci-unit-tests.yml`

- [ ] **Step 1: Rewrite the workflow**

Replace the entire contents of `.github/workflows/ci-unit-tests.yml` with:

```yaml
name: CI
on: [push, pull_request]

jobs:
  ci:
    name: CI
    runs-on: ubuntu-latest
    timeout-minutes: 15
    env:
      PGHOST: localhost
      PVAPI_TEST_DB_USER: runner
      PVAPI_TEST_DB_HOST: localhost
      PVAPI_TEST_DB_PORT: "5432"
      PVAPI_TEST_DB_ADMIN_DBNAME: runner
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: 'stable'

      - name: Add PostgreSQL binaries to PATH
        shell: bash
        run: echo "$(pg_config --bindir)" >> $GITHUB_PATH

      - name: Start preinstalled PostgreSQL
        shell: bash
        run: |
          echo "Initializing database cluster..."
          export PGHOST="${RUNNER_TEMP}/postgres"
          export PGDATA="$PGHOST/pgdata"
          mkdir -p "$PGDATA"

          export PWFILE="$RUNNER_TEMP/pwfile"
          echo "postgres" > "$PWFILE"
          initdb --pgdata="$PGDATA" --username="postgres" --pwfile="$PWFILE"

          echo "Starting PostgreSQL..."
          echo "unix_socket_directories = '$PGHOST'" >> "$PGDATA/postgresql.conf"
          pg_ctl start

          createuser -U postgres -s runner
          createdb -U runner runner

      - name: Build
        run: go build ./...

      - name: Lint
        uses: golangci/golangci-lint-action@v6
        with:
          version: latest

      - name: Test
        run: |
          go install github.com/onsi/ginkgo/v2/ginkgo@latest
          ginkgo run -r --race --junit-report test-report.xml

      - name: Publish Test Report
        uses: mikepenz/action-junit-report@v5
        if: success() || failure()
        with:
          report_paths: 'test-report.xml'

      - name: Coverage
        run: ginkgo run -r --coverprofile=coverage.txt --covermode=atomic

      - name: Upload results to Codecov
        uses: codecov/codecov-action@v5
        with:
          token: ${{ secrets.CODECOV_TOKEN }}

      - name: Upload test results to Codecov
        if: ${{ !cancelled() }}
        uses: codecov/test-results-action@v1
        with:
          token: ${{ secrets.CODECOV_TOKEN }}
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/ci-unit-tests.yml
git commit -m "$(cat <<'EOF'
trim CI workflow for pvapi 3.0

Removes Plaid credential env vars (no consumer in 3.0) and adds an
explicit build and lint job so the pipeline fails fast on basic
breakage. Exposes PVAPI_TEST_DB_* variables the sql suite now reads.
EOF
)"
```

---

## Task 8: Ignore build artifacts

**Purpose:** There are checked-in artifacts (`pvapi` binary, `coverprofile.out`) that shouldn't live in git. Add `.gitignore` entries and remove them from tracking.

**Files:**
- Modify: `.gitignore`
- Delete (from index): `pvapi`, `coverprofile.out`, `test-report.xml` (if present)

- [ ] **Step 1: Check what artifacts are tracked**

Run: `git ls-files | grep -E '^(pvapi$|coverprofile\.out|.*\.xml|.*\.sqlite)'`
Note whatever is listed.

- [ ] **Step 2: Update `.gitignore`**

Read `.gitignore`, then append these lines (dedupe any already present):

```
# build artifacts
/pvapi
coverprofile.out
test-report.xml
*.sqlite
```

- [ ] **Step 3: Untrack any artifacts still in the index**

For each artifact found in Step 1:

```bash
git rm --cached <path>
```

(Run it individually per file so nothing is accidentally removed.)

- [ ] **Step 4: Verify nothing sensitive slipped in**

Run: `git status`
Expected: `.gitignore` modified; the artifacts from Step 1 show as deleted (from the index). No new untracked files of concern (no `.env` becoming tracked, etc.).

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
ignore built binaries and test output artifacts

Untracks the committed pvapi binary and coverprofile.out left over
from 2.x development, and adds .gitignore rules so they stay out.
EOF
)"
```

---

## Task 9: End-to-end smoke

**Purpose:** Final gate before the plan is done. Build, lint, and run the full test suite to confirm the scaffold holds together.

- [ ] **Step 1: Clean build**

Run: `make build`
Expected: `pvapi` binary produced; no errors.

- [ ] **Step 2: Lint**

Run: `make lint`
Expected: no errors. `go vet` passes; `golangci-lint` reports clean (only the linters configured in `.golangci.yml`).

- [ ] **Step 3: Full test run (requires local Postgres)**

Run:

```bash
PVAPI_TEST_DB_USER=${USER} \
PVAPI_TEST_DB_HOST=localhost \
PVAPI_TEST_DB_PORT=5432 \
PVAPI_TEST_DB_ADMIN_DBNAME=postgres \
ginkgo run -r --race
```

Expected:
- `Sql Suite` runs the migration specs (3 It blocks) against the placeholder migration.
- `Api Suite` runs the healthz + middleware specs (5 It blocks).
- All specs pass. No `0 Passed, 0 Failed, 0 Pending` — if a suite reports zero specs, the suite did not wire up correctly.

- [ ] **Step 4: Live smoke**

```bash
./pvapi server --server-port 3030 &
PID=$!
sleep 1
curl -fs -w '\nhttp_code=%{http_code}\n' http://localhost:3030/healthz
curl -sI http://localhost:3030/healthz | grep -Ei '^(x-request-id|server-timing):'
kill -TERM $PID
wait $PID 2>/dev/null
```

Expected:
- `ok` body followed by `http_code=200`.
- One `X-Request-Id:` line and one `Server-Timing: app;dur=…` line.
- Clean shutdown.

- [ ] **Step 5: Push the branch**

```bash
git push
```

No commit on this task; this is verification only. Plan 1 is complete when all steps pass.

---

## Self-review summary

- **Spec coverage (foundation slice only):** package layout (cmd, api, sql, types, pkginfo retained; account and cache removed — ✓ Task 1/2), Fiber v3 server (✓ Task 4), request-id + logger + timer middleware (✓ Task 5), Cobra server subcommand (✓ Task 6), fresh migration scaffolding (✓ Task 2), go.mod cleanup (✓ Task 3), CI updates (✓ Task 7), build-artifact hygiene (✓ Task 8), end-to-end smoke (✓ Task 9). Plan deliberately does **not** cover auth, real tables, portfolio/strategy/backtest packages, openapi codegen, or runners — those land in Plans 2+.
- **Placeholders:** no `TBD`, no "handle edge cases" steps, no "similar to Task N" pointers.
- **Type consistency:** `api.Config` (Port + AllowOrigins) used in both `server.go` and `cmd/server.go`; `types.RequestIDKey{}` used in both `requestIDMiddleware` and `loggerMiddleware`; `X-Request-Id` header capitalization consistent across middleware and tests; middleware order (requestID → timer → cors → logger) documented and matched in code.
