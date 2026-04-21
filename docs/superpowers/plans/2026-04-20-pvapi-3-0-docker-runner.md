# pvapi 3.0 — Docker runner (plan 8) implementation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a second backtest runner that executes strategies inside Docker containers, plus an image-based install path for officials and an ephemeral image-build for unofficials. A single `runner.mode` config toggle picks which runner + installer pair is wired at startup; mode switches self-heal on the next sync tick via an artifact-kind mismatch trigger.

**Architecture:** The `backtest.Runner` interface gains an `ArtifactKind` (binary | image) on its `RunRequest`; the resolver is renamed `BinaryResolver` → `ArtifactResolver` but keeps the same shape. `HostRunner` accepts only `ArtifactBinary`. A new `DockerRunner` accepts only `ArtifactImage` and talks to Docker through a narrow `dockercli.Client` interface (mockable in unit tests). The strategy sync loop becomes mode-aware: in docker mode it calls `strategy.InstallDocker` instead of `strategy.Install`, and an `artifact_kind` mismatch on an existing row forces re-install regardless of `last_attempted_ver`. `strategy.EphemeralImageBuild` mirrors the plan-7 `EphemeralBuild` for unofficial portfolios. **Describe-at-create stays on the host toolchain** (`EphemeralBuild`) regardless of runner mode — only backtest execution changes.

**Tech Stack:** Go 1.25, `github.com/docker/docker/client` (Docker Engine SDK), `github.com/docker/go-units` (memory-limit parsing), Ginkgo/Gomega, Fiber v3 (unchanged), pgx v5 (unchanged). Base images `golang:<goVer>-alpine` and `gcr.io/distroless/static-debian12:nonroot`.

**Spec:** `docs/superpowers/specs/2026-04-20-pvapi-3-0-docker-runner.md`.

---

## File map

Created:
- `dockercli/client.go`
- `backtest/docker.go`
- `backtest/docker_test.go`
- `backtest/docker_integration_test.go` (`//go:build integration`)
- `strategy/dockerfile.go`
- `strategy/dockerfile_test.go`
- `strategy/docker_install.go`
- `strategy/docker_install_test.go`
- `strategy/docker_ephemeral.go`
- `strategy/docker_ephemeral_test.go`

Modified:
- `go.mod` / `go.sum` — add `github.com/docker/docker`, `github.com/docker/go-units`, transitive deps
- `backtest/runner.go` — rename `Binary` → `Artifact`, add `ArtifactKind`
- `backtest/run.go` — rename `BinaryResolver` → `ArtifactResolver`, pass `ArtifactKind` in the `RunRequest` it constructs
- `backtest/host.go` — assert `ArtifactKind == ArtifactBinary`, read `req.Artifact` (was `req.Binary`)
- `backtest/host_test.go` — update `RunRequest` literals to `Artifact` + `ArtifactKind`
- `backtest/run_test.go` — update `BinaryResolver` references
- `backtest/errors.go` — add `ErrArtifactKindMismatch`, loosen `ErrUnsupportedRunnerMode` message
- `backtest/config.go` — accept `"docker"` in `Validate`
- `strategy/types.go` — (no change)
- `strategy/install.go` — `InstallResult` gains `ArtifactRef` field
- `strategy/sync.go` — `SyncerOptions` gains `RunnerMode` + `DockerInstaller`; artifact-kind mismatch trigger; `runInstall` dispatches by mode
- `strategy/sync_test.go` — new case for docker mode + artifact-kind mismatch
- `api/server.go` — `RegistryConfig` gains `RunnerMode` + `DockerInstaller`; threaded through `startRegistrySync`
- `cmd/config.go` — `runnerConf` gains nested `dockerConf`
- `cmd/viper.go` — defaults for `runner.docker.*`
- `cmd/server.go` — switch on `runner.mode`; build `DockerRunner` + docker resolver + docker installer closure
- `pvapi.toml` — example `[runner.docker]` section
- `README.md` — "Running with the Docker runner" subsection

---

## Task 1: Add Docker dependencies to go.mod

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Pull the Docker Engine SDK and go-units**

Run:
```bash
go get github.com/docker/docker@v27.5.1+incompatible
go get github.com/docker/go-units@v0.5.0
```

