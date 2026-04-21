# pvapi 3.0 — Docker runner (plan 8)

**Status:** draft for review
**Date:** 2026-04-20
**Author:** Jeremy Fergason (with Claude)
**Builds on:** `2026-04-16-pvapi-3-0-design.md` (§ Backtest execution, § Install
per runner), `2026-04-19-pvapi-3-0-unofficial-strategies.md` (resolver shape).

## Summary

Plan 8 adds a second backtest runner that executes strategies inside Docker
containers instead of as host processes. Official strategies are built into
versioned images at install time; unofficial strategies are built into
disposable images per backtest and removed on the way out. A single
`runner.mode` config toggle picks which runner + installer pair is wired at
startup. Mode switches self-heal on the next sync tick via an
artifact-kind mismatch trigger.

## Goals

- Provide sandboxing for strategy execution: a misbehaving or malicious
  strategy cannot read/write the host filesystem beyond the bind-mounted
  snapshots directory.
- Keep the orchestration layer (`backtest.Run`, scheduler, dispatcher)
  unchanged and runner-agnostic.
- Keep the host path working end-to-end. Switching between `host` and
  `docker` requires only a config change plus one sync cycle.
- Keep the "one codepath per portfolio kind" invariant from plan 7:
  officials go through the install cache; unofficials go through an
  ephemeral build.
- Support pvapi running in Docker and shelling out to the host Docker
  daemon via the mounted socket.

## Non-goals

- Registry push / pull for official images. Images live in the local
  Docker daemon only. Plan 9 (Kubernetes) introduces registry workflow.
- Kubernetes runner. Plan 9.
- Pruning old versioned images. Old host binaries are kept forever
  ("old versioned directories remain on disk"); old images follow the
  same convention. Disk management is the operator's responsibility.
- Per-strategy custom Dockerfile. pvapi generates a single canonical
  Dockerfile shape (go-builder + distroless/static).
- Updating pvapi's own production Dockerfile. Deployment-docs-only
  guidance lives in the README.
- Changes to the scheduler, dispatcher, or derived-data endpoints.

## Storage

No migration. `strategies.artifact_kind` already accepts `'image'`;
`artifact_ref` already stores free-form strings. Existing rows carry
`artifact_kind = 'binary'` or `'image'` depending on which mode was
active when the sync that populated them ran.

## Artifact naming

- **Official images:** `<image_prefix>/<owner>/<repo>:<version>` where
  `<image_prefix>` comes from config (default `pvapi-strategy`).
  Example: `pvapi-strategy/penny-vault/adm:v1.2.3`. Stored as
  `artifact_kind = 'image'`, `artifact_ref = <full ref>`.
- **Ephemeral images:** `<image_prefix>/ephemeral/<uuid>:latest`. Not
  persisted anywhere; tag exists only so `docker image rm` can find it.
  Cleanup closure calls `ImageRemove` + `os.RemoveAll(buildDir)`.

## Config

Additions to `pvapi.toml`:

```toml
[runner]
mode = "docker"   # host | docker | kubernetes

  [runner.docker]
  socket              = "unix:///var/run/docker.sock"
  network             = ""         # empty = Docker default (bridge)
  cpu_limit           = 0.0        # 0 = unlimited; 2.0 = 2 cores
  memory_limit        = ""         # "" or "0" = unlimited; else "512Mi" / "1Gi"
  build_timeout       = "10m"      # clone + image build upper bound
  image_prefix        = "pvapi-strategy"
  snapshots_host_path = ""         # see "Snapshots bind mount" below
```

`cmd/config.go` gains a `dockerConf` struct under `runnerConf`:

