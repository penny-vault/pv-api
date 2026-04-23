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

	"github.com/penny-vault/pv-api/progress"
)

// WithHub attaches a progress Hub for the SSE progress streaming endpoint.
func (h *Handler) WithHub(hub *progress.Hub) *Handler {
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

	// Copy params off the Fiber context before any goroutine boundary.
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

	// Run is already terminal — synthesize from DB state.
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

	// Active run — subscribe and stream live events.
	events, unsub := h.hub.Subscribe(runID)
	reqCtx := c.Context()
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
					_ = w.Flush()
				} else if evt.Terminal != nil {
					writeSSETerminal(w, evt.Terminal.Status, evt.Terminal.Error)
					return
				}
			case <-reqCtx.Done():
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

func writeSSEProgress(w *bufio.Writer, msg *progress.ProgressMessage) {
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
	_ = w.Flush()
}