(If Go's proxy surfaces a newer v27 patch release, accept it. v27 is the current Docker Engine SDK line at the time of writing; it uses the `container.WaitCondition`, `container.Config`, `image.RemoveOptions` shapes referenced later in this plan.)

- [ ] **Step 2: Tidy**

Run:
```bash
go mod tidy
```

- [ ] **Step 3: Verify build still passes**

Run:
```bash
go build ./...
```

Expected: success. The packages aren't imported anywhere yet; `go mod tidy` keeps them as indirect deps. To pin them as direct we add a throwaway import in the next task.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "add docker engine sdk and go-units dependencies"
```

---

## Task 2: `dockercli` package

**Files:**
- Create: `dockercli/client.go`

- [ ] **Step 1: Write the interface**

`dockercli/client.go`:
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

// Package dockercli defines the narrow Docker Engine SDK surface the backtest
// runner and the strategy docker installer rely on. Lives in its own leaf
// package so neither backtest/ nor strategy/ depends on the other.
package dockercli

import (
	"context"
	"io"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Client is the subset of *docker/client.Client pvapi uses. Production wires
// a real *client.Client built with client.NewClientWithOpts; tests pass a
// fake.
type Client interface {
	ImageBuild(ctx context.Context, buildContext io.Reader, opts types.ImageBuildOptions) (types.ImageBuildResponse, error)
	ImageRemove(ctx context.Context, imageID string, opts image.RemoveOptions) ([]image.DeleteResponse, error)
	ContainerCreate(ctx context.Context, cfg *container.Config, hostCfg *container.HostConfig, netCfg *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, id string, opts container.StartOptions) error
	ContainerLogs(ctx context.Context, id string, opts container.LogsOptions) (io.ReadCloser, error)
	ContainerWait(ctx context.Context, id string, cond container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
	ContainerKill(ctx context.Context, id, signal string) error
	ContainerRemove(ctx context.Context, id string, opts container.RemoveOptions) error
}
```

- [ ] **Step 2: Build**

Run:
```bash
go build ./...
```

Expected: success. The SDK types resolve; `*client.Client` from the Docker SDK satisfies this interface by virtue of matching method signatures (interface satisfaction is implicit in Go — no further wiring needed).

- [ ] **Step 3: Commit**

```bash
git add dockercli/client.go
git commit -m "dockercli: narrow interface over docker engine sdk"
```

---

## Task 3: Generalize `RunRequest` (ArtifactKind + Artifact rename)

**Files:**
- Modify: `backtest/runner.go`
- Modify: `backtest/run.go`
- Modify: `backtest/host.go`
- Modify: `backtest/host_test.go`
- Modify: `backtest/run_test.go`
- Modify: `backtest/errors.go`

- [ ] **Step 1: Update `RunRequest`, add `ArtifactKind`**

Edit `backtest/runner.go`:
```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
// ... (keep existing license header)

package backtest

import (
	"context"
	"time"
)

// Runner executes a strategy artifact and produces a SQLite snapshot at
// RunRequest.OutPath. Implementations: HostRunner (binary), DockerRunner (image).
type Runner interface {
	Run(ctx context.Context, req RunRequest) error
}

// ArtifactKind selects how the Runner should interpret RunRequest.Artifact.
type ArtifactKind int

const (
	// ArtifactBinary means Artifact is an absolute filesystem path to an
	// executable. Consumed by HostRunner.
	ArtifactBinary ArtifactKind = iota
	// ArtifactImage means Artifact is a Docker image reference
	// ("repo/name:tag"). Consumed by DockerRunner.
	ArtifactImage
)

// RunRequest carries everything a Runner needs to produce one snapshot.
type RunRequest struct {
	Artifact     string        // binary path for host; image ref for docker
	ArtifactKind ArtifactKind  // must match the runner
	Args         []string      // strategy-specific CLI flags (parameters + benchmark)
	OutPath      string        // absolute path where the snapshot must be written
	Timeout      time.Duration // 0 means use Config.Timeout default
}
```

- [ ] **Step 2: Add the mismatch error**

Edit `backtest/errors.go`. Under the existing vars, add:
```go
// ErrArtifactKindMismatch is returned when a runner is handed a RunRequest
// whose ArtifactKind does not match what the runner supports. Indicates a
// wiring bug at startup (resolver + runner are wired together by
// cmd/server.go).
ErrArtifactKindMismatch = errors.New("backtest: artifact kind mismatch")
```

- [ ] **Step 3: Update `HostRunner.Run` to read `Artifact` and validate kind**

Edit `backtest/host.go`, replace the body of `Run`:
```go
func (r *HostRunner) Run(ctx context.Context, req RunRequest) error {
	if req.ArtifactKind != ArtifactBinary {
		return fmt.Errorf("%w: HostRunner requires ArtifactBinary, got %d", ErrArtifactKindMismatch, req.ArtifactKind)
	}

	timeoutCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		timeoutCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	args := append([]string{"backtest", "--output", req.OutPath}, req.Args...)
	cmd := exec.CommandContext(timeoutCtx, req.Artifact, args...) //nolint:gosec // G204: artifact path comes from admin-controlled strategy registry

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout := newLogWriter("strategy-stdout")
	cmd.Stdout = stdout

	runErr := cmd.Run()

	if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("%w: %s", ErrTimedOut, firstNBytes(stderr.String(), 2048))
	}

	if runErr != nil {
		return fmt.Errorf("%w: %s: %s", ErrRunnerFailed, runErr.Error(), firstNBytes(stderr.String(), 2048))
	}

	return nil
}
```

- [ ] **Step 4: Rename `BinaryResolver` → `ArtifactResolver` and update `run.go`**

Edit `backtest/run.go`. Replace the `BinaryResolver` type and update the struct field:
```go
// ArtifactResolver resolves a strategy artifact (binary path or image ref)
// for the given cloneURL and version. The semantics of the returned
// artifactRef depends on the runner wired alongside this resolver at
// startup: path for HostRunner, image reference for DockerRunner. The
// orchestrator never interprets artifactRef — it passes it straight into
// RunRequest.Artifact paired with the runner's declared ArtifactKind.
// Callers must always call cleanup when err is nil.
type ArtifactResolver func(ctx context.Context, cloneURL, ver string) (artifactRef string, cleanup func(), err error)

type orchestrator struct {
	cfg         Config
	runner      Runner
	artifactKind ArtifactKind
	ps          PortfolioStore
	rs          RunStore
	resolve     ArtifactResolver
}

// NewRunner builds the orchestration object that ties together the runner,
// portfolio store, run store, and artifact resolver. artifactKind tells the
// orchestrator which kind of artifact this runner+resolver pair produces; it
// is stamped onto every RunRequest.
func NewRunner(cfg Config, runner Runner, artifactKind ArtifactKind, ps PortfolioStore, rs RunStore, resolve ArtifactResolver) *orchestrator {
	cfg.ApplyDefaults()
	return &orchestrator{cfg: cfg, runner: runner, artifactKind: artifactKind, ps: ps, rs: rs, resolve: resolve}
}
```

Update the `Run` method — change the `resolve` return variable name from `binary` to `artifact`, and update the `RunRequest` literal:
```go
artifact, cleanup, err := o.resolve(ctx, row.StrategyCloneURL, row.StrategyVer)
if err != nil {
	return o.fail(ctx, portfolioID, runID, started, fmt.Errorf("%w: %w", ErrStrategyNotInstalled, err))
}
defer cleanup()

tmp := filepath.Join(o.cfg.SnapshotsDir, portfolioID.String()+".sqlite.tmp")
final := filepath.Join(o.cfg.SnapshotsDir, portfolioID.String()+".sqlite")
_ = os.Remove(tmp)

if err := o.runner.Run(ctx, RunRequest{
	Artifact:     artifact,
	ArtifactKind: o.artifactKind,
	Args:         BuildArgs(row.Parameters, row.Benchmark),
	OutPath:      tmp,
	Timeout:      o.cfg.Timeout,
}); err != nil {
	return o.fail(ctx, portfolioID, runID, started, err)
}
```

- [ ] **Step 5: Update `host_test.go` and `run_test.go` literals**

`backtest/host_test.go` — every `backtest.RunRequest{ Binary: ..., ...}` becomes `backtest.RunRequest{ Artifact: ..., ArtifactKind: backtest.ArtifactBinary, ... }`. Add a new test case:
```go
It("returns ErrArtifactKindMismatch when given an image artifact", func() {
	out := filepath.Join(GinkgoT().TempDir(), "out.sqlite")
	err := runner.Run(context.Background(), backtest.RunRequest{
		Artifact:     "some/image:latest",
		ArtifactKind: backtest.ArtifactImage,
		OutPath:      out,
		Timeout:      time.Second,
	})
	Expect(err).To(MatchError(backtest.ErrArtifactKindMismatch))
})
```

`backtest/run_test.go` — find references to `BinaryResolver` and rename to `ArtifactResolver`; pass `backtest.ArtifactBinary` into `NewRunner` (additional arg).

- [ ] **Step 6: Update cmd/server.go `NewRunner` call**

Edit `cmd/server.go` — `backtest.NewRunner(btCfg, runner, portfolioAdapter, runAdapter, resolve)` becomes:
```go
orch := backtest.NewRunner(btCfg, runner, backtest.ArtifactBinary, portfolioAdapter, runAdapter, resolve)
```

(This will be replaced in Task 12, but must compile in the meantime.)

- [ ] **Step 7: Run the build + tests**

Run:
```bash
go build ./...
ginkgo run ./backtest
```

Expected: build succeeds; existing HostRunner tests pass; the new mismatch test passes.

- [ ] **Step 8: Commit**

```bash
git add backtest/ cmd/server.go
git commit -m "backtest: generalize RunRequest to carry ArtifactKind + Artifact ref"
```

---

## Task 4: Config — `runner.docker` section + flags + defaults

**Files:**
- Modify: `cmd/config.go`
- Modify: `cmd/viper.go`
- Modify: `cmd/server.go`
- Modify: `pvapi.toml`
- Create: `cmd/config_runner_docker_test.go` (thin, unmarshals a TOML fixture)

- [ ] **Step 1: Extend `runnerConf` in cmd/config.go**

```go
// runnerConf holds the runner execution-mode setting.
type runnerConf struct {
	Mode   string     `mapstructure:"mode"`
	Docker dockerConf `mapstructure:"docker"`
}

// dockerConf configures DockerRunner + InstallDocker when runner.mode = "docker".
type dockerConf struct {
	Socket            string        `mapstructure:"socket"`
	Network           string        `mapstructure:"network"`
	CPULimit          float64       `mapstructure:"cpu_limit"`
	MemoryLimit       string        `mapstructure:"memory_limit"`
	BuildTimeout      time.Duration `mapstructure:"build_timeout"`
	ImagePrefix       string        `mapstructure:"image_prefix"`
	SnapshotsHostPath string        `mapstructure:"snapshots_host_path"`
}
```

- [ ] **Step 2: Add viper defaults**

Edit `cmd/viper.go`, under the existing `runner.mode` default add:
```go
viper.SetDefault("runner.docker.socket", "unix:///var/run/docker.sock")
viper.SetDefault("runner.docker.network", "")
viper.SetDefault("runner.docker.cpu_limit", 0.0)
viper.SetDefault("runner.docker.memory_limit", "")
viper.SetDefault("runner.docker.build_timeout", 10*time.Minute)
viper.SetDefault("runner.docker.image_prefix", "pvapi-strategy")
viper.SetDefault("runner.docker.snapshots_host_path", "")
```

- [ ] **Step 3: Add flags to `serverCmd.Flags()`**

Edit `cmd/server.go` `init()`, alongside the existing strategy flags:
```go
serverCmd.Flags().String("runner-docker-socket", "unix:///var/run/docker.sock", "Docker daemon socket URL")
serverCmd.Flags().String("runner-docker-network", "", "Docker network for backtest containers; empty = daemon default")
serverCmd.Flags().Float64("runner-docker-cpu-limit", 0.0, "per-container CPU limit in cores; 0 = unlimited")
serverCmd.Flags().String("runner-docker-memory-limit", "", "per-container memory limit (e.g. 512Mi, 1Gi); empty = unlimited")
serverCmd.Flags().Duration("runner-docker-build-timeout", 10*time.Minute, "max time for one docker image build")
serverCmd.Flags().String("runner-docker-image-prefix", "pvapi-strategy", "prefix for strategy image tags")
serverCmd.Flags().String("runner-docker-snapshots-host-path", "", "host path that maps to backtest.snapshots_dir when pvapi itself runs in docker; empty = snapshots_dir")
```

- [ ] **Step 4: Add `[runner.docker]` to `pvapi.toml`**

Append to `pvapi.toml`:
```toml
[runner]
mode = "host"   # host | docker | kubernetes

  [runner.docker]
  socket              = "unix:///var/run/docker.sock"
  network             = ""
  cpu_limit           = 0.0
  memory_limit        = ""
  build_timeout       = "10m"
  image_prefix        = "pvapi-strategy"
  snapshots_host_path = ""
```

- [ ] **Step 5: Write a unit test that parses the config**

`cmd/config_runner_docker_test.go`:
```go
package cmd_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/penny-vault/pv-api/cmd"
)

func TestRunnerDockerConfig(t *testing.T) {
	t.Setenv("PVAPI_RUNNER_MODE", "")
	v := viper.New()
	v.SetConfigType("toml")
	err := v.ReadConfig(bytes.NewBufferString(`
[runner]
mode = "docker"
  [runner.docker]
  socket       = "tcp://daemon:2375"
  network      = "pvapi"
  cpu_limit    = 2.0
  memory_limit = "1Gi"
  build_timeout = "7m"
  image_prefix = "pvapi-strat"
  snapshots_host_path = "/srv/snapshots"
`))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var c cmd.Config
	if err := v.Unmarshal(&c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Runner.Mode != "docker" {
		t.Errorf("mode = %q; want docker", c.Runner.Mode)
	}
	if c.Runner.Docker.Socket != "tcp://daemon:2375" {
		t.Errorf("socket = %q", c.Runner.Docker.Socket)
	}
	if c.Runner.Docker.CPULimit != 2.0 {
		t.Errorf("cpu_limit = %v", c.Runner.Docker.CPULimit)
	}
	if c.Runner.Docker.MemoryLimit != "1Gi" {
		t.Errorf("memory_limit = %q", c.Runner.Docker.MemoryLimit)
	}
	if c.Runner.Docker.BuildTimeout != 7*time.Minute {
		t.Errorf("build_timeout = %v", c.Runner.Docker.BuildTimeout)
	}
	if c.Runner.Docker.SnapshotsHostPath != "/srv/snapshots" {
		t.Errorf("snapshots_host_path = %q", c.Runner.Docker.SnapshotsHostPath)
	}
}
```

If `cmd.Config` is unexported, move the test into `package cmd` (white-box) and drop the alias. Match whichever pattern the existing `cmd/` tests use (check `cmd/config_test.go` or equivalent first; if none exists, white-box is fine).

- [ ] **Step 6: Run the test**

Run:
```bash
go test ./cmd -run TestRunnerDockerConfig -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/config.go cmd/viper.go cmd/server.go cmd/config_runner_docker_test.go pvapi.toml
git commit -m "config: add [runner.docker] section and flags"
```

---

## Task 5: `strategy/dockerfile.go`

**Files:**
- Create: `strategy/dockerfile.go`
- Create: `strategy/dockerfile_test.go`
- Create: `strategy/testdata/gomods/go122/go.mod`
- Create: `strategy/testdata/gomods/go124/go.mod`
- Create: `strategy/testdata/gomods/garbage/go.mod`

- [ ] **Step 1: Write fixtures**

`strategy/testdata/gomods/go122/go.mod`:
```
module example.com/x

go 1.22
```

`strategy/testdata/gomods/go124/go.mod`:
```
module example.com/x

go 1.24
```

`strategy/testdata/gomods/garbage/go.mod`:
```
this is not a go.mod file
```

- [ ] **Step 2: Write the failing tests**

`strategy/dockerfile_test.go`:
```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
// ... license header

package strategy_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("parseGoVersion", func() {
	It("reads 1.22", func() {
		v, err := strategy.ParseGoVersion("testdata/gomods/go122")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal("1.22"))
	})

	It("reads 1.24", func() {
		v, err := strategy.ParseGoVersion("testdata/gomods/go124")
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(Equal("1.24"))
	})

	It("returns empty on garbage go.mod", func() {
		v, _ := strategy.ParseGoVersion("testdata/gomods/garbage")
		Expect(v).To(Equal(""))
	})

	It("returns empty when go.mod is missing", func() {
		v, _ := strategy.ParseGoVersion("testdata/gomods/doesnotexist")
		Expect(v).To(Equal(""))
	})
})

var _ = Describe("RenderDockerfile", func() {
	It("uses the given go version in the build stage", func() {
		df := string(strategy.RenderDockerfile("1.23"))
		Expect(df).To(ContainSubstring("FROM golang:1.23-alpine AS build"))
		Expect(df).To(ContainSubstring("FROM gcr.io/distroless/static-debian12:nonroot"))
		Expect(df).To(ContainSubstring(`ENTRYPOINT ["/strategy"]`))
	})

	It("falls back to the default when goVer is empty", func() {
		df := string(strategy.RenderDockerfile(""))
		Expect(df).To(ContainSubstring("FROM golang:1.24-alpine AS build"))
	})
})
```

- [ ] **Step 3: Run the tests and watch them fail**

Run:
```bash
ginkgo run ./strategy
```

Expected: compile failure — `strategy.ParseGoVersion` / `strategy.RenderDockerfile` undefined.

- [ ] **Step 4: Implement `dockerfile.go`**

`strategy/dockerfile.go`:
```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
// ... license header

package strategy

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"
)

// DefaultGoVersion is used when a strategy repo's go.mod does not declare a
// go version (or cannot be parsed).
const DefaultGoVersion = "1.24"

// ParseGoVersion reads <dir>/go.mod and returns the value of the `go`
// directive ("1.22", "1.24"), or "" if go.mod is missing / malformed.
// ParseGoVersion never returns a non-nil error — parsing failures degrade
// to the empty-string result so callers can fall back to DefaultGoVersion.
func ParseGoVersion(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod")) //nolint:gosec // dir is an internal path
	if err != nil {
		return "", nil
	}
	f, err := modfile.ParseLax("go.mod", data, nil)
	if err != nil || f.Go == nil {
		return "", nil
	}
	return f.Go.Version, nil
}

// RenderDockerfile returns a two-stage Dockerfile that builds the strategy
// with golang:<goVer>-alpine and copies the resulting binary into
// distroless/static-debian12:nonroot. Empty goVer falls back to
// DefaultGoVersion.
func RenderDockerfile(goVer string) []byte {
	if goVer == "" {
		goVer = DefaultGoVersion
	}
	return []byte(fmt.Sprintf(`# syntax=docker/dockerfile:1.7
FROM golang:%s-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/strategy .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/strategy /strategy
USER nonroot:nonroot
ENTRYPOINT ["/strategy"]
`, goVer))
}
```

`golang.org/x/mod/modfile` is already an indirect dep in the repo (it shows as `golang.org/x/mod v0.33.0` in go.mod). Add the direct import; `go mod tidy` will promote it.

- [ ] **Step 5: Run the tests**

Run:
```bash
ginkgo run ./strategy
```

Expected: PASS for the new tests.

- [ ] **Step 6: Tidy + commit**

Run:
```bash
go mod tidy
```

Then:
```bash
git add strategy/dockerfile.go strategy/dockerfile_test.go strategy/testdata/gomods go.mod go.sum
git commit -m "strategy: generated dockerfile + go.mod parser"
```

---

## Task 6: `imageTag` helper + `InstallResult.ArtifactRef`

**Files:**
- Modify: `strategy/install.go`
- Create: `strategy/docker_install.go` (just the tag helper here)
- Create: `strategy/docker_install_test.go` (just the tag test here)

- [ ] **Step 1: Add `ArtifactRef` to `InstallResult`**

Edit `strategy/install.go`:
```go
// InstallResult is what a successful install produces.
type InstallResult struct {
	BinPath      string // absolute path to the built binary (host mode only; "" in docker mode)
	ArtifactRef  string // image ref in docker mode; same as BinPath in host mode
	DescribeJSON []byte // raw `<bin> describe --json` output
	ShortCode    string // parsed from the describe output
}
```

At the bottom of the `Install` function, set `ArtifactRef = binPath` on the successful return so the syncer's mode-agnostic `runInstall` can always read `result.ArtifactRef`:
```go
return &InstallResult{
	BinPath:      binPath,
	ArtifactRef:  binPath,
	DescribeJSON: describeBytes,
	ShortCode:    parsed.ShortCode,
}, nil
```

- [ ] **Step 2: Write the tag-helper test**

`strategy/docker_install_test.go`:
```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
// ... license header

package strategy_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("ImageTag", func() {
	DescribeTable("builds a tag from a GitHub clone URL",
		func(prefix, cloneURL, ver, want string) {
			Expect(strategy.ImageTag(prefix, cloneURL, ver)).To(Equal(want))
		},
		Entry("plain", "pvapi-strategy", "https://github.com/penny-vault/adm", "v1.2.3",
			"pvapi-strategy/penny-vault/adm:v1.2.3"),
		Entry("with .git suffix", "pvapi-strategy", "https://github.com/penny-vault/adm.git", "v1.2.3",
			"pvapi-strategy/penny-vault/adm:v1.2.3"),
		Entry("non-github fallback", "pvapi-strategy", "https://example.com/foo/bar", "abc123",
			"pvapi-strategy/unknown/example.com-foo-bar:abc123"),
	)
})
```

- [ ] **Step 3: Run the test and watch it fail**

Run:
```bash
ginkgo run ./strategy
```

Expected: `strategy.ImageTag` undefined.

- [ ] **Step 4: Implement the tag helper**

`strategy/docker_install.go`:
```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
// ... license header

// Package strategy — docker install support.
package strategy

import (
	"net/url"
	"strings"
)

// ImageTag returns "<prefix>/<owner>/<repo>:<ver>" for a canonical
// https://github.com/<owner>/<repo>(.git)? URL. Non-GitHub URLs (only
// reachable when the caller has set SkipURLValidation) fall through to
// "<prefix>/unknown/<slugified host+path>:<ver>".
func ImageTag(prefix, cloneURL, ver string) string {
	u, err := url.Parse(cloneURL)
	if err == nil && u.Host == "github.com" {
		path := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 2 {
			return prefix + "/" + parts[0] + "/" + parts[1] + ":" + ver
		}
	}
	slug := cloneURL
	if err == nil && u.Host != "" {
		slug = u.Host + u.Path
	}
	slug = strings.ReplaceAll(slug, "/", "-")
	slug = strings.ReplaceAll(slug, ":", "-")
	return prefix + "/unknown/" + slug + ":" + ver
}
```

- [ ] **Step 5: Run the tests**

Run:
```bash
ginkgo run ./strategy
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add strategy/install.go strategy/docker_install.go strategy/docker_install_test.go
git commit -m "strategy: InstallResult.ArtifactRef + ImageTag helper"
```

---

## Task 7: `strategy.InstallDocker`

**Files:**
- Modify: `strategy/docker_install.go`
- Modify: `strategy/docker_install_test.go`

- [ ] **Step 1: Add a package-level error + helpers**

Append to `strategy/docker_install.go`:
```go
import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/penny-vault/pv-api/dockercli"
)

// ErrDockerBuildFailed wraps errors returned from ImageBuild.
var ErrDockerBuildFailed = errors.New("strategy: docker image build failed")

// DockerInstallDeps configures InstallDocker.
type DockerInstallDeps struct {
	Client       dockercli.Client
	ImagePrefix  string
	BuildTimeout time.Duration
}

// InstallDocker performs a single version-pinned Docker install:
//   1. git clone --depth=1 --branch <Version> <CloneURL> <DestDir>
//   2. write generated Dockerfile into <DestDir>/Dockerfile
//   3. ImageBuild, tag = ImageTag(prefix, CloneURL, Version)
//   4. docker run --rm <image> describe --json  (via the sdk)
//   5. validate describe.shortCode matches req.ShortCode
func InstallDocker(ctx context.Context, req InstallRequest, deps DockerInstallDeps) (*InstallResult, error) {
	if req.ShortCode == "" || req.CloneURL == "" || req.Version == "" || req.DestDir == "" {
		return nil, ErrInstallMissingFields
	}
	if deps.Client == nil {
		return nil, errors.New("InstallDocker: nil Client")
	}
	if deps.ImagePrefix == "" {
		deps.ImagePrefix = "pvapi-strategy"
	}
	timeout := deps.BuildTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	bctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 1. clone.
	cloneCmd := exec.CommandContext(bctx, "git", "clone", "--depth=1", //nolint:gosec // sourced from trusted sync state
		"--branch", req.Version, req.CloneURL, req.DestDir)
	var cloneOut bytes.Buffer
	cloneCmd.Stdout = &cloneOut
	cloneCmd.Stderr = &cloneOut
	if err := cloneCmd.Run(); err != nil {
		return nil, fmt.Errorf("git clone %s@%s: %w\n%s", req.CloneURL, req.Version, err, cloneOut.String())
	}

	// 2. write generated Dockerfile.
	goVer, _ := ParseGoVersion(req.DestDir)
	dfPath := filepath.Join(req.DestDir, "Dockerfile")
	if err := os.WriteFile(dfPath, RenderDockerfile(goVer), 0o600); err != nil {
		return nil, fmt.Errorf("write Dockerfile: %w", err)
	}

	// 3. build.
	tag := ImageTag(deps.ImagePrefix, req.CloneURL, req.Version)
	buildCtx, err := tarDir(req.DestDir)
	if err != nil {
		return nil, fmt.Errorf("tar build context: %w", err)
	}
	resp, err := deps.Client.ImageBuild(bctx, buildCtx, types.ImageBuildOptions{
		Dockerfile: "Dockerfile",
		Tags:       []string{tag},
		Remove:     true,
		PullParent: true,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDockerBuildFailed, err)
	}
	defer resp.Body.Close()
	buildOut, bErr := drainBuildStream(resp.Body)
	if bErr != nil {
		return nil, fmt.Errorf("%w: %w\n%s", ErrDockerBuildFailed, bErr, buildOut)
	}

	// 4. describe.
	describeJSON, err := runDescribeInContainer(bctx, deps.Client, tag)
	if err != nil {
		return nil, fmt.Errorf("describe after build: %w", err)
	}

	// 5. validate short code.
	var parsed Describe
	if err := json.Unmarshal(describeJSON, &parsed); err != nil {
		return nil, fmt.Errorf("parsing describe output: %w", err)
	}
	if parsed.ShortCode != req.ShortCode {
		return nil, fmt.Errorf("%w: want %q, got %q", ErrShortCodeMismatch, req.ShortCode, parsed.ShortCode)
	}

	return &InstallResult{
		BinPath:      "",
		ArtifactRef:  tag,
		DescribeJSON: describeJSON,
		ShortCode:    parsed.ShortCode,
	}, nil
}

// tarDir packs dir (recursively) into an in-memory tar suitable as an
// ImageBuild context.
func tarDir(dir string) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		hdr, herr := tar.FileInfoHeader(info, "")
		if herr != nil {
			return herr
		}
		hdr.Name = filepath.ToSlash(rel)
		if werr := tw.WriteHeader(hdr); werr != nil {
			return werr
		}
		if info.Mode().IsRegular() {
			f, oerr := os.Open(path) //nolint:gosec // path is internal
			if oerr != nil {
				return oerr
			}
			defer f.Close()
			if _, cerr := io.Copy(tw, f); cerr != nil {
				return cerr
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// drainBuildStream reads the newline-delimited JSON ImageBuild stream,
// surfacing any {"errorDetail":{"message":...}} frames as errors.
func drainBuildStream(r io.Reader) (string, error) {
	var out bytes.Buffer
	dec := json.NewDecoder(r)
	for dec.More() {
		var frame struct {
			Stream      string `json:"stream,omitempty"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail,omitempty"`
		}
		if err := dec.Decode(&frame); err != nil {
			return out.String(), err
		}
		out.WriteString(frame.Stream)
		if frame.ErrorDetail != nil {
			return out.String(), errors.New(frame.ErrorDetail.Message)
		}
	}
	return out.String(), nil
}

