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

package strategy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
)

// RunDescribe runs `<binPath> describe --json` and returns stdout.
// Errors include the captured stderr.
func RunDescribe(ctx context.Context, binPath string) ([]byte, error) {
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, binPath, "describe", "--json")
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("describe failed: %w\n%s", err, errOut.String())
	}
	return out.Bytes(), nil
}

// BuilderFunc is the EphemeralBuild signature as a function type.
type BuilderFunc func(ctx context.Context, opts EphemeralOptions) (string, func(), error)

// URLValidatorFunc validates a clone URL.
type URLValidatorFunc func(string) error

// DescribeHandler serves GET /strategies/describe. Builder and URLValidator
// are injected so tests can substitute stubs; production wires
// strategy.EphemeralBuild and strategy.ValidateCloneURL.
type DescribeHandler struct {
	Builder       BuilderFunc
	URLValidator  URLValidatorFunc
	EphemeralOpts EphemeralOptions // only Dir and Timeout are read; CloneURL is set per request
}

// Describe implements GET /strategies/describe.
func (h *DescribeHandler) Describe(c fiber.Ctx) error {
	// Copy off Fiber's buffer — see Fiber v3 buffer-reuse gotcha.
	cloneURL := string([]byte(c.Query("cloneUrl")))

	if err := h.URLValidator(cloneURL); err != nil {
		return writeProblem(c, fiber.StatusBadRequest, "Bad Request", err.Error())
	}

	opts := h.EphemeralOpts
	opts.CloneURL = cloneURL
	opts.Ver = "" // describe always runs against HEAD

	binPath, cleanup, err := h.Builder(c.Context(), opts)
	if err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Build failed", err.Error())
	}
	defer cleanup()

	raw, err := RunDescribe(c.Context(), binPath)
	if err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Describe failed", err.Error())
	}

	var d Describe
	if err := json.Unmarshal(raw, &d); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Describe JSON malformed", err.Error())
	}

	body, err := sonic.Marshal(d)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	c.Set(fiber.HeaderContentType, "application/json")
	return c.Status(fiber.StatusOK).Send(body)
}
