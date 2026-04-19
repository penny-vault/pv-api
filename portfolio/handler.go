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
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"

	"github.com/penny-vault/pv-api/strategy"
	"github.com/penny-vault/pv-api/types"
)

// ErrNoSubject is returned by subject() when the Auth0 sub is not present
// on fiber locals. In production this is unreachable because the auth
// middleware always populates it.
var ErrNoSubject = errors.New("missing authenticated subject")

// Handler serves the POST/GET/PATCH/DELETE endpoints of /portfolios and
// /portfolios/{slug}.
type Handler struct {
	store      Store
	strategies strategy.ReadStore
}

// NewHandler constructs a handler. strategies is used to validate the
// referenced strategy at create time.
func NewHandler(store Store, strategies strategy.ReadStore) *Handler {
	return &Handler{store: store, strategies: strategies}
}

// List implements GET /portfolios.
func (h *Handler) List(c fiber.Ctx) error {
	ownerSub, err := subject(c)
	if err != nil {
		return writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
	}
	rows, err := h.store.List(c.Context(), ownerSub)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	out := make([]portfolioView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toView(r))
	}
	return writeJSON(c, fiber.StatusOK, out)
}

// Get implements GET /portfolios/{slug} (config only).
func (h *Handler) Get(c fiber.Ctx) error {
	ownerSub, err := subject(c)
	if err != nil {
		return writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
	}
	slug := c.Params("slug")
	p, err := h.store.Get(c.Context(), ownerSub, slug)
	if errors.Is(err, ErrNotFound) {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "portfolio not found: "+slug)
	}
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return writeJSON(c, fiber.StatusOK, toView(p))
}

// Create implements POST /portfolios.
func (h *Handler) Create(c fiber.Ctx) error {
	ownerSub, err := subject(c)
	if err != nil {
		return writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
	}

	var body createBody
	if err := sonic.Unmarshal(c.Body(), &body); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", fmt.Sprintf("body is not valid JSON: %v", err))
	}
	req := body.toRequest()

	s, err := h.strategies.Get(c.Context(), req.StrategyCode)
	if errors.Is(err, strategy.ErrNotFound) {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unknown strategy", "no registered strategy with short_code="+req.StrategyCode)
	}
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	norm, err := ValidateCreate(req, s)
	if err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Invalid portfolio", err.Error())
	}

	var describe strategy.Describe
	if err := json.Unmarshal(s.DescribeJSON, &describe); err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", "strategy describe is malformed")
	}
	slug, err := Slug(norm, describe)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	presetName := presetMatch(norm.Parameters, describe)

	p := Portfolio{
		OwnerSub:     ownerSub,
		Slug:         slug,
		Name:         norm.Name,
		StrategyCode: norm.StrategyCode,
		StrategyVer:  norm.StrategyVer,
		Parameters:   norm.Parameters,
		PresetName:   presetName,
		Benchmark:    norm.Benchmark,
		Mode:         norm.Mode,
		Status:       StatusPending,
	}
	if norm.Schedule != "" {
		sch := norm.Schedule
		p.Schedule = &sch
	}

	if err := h.store.Insert(c.Context(), p); err != nil {
		if errors.Is(err, ErrDuplicateSlug) {
			return writeProblem(c, fiber.StatusConflict, "Conflict", "portfolio with slug "+slug+" already exists for this user")
		}
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	// Re-read so CreatedAt / UpdatedAt / ID reflect the DB row. Falls back
	// to an in-memory view if the read fails.
	stored, err := h.store.Get(c.Context(), ownerSub, slug)
	if err == nil {
		return writeJSON(c, fiber.StatusCreated, toView(stored))
	}
	return writeJSON(c, fiber.StatusCreated, toView(p))
}

// Patch implements PATCH /portfolios/{slug} (name-only).
func (h *Handler) Patch(c fiber.Ctx) error {
	ownerSub, err := subject(c)
	if err != nil {
		return writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
	}
	slug := c.Params("slug")

	// Strict decode: only `name` is allowed. Any other field is a 422.
	var raw map[string]json.RawMessage
	if err := sonic.Unmarshal(c.Body(), &raw); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", fmt.Sprintf("body is not valid JSON: %v", err))
	}
	for k := range raw {
		if k != "name" {
			return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", "only `name` may be updated; rejected field: "+k)
		}
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := sonic.Unmarshal(c.Body(), &body); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", fmt.Sprintf("body is not valid JSON: %v", err))
	}
	if body.Name == "" {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", "`name` must be non-empty")
	}

	if err := h.store.UpdateName(c.Context(), ownerSub, slug, body.Name); err != nil {
		if errors.Is(err, ErrNotFound) {
			return writeProblem(c, fiber.StatusNotFound, "Not Found", "portfolio not found: "+slug)
		}
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	p, err := h.store.Get(c.Context(), ownerSub, slug)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return writeJSON(c, fiber.StatusOK, toView(p))
}