// runDescribeInContainer creates a disposable container from the given image
// with cmd = ["describe", "--json"], starts it, waits for exit 0, and
// returns stdout. AutoRemove=true cleans up the container on exit.
func runDescribeInContainer(ctx context.Context, c dockercli.Client, image string) ([]byte, error) {
	resp, err := c.ContainerCreate(ctx, &container.Config{
		Image: image,
		Cmd:   []string{"describe", "--json"},
		Tty:   false,
	}, &container.HostConfig{AutoRemove: true}, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("container create: %w", err)
	}
	if err := c.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("container start: %w", err)
	}

	waitCh, errCh := c.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case werr := <-errCh:
		return nil, fmt.Errorf("container wait: %w", werr)
	case st := <-waitCh:
		if st.StatusCode != 0 {
			return nil, fmt.Errorf("describe exited %d", st.StatusCode)
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	logs, err := c.ContainerLogs(ctx, resp.ID, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return nil, fmt.Errorf("container logs: %w", err)
	}
	defer logs.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, logs); err != nil && err != io.EOF {
		return nil, fmt.Errorf("demux logs: %w", err)
	}
	// Trim any trailing whitespace; pvbt's describe prints no newline, but the
	// log framing may contain one.
	return bytes.TrimSpace(stdout.Bytes()), nil
}

