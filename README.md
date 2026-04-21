# pv-api

## Deployment

### Running with the Docker runner

pvapi can execute every backtest inside a one-shot Docker container for
sandboxing. Set `runner.mode = "docker"` in `pvapi.toml` (or
`PVAPI_RUNNER_MODE=docker`). The next strategy-sync tick (within
`strategy.registry_sync_interval`) will rebuild every official strategy as
a Docker image; restart pvapi to force an immediate tick.

Two deployment shapes work:

**1. pvapi on a host with Docker installed.** Leave
`runner.docker.snapshots_host_path` empty. The runner bind-mounts
`backtest.snapshots_dir` directly; nothing else to configure.

**2. pvapi itself running in Docker.** Mount the Docker socket into pvapi
(`-v /var/run/docker.sock:/var/run/docker.sock`) so it can drive the host
daemon. Mount the host snapshots directory into pvapi **at the same
in-container path** as `backtest.snapshots_dir` — e.g. `-v
/srv/pvapi/snapshots:/var/lib/pvapi/snapshots`. Set
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