// Delete implements DELETE /portfolios/{slug}.
func (h *Handler) Delete(c fiber.Ctx) error {
	ownerSub, err := subject(c)
	if err != nil {
		return writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
	}
	slug := c.Params("slug")

	if err := h.store.Delete(c.Context(), ownerSub, slug); err != nil {
		if errors.Is(err, ErrNotFound) {
			return writeProblem(c, fiber.StatusNotFound, "Not Found", "portfolio not found: "+slug)
		}
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// createBody mirrors the OpenAPI PortfolioCreateRequest shape. A separate
// type keeps JSON-tag details out of CreateRequest (which is the domain
// type used elsewhere).
type createBody struct {
	Name         string         `json:"name"`
	StrategyCode string         `json:"strategyCode"`
	StrategyVer  string         `json:"strategyVer,omitempty"`
	Parameters   map[string]any `json:"parameters"`
	Benchmark    string         `json:"benchmark,omitempty"`
	Mode         string         `json:"mode"`
	Schedule     string         `json:"schedule,omitempty"`
	RunNow       bool           `json:"runNow,omitempty"`
}

func (b createBody) toRequest() CreateRequest {
	return CreateRequest{
		Name:         b.Name,
		StrategyCode: b.StrategyCode,
		StrategyVer:  b.StrategyVer,
		Parameters:   b.Parameters,
		Benchmark:    b.Benchmark,
		Mode:         Mode(b.Mode),
		Schedule:     b.Schedule,
		RunNow:       b.RunNow,
	}
}

// portfolioView mirrors the OpenAPI Portfolio schema (config only).
type portfolioView struct {
	Slug         string         `json:"slug"`
	Name         string         `json:"name"`
	StrategyCode string         `json:"strategyCode"`
	StrategyVer  string         `json:"strategyVer"`
	Parameters   map[string]any `json:"parameters"`
	PresetName   *string        `json:"presetName"`
	Benchmark    string         `json:"benchmark"`
	Mode         string         `json:"mode"`
	Schedule     *string        `json:"schedule"`
	Status       string         `json:"status"`
	CreatedAt    string         `json:"createdAt"`
	UpdatedAt    string         `json:"updatedAt"`
	LastRunAt    *string        `json:"lastRunAt"`
	LastError    *string        `json:"lastError"`
}

func toView(p Portfolio) portfolioView {
	v := portfolioView{
		Slug:         p.Slug,
		Name:         p.Name,
		StrategyCode: p.StrategyCode,
		StrategyVer:  p.StrategyVer,
		Parameters:   p.Parameters,
		PresetName:   p.PresetName,
		Benchmark:    p.Benchmark,
		Mode:         string(p.Mode),
		Schedule:     p.Schedule,
		Status:       string(p.Status),
		CreatedAt:    p.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:    p.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		LastError:    p.LastError,
	}
	if p.LastRunAt != nil {
		t := p.LastRunAt.UTC().Format("2006-01-02T15:04:05Z")
		v.LastRunAt = &t
	}
	return v
}

// presetMatch returns the preset name (stored form, not kebab-cased) whose
// parameters deep-equal params, or nil.
func presetMatch(params map[string]any, d strategy.Describe) *string {
	for _, p := range d.Presets {
		if presetParametersEqual(p.Parameters, params) {
			name := p.Name
			return &name
		}
	}
	return nil
}

// subject extracts the Auth0 sub from fiber locals; returns an error if
// missing (should be unreachable in production since auth middleware
// always sets it).
func subject(c fiber.Ctx) (string, error) {
	sub, ok := c.Locals(types.AuthSubjectKey{}).(string)
	if !ok || sub == "" {
		return "", ErrNoSubject
	}
	// Return a copy so the caller-owned string is not backed by Fiber's
	// internal request buffer, which is reused across requests.
	return string([]byte(sub)), nil
}

func writeJSON(c fiber.Ctx, status int, v any) error {
	body, err := sonic.Marshal(v)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	c.Set(fiber.HeaderContentType, "application/json")
	return c.Status(status).Send(body)
}

func writeProblem(c fiber.Ctx, status int, title, detail string) error {
	type problem struct {
		Type     string `json:"type"`
		Title    string `json:"title"`
		Status   int    `json:"status"`
		Detail   string `json:"detail,omitempty"`
		Instance string `json:"instance,omitempty"`
	}
	body, _ := sonic.Marshal(problem{
		Type: "about:blank", Title: title, Status: status, Detail: detail, Instance: c.Path(),
	})
	c.Set(fiber.HeaderContentType, "application/problem+json")
	return c.Status(status).Send(body)
}