// _ unused import guard.
var _ = url.URL{}
```

Drop the `var _ = url.URL{}` guard once the build passes; it exists only so the editor doesn't prune the import prematurely while you type.

- [ ] **Step 2: Write a fake Docker client for tests**

Create `strategy/fakeclient_test.go` (shared by docker-install and docker-ephemeral tests):
```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
// ... license header

package strategy_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// fakeDocker is a tiny stub implementing dockercli.Client enough for our
// strategy-package tests. Configure via the public fields; default behavior
// is "succeed with a canned describe payload".
type fakeDocker struct {
	mu sync.Mutex

	ImageBuildErr  error
	ImageBuildResp string // JSON stream body

	DescribeStdout []byte
	ContainerExit  int64

	CreatedImages []string
	RemovedImages []string
	CreatedCmds   [][]string
	CreatedHosts  []*container.HostConfig
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		ImageBuildResp: `{"stream":"Step 1/1 : FROM scratch\n"}
{"stream":"Successfully built abc123\n"}
`,
		DescribeStdout: []byte(`{"shortCode":"fake","name":"Fake","parameters":[],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`),
	}
}

func (f *fakeDocker) ImageBuild(_ context.Context, _ io.Reader, opts types.ImageBuildOptions) (types.ImageBuildResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ImageBuildErr != nil {
		return types.ImageBuildResponse{}, f.ImageBuildErr
	}
	f.CreatedImages = append(f.CreatedImages, opts.Tags...)
	return types.ImageBuildResponse{
		Body: io.NopCloser(bytes.NewBufferString(f.ImageBuildResp)),
	}, nil
}

func (f *fakeDocker) ImageRemove(_ context.Context, id string, _ image.RemoveOptions) ([]image.DeleteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RemovedImages = append(f.RemovedImages, id)
	return nil, nil
}

func (f *fakeDocker) ContainerCreate(_ context.Context, cfg *container.Config, hostCfg *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CreatedCmds = append(f.CreatedCmds, cfg.Cmd)
	f.CreatedHosts = append(f.CreatedHosts, hostCfg)
	return container.CreateResponse{ID: fmt.Sprintf("ctr-%d", len(f.CreatedCmds))}, nil
}

func (f *fakeDocker) ContainerStart(context.Context, string, container.StartOptions) error { return nil }

func (f *fakeDocker) ContainerLogs(_ context.Context, _ string, _ container.LogsOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Emit the describe stdout through the Docker log multiplex header
	// (stream=1 = stdout) so stdcopy.StdCopy demuxes it correctly.
	var buf bytes.Buffer
	w := stdcopy.NewStdWriter(&buf, stdcopy.Stdout)
	_, _ = w.Write(f.DescribeStdout)
	return io.NopCloser(&buf), nil
}

func (f *fakeDocker) ContainerWait(_ context.Context, _ string, _ container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	wait := make(chan container.WaitResponse, 1)
	errCh := make(chan error, 1)
	wait <- container.WaitResponse{StatusCode: f.ContainerExit}
	return wait, errCh
}

