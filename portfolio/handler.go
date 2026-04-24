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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pv-api/openapi"
	"github.com/penny-vault/pv-api/progress"
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
	opener     SnapshotOpener
	dispatcher Dispatcher
	hub        *progress.Hub

	ephemeralBuilder strategy.BuilderFunc
	urlValidator     strategy.URLValidatorFunc
	ephemeralOpts    strategy.EphemeralOptions
}

// Dispatcher is the subset of backtest.Dispatcher the handler needs.
type Dispatcher interface {
	Submit(ctx context.Context, portfolioID uuid.UUID) (runID uuid.UUID, err error)
}

// NewHandler constructs a handler. strategies is used to validate the
// referenced strategy at create time. builder, urlValidator, and
// ephemeralOpts support unofficial (clone-URL) strategy creation.
func NewHandler(
	store Store,
	strategies strategy.ReadStore,
	opener SnapshotOpener,
	dispatcher Dispatcher,
	builder strategy.BuilderFunc,
	urlValidator strategy.URLValidatorFunc,
	ephemeralOpts strategy.EphemeralOptions,
) *Handler {
	return &Handler{
		store:            store,
		strategies:       strategies,
		opener:           opener,
		dispatcher:       dispatcher,
		ephemeralBuilder: builder,
		urlValidator:     urlValidator,
		ephemeralOpts:    ephemeralOpts,
	}
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

// Create implements POST /portfolios. It dispatches to createOfficial or
// createUnofficial depending on which of strategyCode / strategyCloneUrl
// is set. Both set or neither set yields 422.
func (h *Handler) Create(c fiber.Ctx) error {
	ownerSub, err := subject(c)
	if err != nil {
		return writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
	}

	var body createBody
	if err := sonic.Unmarshal(c.Body(), &body); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity",
			fmt.Sprintf("body is not valid JSON: %v", err))
	}
	startDate, err := parseDate(body.StartDate)
	if err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
	}
	endDate, err := parseDate(body.EndDate)
	if err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
	}
	req := body.toRequest(startDate, endDate)

	switch {
	case req.StrategyCode != "" && req.StrategyCloneURL != "":
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity",
			"exactly one of strategyCode or strategyCloneUrl is required")
	case req.StrategyCode == "" && req.StrategyCloneURL == "":
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity",
			"one of strategyCode or strategyCloneUrl is required")
	case req.StrategyCloneURL != "":
		return h.createUnofficial(c, ownerSub, req)
	default:
		return h.createOfficial(c, ownerSub, req)
	}
}

func (h *Handler) createOfficial(c fiber.Ctx, ownerSub string, req CreateRequest) error {
	s, err := h.strategies.Get(c.Context(), req.StrategyCode)
	if errors.Is(err, strategy.ErrNotFound) {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unknown strategy",
			"no registered strategy with short_code="+req.StrategyCode)
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
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error",
			errStrategyMalformed.Error())
	}
	return h.insertAndDispatch(c, ownerSub, norm, describe, s.CloneURL,
		append([]byte(nil), s.DescribeJSON...))
}

func (h *Handler) createUnofficial(c fiber.Ctx, ownerSub string, req CreateRequest) error {
	if err := h.urlValidator(req.StrategyCloneURL); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Invalid clone URL", err.Error())
	}

	opts := h.ephemeralOpts
	opts.CloneURL = req.StrategyCloneURL

	binPath, cleanup, err := h.ephemeralBuilder(c.Context(), opts)
	if err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Build failed", err.Error())
	}
	defer cleanup()

	raw, err := strategy.RunDescribe(c.Context(), binPath)
	if err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Describe failed", err.Error())
	}
	var describe strategy.Describe
	if err := json.Unmarshal(raw, &describe); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Describe JSON malformed", err.Error())
	}

	norm, err := ValidateCreateUnofficial(req, describe)
	if err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Invalid portfolio", err.Error())
	}
	norm.StrategyCode = describe.ShortCode

	return h.insertAndDispatch(c, ownerSub, norm, describe, req.StrategyCloneURL, raw)
}

