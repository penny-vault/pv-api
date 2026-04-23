# Portfolio Status Field & Backtest Progress Streaming — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose the portfolio status field in API responses, and stream fine-grained backtest progress from the strategy binary over SSE.

**Architecture:** A `ProgressHub` in the `backtest` package maintains one in-memory pub/sub broadcaster per active run, with a 20-message circular buffer for late-joining clients. The orchestrator wraps a `progressLineWriter` that parses `--json` stdout from the strategy binary and publishes to the hub. A new Fiber SSE endpoint at `GET /portfolios/:slug/runs/:runId/progress` subscribes and streams events to the client.

**Tech Stack:** Go 1.23+, Fiber v3, Ginkgo v2/Gomega (existing test framework), `oapi-codegen` v2 for OpenAPI type generation.

---

## File Map

| Action | Path | Responsibility |
|--------|------|----------------|
| Modify | `openapi/openapi.yaml` | Add `Portfolio.status`, `ProgressMessage`, `RunTerminalEvent` schemas, SSE endpoint |
| Regen  | `openapi/openapi.gen.go` | Updated generated types |
| Modify | `portfolio/handler.go` | Add `Status` to `portfolioView` and `toView()` |
| Create | `backtest/progress.go` | `ProgressMessage`, `TerminalEvent`, `Event`, `ProgressHub`, `runBroadcaster`, `progressLineWriter` |
| Create | `backtest/progress_test.go` | Unit tests for the above |
| Modify | `backtest/runner.go` | Add `ProgressWriter io.Writer` to `RunRequest` |
| Modify | `backtest/host.go` | Tee stdout to `ProgressWriter` when set; add `--json` flag |
| Modify | `backtest/docker.go` | Same for container stdout |
| Modify | `backtest/run.go` | Add `hub *ProgressHub` field, `WithProgressHub`, call `hub.Complete` |
| Modify | `backtest/run_test.go` | Verify hub.Complete called on success and failure |
| Create | `portfolio/progress_handler.go` | `StreamRunProgress` SSE handler, `Handler.hub` field, `WithHub` |
| Create | `portfolio/progress_handler_test.go` | Tests for the SSE handler |
| Modify | `api/portfolios.go` | Add SSE route to both stub and real registration |
| Modify | `api/server.go` | Add `ProgressHub *backtest.ProgressHub` to `Config`; wire hub into handler |
| Modify | `cmd/server.go` | Create hub, call `orch.WithProgressHub(hub)`, pass to `api.Config` |

---

## Task 1: Expose `status` in Portfolio API response

**Files:**
- Modify: `openapi/openapi.yaml`
- Regen: `openapi/openapi.gen.go` (via `go generate ./openapi/`)
- Modify: `portfolio/handler.go:441-486`

- [ ] **Step 1: Write a failing test that checks `status` is present in the GET /portfolios/:slug response**

