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

// NewProgressLineWriter returns a writer that parses strategy stdout JSON lines
// and publishes progress events to hub.
func NewProgressLineWriter(hub *ProgressHub, runID uuid.UUID) *progressLineWriter {
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