func (h *Handler) insertAndDispatch(c fiber.Ctx, ownerSub string, norm CreateRequest,
	describe strategy.Describe, cloneURL string, describeJSON []byte) error {

	p, err := h.buildPortfolio(ownerSub, norm, describe, cloneURL, describeJSON)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	if err := h.store.Insert(c.Context(), p); err != nil {
		if errors.Is(err, ErrDuplicateSlug) {
			return writeProblem(c, fiber.StatusConflict, "Conflict",
				"portfolio with slug "+p.Slug+" already exists for this user")
		}
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	// Re-read so CreatedAt / UpdatedAt / ID reflect the DB row. Falls back
	// to an in-memory view if the read fails.
	stored, err := h.store.Get(c.Context(), ownerSub, p.Slug)
	created := p
	if err == nil {
		created = stored
	}

	runID, status, pb := h.autoTriggerOrProblem(c, created)
	if status != 0 {
		// Rollback the portfolio row because we could not queue its first run.
		if delErr := h.store.Delete(c.Context(), ownerSub, created.Slug); delErr != nil {
			log.Warn().Err(delErr).Stringer("portfolio_id", created.ID).Msg("rollback delete failed")
		}
		return writeProblem(c, status, pb.title, pb.detail)
	}

	v := toView(created)
	if runID != (uuid.UUID{}) {
		s := runID.String()
		v.RunID = &s
	}
	return writeJSON(c, fiber.StatusCreated, v)
}

// buildPortfolio constructs a Portfolio value from a validated create request.
func (h *Handler) buildPortfolio(ownerSub string, norm CreateRequest, describe strategy.Describe,
	cloneURL string, describeJSON []byte) (Portfolio, error) {

	slug, err := Slug(norm, describe)
	if err != nil {
		return Portfolio{}, err
	}
	presetName := presetMatch(norm.Parameters, describe)
	p := Portfolio{
		OwnerSub:             ownerSub,
		Slug:                 slug,
		Name:                 norm.Name,
		StrategyCode:         norm.StrategyCode,
		StrategyCloneURL:     cloneURL,
		StrategyDescribeJSON: describeJSON,
		Parameters:           norm.Parameters,
		PresetName:           presetName,
		Benchmark:            norm.Benchmark,
		StartDate:            norm.StartDate,
		EndDate:              norm.EndDate,
		Status:               StatusPending,
	}
	if norm.StrategyVer != "" {
		v := norm.StrategyVer
		p.StrategyVer = &v
	}
	return p, nil
}

// autoTriggerOrProblem dispatches an immediate backtest run on creation.
// Returns the new run ID, a non-zero HTTP status + body on failure, or zero status on success.
func (h *Handler) autoTriggerOrProblem(c fiber.Ctx, created Portfolio) (uuid.UUID, int, problemBody) {
	if h.dispatcher == nil {
		return uuid.UUID{}, 0, problemBody{}
	}
	runID, err := h.dispatcher.Submit(c.Context(), created.ID)
	if err != nil {
		if errors.Is(err, ErrQueueFull) {
			return uuid.UUID{}, fiber.StatusServiceUnavailable, problemBody{
				title:  "Service Unavailable",
				detail: "backtest queue is full, try again later",
			}
		}
		return uuid.UUID{}, fiber.StatusInternalServerError, problemBody{
			title:  "Internal Server Error",
			detail: err.Error(),
		}
	}
	return runID, 0, problemBody{}
}

type problemBody struct {
	title  string
	detail string
}

// patchBody holds the fields accepted by PATCH /portfolios/{slug}.
type patchBody struct {
	Name      string `json:"name"`
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
}

// parsePatchBody validates that the request contains only allowed fields and
// returns the decoded body plus parsed date pointers.
func parsePatchBody(data []byte) (patchBody, *time.Time, *time.Time, error) {
	var raw map[string]json.RawMessage
	if err := sonic.Unmarshal(data, &raw); err != nil {
		return patchBody{}, nil, nil, fmt.Errorf("body is not valid JSON: %w", err)
	}
	allowed := map[string]bool{"name": true, "startDate": true, "endDate": true}
	for k := range raw {
		if !allowed[k] {
			return patchBody{}, nil, nil, fmt.Errorf("rejected field %q: %w", k, ErrImmutableField)
		}
	}
	var body patchBody
	if err := sonic.Unmarshal(data, &body); err != nil {
		return patchBody{}, nil, nil, fmt.Errorf("body is not valid JSON: %w", err)
	}
	startDate, err := parseDate(body.StartDate)
	if err != nil {
		return patchBody{}, nil, nil, err
	}
	endDate, err := parseDate(body.EndDate)
	if err != nil {
		return patchBody{}, nil, nil, err
	}
	if err := validateDates(startDate, endDate); err != nil {
		return patchBody{}, nil, nil, err
	}
	return body, startDate, endDate, nil
}

// applyStoreUpdate calls fn and maps ErrNotFound to a 404 problem response.
func applyStoreUpdate(c fiber.Ctx, slug string, fn func() error) error {
	if err := fn(); err != nil {
		if errors.Is(err, ErrNotFound) {
			return writeProblem(c, fiber.StatusNotFound, "Not Found", "portfolio not found: "+slug)
		}
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return nil
}

// Patch implements PATCH /portfolios/{slug}.
// Allows updating: name, startDate, endDate.
func (h *Handler) Patch(c fiber.Ctx) error {
	ownerSub, err := subject(c)
	if err != nil {
		return writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
	}
	slug := string([]byte(c.Params("slug")))

	body, startDate, endDate, err := parsePatchBody(c.Body())
	if err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
	}

	if body.Name != "" {
		if err := applyStoreUpdate(c, slug, func() error {
			return h.store.UpdateName(c.Context(), ownerSub, slug, body.Name)
		}); err != nil {
			return err
		}
	}
	if startDate != nil || endDate != nil {
		if err := applyStoreUpdate(c, slug, func() error {
			return h.store.UpdateDates(c.Context(), ownerSub, slug, startDate, endDate)
		}); err != nil {
			return err
		}
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

// createBody mirrors the OpenAPI PortfolioCreateRequest shape.
type createBody struct {
	Name             string         `json:"name"`
	StrategyCode     string         `json:"strategyCode,omitempty"`
	StrategyCloneURL string         `json:"strategyCloneUrl,omitempty"`
	StrategyVer      string         `json:"strategyVer,omitempty"`
	Parameters       map[string]any `json:"parameters"`
	Benchmark        string         `json:"benchmark,omitempty"`
	StartDate        string         `json:"startDate,omitempty"`
	EndDate          string         `json:"endDate,omitempty"`
}

func (b createBody) toRequest(startDate, endDate *time.Time) CreateRequest {
	return CreateRequest{
		Name:             b.Name,
		StrategyCode:     b.StrategyCode,
		StrategyCloneURL: b.StrategyCloneURL,
		StrategyVer:      b.StrategyVer,
		Parameters:       b.Parameters,
		Benchmark:        b.Benchmark,
		StartDate:        startDate,
		EndDate:          endDate,
	}
}

// parseDate parses an optional YYYY-MM-DD string into a *time.Time.
// Returns nil, nil for empty strings.
func parseDate(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", s, ErrInvalidDate)
	}
	return &t, nil
}

// portfolioView mirrors the OpenAPI Portfolio schema (config only).
type portfolioView struct {
	Slug             string         `json:"slug"`
	Name             string         `json:"name"`
	Status           string         `json:"status"`
	RunID            *string        `json:"runId,omitempty"`
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

// GET /portfolios/{slug}/summary
func (h *Handler) Summary(c fiber.Ctx) error {
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.Summary(c.Context())
	})
}

// readSnapshot is the shared skeleton for all derived-data endpoints.
func (h *Handler) readSnapshot(c fiber.Ctx, fn func(SnapshotReader) (any, error)) error {
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
	if p.Status != StatusReady || p.SnapshotPath == nil || *p.SnapshotPath == "" {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "no successful run")
	}
	reader, err := h.opener.Open(*p.SnapshotPath)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	defer func() { _ = reader.Close() }()
	out, err := fn(reader)
	if errors.Is(err, errNotFoundSentinel) {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "not found")
	}
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return c.JSON(out)
}