func (f *fakeDocker) ContainerKill(context.Context, string, string) error            { return nil }
func (f *fakeDocker) ContainerRemove(context.Context, string, container.RemoveOptions) error {
	return nil
}
```

(`github.com/docker/docker/pkg/stdcopy` exposes both `StdCopy` and a `NewStdWriter`-style multiplexer; if the exact name differs in the SDK version vendored by `go mod tidy`, adjust to the available symbol.)

- [ ] **Step 3: Write `InstallDocker` happy-path test**

Append to `strategy/docker_install_test.go`:
```go
import (
	"context"
	"path/filepath"

	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("InstallDocker", func() {
	It("clones, builds, describes, and returns the image ref", func() {
		// Clone target = fake-strategy-src copied into a tempdir so the
		// real `git clone` shells out to a local repo (fast, no network).
		srcRepo := makeLocalGitRepo(GinkgoT(), "testdata/fake-strategy-src", "v1.0.0")
		fc := newFakeDocker()
		destDir := filepath.Join(GinkgoT().TempDir(), "install")

		result, err := strategy.InstallDocker(context.Background(),
			strategy.InstallRequest{
				ShortCode: "fake",
				CloneURL:  "file://" + srcRepo,
				Version:   "v1.0.0",
				DestDir:   destDir,
			},
			strategy.DockerInstallDeps{Client: fc, ImagePrefix: "pvapi-test"},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.ArtifactRef).To(HavePrefix("pvapi-test/unknown/"))
		Expect(result.ShortCode).To(Equal("fake"))
		Expect(fc.CreatedImages).To(HaveLen(1))
	})

	It("returns ErrDockerBuildFailed when ImageBuild fails", func() {
		srcRepo := makeLocalGitRepo(GinkgoT(), "testdata/fake-strategy-src", "v1.0.0")
		fc := newFakeDocker()
		fc.ImageBuildErr = fmt.Errorf("build boom")
		destDir := filepath.Join(GinkgoT().TempDir(), "install-fail")

		_, err := strategy.InstallDocker(context.Background(),
			strategy.InstallRequest{
				ShortCode: "fake",
				CloneURL:  "file://" + srcRepo,
				Version:   "v1.0.0",
				DestDir:   destDir,
			},
			strategy.DockerInstallDeps{Client: fc, ImagePrefix: "pvapi-test"},
		)
		Expect(err).To(MatchError(strategy.ErrDockerBuildFailed))
	})

	It("returns ErrShortCodeMismatch when describe disagrees", func() {
		srcRepo := makeLocalGitRepo(GinkgoT(), "testdata/fake-strategy-src", "v1.0.0")
		fc := newFakeDocker()
		fc.DescribeStdout = []byte(`{"shortCode":"different","parameters":[],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`)
		destDir := filepath.Join(GinkgoT().TempDir(), "install-mismatch")

		_, err := strategy.InstallDocker(context.Background(),
			strategy.InstallRequest{
				ShortCode: "fake",
				CloneURL:  "file://" + srcRepo,
				Version:   "v1.0.0",
				DestDir:   destDir,
			},
			strategy.DockerInstallDeps{Client: fc, ImagePrefix: "pvapi-test"},
		)
		Expect(err).To(MatchError(strategy.ErrShortCodeMismatch))
	})
})
```

`makeLocalGitRepo` is a test helper that copies a source tree into a temp dir, runs `git init && git add . && git commit -m init && git tag <ver>`, and returns the absolute path. Plan 3 / plan 7 tests already have an equivalent helper — reuse `makeLocalGitRepo` from `strategy/install_test.go` if it exists; otherwise define it in this file. Check:

```bash
grep -n "makeLocalGitRepo\|initLocalRepo" strategy/*_test.go
```

If the helper exists, just import it. If not, add:
```go
func makeLocalGitRepo(t GinkgoTInterface, src, tag string) string {
	dir := t.TempDir()
	copyTree(t, src, dir)
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "add", "."},
		{"git", "-c", "user.email=ci@pvapi", "-c", "user.name=CI", "commit", "-m", "init"},
		{"git", "tag", tag},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		Expect(cmd.Run()).To(Succeed(), "step: %v", args)
	}
	return dir
}

func copyTree(t GinkgoTInterface, src, dst string) {
	Expect(filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(target, b, info.Mode())
	})).To(Succeed())
}
```

- [ ] **Step 4: Run the tests**

Run:
```bash
go mod tidy
ginkgo run ./strategy
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add strategy/docker_install.go strategy/docker_install_test.go strategy/fakeclient_test.go go.mod go.sum
git commit -m "strategy: InstallDocker via engine sdk ImageBuild + describe-in-container"
```

---

## Task 8: `strategy.EphemeralImageBuild`

**Files:**
- Create: `strategy/docker_ephemeral.go`
- Create: `strategy/docker_ephemeral_test.go`

- [ ] **Step 1: Write the failing tests**

`strategy/docker_ephemeral_test.go`:
```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
// ... license header

package strategy_test

import (
	"context"
	"fmt"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("EphemeralImageBuild", func() {
	It("clones, builds, and returns an ephemeral image ref", func() {
		srcRepo := makeLocalGitRepo(GinkgoT(), "testdata/fake-strategy-src", "v1.0.0")
		fc := newFakeDocker()

		ref, cleanup, err := strategy.EphemeralImageBuild(context.Background(), strategy.DockerEphemeralOptions{
			CloneURL:          "file://" + srcRepo,
			Ver:               "v1.0.0",
			Dir:               GinkgoT().TempDir(),
			SkipURLValidation: true,
			Client:            fc,
			ImagePrefix:       "pvapi-ephem",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(ref).To(HavePrefix("pvapi-ephem/ephemeral/"))
		Expect(cleanup).NotTo(BeNil())

		// cleanup removes image + tempdir; idempotent.
		cleanup()
		Expect(fc.RemovedImages).To(ContainElement(ref))
		cleanup() // second call is a no-op
		Expect(fc.RemovedImages).To(HaveLen(1))
	})

	It("removes the tempdir when ImageBuild fails", func() {
		srcRepo := makeLocalGitRepo(GinkgoT(), "testdata/fake-strategy-src", "v1.0.0")
		fc := newFakeDocker()
		fc.ImageBuildErr = fmt.Errorf("nope")

		parent := GinkgoT().TempDir()
		ref, cleanup, err := strategy.EphemeralImageBuild(context.Background(), strategy.DockerEphemeralOptions{
			CloneURL:          "file://" + srcRepo,
			Ver:               "v1.0.0",
			Dir:               parent,
			SkipURLValidation: true,
			Client:            fc,
			ImagePrefix:       "pvapi-ephem",
		})
		Expect(err).To(HaveOccurred())
		Expect(ref).To(Equal(""))
		Expect(cleanup).To(BeNil())

		entries, _ := os.ReadDir(parent)
		Expect(entries).To(BeEmpty(), "tempdir should have been removed")
	})

	It("rejects a non-allowlisted URL when SkipURLValidation is false", func() {
		fc := newFakeDocker()
		_, _, err := strategy.EphemeralImageBuild(context.Background(), strategy.DockerEphemeralOptions{
			CloneURL:    "https://gitlab.com/foo/bar",
			Ver:         "v1",
			Dir:         GinkgoT().TempDir(),
			Client:      fc,
			ImagePrefix: "pvapi-ephem",
		})
		Expect(err).To(MatchError(strategy.ErrInvalidCloneURL))
	})
})
```

- [ ] **Step 2: Run the tests and watch them fail**

Run:
```bash
ginkgo run ./strategy
```

Expected: compile failure — `DockerEphemeralOptions` and `EphemeralImageBuild` undefined.

- [ ] **Step 3: Implement `docker_ephemeral.go`**

`strategy/docker_ephemeral.go`:
```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
// ... license header

package strategy

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
	"github.com/google/uuid"

	"github.com/penny-vault/pv-api/dockercli"
)

// DockerEphemeralOptions configures a single EphemeralImageBuild call.
type DockerEphemeralOptions struct {
	CloneURL          string
	Ver               string // optional; empty = default branch HEAD
	Dir               string // parent for mkdtemp
	Timeout           time.Duration
	SkipURLValidation bool
	Client            dockercli.Client
	ImagePrefix       string
}

// EphemeralImageBuild clones CloneURL into mkdtemp(Dir, "build-*"), renders
// the generated Dockerfile, and builds a disposable image tagged
// "<ImagePrefix>/ephemeral/<uuid>:latest". Returns (imageRef, cleanup, nil).
// cleanup is idempotent, calls ImageRemove(imageRef, force=true), and
// removes the tempdir. On any error before a successful return the tempdir
// is removed internally and ("", nil, err) is returned.
func EphemeralImageBuild(ctx context.Context, opts DockerEphemeralOptions) (string, func(), error) {
	if !opts.SkipURLValidation {
		if err := ValidateCloneURL(opts.CloneURL); err != nil {
			return "", nil, err
		}
	}
	if opts.Client == nil {
		return "", nil, fmt.Errorf("EphemeralImageBuild: nil Client")
	}
	if opts.ImagePrefix == "" {
		opts.ImagePrefix = "pvapi-strategy"
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = defaultEphemeralTimeout
	}
	parent := opts.Dir
	if parent == "" {
		parent = os.TempDir()
	}
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return "", nil, fmt.Errorf("ephemeral-image: parent dir: %w", err)
	}
	buildDir, err := os.MkdirTemp(parent, "build-*")
	if err != nil {
		return "", nil, fmt.Errorf("ephemeral-image: mkdtemp: %w", err)
	}

	tag := opts.ImagePrefix + "/ephemeral/" + uuid.NewString() + ":latest"
	var (
		removeMu sync.Mutex
		removed  bool
	)
	cleanup := func() {
		removeMu.Lock()
		defer removeMu.Unlock()
		if removed {
			return
		}
		removed = true
		_, _ = opts.Client.ImageRemove(context.Background(), tag, image.RemoveOptions{Force: true, PruneChildren: true})
		_ = os.RemoveAll(buildDir)
	}

	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Clone.
	cloneArgs := []string{"clone", "--depth=1"}
	if opts.Ver != "" {
		cloneArgs = append(cloneArgs, "--branch", opts.Ver)
	}
	cloneArgs = append(cloneArgs, opts.CloneURL, buildDir)
	cloneCmd := exec.CommandContext(tctx, "git", cloneArgs...) //nolint:gosec // URL validated or explicitly skipped
	var cloneOut bytes.Buffer
	cloneCmd.Stdout = &cloneOut
	cloneCmd.Stderr = &cloneOut
	if err := cloneCmd.Run(); err != nil {
		_ = os.RemoveAll(buildDir)
		return "", nil, fmt.Errorf("ephemeral-image: git clone: %w\n%s", err, cloneOut.String())
	}

	// Render Dockerfile.
	goVer, _ := ParseGoVersion(buildDir)
	if err := os.WriteFile(filepath.Join(buildDir, "Dockerfile"), RenderDockerfile(goVer), 0o600); err != nil {
		_ = os.RemoveAll(buildDir)
		return "", nil, fmt.Errorf("ephemeral-image: write Dockerfile: %w", err)
	}

	// Build.
	buildCtx, err := tarDir(buildDir)
	if err != nil {
		_ = os.RemoveAll(buildDir)
		return "", nil, fmt.Errorf("ephemeral-image: tar: %w", err)
	}
	resp, err := opts.Client.ImageBuild(tctx, buildCtx, types.ImageBuildOptions{
		Dockerfile: "Dockerfile",
		Tags:       []string{tag},
		Remove:     true,
		PullParent: true,
	})
	if err != nil {
		_ = os.RemoveAll(buildDir)
		return "", nil, fmt.Errorf("%w: %w", ErrDockerBuildFailed, err)
	}
	defer resp.Body.Close()
	if _, bErr := drainBuildStream(resp.Body); bErr != nil {
		_ = os.RemoveAll(buildDir)
		return "", nil, fmt.Errorf("%w: %w", ErrDockerBuildFailed, bErr)
	}

	return tag, cleanup, nil
}
```

- [ ] **Step 4: Run the tests**

Run:
```bash
ginkgo run ./strategy
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add strategy/docker_ephemeral.go strategy/docker_ephemeral_test.go
git commit -m "strategy: EphemeralImageBuild mirrors EphemeralBuild for docker mode"
```

---

## Task 9: Syncer — `RunnerMode` + `DockerInstaller` + artifact-kind mismatch

**Files:**
- Modify: `strategy/sync.go`
- Modify: `strategy/sync_test.go`

- [ ] **Step 1: Extend `SyncerOptions`**

Edit `strategy/sync.go`:
```go
type SyncerOptions struct {
	Discovery       DiscoveryFunc
	ResolveVer      ResolveVerFunc
	Installer       InstallerFunc // host-mode installer
	DockerInstaller InstallerFunc // docker-mode installer; required when RunnerMode == "docker"
	RunnerMode      string        // "host" (default) | "docker"
	OfficialDir     string
	Concurrency     int
	Interval        time.Duration
}
```

Add a helper:
```go
// expectedArtifactKind returns the artifact_kind string the current runner
// mode produces. Unknown modes treat as "binary" (host default).
func expectedArtifactKind(mode string) string {
	if mode == "docker" {
		return "image"
	}
	return "binary"
}
```

Change `Tick` to bypass the skip when artifact kind mismatches:
```go
if existing.LastAttemptedVer != nil && *existing.LastAttemptedVer == remote {
	kindMatches := existing.ArtifactKind != nil && *existing.ArtifactKind == expectedArtifactKind(s.opts.RunnerMode)
	if kindMatches {
		continue
	}
	log.Info().
		Str("short_code", shortCode).
		Str("existing_kind", ptrStr(existing.ArtifactKind)).
		Str("expected_kind", expectedArtifactKind(s.opts.RunnerMode)).
		Msg("artifact kind mismatch; scheduling reinstall")
}
```

Add at the bottom of `sync.go`:
```go
func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
```

Change `runInstall` to dispatch on mode:
```go
func (s *Syncer) runInstall(ctx context.Context, shortCode, cloneURL, version, dest string) {
	if err := s.store.MarkAttempt(ctx, shortCode, version); err != nil {
		log.Warn().Err(err).Str("short_code", shortCode).Msg("mark attempt failed")
		return
	}

	installer := s.opts.Installer
	kind := "binary"
	if s.opts.RunnerMode == "docker" {
		installer = s.opts.DockerInstaller
		kind = "image"
	}
	if installer == nil {
		_ = s.store.MarkFailure(ctx, shortCode, version, "no installer wired for runner mode "+s.opts.RunnerMode)
		return
	}

	result, err := installer(ctx, InstallRequest{
		ShortCode: shortCode, CloneURL: cloneURL, Version: version, DestDir: dest,
	})
	if err != nil {
		_ = s.store.MarkFailure(ctx, shortCode, version, err.Error())
		return
	}
	_ = s.store.MarkSuccess(ctx, shortCode, version, kind, result.ArtifactRef, result.DescribeJSON)
}
```

- [ ] **Step 2: Update `strategy/sync_test.go`**

Find the existing test that asserts `LastAttemptedVer == remote` → skip. Add a new `Context`/`It` that:

1. Seeds the store with a row where `ArtifactKind = "binary"`, `LastAttemptedVer = "v1.0.0"`, `InstalledVer = "v1.0.0"`.
2. Configures the fake `ResolveVer` to return `"v1.0.0"` (no change).
3. Runs `Syncer.Tick` with `RunnerMode = "docker"` and a `DockerInstaller` stub that records calls.
4. Asserts the docker installer was called exactly once (artifact-kind mismatch forced re-attempt).
5. Asserts `MarkSuccess` was called with `kind = "image"`, `ref = <what installer returned>`.

Template:
```go
It("reinstalls when runner mode changes and artifact_kind no longer matches", func() {
	s := &fakeStore{}
	kind := "binary"
	attempted := "v1.0.0"
	installed := "v1.0.0"
	s.rows = append(s.rows, strategy.Strategy{
		ShortCode:        "adm",
		RepoOwner:        "penny-vault",
		RepoName:         "adm",
		CloneURL:         "https://github.com/penny-vault/adm",
		IsOfficial:       true,
		LastAttemptedVer: &attempted,
		InstalledVer:     &installed,
		ArtifactKind:     &kind,
	})

	var dockerCalls int
	syncer := strategy.NewSyncer(s, strategy.SyncerOptions{
		Discovery:       func(context.Context) ([]strategy.Listing, error) {
			return []strategy.Listing{{Name: "adm", Owner: "penny-vault", CloneURL: "https://github.com/penny-vault/adm"}}, nil
		},
		ResolveVer:      func(context.Context, string) (string, error) { return "v1.0.0", nil },
		DockerInstaller: func(context.Context, strategy.InstallRequest) (*strategy.InstallResult, error) {
			dockerCalls++
			return &strategy.InstallResult{ArtifactRef: "pvapi-strategy/penny-vault/adm:v1.0.0", DescribeJSON: []byte(`{}`)}, nil
		},
		RunnerMode: "docker",
		Interval:   time.Second,
	})
	Expect(syncer.Tick(context.Background())).To(Succeed())
	Expect(dockerCalls).To(Equal(1))
	Expect(s.markSuccessCalls).To(HaveLen(1))
	Expect(s.markSuccessCalls[0].kind).To(Equal("image"))
})
```

Adjust field names (`markSuccessCalls`, `rows`) to match the existing `fakeStore` in `strategy/sync_test.go`. Grep first:
```bash
grep -n "fakeStore\|markSuccess\|MarkSuccess" strategy/sync_test.go
```

- [ ] **Step 3: Run the tests**

Run:
```bash
ginkgo run ./strategy
```

Expected: PASS. Existing host-mode tests still pass (they leave `RunnerMode` empty, so behavior falls back to the host path).

- [ ] **Step 4: Commit**

```bash
git add strategy/sync.go strategy/sync_test.go
git commit -m "strategy: sync dispatches installer by runner mode; artifact-kind mismatch forces reinstall"
```

---

## Task 10: `backtest.DockerRunner`

**Files:**
- Create: `backtest/docker.go`
- Create: `backtest/docker_test.go`
- Create: `backtest/fakeclient_test.go` (shared fake; parallel copy of strategy/fakeclient_test.go)

- [ ] **Step 1: Write the fake client**

Copy `strategy/fakeclient_test.go` into `backtest/fakeclient_test.go` with `package backtest_test`. This is a deliberate duplicate — both test packages need the same stub and wiring up a shared `testing`-only helper package costs more than the ~80 lines of duplication.

- [ ] **Step 2: Write the failing tests**

`backtest/docker_test.go`:
```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
// ... license header

package backtest_test

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/docker/docker/api/types/container"

	"github.com/penny-vault/pv-api/backtest"
)

var _ = Describe("DockerRunner", func() {
	var (
		fc     *fakeDocker
		runner *backtest.DockerRunner
	)

	BeforeEach(func() {
		fc = newFakeDocker()
		runner = &backtest.DockerRunner{
			Client:           fc,
			Network:          "pvapi",
			NanoCPUs:         2_000_000_000,
			MemoryBytes:      1 << 30,
			SnapshotsHostDir: "/host/snapshots",
			SnapshotsDir:     "/var/lib/pvapi/snapshots",
		}
	})

	It("runs the strategy image and returns nil on exit 0", func() {
		err := runner.Run(context.Background(), backtest.RunRequest{
			Artifact:     "pvapi-strategy/foo/bar:v1",
			ArtifactKind: backtest.ArtifactImage,
			Args:         []string{"--benchmark", "SPY"},
			OutPath:      "/var/lib/pvapi/snapshots/abc.sqlite.tmp",
			Timeout:      5 * time.Second,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(fc.CreatedCmds).To(HaveLen(1))
		Expect(fc.CreatedCmds[0]).To(Equal([]string{"backtest", "--output", "/var/lib/pvapi/snapshots/abc.sqlite.tmp", "--benchmark", "SPY"}))
		Expect(fc.CreatedHosts).To(HaveLen(1))
		hc := fc.CreatedHosts[0]
		Expect(hc.Resources.NanoCPUs).To(Equal(int64(2_000_000_000)))
		Expect(hc.Resources.Memory).To(Equal(int64(1 << 30)))
		Expect(hc.Mounts).To(HaveLen(1))
		Expect(string(hc.Mounts[0].Type)).To(Equal("bind"))
		Expect(hc.Mounts[0].Source).To(Equal("/host/snapshots"))
		Expect(hc.Mounts[0].Target).To(Equal("/var/lib/pvapi/snapshots"))
	})

	It("wraps non-zero exit in ErrRunnerFailed", func() {
		fc.ContainerExit = 2
		err := runner.Run(context.Background(), backtest.RunRequest{
			Artifact:     "img",
			ArtifactKind: backtest.ArtifactImage,
			OutPath:      "/snap.sqlite.tmp",
			Timeout:      time.Second,
		})
		Expect(err).To(MatchError(backtest.ErrRunnerFailed))
	})

	It("returns ErrArtifactKindMismatch for a binary artifact", func() {
		err := runner.Run(context.Background(), backtest.RunRequest{
			Artifact:     "/path/to/bin",
			ArtifactKind: backtest.ArtifactBinary,
			OutPath:      "/snap.sqlite.tmp",
		})
		Expect(err).To(MatchError(backtest.ErrArtifactKindMismatch))
	})

	It("returns ErrTimedOut when ContainerWait exceeds the deadline", func() {
		fc.WaitDelay = 100 * time.Millisecond
		err := runner.Run(context.Background(), backtest.RunRequest{
			Artifact:     "img",
			ArtifactKind: backtest.ArtifactImage,
			OutPath:      "/snap.sqlite.tmp",
			Timeout:      20 * time.Millisecond,
		})
		Expect(errors.Is(err, backtest.ErrTimedOut)).To(BeTrue())
		Expect(fc.KilledIDs).NotTo(BeEmpty())
	})

	It("uses the Network setting on the host config", func() {
		_ = runner.Run(context.Background(), backtest.RunRequest{
			Artifact: "img", ArtifactKind: backtest.ArtifactImage,
			OutPath: "/snap.sqlite.tmp", Timeout: time.Second,
		})
		Expect(string(fc.CreatedHosts[0].NetworkMode)).To(Equal("pvapi"))
	})
})

// Extend the fakeDocker in backtest/fakeclient_test.go with a WaitDelay +
// KilledIDs slice so the timeout case works. Edit backtest/fakeclient_test.go
// to add:
//
//   WaitDelay time.Duration
//   KilledIDs []string
//
// and change ContainerWait to honor the delay + ctx, ContainerKill to append
// to KilledIDs.
```

Update `backtest/fakeclient_test.go` per the comment above — after the happy-path field definitions, add:
```go
WaitDelay time.Duration
KilledIDs []string
```

Replace `ContainerWait` with:
```go
func (f *fakeDocker) ContainerWait(ctx context.Context, _ string, _ container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	wait := make(chan container.WaitResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		if f.WaitDelay > 0 {
			select {
			case <-time.After(f.WaitDelay):
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			}
		}
		wait <- container.WaitResponse{StatusCode: f.ContainerExit}
	}()
	return wait, errCh
}
```

And `ContainerKill`:
```go
func (f *fakeDocker) ContainerKill(_ context.Context, id, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.KilledIDs = append(f.KilledIDs, id)
	return nil
}
```

- [ ] **Step 3: Run and watch tests fail**

Run:
```bash
ginkgo run ./backtest
```

Expected: compile failure — `backtest.DockerRunner` undefined.

- [ ] **Step 4: Implement `docker.go`**

`backtest/docker.go`:
```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
// ... license header

package backtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pv-api/dockercli"
)

// DockerRunner executes a strategy image inside a one-shot Docker container
// and produces a SQLite snapshot at RunRequest.OutPath. The snapshots host
// dir is bind-mounted into the container at the same in-container path used
// by pvapi, so OutPath is written to a single filesystem the host can read.
type DockerRunner struct {
	Client           dockercli.Client
	Network          string
	NanoCPUs         int64
	MemoryBytes      int64
	SnapshotsHostDir string // host path used as bind Source
	SnapshotsDir     string // matching target path inside the strategy container
}

// Run implements Runner.
func (r *DockerRunner) Run(ctx context.Context, req RunRequest) error {
	if req.ArtifactKind != ArtifactImage {
		return fmt.Errorf("%w: DockerRunner requires ArtifactImage, got %d", ErrArtifactKindMismatch, req.ArtifactKind)
	}

	timeoutCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		timeoutCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	cmdLine := append([]string{"backtest", "--output", req.OutPath}, req.Args...)
	cfg := &container.Config{
		Image: req.Artifact,
		Cmd:   cmdLine,
		Tty:   false,
	}
	hostCfg := &container.HostConfig{
		AutoRemove:  true,
		NetworkMode: container.NetworkMode(r.Network),
		Resources: container.Resources{
			NanoCPUs: r.NanoCPUs,
			Memory:   r.MemoryBytes,
		},
		Mounts: []mount.Mount{{
			Type:   mount.TypeBind,
			Source: r.SnapshotsHostDir,
			Target: r.SnapshotsDir,
		}},
		Tmpfs: map[string]string{"/tmp": "size=256m"},
	}

	resp, err := r.Client.ContainerCreate(timeoutCtx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return fmt.Errorf("%w: container create: %w", ErrRunnerFailed, err)
	}
	log.Debug().Str("container_id", truncID(resp.ID)).Str("image", req.Artifact).Msg("container created")

	if err := r.Client.ContainerStart(timeoutCtx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("%w: container start: %w", ErrRunnerFailed, err)
	}

	// Logs → zerolog.
	logs, lerr := r.Client.ContainerLogs(timeoutCtx, resp.ID, container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: true,
	})
	var stderrTail bytes.Buffer
	if lerr == nil {
		go streamContainerLogs(logs, &stderrTail)
	}

	waitCh, errCh := r.Client.ContainerWait(timeoutCtx, resp.ID, container.WaitConditionNotRunning)
	select {
	case werr := <-errCh:
		if errors.Is(werr, context.DeadlineExceeded) || errors.Is(werr, context.Canceled) {
			_ = r.Client.ContainerKill(context.Background(), resp.ID, "SIGKILL")
			return fmt.Errorf("%w: %s", ErrTimedOut, firstNBytes(stderrTail.String(), 2048))
		}
		return fmt.Errorf("%w: wait: %w", ErrRunnerFailed, werr)
	case st := <-waitCh:
		if st.StatusCode != 0 {
			return fmt.Errorf("%w: exit=%d: %s", ErrRunnerFailed, st.StatusCode, firstNBytes(stderrTail.String(), 2048))
		}
		return nil
	case <-timeoutCtx.Done():
		_ = r.Client.ContainerKill(context.Background(), resp.ID, "SIGKILL")
		return fmt.Errorf("%w: %s", ErrTimedOut, firstNBytes(stderrTail.String(), 2048))
	}
}

// streamContainerLogs demultiplexes docker's framed log stream into two
// zerolog log-writer sinks and keeps a bounded tail of stderr for error
// messages.
func streamContainerLogs(r io.ReadCloser, stderrTail *bytes.Buffer) {
	defer r.Close()
	stdout := newLogWriter("strategy-stdout")
	stderr := newLogWriter("strategy-stderr")
	// Use an io.MultiWriter so we both log stderr line-by-line and capture a
	// bounded tail for error attachment.
	tail := &tailWriter{buf: stderrTail, max: 2048}
	_, _ = stdcopy.StdCopy(stdout, io.MultiWriter(stderr, tail), r)
}

// tailWriter accumulates the last `max` bytes written to it.
type tailWriter struct {
	buf *bytes.Buffer
	max int
}

func (t *tailWriter) Write(p []byte) (int, error) {
	remaining := t.max - t.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = t.buf.Write(p[:remaining])
		return len(p), nil
	}
	_, _ = t.buf.Write(p)
	return len(p), nil
}

func truncID(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
```

- [ ] **Step 5: Run the tests**

Run:
```bash
ginkgo run ./backtest
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backtest/docker.go backtest/docker_test.go backtest/fakeclient_test.go
git commit -m "backtest: DockerRunner via engine sdk with bind-mounted snapshots dir"
```

---

## Task 11: Accept `"docker"` in `backtest.Config.Validate`

**Files:**
- Modify: `backtest/config.go`
- Modify: `backtest/errors.go`

- [ ] **Step 1: Loosen `ErrUnsupportedRunnerMode`'s wording**

Edit `backtest/errors.go`:
```go
ErrUnsupportedRunnerMode = errors.New(`backtest: runner.mode must be "host" or "docker" (kubernetes lands in plan 9)`)
```

- [ ] **Step 2: Accept both modes in `Validate`**

Edit `backtest/config.go`:
```go
func (c Config) Validate() error {
	if c.SnapshotsDir == "" {
		return ErrSnapshotsDirRequired
	}
	if c.MaxConcurrency < 0 {
		return ErrInvalidConcurrency
	}
	switch c.RunnerMode {
	case "host", "docker":
		// ok
	default:
		return ErrUnsupportedRunnerMode
	}
	return nil
}
```

- [ ] **Step 3: Update or add a config test**

Find the existing test covering `ErrUnsupportedRunnerMode`:
```bash
grep -n "ErrUnsupportedRunnerMode\|RunnerMode" backtest/*_test.go
```

Add one entry for `"docker"` → no error and keep the `"kubernetes"` → error case.

- [ ] **Step 4: Run**

```bash
ginkgo run ./backtest
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backtest/config.go backtest/errors.go backtest/config_test.go
git commit -m "backtest: Validate accepts runner.mode = docker"
```

(If the test file is named differently, adjust the add path.)

---

## Task 12: `cmd/server.go` — switch on `runner.mode`

**Files:**
- Modify: `cmd/server.go`
- Modify: `api/server.go`

- [ ] **Step 1: Extend `api.RegistryConfig` + `startRegistrySync`**

Edit `api/server.go`:
```go
type RegistryConfig struct {
	GitHubToken     string
	SyncInterval    time.Duration
	Concurrency     int
	OfficialDir     string
	GitHubOwner     string
	CacheDir        string
	RunnerMode      string
	DockerInstaller strategy.InstallerFunc
}
```

In `startRegistrySync`, pass both through:
```go
syncer := strategy.NewSyncer(store, strategy.SyncerOptions{
	Discovery:       discovery,
	ResolveVer:      strategy.ResolveVerWithGit,
	Installer:       strategy.Install,
	DockerInstaller: conf.DockerInstaller,
	RunnerMode:      conf.RunnerMode,
	OfficialDir:     conf.OfficialDir,
	Concurrency:     conf.Concurrency,
	Interval:        conf.SyncInterval,
})
```

- [ ] **Step 2: Replace the host-only wiring in `cmd/server.go` with a switch**

Find the block starting at `portfolioStore := portfolio.NewPoolStore(pool)` and replace from there through the existing `dispatcher.Start(ctx)` line. The new shape:

```go
portfolioStore := portfolio.NewPoolStore(pool)
strategyStore := strategy.PoolStore{Pool: pool}

var (
	runner         backtest.Runner
	artifactKind   backtest.ArtifactKind
	resolve        backtest.ArtifactResolver
	dockerInstaller strategy.InstallerFunc
)

switch conf.Runner.Mode {
case "host":
	runner = &backtest.HostRunner{}
	artifactKind = backtest.ArtifactBinary
	resolve = func(resolveCtx context.Context, cloneURL, ver string) (string, func(), error) {
		if ver != "" {
			artifact, err := strategyStore.LookupArtifact(resolveCtx, cloneURL, ver)
			if err == nil && artifact != "" {
				return artifact, func() {}, nil
			}
			if err != nil && !errors.Is(err, strategy.ErrNotFound) {
				return "", nil, err
			}
		}
		return strategy.EphemeralBuild(resolveCtx, strategy.EphemeralOptions{
			CloneURL: cloneURL,
			Ver:      ver,
			Dir:      conf.Strategy.EphemeralDir,
			Timeout:  conf.Strategy.EphemeralInstallTimeout,
		})
	}

case "docker":
	dc, err := client.NewClientWithOpts(
		client.WithHost(conf.Runner.Docker.Socket),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		log.Fatal().Err(err).Msg("docker client")
	}
	var memBytes int64
	if m := conf.Runner.Docker.MemoryLimit; m != "" && m != "0" {
		b, mErr := units.RAMInBytes(m)
		if mErr != nil {
			log.Fatal().Err(mErr).Msg("parse runner.docker.memory_limit")
		}
		memBytes = b
	}
	nanoCPUs := int64(conf.Runner.Docker.CPULimit * 1e9)
	snapHost := conf.Runner.Docker.SnapshotsHostPath
	if snapHost == "" {
		snapHost = conf.Backtest.SnapshotsDir
	}
	runner = &backtest.DockerRunner{
		Client:           dc,
		Network:          conf.Runner.Docker.Network,
		NanoCPUs:         nanoCPUs,
		MemoryBytes:      memBytes,
		SnapshotsHostDir: snapHost,
		SnapshotsDir:     conf.Backtest.SnapshotsDir,
	}
	artifactKind = backtest.ArtifactImage
	resolve = func(resolveCtx context.Context, cloneURL, ver string) (string, func(), error) {
		if ver != "" {
			artifact, err := strategyStore.LookupArtifact(resolveCtx, cloneURL, ver)
			if err == nil && artifact != "" {
				return artifact, func() {}, nil
			}
			if err != nil && !errors.Is(err, strategy.ErrNotFound) {
				return "", nil, err
			}
		}
		return strategy.EphemeralImageBuild(resolveCtx, strategy.DockerEphemeralOptions{
			CloneURL:    cloneURL,
			Ver:         ver,
			Dir:         conf.Strategy.EphemeralDir,
			Timeout:     conf.Strategy.EphemeralInstallTimeout,
			Client:      dc,
			ImagePrefix: conf.Runner.Docker.ImagePrefix,
		})
	}
	dockerInstaller = func(instCtx context.Context, req strategy.InstallRequest) (*strategy.InstallResult, error) {
		return strategy.InstallDocker(instCtx, req, strategy.DockerInstallDeps{
			Client:       dc,
			ImagePrefix:  conf.Runner.Docker.ImagePrefix,
			BuildTimeout: conf.Runner.Docker.BuildTimeout,
		})
	}

case "kubernetes":
	log.Fatal().Msg("runner.mode = kubernetes lands in plan 9")

default:
	log.Fatal().Str("mode", conf.Runner.Mode).Msg("unknown runner.mode")
}

portfolioAdapter := backtestPortfolioStoreAdapter{store: portfolioStore}
runAdapter := backtestRunStoreAdapter{store: portfolioStore.PoolRunStore}
orch := backtest.NewRunner(btCfg, runner, artifactKind, portfolioAdapter, runAdapter, resolve)
dispatcher := backtest.NewDispatcher(btCfg, runner, runAdapter, orch.Run)
dispatcher.Start(ctx)
```

Add the two new imports near the top:
```go
"github.com/docker/docker/client"
units "github.com/docker/go-units"
```

Plumb `RunnerMode` + `DockerInstaller` into `api.NewApp(...).RegistryConfig`:
```go
Registry: api.RegistryConfig{
	GitHubToken:     conf.GitHub.Token,
	SyncInterval:    conf.Strategy.RegistrySyncInterval,
	Concurrency:     conf.Strategy.InstallConcurrency,
	OfficialDir:     conf.Strategy.OfficialDir,
	GitHubOwner:     "penny-vault",
	RunnerMode:      conf.Runner.Mode,
	DockerInstaller: dockerInstaller,
},
```

- [ ] **Step 3: Build**

Run:
```bash
go build ./...
```

Expected: success.

- [ ] **Step 4: Run the full test suite**

Run:
```bash
ginkgo run ./...
```

Expected: PASS for every package.

- [ ] **Step 5: Commit**

```bash
git add cmd/server.go api/server.go
git commit -m "cmd: switch on runner.mode; build docker runner + installer + resolver"
```

---

## Task 13: Integration test (behind `//go:build integration`)

**Files:**
- Create: `backtest/docker_integration_test.go`

- [ ] **Step 1: Write the test**

`backtest/docker_integration_test.go`:
```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
//go:build integration
// +build integration

package backtest_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/docker/docker/client"

	"github.com/penny-vault/pv-api/strategy"
)

func TestEphemeralImageBuild_IntegrationBootstrap(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "DockerIntegration Suite")
}

var _ = Describe("EphemeralImageBuild (real daemon)", func() {
	It("builds + runs describe end-to-end", func() {
		dc, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		Expect(err).NotTo(HaveOccurred())

		repo := itMakeLocalGitRepo(GinkgoT(), "../strategy/testdata/fake-strategy-src", "v1.0.0")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		ref, cleanup, berr := strategy.EphemeralImageBuild(ctx, strategy.DockerEphemeralOptions{
			CloneURL:          "file://" + repo,
			Ver:               "v1.0.0",
			Dir:               GinkgoT().TempDir(),
			SkipURLValidation: true,
			Client:            dc,
			ImagePrefix:       "pvapi-it",
		})
		Expect(berr).NotTo(HaveOccurred())
		DeferCleanup(cleanup)

		out, rerr := exec.CommandContext(ctx, "docker", "run", "--rm", ref, "describe", "--json").Output()
		Expect(rerr).NotTo(HaveOccurred())
		Expect(string(out)).To(ContainSubstring(`"shortCode": "fake"`))
	})
})

// itMakeLocalGitRepo copies src into a tempdir, runs git init/add/commit/tag,
// and returns the repo path. Prefixed `it` to avoid colliding with any
// helper of the same name elsewhere in the backtest_test package.
func itMakeLocalGitRepo(t GinkgoTInterface, src, tag string) string {
	dir := t.TempDir()
	itCopyTree(t, src, dir)
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "add", "."},
		{"git", "-c", "user.email=ci@pvapi", "-c", "user.name=CI", "commit", "-m", "init"},
		{"git", "tag", tag},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		Expect(cmd.Run()).To(Succeed(), "step: %v", args)
	}
	return dir
}

func itCopyTree(t GinkgoTInterface, src, dst string) {
	Expect(filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(target, b, info.Mode())
	})).To(Succeed())
}
```

- [ ] **Step 2: Verify it builds (skipped by default)**

Run:
```bash
go build -tags integration ./backtest
```

Expected: success. `go test` without the tag ignores this file.

- [ ] **Step 3: Commit**

```bash
git add backtest/docker_integration_test.go
git commit -m "backtest: integration test for ephemeral image build + describe"
```

---

## Task 14: README "Running with the Docker runner"

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Locate the deployment section**

Run:
```bash
grep -n "Deployment\|Docker\|runner.mode" README.md
```

Identify the best anchor — likely an existing "Deployment" or "Operating" section.

- [ ] **Step 2: Append the subsection**

Add:
```markdown
### Running with the Docker runner

pvapi can execute every backtest inside a one-shot Docker container for
sandboxing. Set `runner.mode = "docker"` in `pvapi.toml` (or `PVAPI_RUNNER_MODE=docker`).
The next strategy-sync tick (within `strategy.registry_sync_interval`) will
rebuild every official strategy as a Docker image; restart pvapi to force an
immediate tick.

Two deployment shapes work:

**1. pvapi on a host with Docker installed.** Leave
`runner.docker.snapshots_host_path` empty. The runner bind-mounts
`backtest.snapshots_dir` directly; nothing else to configure.

**2. pvapi itself running in Docker.** Mount the Docker socket into pvapi
(`-v /var/run/docker.sock:/var/run/docker.sock`) so it can drive the host
daemon. Mount the host snapshots directory into pvapi **at the same
in-container path** as `backtest.snapshots_dir` — e.g.
`-v /srv/pvapi/snapshots:/var/lib/pvapi/snapshots`. Set
`runner.docker.snapshots_host_path = "/srv/pvapi/snapshots"` so pvapi
passes the host path (not its own container-local view) to the Docker
daemon in the strategy container's bind mount.

Mounting the Docker socket gives pvapi the ability to run arbitrary
containers on the host. This is the same privilege any CI runner receives;
treat pvapi's host accordingly.

Per-container resource limits come from `[runner.docker]`:

- `cpu_limit` in cores (0 = unlimited)
- `memory_limit` as a go-units string (`512Mi`, `1Gi`; empty = unlimited)
- `build_timeout` — max wall-clock for one `git clone` + `docker build`
```

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: runner.mode = docker deployment notes"
```

---

## Task 15: Lint + full test pass + final verify

**Files:** (none new; verification only)

- [ ] **Step 1: gofmt / goimports**

Run:
```bash
gofmt -l -w .
```

Expected: no changes (if there are changes, review and re-commit as a polish commit).

- [ ] **Step 2: Full test suite**

Run:
```bash
go test ./...
ginkgo run ./...
```

Expected: all PASS.

- [ ] **Step 3: Lint**

Run:
```bash
golangci-lint run ./...
```

Fix anything surfaced by the linter. Common hits:
- new `nolint:gosec` hints required on shell-out lines — match the existing `//nolint:gosec // G204` style.
- `errcheck` on `stdcopy.StdCopy` or `_, _ = dec.Decode(...)` — use `_, _ =` or add an explicit error handling branch.

- [ ] **Step 4: Final commit if polish was needed**

```bash
git add -u
git commit -m "gofmt + lint cleanup on docker runner files" # if needed
```

- [ ] **Step 5: Merge checklist**

Before opening a PR / merge:

- `go test ./...` passes
- `golangci-lint run ./...` clean
- `ginkgo run ./...` passes
- Manual host-mode smoke: `go run . server` (with a working pvapi.toml + postgres) serves `/healthz` and a host-mode backtest still completes end-to-end.
- Manual docker-mode smoke (optional, requires Docker): flip `runner.mode` to `docker` in pvapi.toml; next sync tick rebuilds an official strategy as an image (`docker images | grep pvapi-strategy`); `POST /portfolios/{slug}/runs` completes; snapshot file appears in `snapshots_dir`.

---

## Rollback plan

Every task is its own commit; reverting the last N commits unwinds the plan cleanly. The only external-state change is `go.mod` / `go.sum` (task 1) — `git revert` suffices. No schema migration. No change to host-mode behavior, so reverting never takes host-mode deployments down.
