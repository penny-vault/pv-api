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
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/rs/zerolog/log"
)

// HostRunner runs the strategy binary directly as a host process.
type HostRunner struct{}

// Run implements Runner.
func (r *HostRunner) Run(ctx context.Context, req RunRequest) error {
	timeoutCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		timeoutCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	args := append([]string{"backtest", "--output", req.OutPath}, req.Args...)
	cmd := exec.CommandContext(timeoutCtx, req.Binary, args...) //nolint:gosec // G204: binary path comes from admin-controlled strategy registry

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

// firstNBytes trims a string to at most n bytes.
func firstNBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// logWriter forwards each line written to it to zerolog at debug level.
type logWriter struct {
	scope string
	buf   bytes.Buffer
}

func newLogWriter(scope string) *logWriter { return &logWriter{scope: scope} }

func (w *logWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		idx := strings.IndexByte(w.buf.String(), '\n')
		if idx < 0 {
			break
		}
		line := w.buf.String()[:idx]
		log.Debug().Str("scope", w.scope).Msg(line)
		w.buf.Next(idx + 1)
	}
	return len(p), nil
}