// GET /portfolios/{slug}/drawdowns
func (h *Handler) Drawdowns(c fiber.Ctx) error {
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.Drawdowns(c.Context())
	})
}

// GET /portfolios/{slug}/statistics
func (h *Handler) Statistics(c fiber.Ctx) error {
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.Statistics(c.Context())
	})
}

// GET /portfolios/{slug}/trailing-returns
func (h *Handler) TrailingReturns(c fiber.Ctx) error {
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.TrailingReturns(c.Context())
	})
}

// GET /portfolios/{slug}/holdings
func (h *Handler) Holdings(c fiber.Ctx) error {
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.CurrentHoldings(c.Context())
	})
}

// GET /portfolios/{slug}/holdings/{date}
func (h *Handler) HoldingsAsOf(c fiber.Ctx) error {
	dateStr := string([]byte(c.Params("date")))
	d, perr := time.Parse("2006-01-02", dateStr)
	if perr != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", "date must be YYYY-MM-DD")
	}
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		resp, err := r.HoldingsAsOf(c.Context(), d)
		if errors.Is(err, ErrSnapshotNotFound) {
			return nil, errNotFoundSentinel
		}
		return resp, err
	})
}

// GET /portfolios/{slug}/holdings/history
func (h *Handler) HoldingsHistory(c fiber.Ctx) error {
	from, to, perr := parseFromTo(c)
	if perr != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", perr.Error())
	}
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.HoldingsHistory(c.Context(), from, to)
	})
}