```go
type runnerConf struct {
    Mode   string     `mapstructure:"mode"`
    Docker dockerConf `mapstructure:"docker"`
}

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

Viper defaults registered in `cmd/viper.go` alongside the existing
`runner.mode` default. Flags added to `cmd/server.go`'s `serverCmd.Flags()`
following the existing `[strategy]` pattern.

### Snapshots bind mount

When pvapi itself runs in Docker and spawns strategy containers via the
mounted `/var/run/docker.sock`, the bind-mount path passed in
`HostConfig.Mounts` is resolved by the daemon against the **host**
filesystem, not pvapi's. The deployment contract:

- **pvapi on a VM (or dev host) with Docker installed:** `snapshots_host_path`
  is empty. The runner uses `conf.Backtest.SnapshotsDir` for both the bind
  source and target.
- **pvapi itself in Docker:** operator mounts the host snapshots dir into
  pvapi's container **at the same in-container path** as
  `conf.Backtest.SnapshotsDir` (e.g. `/var/lib/pvapi/snapshots` on both
  sides), and sets `snapshots_host_path` to that host path. The runner
  uses `snapshots_host_path` as the bind source and
  `conf.Backtest.SnapshotsDir` as the target. pvapi and the strategy
  container see the same path, so `OutPath` written by the strategy is
  the same string pvapi reads after the run.

## Runner interface change

`backtest/runner.go`:

```go
type ArtifactKind int

const (
    ArtifactBinary ArtifactKind = iota
    ArtifactImage
)

type RunRequest struct {
    Artifact     string        // binary path for host; image ref for docker
    ArtifactKind ArtifactKind
    Args         []string
    OutPath      string
    Timeout      time.Duration
}
```

Rename the current `Binary` field to `Artifact` at the one call site
(`backtest/run.go`). Each runner validates `ArtifactKind` matches its
mode and returns `ErrArtifactKindMismatch` on mismatch — cheap insurance
against programmer error at wire-up time.

## Resolver rename

`BinaryResolver` → `ArtifactResolver` in `backtest/run.go`. Signature is
unchanged (`(ctx, cloneURL, ver) -> (ref, cleanup, err)`). The semantics
of `ref` depends on which resolver was wired: path for host, image ref
for docker. The orchestrator never interprets `ref` — it passes it
straight into `RunRequest.Artifact` along with the runner-declared
`ArtifactKind`.

## DockerRunner

New file `backtest/docker.go`:

```go
type DockerRunner struct {
    Client           dockercli.Client
    Network          string
    NanoCPUs         int64  // CPULimit * 1e9
    MemoryBytes      int64  // parsed from memory_limit
    SnapshotsHostDir string // bind source
    SnapshotsDir     string // bind target (= backtest.snapshots_dir)
}

func (r *DockerRunner) Run(ctx context.Context, req RunRequest) error
```

Flow:

1. Assert `req.ArtifactKind == ArtifactImage`; else return
   `ErrArtifactKindMismatch`.
2. Apply `req.Timeout` via `context.WithTimeout` (same shape as
   `HostRunner`).
3. `ContainerCreate` with:
   - `Image`: `req.Artifact`
   - `Cmd`: `append([]string{"backtest", "--output", req.OutPath}, req.Args...)`
     (ENTRYPOINT of the strategy image is `/strategy`, so `Cmd` is the
     argument list after the binary)
   - `HostConfig.AutoRemove = true`
   - `HostConfig.NetworkMode = r.Network` (empty passes Docker's default)
   - `HostConfig.Resources.NanoCPUs = r.NanoCPUs`,
     `Memory = r.MemoryBytes`
   - `HostConfig.Mounts = [{Type: bind, Source: r.SnapshotsHostDir,
     Target: r.SnapshotsDir}]` (read-write)
   - `HostConfig.Tmpfs = {"/tmp": "size=256m"}`
   - Container name derived from `run_id` for easier log correlation
     (`pvapi-bt-<runID[:12]>`).
4. `ContainerStart`.
5. Stream `ContainerLogs(stdout=true, stderr=true, follow=true)` through
   zerolog. Each stream framed by Docker's multiplex header; we unwrap
   via `stdcopy.StdCopy` into two `logWriter` sinks (`scope=strategy-stdout`
   and `scope=strategy-stderr`) so per-line logging matches host-runner
   behavior.
6. `ContainerWait(condition = "not-running")` for the exit code.
7. On timeout / ctx.Done: `ContainerKill(signal="SIGKILL")` then return
   `ErrTimedOut`.
8. On non-zero exit: return
   `fmt.Errorf("%w: exit=%d: %s", ErrRunnerFailed, code, lastStderrChunk)`.
   `lastStderrChunk` is a bounded (2 KiB) tail of the stderr stream,
   captured by the stderr `logWriter`.
9. `AutoRemove` deletes the container on exit. No explicit
   `ContainerRemove` call except in the error recovery paths where the
   container may still exist (timeout / kill).

## DockerClient interface

Both `backtest.DockerRunner` and `strategy.InstallDocker` need to talk
to the Docker daemon. To avoid a new `backtest`↔`strategy` dependency
edge, the interface lives in a new leaf package `dockercli/` at the
repo root, imported by both:

```go
// dockercli/client.go
package dockercli