Add this test to `portfolio/handler_test.go` (or the existing portfolio Ginkgo suite file — match the existing test file's package and structure):

```go
It("includes status in the portfolio response", func() {
    // Find the existing test that calls GET /portfolios/:slug and assert
    // the decoded JSON contains a "status" key.
    // Use the existing test helper pattern to decode the body and check
    // that body["status"] is one of "pending", "running", "ready", "failed".
})
```

If the test suite does not already cover JSON body shape for GET portfolio, add a table-driven entry. Otherwise verify by running the full suite and confirming the absence of a `status` field currently causes no test to fail (the test you're about to write will be the first).

- [ ] **Step 2: Run the existing portfolio tests to establish baseline**

```
go test ./portfolio/... -v 2>&1 | tail -20
```

Expected: all pass.

- [ ] **Step 3: Add `status` to the OpenAPI Portfolio schema**

In `openapi/openapi.yaml`, locate the `Portfolio` schema (line ~750). Add `status` to the `required` list and to `properties`:

```yaml
    Portfolio:
      type: object
      description: Portfolio configuration. Derived backtest output lives on separate endpoints.
      required:
        - slug
        - name
        - strategyCode
        - parameters
        - benchmark
        - createdAt
        - updatedAt
        - status
      properties:
        # ... existing properties unchanged ...
        status:
          type: string
          enum: [pending, running, ready, failed]
          description: Current lifecycle status of the portfolio's backtest.
```

- [ ] **Step 4: Regenerate Go types**

```
go generate ./openapi/
```

Verify `openapi/openapi.gen.go` now contains a `Status` field on the `Portfolio` struct.

- [ ] **Step 5: Add `Status` to `portfolioView` and `toView()`**

In `portfolio/handler.go`, update `portfolioView`:

```go
type portfolioView struct {
	Slug             string         `json:"slug"`
	Name             string         `json:"name"`
	Status           string         `json:"status"`
	StrategyCode     string         `json:"strategyCode"`
	StrategyVer      *string        `json:"strategyVer"`
	StrategyCloneURL string         `json:"strategyCloneUrl"`
	Parameters       map[string]any `json:"parameters"`
	PresetName       *string        `json:"presetName"`
	Benchmark        string         `json:"benchmark"`
	StartDate        *string        `json:"startDate,omitempty"`
	EndDate          *string        `json:"endDate,omitempty"`
	CreatedAt        string         `json:"createdAt"`
	UpdatedAt        string         `json:"updatedAt"`
	LastRunAt        *string        `json:"lastRunAt"`
	LastError        *string        `json:"lastError"`
}
```

In `toView()`, add the field:

```go
func toView(p Portfolio) portfolioView {
	v := portfolioView{
		Slug:             p.Slug,
		Name:             p.Name,
		Status:           string(p.Status),
		StrategyCode:     p.StrategyCode,
		StrategyVer:      p.StrategyVer,
		StrategyCloneURL: p.StrategyCloneURL,
		Parameters:       p.Parameters,
		PresetName:       p.PresetName,
		Benchmark:        p.Benchmark,
		CreatedAt:        p.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:        p.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		LastError:        p.LastError,
	}
	if p.StartDate != nil {
		d := p.StartDate.Format("2006-01-02")
		v.StartDate = &d
	}
	if p.EndDate != nil {
		d := p.EndDate.Format("2006-01-02")
		v.EndDate = &d
	}
	if p.LastRunAt != nil {
		t := p.LastRunAt.UTC().Format("2006-01-02T15:04:05Z")
		v.LastRunAt = &t
	}
	return v
}
```

- [ ] **Step 6: Run tests**

```
go test ./portfolio/... ./openapi/... -v 2>&1 | tail -20
```

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add openapi/openapi.yaml openapi/openapi.gen.go portfolio/handler.go
git commit -m "feat(portfolio): expose status field in API response"
```

---

## Task 2: Define progress types

**Files:**
- Create: `backtest/progress.go`
- Create: `backtest/progress_test.go`

- [ ] **Step 1: Create `backtest/progress.go` with the core types**

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

package backtest

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// ProgressMessage mirrors the strategy binary's --json stdout progress line.
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

// TerminalEvent is the final event sent when a run ends.
type TerminalEvent struct {
	Status string `json:"status"` // "success" | "failed"
	Error  string `json:"error,omitempty"`
}

// Event is the union type sent on subscriber channels.
// Exactly one of Progress or Terminal is non-nil.
type Event struct {
	Progress *ProgressMessage
	Terminal *TerminalEvent
}

const bufSize = 20
const subChanCap = 32

type runBroadcaster struct {
	mu       sync.Mutex
	buf      [bufSize]ProgressMessage
	head     int
	count    int
	subs     []chan Event
	terminal *TerminalEvent
}

// ProgressHub manages in-flight run progress state.
// One instance is created at startup and injected into the orchestrator and HTTP handler.
type ProgressHub struct {
	mu   sync.RWMutex
	runs map[uuid.UUID]*runBroadcaster
}

// NewProgressHub returns a ready-to-use ProgressHub.
func NewProgressHub() *ProgressHub {
	return &ProgressHub{runs: make(map[uuid.UUID]*runBroadcaster)}
}

func (h *ProgressHub) broadcaster(runID uuid.UUID) *runBroadcaster {
	h.mu.RLock()
	b, ok := h.runs[runID]
	h.mu.RUnlock()
	if ok {
		return b
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok = h.runs[runID]
	if !ok {
		b = &runBroadcaster{}
		h.runs[runID] = b
	}
	return b
}

// Publish records msg in the circular buffer and fans it out to all current subscribers.
// Subscribers whose channel is full are dropped.
func (h *ProgressHub) Publish(runID uuid.UUID, msg ProgressMessage) {
	h.broadcaster(runID).publish(msg)
}

func (b *runBroadcaster) publish(msg ProgressMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()

	idx := (b.head + b.count) % bufSize
	b.buf[idx] = msg
	if b.count < bufSize {
		b.count++
	} else {
		b.head = (b.head + 1) % bufSize
	}

	evt := Event{Progress: &msg}
	alive := b.subs[:0]
	for _, ch := range b.subs {
		select {
		case ch <- evt:
			alive = append(alive, ch)
		default:
			close(ch)
		}
	}
	b.subs = alive
}

// Subscribe returns a channel that receives buffered + live events.
// If the run is already terminal the channel is pre-loaded with the terminal event and closed.
// The returned func unsubscribes and releases the channel.
func (h *ProgressHub) Subscribe(runID uuid.UUID) (<-chan Event, func()) {
	return h.broadcaster(runID).subscribe()
}

func (b *runBroadcaster) subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan Event, subChanCap)

	if b.terminal != nil {
		ch <- Event{Terminal: b.terminal}
		close(ch)
		return ch, func() {}
	}

	for i := 0; i < b.count; i++ {
		idx := (b.head + i) % bufSize
		msg := b.buf[idx]
		ch <- Event{Progress: &msg}
	}

	b.subs = append(b.subs, ch)

	unsub := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for i, s := range b.subs {
			if s == ch {
				b.subs = append(b.subs[:i], b.subs[i+1:]...)
				close(ch)
				return
			}
		}
	}
	return ch, unsub
}

// Complete marks the run terminal, notifies all subscribers, and schedules
// cleanup of the broadcaster after a 10-second drain window.
func (h *ProgressHub) Complete(runID uuid.UUID, status, errMsg string) {
	h.broadcaster(runID).complete(TerminalEvent{Status: status, Error: errMsg})
	go func() {
		time.Sleep(10 * time.Second)
		h.mu.Lock()
		delete(h.runs, runID)
		h.mu.Unlock()
	}()
}

func (b *runBroadcaster) complete(evt TerminalEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.terminal != nil {
		return
	}
	b.terminal = &evt

	e := Event{Terminal: &evt}
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
		close(ch)
	}
	b.subs = nil
}

// progressLineWriter parses newline-delimited JSON from strategy stdout and
// publishes progress messages to the hub.
type progressLineWriter struct {
	hub   *ProgressHub
	runID uuid.UUID
	buf   bytes.Buffer
}

func newProgressLineWriter(hub *ProgressHub, runID uuid.UUID) *progressLineWriter {
	return &progressLineWriter{hub: hub, runID: runID}
}

// Write implements io.Writer. Each complete newline-terminated line is parsed
// as JSON; lines with type=="progress" are published to the hub. Malformed
// lines and non-progress types are silently discarded.
func (w *progressLineWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		b := w.buf.Bytes()
		idx := bytes.IndexByte(b, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSpace(string(b[:idx]))
		w.buf.Next(idx + 1)
		if line == "" {
			continue
		}
		var msg ProgressMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			log.Debug().Str("line", line).Msg("strategy stdout: malformed json")
			continue
		}
		if msg.Type == "progress" {
			w.hub.Publish(w.runID, msg)
		}
	}
	return len(p), nil
}
```

- [ ] **Step 2: Create `backtest/progress_test.go`**

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

package backtest_test

import (
	"fmt"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
)

var _ = Describe("ProgressHub", func() {
	var (
		hub   *backtest.ProgressHub
		runID uuid.UUID
	)

	BeforeEach(func() {
		hub = backtest.NewProgressHub()
		runID = uuid.New()
	})

	Describe("Subscribe before any Publish", func() {
		It("returns a live channel that receives subsequent publishes", func() {
			events, unsub := hub.Subscribe(runID)
			defer unsub()

			msg := backtest.ProgressMessage{Type: "progress", Step: 1, TotalSteps: 100, Pct: 1.0}
			hub.Publish(runID, msg)

			evt := <-events
			Expect(evt.Progress).NotTo(BeNil())
			Expect(evt.Progress.Step).To(Equal(int64(1)))
		})
	})

	Describe("Subscribe after several Publishes", func() {
		It("replays the buffered messages then receives live messages", func() {
			for i := 1; i <= 5; i++ {
				hub.Publish(runID, backtest.ProgressMessage{Type: "progress", Step: int64(i), TotalSteps: 10})
			}

			events, unsub := hub.Subscribe(runID)
			defer unsub()

			// Receive replayed messages
			for i := 1; i <= 5; i++ {
				evt := <-events
				Expect(evt.Progress).NotTo(BeNil())
				Expect(evt.Progress.Step).To(Equal(int64(i)))
			}
		})
	})

	Describe("circular buffer overflow (>20 messages)", func() {
		It("keeps only the most recent 20 messages", func() {
			for i := 1; i <= 25; i++ {
				hub.Publish(runID, backtest.ProgressMessage{Type: "progress", Step: int64(i)})
			}

			events, unsub := hub.Subscribe(runID)
			defer unsub()

			first := <-events
			Expect(first.Progress.Step).To(Equal(int64(6))) // oldest surviving = step 6
		})
	})

	Describe("Complete before Subscribe", func() {
		It("returns a pre-closed channel with the terminal event", func() {
			hub.Complete(runID, "success", "")

			events, _ := hub.Subscribe(runID)
			evt := <-events
			Expect(evt.Terminal).NotTo(BeNil())
			Expect(evt.Terminal.Status).To(Equal("success"))

			_, ok := <-events
			Expect(ok).To(BeFalse())
		})
	})

	Describe("Complete after Subscribe", func() {
		It("sends the terminal event to active subscribers", func() {
			events, unsub := hub.Subscribe(runID)
			defer unsub()

			hub.Complete(runID, "failed", "strategy exited 1")

			var terminal *backtest.TerminalEvent
			for evt := range events {
				if evt.Terminal != nil {
					terminal = evt.Terminal
					break
				}
			}
			Expect(terminal).NotTo(BeNil())
			Expect(terminal.Status).To(Equal("failed"))
			Expect(terminal.Error).To(Equal("strategy exited 1"))
		})
	})

	Describe("multiple subscribers", func() {
		It("all receive the same events", func() {
			const n = 3
			channels := make([]<-chan backtest.Event, n)
			unsubs := make([]func(), n)
			for i := range channels {
				channels[i], unsubs[i] = hub.Subscribe(runID)
				defer unsubs[i]()
			}

			hub.Publish(runID, backtest.ProgressMessage{Type: "progress", Step: 42})
			for _, ch := range channels {
				evt := <-ch
				Expect(evt.Progress.Step).To(Equal(int64(42)))
			}
		})
	})
})

var _ = Describe("progressLineWriter", func() {
	var (
		hub    *backtest.ProgressHub
		runID  uuid.UUID
	)

	BeforeEach(func() {
		hub = backtest.NewProgressHub()
		runID = uuid.New()
	})

	It("publishes a complete progress line", func() {
		events, unsub := hub.Subscribe(runID)
		defer unsub()

		w := backtest.NewProgressLineWriter(hub, runID)
		line := `{"type":"progress","step":10,"total_steps":100,"current_date":"2023-01-01","target_date":"2025-01-01","pct":10.0,"elapsed_ms":500,"eta_ms":4500,"measurements":1000}` + "\n"
		_, err := w.Write([]byte(line))
		Expect(err).NotTo(HaveOccurred())

		evt := <-events
		Expect(evt.Progress).NotTo(BeNil())
		Expect(evt.Progress.Step).To(Equal(int64(10)))
		Expect(evt.Progress.Pct).To(BeNumerically("~", 10.0, 0.001))
	})

	It("buffers a partial line and publishes when newline arrives", func() {
		events, unsub := hub.Subscribe(runID)
		defer unsub()

		w := backtest.NewProgressLineWriter(hub, runID)
		line := `{"type":"progress","step":5,"total_steps":100,"current_date":"2023-01-01","target_date":"2025-01-01","pct":5.0,"elapsed_ms":100,"eta_ms":1900,"measurements":50}`
		half := len(line) / 2
		_, _ = w.Write([]byte(line[:half]))
		Consistently(events, "50ms").ShouldNot(Receive())
		_, _ = w.Write([]byte(line[half:] + "\n"))
		Eventually(events, "100ms").Should(Receive())
	})

	It("discards malformed JSON lines", func() {
		events, unsub := hub.Subscribe(runID)
		defer unsub()

		w := backtest.NewProgressLineWriter(hub, runID)
		_, _ = w.Write([]byte("not json\n"))
		Consistently(events, "50ms").ShouldNot(Receive())
	})

	It("discards lines with type != progress", func() {
		events, unsub := hub.Subscribe(runID)
		defer unsub()

		w := backtest.NewProgressLineWriter(hub, runID)
		_, _ = w.Write([]byte(`{"type":"info","message":"starting"}` + "\n"))
		Consistently(events, "50ms").ShouldNot(Receive())
	})

	It("handles multiple lines in a single write", func() {
		events, unsub := hub.Subscribe(runID)
		defer unsub()

		w := backtest.NewProgressLineWriter(hub, runID)
		lines := ""
		for i := 1; i <= 3; i++ {
			lines += fmt.Sprintf(`{"type":"progress","step":%d,"total_steps":10,"current_date":"2023-01-01","target_date":"2025-01-01","pct":%d.0,"elapsed_ms":100,"eta_ms":900,"measurements":%d}`+"\n", i, i*10, i*100)
		}
		_, _ = w.Write([]byte(lines))
		for i := 1; i <= 3; i++ {
			evt := <-events
			Expect(evt.Progress.Step).To(Equal(int64(i)))
		}
	})
})
```

Note: `backtest.NewProgressLineWriter` needs to be exported. Rename `newProgressLineWriter` → `NewProgressLineWriter` in `progress.go`.

- [ ] **Step 3: Export `NewProgressLineWriter` in `backtest/progress.go`**

Change:
```go
func newProgressLineWriter(hub *ProgressHub, runID uuid.UUID) *progressLineWriter {
```
to:
```go
func NewProgressLineWriter(hub *ProgressHub, runID uuid.UUID) *progressLineWriter {
```

- [ ] **Step 4: Run the backtest tests**

```
go test ./backtest/... -v 2>&1 | tail -40
```

Expected: all new tests pass. If any progress test fails, fix before proceeding.

- [ ] **Step 5: Commit**

```bash
git add backtest/progress.go backtest/progress_test.go
git commit -m "feat(backtest): add ProgressHub, progressLineWriter, and Event types"
```

---

## Task 3: Add `ProgressWriter` to `RunRequest` and update `HostRunner`

**Files:**
- Modify: `backtest/runner.go`
- Modify: `backtest/host.go`
- Modify: `backtest/host_test.go`

- [ ] **Step 1: Add `ProgressWriter` to `RunRequest`**

In `backtest/runner.go`, add to the `RunRequest` struct:

```go
// RunRequest carries everything a Runner needs to produce one snapshot.
type RunRequest struct {
	RunID          uuid.UUID     // optional; used for container naming / log correlation
	Artifact       string        // binary path for host; image ref for docker
	ArtifactKind   ArtifactKind  // must match the runner
	Args           []string      // strategy-specific CLI flags (parameters + benchmark)
	OutPath        string        // absolute path where the snapshot must be written
	Timeout        time.Duration // 0 means use Config.Timeout default
	ProgressWriter io.Writer     // if non-nil, --json is passed and stdout is teed here
}
```

Add `"io"` to the import block.

- [ ] **Step 2: Write a failing test for HostRunner --json behavior**

In `backtest/host_test.go`, add:

```go
Describe("HostRunner ProgressWriter", func() {
	It("appends --json to args when ProgressWriter is set", func() {
		// Use the existing fake-strategy binary from testdata, or
		// write a small shell script that echoes the received args to OutPath.
		// Simplest: use a helper binary that writes its args to a file.
		// For now, verify via a mock that checks the exec.Command args.
		// Since HostRunner executes a real binary, use testdata/fake-strategy.
		Skip("requires a mock exec or integration binary — covered by TestHostRunnerPassesJsonFlag below")
	})
})
```

Actually, looking at the existing `host_test.go`, find the pattern used there for testing HostRunner. The fake strategy binary in `backtest/testdata/` (referenced in other tests) writes output based on its args. Check how existing HostRunner tests work to write a matching test.

Open `backtest/host_test.go` and add a test case that:
1. Creates a `RunRequest` with `ProgressWriter` set to a `bytes.Buffer`
2. Verifies the strategy received `--json` in its args (the fake binary can write received args to stdout)
3. Verifies the `bytes.Buffer` received the stdout output

Match the test structure already in `host_test.go`.

- [ ] **Step 3: Update `HostRunner.Run` to handle `ProgressWriter`**

In `backtest/host.go`, update the `Run` method:

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

	args := []string{"backtest", "--output", req.OutPath}
	if req.ProgressWriter != nil {
		args = append(args, "--json")
	}
	args = append(args, req.Args...)
	cmd := exec.CommandContext(timeoutCtx, req.Artifact, args...) //nolint:gosec // G204: artifact path comes from admin-controlled strategy registry

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	logW := newLogWriter("strategy-stdout")
	if req.ProgressWriter != nil {
		cmd.Stdout = io.MultiWriter(logW, req.ProgressWriter)
	} else {
		cmd.Stdout = logW
	}

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

Add `"io"` to the import block.

- [ ] **Step 4: Run tests**

```
go test ./backtest/... -v 2>&1 | tail -20
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add backtest/runner.go backtest/host.go backtest/host_test.go
git commit -m "feat(backtest): add ProgressWriter to RunRequest; HostRunner tees stdout when set"
```

---

## Task 4: Update `DockerRunner` to handle `ProgressWriter`

**Files:**
- Modify: `backtest/docker.go`
- Modify: `backtest/docker_test.go`

- [ ] **Step 1: Write a failing test for DockerRunner --json behavior**

In `backtest/docker_test.go`, find the existing test for DockerRunner (it uses a mock Docker client). Add a test case:

```go
It("appends --json to the container command when ProgressWriter is set", func() {
	// Use the existing fake Docker client pattern.
	// Capture the container config passed to ContainerCreate.
	// Assert that req.Artifact + ["backtest", "--output", ..., "--json"] matches cfg.Cmd.
	// This test should FAIL before the implementation change.
})
```

Match the existing fake client pattern in `backtest/fakeclient_test.go` and `backtest/docker_test.go`.

- [ ] **Step 2: Update `DockerRunner.Run` to pass `--json` when `ProgressWriter` is set**

In `backtest/docker.go`, change the cmdLine construction:

```go
cmdLine := []string{"backtest", "--output", req.OutPath}
if req.ProgressWriter != nil {
    cmdLine = append(cmdLine, "--json")
}
cmdLine = append(cmdLine, req.Args...)
```

- [ ] **Step 3: Update `streamContainerLogs` to accept and tee to a progress writer**

Change the function signature and body:

```go
func streamContainerLogs(r io.ReadCloser, tail *tailWriter, progressWriter io.Writer) {
	defer r.Close() //nolint:errcheck // log stream close is best-effort
	logW := newLogWriter("strategy-stdout")
	var stdoutW io.Writer = logW
	if progressWriter != nil {
		stdoutW = io.MultiWriter(logW, progressWriter)
	}
	stderr := newLogWriter("strategy-stderr")
	_, _ = stdcopy.StdCopy(stdoutW, io.MultiWriter(stderr, tail), r)
}
```

- [ ] **Step 4: Update the call site in `DockerRunner.Run`**

Find the goroutine that calls `streamContainerLogs` and pass `req.ProgressWriter`:

```go
go func() {
    defer close(done)
    streamContainerLogs(logs, &tail, req.ProgressWriter)
}()
```

- [ ] **Step 5: Run tests**

```
go test ./backtest/... -v 2>&1 | tail -20
```

Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add backtest/docker.go backtest/docker_test.go
git commit -m "feat(backtest): DockerRunner tees container stdout to ProgressWriter when set"
```

---

## Task 5: Wire `ProgressHub` into the orchestrator

**Files:**
- Modify: `backtest/run.go`
- Modify: `backtest/run_test.go`

- [ ] **Step 1: Write failing tests for hub.Complete calls**

In `backtest/run_test.go`, find the existing orchestrator tests. Add:

```go
Describe("ProgressHub integration", func() {
	var (
		hub   *backtest.ProgressHub
		runID uuid.UUID
	)

	BeforeEach(func() {
		hub = backtest.NewProgressHub()
		runID = uuid.New()
	})

	It("calls hub.Complete with 'success' when the run succeeds", func() {
		// Set up a successful fake run using the existing fake stores and runner.
		// After orchestrator.Run returns nil, subscribe to the hub and verify
		// the channel is already closed with a success terminal event.
		events, _ := hub.Subscribe(runID)
		// ... set up orch with hub, run it, then:
		Eventually(func() bool {
			evt, ok := <-events
			return ok && evt.Terminal != nil && evt.Terminal.Status == "success"
		}).Should(BeTrue())
	})

	It("calls hub.Complete with 'failed' when the run fails", func() {
		// Similar but with a failing runner.
		// After orchestrator.Run returns an error, subscribe and verify failed terminal event.
	})
})
```

Look at existing orchestrator tests in `run_test.go` for the fake store/runner pattern to use.

- [ ] **Step 2: Add `hub` field and `WithProgressHub` to the orchestrator**

In `backtest/run.go`, update the `orchestrator` struct:

```go
type orchestrator struct {
	cfg          Config
	runner       Runner
	artifactKind ArtifactKind
	ps           PortfolioStore
	rs           RunStore
	resolve      ArtifactResolver
	notifier     Notifier
	hub          *ProgressHub
}
```

Add the method after `WithNotifier`:

```go
// WithProgressHub attaches an optional ProgressHub for live progress streaming.
func (o *orchestrator) WithProgressHub(h *ProgressHub) *orchestrator {
	o.hub = h
	return o
}
```

- [ ] **Step 3: Create a `progressLineWriter` and pass it to the runner when hub is set**

In `orchestrator.Run`, after resolving the artifact and before calling `o.runner.Run`, add:

```go
var progressWriter io.Writer
if o.hub != nil {
    progressWriter = NewProgressLineWriter(o.hub, runID)
}

if err := o.runner.Run(ctx, RunRequest{
    RunID:          runID,
    Artifact:       artifact,
    ArtifactKind:   o.artifactKind,
    Args:           BuildArgs(row.Parameters, row.Benchmark, row.StartDate, row.EndDate),
    OutPath:        tmp,
    Timeout:        o.cfg.Timeout,
    ProgressWriter: progressWriter,
}); err != nil {
    return o.fail(ctx, portfolioID, runID, started, err)
}
```

Add `"io"` to the import block.

- [ ] **Step 4: Call `hub.Complete` on success**

After the `MarkReadyTx` call succeeds in `orchestrator.Run`, add:

```go
if o.hub != nil {
    o.hub.Complete(runID, "success", "")
}
```

- [ ] **Step 5: Call `hub.Complete` on failure**

In the `fail` method, after calling `o.ps.MarkFailedTx`, add:

```go
if o.hub != nil {
    o.hub.Complete(runID, "failed", msg)
}
```

Place this before the notifier call and before the return.

- [ ] **Step 6: Run tests**

```
go test ./backtest/... -v 2>&1 | tail -20
```

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add backtest/run.go backtest/run_test.go
git commit -m "feat(backtest): orchestrator publishes progress and fires hub.Complete on terminal state"
```

---

## Task 6: SSE progress endpoint

**Files:**
- Create: `portfolio/progress_handler.go`
- Create: `portfolio/progress_handler_test.go`

- [ ] **Step 1: Write a failing test**

Create `portfolio/progress_handler_test.go`:

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
	"bufio"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
	"github.com/penny-vault/pv-api/portfolio"
)

var _ = Describe("StreamRunProgress", func() {
	var (
		hub    *backtest.ProgressHub
		runID  uuid.UUID
		portID uuid.UUID
	)

	BeforeEach(func() {
		hub = backtest.NewProgressHub()
		runID = uuid.New()
		portID = uuid.New()
	})

	It("returns 501 when hub is not configured", func() {
		app := newTestApp(nil, nil, nil)
		req := httptest.NewRequest("GET", "/portfolios/test-slug/runs/"+runID.String()+"/progress", nil)
		req.Header.Set("X-Test-Sub", "user1")
		resp, err := app.Test(req, -1)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusNotImplemented))
	})

	It("returns 404 when run does not exist", func() {
		store := &fakeStoreWithRun{
			portfolio: portfolio.Portfolio{ID: portID, Slug: "test-slug", OwnerSub: "user1"},
			runErr:    portfolio.ErrNotFound,
		}
		app := newTestApp(store, hub, nil)
		req := httptest.NewRequest("GET", "/portfolios/test-slug/runs/"+runID.String()+"/progress", nil)
		req.Header.Set("X-Test-Sub", "user1")
		resp, err := app.Test(req, -1)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusNotFound))
	})

	It("immediately streams a terminal event for a completed run", func() {
		completedRun := portfolio.Run{
			ID:     runID,
			Status: "success",
		}
		store := &fakeStoreWithRun{
			portfolio: portfolio.Portfolio{ID: portID, Slug: "test-slug", OwnerSub: "user1"},
			run:       completedRun,
		}
		app := newTestApp(store, hub, nil)
		req := httptest.NewRequest("GET", "/portfolios/test-slug/runs/"+runID.String()+"/progress", nil)
		req.Header.Set("X-Test-Sub", "user1")
		resp, err := app.Test(req, 2000)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))
		Expect(resp.Header.Get("Content-Type")).To(ContainSubstring("text/event-stream"))
		scanner := bufio.NewScanner(resp.Body)
		var lines []string
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		Expect(strings.Join(lines, "\n")).To(ContainSubstring("event: done"))
	})

	It("streams live progress events then a terminal event", func() {
		activeRun := portfolio.Run{
			ID:     runID,
			Status: "running",
		}
		store := &fakeStoreWithRun{
			portfolio: portfolio.Portfolio{ID: portID, Slug: "test-slug", OwnerSub: "user1"},
			run:       activeRun,
		}
		app := newTestApp(store, hub, nil)

		// Publish events from a separate goroutine after the request starts
		go func() {
			time.Sleep(20 * time.Millisecond)
			hub.Publish(runID, backtest.ProgressMessage{Type: "progress", Step: 1, TotalSteps: 10, Pct: 10.0})
			time.Sleep(10 * time.Millisecond)
			hub.Complete(runID, "success", "")
		}()

		req := httptest.NewRequest("GET", "/portfolios/test-slug/runs/"+runID.String()+"/progress", nil)
		req.Header.Set("X-Test-Sub", "user1")
		resp, err := app.Test(req, 2000)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))

		body := new(strings.Builder)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			body.WriteString(scanner.Text() + "\n")
		}
		Expect(body.String()).To(ContainSubstring("event: progress"))
		Expect(body.String()).To(ContainSubstring("event: done"))
	})
})

// fakeStoreWithRun is a minimal Store that returns a fixed portfolio and run.
// Embed the existing fakeStore from handler_test.go (or define inline).
type fakeStoreWithRun struct {
	portfolio portfolio.Portfolio
	run       portfolio.Run
	runErr    error
	// embed a no-op base for the rest of the Store interface
	fakePortfolioStore // defined in handler_test.go or add a no-op here
}

func (f *fakeStoreWithRun) Get(_ context.Context, ownerSub, slug string) (portfolio.Portfolio, error) {
	if f.portfolio.Slug != slug || f.portfolio.OwnerSub != ownerSub {
		return portfolio.Portfolio{}, portfolio.ErrNotFound
	}
	return f.portfolio, nil
}

func (f *fakeStoreWithRun) GetRun(_ context.Context, portfolioID, runID uuid.UUID) (portfolio.Run, error) {
	if f.runErr != nil {
		return portfolio.Run{}, f.runErr
	}
	return f.run, nil
}

// newTestApp builds a minimal Fiber app with a subject-injection middleware
// and the SSE route wired to the given handler.
func newTestApp(store portfolio.Store, hub *backtest.ProgressHub, _ interface{}) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		sub := c.Get("X-Test-Sub")
		if sub != "" {
			c.Locals("sub", sub)
		}
		return c.Next()
	})
	h := portfolio.NewHandler(store, nil, nil, nil, nil, nil, strategy.EphemeralOptions{})
	if hub != nil {
		h.WithHub(hub)
	}
	app.Get("/portfolios/:slug/runs/:runId/progress", h.StreamRunProgress)
	return app
}
```

Note: The `fakePortfolioStore` referenced above must satisfy the full `portfolio.Store` interface. Look at existing fake stores in `portfolio/handler_test.go` and embed or reuse them.

- [ ] **Step 2: Run the test to confirm it fails**

```
go test ./portfolio/... -run TestPortfolioSuite/StreamRunProgress -v 2>&1 | tail -20
```

Expected: compilation error (StreamRunProgress and WithHub don't exist yet).

- [ ] **Step 3: Create `portfolio/progress_handler.go`**

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
	"bufio"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/penny-vault/pv-api/backtest"
)

// WithHub attaches a ProgressHub for the SSE progress streaming endpoint.
func (h *Handler) WithHub(hub *backtest.ProgressHub) *Handler {
	h.hub = hub
	return h
}

// StreamRunProgress implements GET /portfolios/:slug/runs/:runId/progress.
// It streams Server-Sent Events with per-step progress until the run reaches
// a terminal state, then closes the connection.
func (h *Handler) StreamRunProgress(c fiber.Ctx) error {
	if h.hub == nil {
		return writeProblem(c, fiber.StatusNotImplemented, "Not Implemented", "progress streaming not configured")
	}

	sub, err := subject(c)
	if err != nil {
		return writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
	}

	slug := string([]byte(c.Params("slug")))
	p, err := h.store.Get(c.Context(), sub, slug)
	if errors.Is(err, ErrNotFound) {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "portfolio not found: "+slug)
	}
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	runIDStr := string([]byte(c.Params("runId")))
	runID, parseErr := uuid.Parse(runIDStr)
	if parseErr != nil {
		return writeProblem(c, fiber.StatusBadRequest, "Bad Request", "invalid run ID")
	}

	run, err := h.store.GetRun(c.Context(), p.ID, runID)
	if errors.Is(err, ErrNotFound) {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "run not found")
	}
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("X-Accel-Buffering", "no")
	c.Set("Connection", "keep-alive")

	// Run is already in a terminal state — synthesize the terminal event from DB state.
	if run.Status == "success" || run.Status == "failed" {
		errMsg := ""
		if run.Error != nil {
			errMsg = *run.Error
		}
		finalStatus := run.Status
		finalErr := errMsg
		return c.SendStreamWriter(func(w *bufio.Writer) {
			writeSSETerminal(w, finalStatus, finalErr)
		})
	}

	// Active run — subscribe to the hub and stream live events.
	events, unsub := h.hub.Subscribe(runID)
	ctx := c.Context()
	return c.SendStreamWriter(func(w *bufio.Writer) {
		defer unsub()
		for {
			select {
			case evt, ok := <-events:
				if !ok {
					return
				}
				if evt.Progress != nil {
					writeSSEProgress(w, evt.Progress)
					w.Flush()
				} else if evt.Terminal != nil {
					writeSSETerminal(w, evt.Terminal.Status, evt.Terminal.Error)
					return
				}
			case <-ctx.Done():
				return
			}
		}
	})
}

type progressSSEData struct {
	Step         int64   `json:"step"`
	TotalSteps   int64   `json:"total_steps"`
	CurrentDate  string  `json:"current_date"`
	TargetDate   string  `json:"target_date"`
	Pct          float64 `json:"pct"`
	ElapsedMS    int64   `json:"elapsed_ms"`
	EtaMS        int64   `json:"eta_ms"`
	Measurements int64   `json:"measurements"`
}

func writeSSEProgress(w *bufio.Writer, msg *backtest.ProgressMessage) {
	data, _ := json.Marshal(progressSSEData{
		Step:         msg.Step,
		TotalSteps:   msg.TotalSteps,
		CurrentDate:  msg.CurrentDate,
		TargetDate:   msg.TargetDate,
		Pct:          msg.Pct,
		ElapsedMS:    msg.ElapsedMS,
		EtaMS:        msg.EtaMS,
		Measurements: msg.Measurements,
	})
	fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
}

type terminalSSEData struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func writeSSETerminal(w *bufio.Writer, status, errMsg string) {
	evtName := "done"
	if status == "failed" {
		evtName = "error"
	}
	data, _ := json.Marshal(terminalSSEData{Status: status, Error: errMsg})
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evtName, data)
	w.Flush()
}
```

Also add `hub *backtest.ProgressHub` to the `Handler` struct in `portfolio/handler.go`:

```go
type Handler struct {
	store      Store
	strategies strategy.ReadStore
	opener     SnapshotOpener
	dispatcher Dispatcher
	hub        *backtest.ProgressHub

	ephemeralBuilder strategy.BuilderFunc
	urlValidator     strategy.URLValidatorFunc
	ephemeralOpts    strategy.EphemeralOptions
}
```

Add `"github.com/penny-vault/pv-api/backtest"` to `portfolio/handler.go`'s import block.

- [ ] **Step 4: Run the tests**

```
go test ./portfolio/... -v 2>&1 | tail -30
```

Expected: all pass including the new SSE tests.

- [ ] **Step 5: Commit**

```bash
git add portfolio/handler.go portfolio/progress_handler.go portfolio/progress_handler_test.go
git commit -m "feat(portfolio): add SSE progress streaming endpoint"
```

---

## Task 7: Wire SSE route and `ProgressHub` into the server

**Files:**
- Modify: `api/portfolios.go`
- Modify: `api/server.go`
- Modify: `cmd/server.go`

- [ ] **Step 1: Add the SSE route to `api/portfolios.go`**

In `RegisterPortfolioRoutes` (stub function), add:

```go
r.Get("/portfolios/:slug/runs/:runId/progress", stubPortfolio)
```

In `RegisterPortfolioRoutesWith`, add:

```go
r.Get("/portfolios/:slug/runs/:runId/progress", h.StreamRunProgress)
```

- [ ] **Step 2: Add `ProgressHub` to `api.Config`**

In `api/server.go`, add to the `Config` struct:

```go
// Config holds HTTP-layer configuration.
type Config struct {
	Port           int
	AllowOrigins   string
	Auth           AuthConfig
	Registry       RegistryConfig
	Pool           *pgxpool.Pool
	Dispatcher     portfolio.Dispatcher
	SnapshotOpener portfolio.SnapshotOpener
	Ephemeral      EphemeralConfig
	ProgressHub    *backtest.ProgressHub // optional; enables SSE progress endpoint
}
```

Add `"github.com/penny-vault/pv-api/backtest"` to `api/server.go`'s import block.

- [ ] **Step 3: Wire the hub into the portfolio handler in `api/server.go`**

In `NewApp`, change the portfolioHandler construction:

```go
portfolioHandler := portfolio.NewHandler(
    portfolioStore, strategyStore, opener, conf.Dispatcher,
    strategy.EphemeralBuild,
    strategy.ValidateCloneURL,
    ephOpts,
).WithHub(conf.ProgressHub)
```

- [ ] **Step 4: Create and wire the hub in `cmd/server.go`**

In `cmd/server.go`, after the orchestrator and dispatcher are created (around line 342), add:

```go
hub := backtest.NewProgressHub()
orch.WithProgressHub(hub)
```

Then in the `api.Config` literal, add:

```go
ProgressHub: hub,
```

- [ ] **Step 5: Run the full test suite**

```
go test ./... 2>&1 | tail -30
```

Expected: all pass. Fix any compilation errors.

- [ ] **Step 6: Commit**

```bash
git add api/portfolios.go api/server.go cmd/server.go
git commit -m "feat(api): wire ProgressHub into server; register SSE progress route"
```

---

## Task 8: OpenAPI spec for SSE endpoint and new schemas

**Files:**
- Modify: `openapi/openapi.yaml`
- Regen: `openapi/openapi.gen.go`

- [ ] **Step 1: Add `ProgressMessage` and `RunTerminalEvent` schemas to the spec**

In `openapi/openapi.yaml`, in the `components.schemas` section, add after `RunStatus`:

```yaml
    ProgressMessage:
      type: object
      description: One progress tick emitted by the strategy binary during a backtest run.
      required:
        - step
        - total_steps
        - current_date
        - target_date
        - pct
        - elapsed_ms
        - eta_ms
        - measurements
      properties:
        step:
          type: integer
          format: int64
        total_steps:
          type: integer
          format: int64
        current_date:
          type: string
          format: date
        target_date:
          type: string
          format: date
        pct:
          type: number
          format: double
        elapsed_ms:
          type: integer
          format: int64
        eta_ms:
          type: integer
          format: int64
        measurements:
          type: integer
          format: int64

    RunTerminalEvent:
      type: object
      description: Final SSE event emitted when a backtest run reaches a terminal state.
      required:
        - status
      properties:
        status:
          type: string
          enum: [success, failed]
        error:
          type: string
          nullable: true
```

- [ ] **Step 2: Add the SSE endpoint to the spec**

In `openapi/openapi.yaml`, in the `paths` section, add after the `GET /portfolios/{slug}/runs/{runId}` entry:

```yaml
  /portfolios/{slug}/runs/{runId}/progress:
    get:
      summary: Stream backtest run progress
      description: >
        Server-Sent Events stream for a single backtest run.
        Emits `progress` events while the run is executing and a `done` or
        `error` terminal event when it finishes. Clients that connect after
        the run completes receive a single terminal event immediately.
      operationId: streamRunProgress
      tags:
        - Portfolios
      parameters:
        - name: slug
          in: path
          required: true
          schema:
            type: string
        - name: runId
          in: path
          required: true
          schema:
            type: string
            format: uuid
      responses:
        '200':
          description: SSE stream
          content:
            text/event-stream:
              schema:
                type: string
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
```

- [ ] **Step 3: Regenerate**

```
go generate ./openapi/
```

- [ ] **Step 4: Run full suite**

```
go test ./... 2>&1 | tail -20
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add openapi/openapi.yaml openapi/openapi.gen.go
git commit -m "feat(openapi): add ProgressMessage, RunTerminalEvent schemas and SSE progress endpoint"
```

---

## Final Check

- [ ] **Run the complete test suite one more time**

```
go test ./... 2>&1 | grep -E "FAIL|ok|---"
```

Expected: all packages report `ok`.

- [ ] **Verify the binary compiles**

```
go build ./... 2>&1
```

Expected: no output (clean build).