// GET /portfolios/{slug}/performance
func (h *Handler) Performance(c fiber.Ctx) error {
	from, to, perr := parseFromTo(c)
	if perr != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", perr.Error())
	}
	slug := string([]byte(c.Params("slug")))
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.Performance(c.Context(), slug, from, to)
	})
}

// GET /portfolios/{slug}/transactions
func (h *Handler) Transactions(c fiber.Ctx) error {
	from, to, perr := parseFromTo(c)
	if perr != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", perr.Error())
	}
	var filter SnapshotTxFilter
	filter.From = from
	filter.To = to
	if s := string([]byte(c.Query("type"))); s != "" {
		filter.Types = strings.Split(s, ",")
	}
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.Transactions(c.Context(), filter)
	})
}

// parseFromTo parses optional ?from= and ?to= query params as YYYY-MM-DD.
func parseFromTo(c fiber.Ctx) (*time.Time, *time.Time, error) {
	var from, to *time.Time
	if s := string([]byte(c.Query("from"))); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return nil, nil, errFromDate
		}
		from = &t
	}
	if s := string([]byte(c.Query("to"))); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return nil, nil, errToDate
		}
		to = &t
	}
	return from, to, nil
}

// GET /portfolios/{slug}/runs
func (h *Handler) ListRuns(c fiber.Ctx) error {
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
	runs, err := h.store.ListRuns(c.Context(), p.ID)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	out := make([]openapi.BacktestRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, toAPIRun(r, slug))
	}
	return c.JSON(out)
}

// GET /portfolios/{slug}/runs/{runId}
func (h *Handler) GetRun(c fiber.Ctx) error {
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
	runID, perr := uuid.Parse(runIDStr)
	if perr != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", "runId must be a uuid")
	}
	r, err := h.store.GetRun(c.Context(), p.ID, runID)
	if errors.Is(err, ErrNotFound) {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "run not found")
	}
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return c.JSON(toAPIRun(r, slug))
}

// toAPIRun converts a domain Run to the OpenAPI shape.
func toAPIRun(r Run, slug string) openapi.BacktestRun {
	out := openapi.BacktestRun{
		Id:            r.ID,
		PortfolioSlug: slug,
		Status:        openapi.RunStatus(r.Status),
	}
	if r.StartedAt != nil {
		out.StartedAt = r.StartedAt
	}
	if r.FinishedAt != nil {
		out.FinishedAt = r.FinishedAt
	}
	if r.DurationMs != nil {
		v := int(*r.DurationMs)
		out.DurationMs = &v
	}
	if r.Error != nil {
		out.Error = r.Error
	}
	return out
}

// CreateRun implements POST /portfolios/{slug}/runs. It creates a queued
// backtest run and submits it to the dispatcher.
func (h *Handler) CreateRun(c fiber.Ctx) error {
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
	if p.Status == StatusRunning {
		return writeProblem(c, fiber.StatusConflict, "Conflict", "portfolio is already running")
	}
	if h.dispatcher == nil {
		return writeProblem(c, fiber.StatusNotImplemented, "Not Implemented", "backtest dispatcher not configured")
	}
	runID, err := h.dispatcher.Submit(c.Context(), p.ID)
	if errors.Is(err, ErrQueueFull) {
		return writeProblem(c, fiber.StatusServiceUnavailable, "Service Unavailable", "backtest queue is full, try again later")
	}
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return c.Status(fiber.StatusAccepted).JSON(openapi.BacktestRun{
		Id:            runID,
		PortfolioSlug: slug,
		Status:        openapi.RunStatusQueued,
	})
}

// errNotFoundSentinel is used internally by readSnapshot to signal 404.
var errNotFoundSentinel = errors.New("not found")

// errFromDate and errToDate are returned by parseFromTo for invalid date params.
var (
	errFromDate          = errors.New("from must be YYYY-MM-DD")
	errToDate            = errors.New("to must be YYYY-MM-DD")
	errStrategyMalformed = errors.New("strategy describe is malformed")
)