type Client interface {
    ImageBuild(ctx context.Context, buildContext io.Reader, opts types.ImageBuildOptions) (types.ImageBuildResponse, error)
    ImageRemove(ctx context.Context, imageID string, opts image.RemoveOptions) ([]image.DeleteResponse, error)
    ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, netConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
    ContainerStart(ctx context.Context, containerID string, opts container.StartOptions) error
    ContainerLogs(ctx context.Context, containerID string, opts container.LogsOptions) (io.ReadCloser, error)
    ContainerWait(ctx context.Context, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
    ContainerKill(ctx context.Context, containerID, signal string) error
    ContainerRemove(ctx context.Context, containerID string, opts container.RemoveOptions) error
}
```

Production passes `*client.Client` from
`github.com/docker/docker/client`, constructed once in `cmd/server.go`
and handed to both the runner and the sync installer. Tests pass a
fake. `dockercli/` has no other responsibilities — it's a single-file
interface package.

## strategy package additions

### `strategy/dockerfile.go`

```go
const defaultGoVersion = "1.24"

// renderDockerfile returns the bytes of a multi-stage Dockerfile that
// compiles the strategy with golang:<goVer>-alpine and copies the
// resulting binary into distroless/static-debian12:nonroot. goVer is
// parsed from the clone dir's go.mod; callers fall back to
// defaultGoVersion on parse failure.
func renderDockerfile(goVer string) []byte

// parseGoVersion reads <dir>/go.mod and returns the "go <ver>" directive,
// or "" if missing.
func parseGoVersion(dir string) (string, error)
```

Generated Dockerfile:

```
# syntax=docker/dockerfile:1.7
FROM golang:{{goVer}}-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/strategy .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/strategy /strategy
USER nonroot:nonroot
ENTRYPOINT ["/strategy"]
```

### `strategy/docker_install.go`

```go
type DockerInstallDeps struct {
    Client       dockercli.Client
    ImagePrefix  string
    BuildTimeout time.Duration
}

// InstallDocker performs a single version-pinned docker install:
//   1. git clone --branch <Version> --depth 1 <CloneURL> <DestDir>
//   2. write generated Dockerfile into <DestDir>
//   3. ImageBuild tarring <DestDir> as build context, tag = <prefix>/<owner>/<repo>:<ver>
//   4. docker run --rm <image> describe --json
//   5. validate describe.shortCode matches req.ShortCode
func InstallDocker(ctx context.Context, req InstallRequest, deps DockerInstallDeps) (*InstallResult, error)
```

Returns `InstallResult{ BinPath: "", ArtifactRef: <image>, DescribeJSON: <bytes>, ShortCode: <parsed> }`.

The describe step runs a throwaway container built from the image
(`ContainerCreate` with `Cmd = ["describe", "--json"]`, no bind mounts,
`AutoRemove=true`); stdout is captured via `ContainerLogs +
stdcopy.StdCopy` and unmarshalled. Same shortCode-mismatch validation
as the host `Install`.

### `strategy/docker_ephemeral.go`

```go
type DockerEphemeralOptions struct {
    CloneURL          string
    Ver               string
    Dir               string        // parent for mkdtemp build context
    Timeout           time.Duration
    SkipURLValidation bool
    Client            dockercli.Client
    ImagePrefix       string
}

// EphemeralImageBuild mirrors EphemeralBuild. Clones into
// mkdtemp(Dir, "build-*"), writes the generated Dockerfile, builds
// image tagged "<ImagePrefix>/ephemeral/<uuid>:latest", returns
// (imageRef, cleanup, nil). Cleanup is idempotent and runs
// ImageRemove(imageRef, force=true) followed by RemoveAll(buildDir).
func EphemeralImageBuild(ctx context.Context, opts DockerEphemeralOptions) (string, func(), error)
```

On any error before a successful return, `EphemeralImageBuild` removes
the tempdir and (if the image was already created) removes the image,
then returns `("", nil, err)`.

The describe-after-build step for ephemerals is the **caller's** job —
matches how `EphemeralBuild` works today (the caller chooses whether
to describe the binary).

### Image tag derivation

```go
// imageTag returns "<prefix>/<owner>/<repo>:<ver>" for a GitHub clone URL.
// Falls back to "<prefix>/unknown/<slugified-host-path>:<ver>" if the URL
// is non-GitHub (only possible with SkipURLValidation).
func imageTag(prefix, cloneURL, ver string) string
```

Unit-tested directly; used by both `InstallDocker` (official) and the
tag-wiring test for the resolver.

## Sync: mode-aware install + kind mismatch

`strategy/sync.go`:

```go
type SyncerOptions struct {
    Discovery        DiscoveryFunc
    ResolveVer       ResolveVerFunc
    Installer        InstallerFunc       // host-mode installer
    DockerInstaller  InstallerFunc       // docker-mode installer (wraps InstallDocker)
    RunnerMode       string              // "host" | "docker"
    OfficialDir      string              // only read in host mode
    Concurrency      int
    Interval         time.Duration
}
```

`runInstall` dispatches on `RunnerMode`:

```go
var installer InstallerFunc
expectedKind := "binary"
if s.opts.RunnerMode == "docker" {
    installer = s.opts.DockerInstaller
    expectedKind = "image"
} else {
    installer = s.opts.Installer
}
result, err := installer(ctx, InstallRequest{...})
...
_ = s.store.MarkSuccess(ctx, shortCode, version, expectedKind, ref, describe)
```

`ref` is `result.BinPath` in host mode, `result.ArtifactRef` in docker
mode. The existing `InstallResult` grows an `ArtifactRef` field
(`BinPath` remains for host-mode callers).

`Tick` gains one extra condition around the `LastAttemptedVer == remote`
skip:

```go
expectedKind := artifactKindFor(s.opts.RunnerMode)
kindMatches := existing.ArtifactKind != nil && *existing.ArtifactKind == expectedKind

if existing.LastAttemptedVer != nil && *existing.LastAttemptedVer == remote && kindMatches {
    continue
}
```

An `artifact_kind` mismatch (or a `NULL` artifact_kind after a first-time
install attempt that predates the toggle) bypasses the skip and schedules
another install. Self-healing on mode switch — operator changes
`runner.mode`, next sync tick rebuilds everything in the new format.

## Orchestrator wiring in `cmd/server.go`

```go
var (
    runner  backtest.Runner
    resolve backtest.ArtifactResolver
    dockerInstaller strategy.InstallerFunc // nil in host mode
)

switch conf.Runner.Mode {
case "host":
    runner = &backtest.HostRunner{}
    resolve = hostResolver(strategyStore, conf.Strategy)

case "docker":
    dc, err := client.NewClientWithOpts(
        client.WithHost(conf.Runner.Docker.Socket),
        client.WithAPIVersionNegotiation(),
    )
    if err != nil { log.Fatal().Err(err).Msg("docker client") }

    nanoCPU := int64(conf.Runner.Docker.CPULimit * 1e9)
    var memBytes int64
    if s := conf.Runner.Docker.MemoryLimit; s != "" && s != "0" {
        b, err := units.RAMInBytes(s) // github.com/docker/go-units — accepts "512Mi", "1Gi", "2Gb", bare bytes, etc.
        if err != nil { log.Fatal().Err(err).Msg("parse memory_limit") }
        memBytes = b
    }

    snapHostDir := conf.Runner.Docker.SnapshotsHostPath
    if snapHostDir == "" { snapHostDir = conf.Backtest.SnapshotsDir }

    runner = &backtest.DockerRunner{
        Client:           dc,
        Network:          conf.Runner.Docker.Network,
        NanoCPUs:         nanoCPU,
        MemoryBytes:      memBytes,
        SnapshotsHostDir: snapHostDir,
        SnapshotsDir:     conf.Backtest.SnapshotsDir,
    }
    resolve = dockerResolver(dc, strategyStore, conf)
    dockerInstaller = func(ctx context.Context, req strategy.InstallRequest) (*strategy.InstallResult, error) {
        return strategy.InstallDocker(ctx, req, strategy.DockerInstallDeps{
            Client:       dc,
            ImagePrefix:  conf.Runner.Docker.ImagePrefix,
            BuildTimeout: conf.Runner.Docker.BuildTimeout,
        })
    }

case "kubernetes":
    log.Fatal().Msg("runner.mode = kubernetes lands in plan 9")
}

// Pass dockerInstaller (nil in host mode) into strategy.SyncerOptions.
```

Two new small helpers:

- `hostResolver(...)`: extracts today's inline resolver from `serverCmd.RunE`.
- `dockerResolver(dc, store, conf)`: mirror with `LookupArtifact` returning
  image refs and a fallback to `strategy.EphemeralImageBuild`.

## Errors

Additions to `backtest/errors.go`:

```go
ErrArtifactKindMismatch = errors.New("backtest: artifact kind mismatch")
```

Additions to `strategy/errors.go` (or equivalent):

```go
ErrDockerBuildFailed = errors.New("strategy: docker image build failed")
```

Loosen `ErrUnsupportedRunnerMode` message in `backtest/errors.go` to
`runner.mode must be "host" or "docker" in plan 8 (kubernetes lands in plan 9)`.

`Config.Validate` in `backtest/config.go` accepts `"host"` and `"docker"`.

## Logging

Per-run fields retained (`run_id`, `portfolio_id`, `short_code`,
`strategy_ver`). Add:

- `container_id` (first 12 chars) on `ContainerCreate` success.
- `image_ref` on `ImageBuild` success.

Strategy stdout/stderr come in over `ContainerLogs` with Docker's
multiplex framing; after `stdcopy.StdCopy` unwraps them they go to two
existing-shape `logWriter`s at `info` with `scope=strategy-stdout` and
`scope=strategy-stderr`. Per-line framing is the same as host.

## Testing

Mock the `DockerClient` interface for unit tests. One
`//go:build integration` test exercises a real daemon.

### New test files

- `backtest/docker_test.go`:
  - happy path: fake client returns exit code 0, `Run` returns nil.
  - non-zero exit → `ErrRunnerFailed` wrapping exit code + stderr tail.
  - timeout: fake `ContainerWait` blocks forever, ctx expires, assert
    `ContainerKill` called with `SIGKILL` and `ErrTimedOut` returned.
  - artifact-kind mismatch: `ArtifactKind = ArtifactBinary` → returns
    `ErrArtifactKindMismatch` without touching the client.
  - bind mount: asserts `HostConfig.Mounts[0].{Source, Target}` derived
    from `SnapshotsHostDir` / `SnapshotsDir`.
  - resource limits: asserts `NanoCPUs` / `Memory` propagate into
    `HostConfig.Resources`.
- `strategy/dockerfile_test.go`:
  - `parseGoVersion` against three go.mod fixtures (`1.22`, `1.24`,
    `"garbage\n"`) → third falls back to default.
  - `renderDockerfile` contains the expected `FROM golang:X-alpine` and
    `FROM gcr.io/distroless/static-debian12:nonroot` lines for each.
- `strategy/docker_install_test.go`:
  - happy path: fake client `ImageBuild` returns aux-json with the
    expected tag; `ContainerCreate`/`Start`/`Logs`/`Wait` returns
    describe JSON; `MarkSuccess` receives `artifact_kind = "image"`
    and the right ref.
  - shortCode mismatch from describe → `ErrShortCodeMismatch`.
  - `ImageBuild` returns error → wrapped `ErrDockerBuildFailed`.
- `strategy/docker_ephemeral_test.go`:
  - happy path.
  - cleanup closure calls `ImageRemove` + removes tempdir.
  - cleanup closure idempotent (callable twice).
  - `ImageBuild` fails → tempdir cleaned up before return, closure nil,
    image not tagged.
- `strategy/sync_test.go` extension:
  - new case: `RunnerMode = "docker"`, existing row with
    `artifact_kind = "binary"` and `LastAttemptedVer == remote`. Assert
    the docker installer is called (bypasses the usual skip).
  - existing host-mode tests unchanged.

### Integration test

`backtest/docker_integration_test.go` (build tag `integration`):

- Requires `/var/run/docker.sock` (skipped when `client.NewClientWithOpts`
  fails).
- Uses `testdata/fakestrategy` (plan-3 fixture) served via `file://` URL
  with `SkipURLValidation = true`.
- Calls `EphemeralImageBuild` against it, asserts image exists, runs
  `docker run --rm <image> describe --json` via the SDK, asserts the
  expected describe JSON.
- Calls cleanup, asserts image is gone.

## Deployment notes (README update)

Add a "Running with the Docker runner" subsection to the README's
deployment section:

- Set `runner.mode = "docker"`. Officials are re-installed as images on
  the next sync tick (~1h by default, or restart pvapi to force an
  immediate tick).
- On a VM: install Docker, leave `runner.docker.snapshots_host_path`
  empty. That's it.
- pvapi-in-Docker:
  - Mount `/var/run/docker.sock` into pvapi.
  - Mount the host snapshots path into pvapi at the **same path** as
    `backtest.snapshots_dir` (e.g. `-v /srv/pvapi/snapshots:/var/lib/pvapi/snapshots`).
  - Set `runner.docker.snapshots_host_path` to that **host** path
    (`/srv/pvapi/snapshots` in the example).
  - Understand the privilege implications of mounting the Docker socket;
    pvapi can launch arbitrary containers on the host.

## Plan boundaries

Inside scope:
- `DockerRunner` + `DockerClient` interface.
- `RunRequest.ArtifactKind` field, `BinaryResolver` → `ArtifactResolver`
  rename.
- `strategy.InstallDocker`, `strategy.EphemeralImageBuild`,
  `strategy/dockerfile.go`.
- Syncer mode-awareness + artifact-kind mismatch trigger.
- `[runner.docker]` config + flags + viper defaults.
- `cmd/server.go` switch-on-mode wiring.
- Tests (unit via mock client + one integration-tagged end-to-end).
- README deployment notes.

Outside scope:
- Registry push / pull (plan 9).
- `KubernetesRunner` (plan 9).
- Image GC / pruning (matches host: kept forever).
- pvapi production Dockerfile changes.
- Per-strategy custom Dockerfile support.
- Docker credentials / authenticated pulls (the strategy images are
  built locally; base images `golang:alpine` and `distroless/static`
  are pulled from public registries without credentials in plan 8).

## Open questions

None — all design decisions in this spec are fixed. Open items that
surface during implementation are normal plan-execution detail, not
brainstorming.
