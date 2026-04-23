# Portfolio Status Field & Backtest Progress Streaming

**Date:** 2026-04-22
**Status:** Draft

## Overview

Two related changes:

1. Expose the existing `portfolios.status` column (`pending | running | ready | failed`) in the Portfolio API response.
2. Add a Server-Sent Events endpoint that streams fine-grained progress from the strategy binary's `--json` stdout during a backtest run.

---

## Part 1 — Portfolio Status Field

### What changes

The `portfolios.status` column is already maintained by the orchestrator via `MarkRunningTx`, `MarkReadyTx`, and `MarkFailedTx`. It is loaded into the `Portfolio` domain struct but stripped from the API response in `portfolioView`.

**OpenAPI (`openapi/openapi.yaml`):**
- Add `status` to the `Portfolio` schema as a required string enum: `pending | running | ready | failed`.

**Handler (`portfolio/handler.go`):**
- Add `Status string` to `portfolioView`.
- Populate it in `toView()` from `p.Status`.

**Generated types:** regenerate `openapi.gen.go` after the spec change.

No DB, domain, or migration changes needed.

---

## Part 2 — Progress Streaming

### Strategy binary protocol

When launched with `--json`, the strategy binary writes newline-delimited JSON to stdout. Progress messages have this shape:

```json
{"type":"progress","step":1234,"total_steps":5000,"current_date":"2023-05-15","target_date":"2025-01-01","pct":24.68,"elapsed_ms":3200,"eta_ms":9800,"measurements":45231}
```

Other `type` values may appear on stdout; only `"progress"` messages are forwarded to subscribers.

### Transport: Server-Sent Events

Progress is server-to-client only. SSE is the right fit: native browser support, works over HTTP/1.1, no protocol upgrade, auto-reconnect via `EventSource`. No WebSocket needed.

### Architecture

#### `backtest.ProgressHub`

A new type in the `backtest` package. One instance is created at startup and injected wherever needed (orchestrator and HTTP handler).

```
ProgressHub
  mu   sync.RWMutex
  runs map[uuid.UUID]*runBroadcaster
```

```
runBroadcaster
  mu       sync.Mutex
  buf      [20]ProgressMessage   // circular buffer, head + count
  subs     []chan Event           // buffered channels, one per subscriber
  terminal *TerminalEvent        // non-nil once the run ends
```

**`Hub.Publish(runID uuid.UUID, msg ProgressMessage)`**
- Looks up the broadcaster under a read lock; creates one if absent (write lock).
- Under the broadcaster's lock: appends `msg` to the circular buffer (overwrites oldest), then fans out to all subscriber channels.
- Fan-out is non-blocking: if a subscriber channel is full, that subscriber is dropped and its channel closed.

**`Hub.Subscribe(runID uuid.UUID) (<-chan Event, func())`**
- If the run is already terminal, returns a closed channel pre-loaded with the terminal event.
- Otherwise, creates a buffered channel (capacity 32), replays the circular buffer into it, adds it to the subscriber list, and returns it along with an unsubscribe func.

**`Hub.Complete(runID uuid.UUID, status string, errMsg string)`**
- Sets `terminal` on the broadcaster, sends a `TerminalEvent` to all subscribers, closes their channels.
- Schedules deletion of the broadcaster from the `runs` map after a 10-second drain window so late-joining clients still get the terminal event.

#### Event types

```go
type ProgressMessage struct {
    Type         string  `json:"type"`
    Step         int64   `json:"step"`
    TotalSteps   int64   `json:"total_steps"`
    CurrentDate  string  `json:"current_date"`
    TargetDate   string  `json:"target_date"`
    Pct          float64 `json:"pct"`
    ElapsedMS    int64   `json:"elapsed_ms"`
    EtaMS        int64   `json:"eta_ms"`
    Measurements int64   `json:"measurements"`
}

type TerminalEvent struct {
    Status string `json:"status"` // "success" | "failed"
    Error  string `json:"error,omitempty"`
}

// Event is the union sent on subscriber channels
type Event struct {
    Progress *ProgressMessage
    Terminal *TerminalEvent
}
```

#### Runner changes — `RunRequest.ProgressWriter`

Add an optional `ProgressWriter io.Writer` field to `RunRequest`. When non-nil:
- The runner appends `--json` to the strategy command arguments.
- `HostRunner`: `cmd.Stdout` becomes `io.MultiWriter(logWriter("strategy-stdout"), req.ProgressWriter)`.
- `DockerRunner`: the stdout side of `stdcopy.StdCopy` becomes `io.MultiWriter(logWriter("strategy-stdout"), req.ProgressWriter)`.

When `ProgressWriter` is nil, behavior is unchanged (no `--json` flag, stdout goes to log only).

#### Orchestrator changes — `progressLineWriter`

A new unexported type in the `backtest` package:

```go
type progressLineWriter struct {
    hub   *ProgressHub
    runID uuid.UUID
    buf   bytes.Buffer
}
```

Implements `io.Writer`. On each `Write`, appends to an internal line buffer, extracts complete lines, attempts `json.Unmarshal` into `ProgressMessage`, and calls `hub.Publish` for messages with `type == "progress"`. Malformed or unknown-type lines are logged at debug level and discarded.

The orchestrator constructs a `progressLineWriter` when a hub is configured and passes it as `RunRequest.ProgressWriter`. It calls `hub.Complete(runID, ...)` in both the success and failure paths (in `Run` and `fail`).

The hub is optional — if nil, the orchestrator behaves exactly as today.

### SSE endpoint

**Route:** `GET /portfolios/{slug}/runs/{runId}/progress`

**Auth:** standard subject extraction; portfolio must be owned by the caller (existing `store.Get(ctx, sub, slug)` pattern).

**Logic:**

1. Resolve the portfolio by slug + subject; 404 if not found.
2. Look up the run by ID; 404 if not found or not owned by this portfolio.
3. Call `hub.Subscribe(runID)`. Set `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `X-Accel-Buffering: no`.
4. Stream events until the channel is closed (terminal event received) or the client disconnects.

**SSE wire format:**

```
event: progress
data: {"step":1234,"total_steps":5000,"current_date":"2023-05-15","target_date":"2025-01-01","pct":24.68,"elapsed_ms":3200,"eta_ms":9800,"measurements":45231}

event: done
data: {"status":"success"}

event: error
data: {"status":"failed","error":"strategy exited with code 1"}
```

Progress events omit the `type` field (it's implied by the SSE event name). Terminal state uses `done` or `error` event names.

**Late-join / post-completion:** if the broadcaster has already been cleaned up from the hub (run ended more than 10 s ago), the handler queries `RunStore.GetRun` for the final status and immediately writes a single terminal event, then closes the stream. This covers the case where the client connects after the run finishes.

### OpenAPI additions

- `ProgressMessage` schema: matches the struct above (all fields required except nothing optional).
- `RunTerminalEvent` schema: `status` (enum: `success | failed`), `error` (string, nullable).
- New path: `GET /portfolios/{slug}/runs/{runId}/progress`
  - Tags: `Portfolios`
  - Produces: `text/event-stream`
  - 200: SSE stream (described as string format for OpenAPI compatibility)
  - 401: Unauthorized
  - 404: Run or portfolio not found

---

## Wiring

`ProgressHub` is constructed once in `main.go` (or wherever the dispatcher is built) and injected into:
- The orchestrator via a new `WithProgressHub(h *ProgressHub) *orchestrator` method.
- The portfolio HTTP handler via a new field `Hub *backtest.ProgressHub` on `Handler` (used only by the SSE endpoint).

---

## Testing

- `ProgressHub` unit tests: publish/subscribe, circular buffer replay, late-join terminal event, slow-subscriber drop.
- `progressLineWriter` unit tests: partial line buffering, malformed JSON skip, non-progress type skip.
- `HostRunner` unit tests: `--json` flag added when `ProgressWriter != nil`, absent when nil.
- SSE handler integration test: mount a test hub, publish a few messages + terminal event, assert SSE output.
