# Email Summary On Demand — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `POST /api/v3/portfolios/:slug/email-summary` that sends the portfolio summary email to a caller-supplied recipient on demand.

**Architecture:** Add `SendSummary` to the existing `alert.Checker` (which already owns all the snapshot-reading and email-sending logic), expose it through `alert.AlertHandler` via a new `EmailSummarizer` interface, thread `Checker` through `api.Config`, and register the route.

**Tech Stack:** Go 1.25, Fiber v3, Mailgun HTTP API (via existing `alert/email` package), `httpmock` for tests.

---

## File Map

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `alert/checker.go` | Add `ErrEmailNotConfigured` sentinel + `SendSummary` public method |
| Create | `alert/checker_test.go` | Unit test for `SendSummary` early-exit when no API key |
| Modify | `alert/handler.go` | Add `PortfolioReader` interface, `EmailSummarizer` interface, `checker` field, `NewAlertHandlerWithChecker`, `SendSummary` handler |
| Create | `alert/handler_test.go` | Unit tests for `SendSummary` handler (nil checker → 503, bad body → 400, success → 201) |
| Modify | `api/server.go` | Add `AlertChecker alert.EmailSummarizer` to `Config`; pass to handler in `NewApp` |
| Modify | `api/portfolios.go` | Add stub + real route registration for `POST /portfolios/:slug/email-summary` |
| Modify | `cmd/server.go` | Pass `AlertChecker: checker` to `api.Config` struct literal |

---

## Task 1: Add `SendSummary` to `alert.Checker`

**Files:**
- Modify: `alert/checker.go`
- Create: `alert/checker_test.go`

- [ ] **Step 1: Add `ErrEmailNotConfigured` and `SendSummary` to `alert/checker.go`**

  Open `alert/checker.go`. After the existing `var` block (or after the imports if there is no var block), add the sentinel error. Then add the `SendSummary` method after `NotifyRunComplete`.

  Add at the top of the file, after the `import` block:
  ```go
  // ErrEmailNotConfigured is returned by SendSummary when no Mailgun API key is set.
  var ErrEmailNotConfigured = errors.New("email not configured: no Mailgun API key")
  ```

  Add `"errors"` to the import block if it is not already present.

  Add the following method after `NotifyRunComplete`:
  ```go
  // SendSummary sends a one-off portfolio summary email to recipient.
  // Returns ErrEmailNotConfigured if no Mailgun API key is set.
  func (c *Checker) SendSummary(ctx context.Context, portfolioID uuid.UUID, recipient string) error {
  	if c.emailConfig.APIKey == "" {
  		return ErrEmailNotConfigured
  	}
  	port, err := c.loadPortfolio(ctx, portfolioID)
  	if err != nil {
  		return fmt.Errorf("send summary: load portfolio: %w", err)
  	}
  	now := time.Now().UTC()
  	payload := c.buildPayload(ctx, Alert{}, port, now, port.Status == "success")
  	htmlBody, textBody, err := email.Render(payload)
  	if err != nil {
  		return fmt.Errorf("send summary: render: %w", err)
  	}
  	subject := fmt.Sprintf("Portfolio Update: %s", port.Name)
  	if port.Status != "success" {
  		subject = fmt.Sprintf("Portfolio Error: %s", port.Name)
  	}
  	if err := email.Send(ctx, c.emailConfig, []string{recipient}, subject, htmlBody, textBody); err != nil {
  		return fmt.Errorf("send summary: send: %w", err)
  	}
  	return nil
  }
  ```

- [ ] **Step 2: Write the failing test in `alert/checker_test.go`**

  Create the file:
  ```go
  package alert

  import (
  	"context"
  	"errors"
  	"testing"

  	"github.com/google/uuid"
  	"github.com/penny-vault/pv-api/alert/email"
  )

  func TestSendSummaryNoAPIKey(t *testing.T) {
  	c := &Checker{emailConfig: email.Config{}}
  	err := c.SendSummary(context.Background(), uuid.New(), "user@example.com")
  	if !errors.Is(err, ErrEmailNotConfigured) {
  		t.Fatalf("expected ErrEmailNotConfigured, got %v", err)
  	}
  }
  ```

- [ ] **Step 3: Run the test to confirm it fails (method does not exist yet)**

  ```bash
  cd /Users/jdf/Developer/penny-vault/pv-api && go test ./alert/... -run TestSendSummaryNoAPIKey -v
  ```
  Expected: compile error — `SendSummary` undefined.

- [ ] **Step 4: Implement (step 1 already done) — run the test to confirm it passes**

  ```bash
  go test ./alert/... -run TestSendSummaryNoAPIKey -v
  ```
  Expected: `PASS`.

- [ ] **Step 5: Run the full alert package tests to check for regressions**

  ```bash
  go test ./alert/... -v
  ```
  Expected: all tests pass.

- [ ] **Step 6: Commit**

  ```bash
  git add alert/checker.go alert/checker_test.go
  git commit -m "feat(alert): add Checker.SendSummary for on-demand email"
  ```

---

## Task 2: Handler glue in `alert/handler.go` + tests

**Files:**
- Modify: `alert/handler.go`
- Create: `alert/handler_test.go`

- [ ] **Step 1: Write the failing handler test in `alert/handler_test.go`**

  Create the file:
  ```go
  package alert_test

  import (
  	"bytes"
  	"context"
  	"errors"
  	"net/http/httptest"
  	"testing"

  	"github.com/gofiber/fiber/v3"
  	"github.com/google/uuid"

  	"github.com/penny-vault/pv-api/alert"
  	"github.com/penny-vault/pv-api/portfolio"
  	"github.com/penny-vault/pv-api/types"
  )

  // stubPortfolio implements alert.PortfolioReader for tests.
  type stubPortfolio struct {
  	p   portfolio.Portfolio
  	err error
  }

  func (s stubPortfolio) Get(_ context.Context, _, _ string) (portfolio.Portfolio, error) {
  	return s.p, s.err
  }

  // stubAlertStore implements alert.Store with panics for unused methods.
  type stubAlertStore struct{}

  func (s stubAlertStore) Create(_ context.Context, _ uuid.UUID, _ string, _ []string) (alert.Alert, error) {
  	panic("unexpected call")
  }
  func (s stubAlertStore) List(_ context.Context, _ uuid.UUID) ([]alert.Alert, error) {
  	panic("unexpected call")
  }
  func (s stubAlertStore) Get(_ context.Context, _ uuid.UUID) (alert.Alert, error) {
  	panic("unexpected call")
  }
  func (s stubAlertStore) Update(_ context.Context, _ uuid.UUID, _ string, _ []string) (alert.Alert, error) {
  	panic("unexpected call")
  }
  func (s stubAlertStore) Delete(_ context.Context, _ uuid.UUID) error { panic("unexpected call") }
  func (s stubAlertStore) MarkSent(_ context.Context, _ uuid.UUID, _ interface{}, _ float64) error {
  	panic("unexpected call")
  }

  // stubSummarizer implements alert.EmailSummarizer for tests.
  type stubSummarizer struct{ err error }

  func (s stubSummarizer) SendSummary(_ context.Context, _ uuid.UUID, _ string) error { return s.err }

  // newTestApp wires h into a minimal fiber app with auth subject pre-set.
  func newTestApp(h *alert.AlertHandler) *fiber.App {
  	app := fiber.New(fiber.Config{})
  	app.Use(func(c fiber.Ctx) error {
  		c.Locals(types.AuthSubjectKey{}, "user-1")
  		return c.Next()
  	})
  	app.Post("/portfolios/:slug/email-summary", h.SendSummary)
  	return app
  }

  func TestSendSummaryNilChecker(t *testing.T) {
  	h := alert.NewAlertHandlerWithChecker(stubPortfolio{}, stubAlertStore{}, nil)
  	app := newTestApp(h)

  	req := httptest.NewRequest("POST", "/portfolios/my-port/email-summary",
  		bytes.NewBufferString(`{"recipient":"a@b.com"}`))
  	req.Header.Set("Content-Type", "application/json")
  	resp, err := app.Test(req)
  	if err != nil {
  		t.Fatal(err)
  	}
  	defer resp.Body.Close()
  	if resp.StatusCode != fiber.StatusServiceUnavailable {
  		t.Errorf("expected 503, got %d", resp.StatusCode)
  	}
  }

  func TestSendSummaryMissingRecipient(t *testing.T) {
  	port := portfolio.Portfolio{ID: uuid.New(), OwnerSub: "user-1", Slug: "my-port", Status: portfolio.StatusReady}
  	h := alert.NewAlertHandlerWithChecker(stubPortfolio{p: port}, stubAlertStore{}, stubSummarizer{})
  	app := newTestApp(h)

  	req := httptest.NewRequest("POST", "/portfolios/my-port/email-summary",
  		bytes.NewBufferString(`{}`))
  	req.Header.Set("Content-Type", "application/json")
  	resp, err := app.Test(req)
  	if err != nil {
  		t.Fatal(err)
  	}
  	defer resp.Body.Close()
  	if resp.StatusCode != fiber.StatusBadRequest {
  		t.Errorf("expected 400, got %d", resp.StatusCode)
  	}
  }

  func TestSendSummarySuccess(t *testing.T) {
  	port := portfolio.Portfolio{ID: uuid.New(), OwnerSub: "user-1", Slug: "my-port", Status: portfolio.StatusReady}
  	h := alert.NewAlertHandlerWithChecker(stubPortfolio{p: port}, stubAlertStore{}, stubSummarizer{err: nil})
  	app := newTestApp(h)

  	req := httptest.NewRequest("POST", "/portfolios/my-port/email-summary",
  		bytes.NewBufferString(`{"recipient":"a@b.com"}`))
  	req.Header.Set("Content-Type", "application/json")
  	resp, err := app.Test(req)
  	if err != nil {
  		t.Fatal(err)
  	}
  	defer resp.Body.Close()
  	if resp.StatusCode != fiber.StatusCreated {
  		t.Errorf("expected 201, got %d", resp.StatusCode)
  	}
  }

  func TestSendSummaryEmailNotConfigured(t *testing.T) {
  	port := portfolio.Portfolio{ID: uuid.New(), OwnerSub: "user-1", Slug: "my-port", Status: portfolio.StatusReady}
  	h := alert.NewAlertHandlerWithChecker(stubPortfolio{p: port}, stubAlertStore{}, stubSummarizer{err: alert.ErrEmailNotConfigured})
  	app := newTestApp(h)

  	req := httptest.NewRequest("POST", "/portfolios/my-port/email-summary",
  		bytes.NewBufferString(`{"recipient":"a@b.com"}`))
  	req.Header.Set("Content-Type", "application/json")
  	resp, err := app.Test(req)
  	if err != nil {
  		t.Fatal(err)
  	}
  	defer resp.Body.Close()
  	if resp.StatusCode != fiber.StatusServiceUnavailable {
  		t.Errorf("expected 503, got %d", resp.StatusCode)
  	}
  }
  ```

  Note: `stubAlertStore.MarkSent` takes `interface{}` for the time parameter — fix this to `time.Time` when writing the actual file (see step 2 below for the correct signature).

- [ ] **Step 2: Modify `alert/handler.go` — add interfaces, checker field, constructor, handler**

  Add to the `import` block (add `"errors"` and `"context"` if not present; `sonic` is already imported):
  ```go
  "context"
  "errors"
  
  "github.com/google/uuid"
  ```

  After the existing imports, add the two new interfaces:
  ```go
  // PortfolioReader is the subset of portfolio.Store the alert package needs.
  type PortfolioReader interface {
  	Get(ctx context.Context, ownerSub, slug string) (portfolio.Portfolio, error)
  }

  // EmailSummarizer sends a one-off summary email for a portfolio.
  type EmailSummarizer interface {
  	SendSummary(ctx context.Context, portfolioID uuid.UUID, recipient string) error
  }
  ```

  Change the `AlertHandler` struct:
  ```go
  type AlertHandler struct {
  	portfolios PortfolioReader
  	alerts     Store
  	checker    EmailSummarizer
  }
  ```

  Keep `NewAlertHandler` unchanged in signature (it already works because `portfolio.Store` satisfies `PortfolioReader`):
  ```go
  func NewAlertHandler(portfolios PortfolioReader, alerts Store) *AlertHandler {
  	return &AlertHandler{portfolios: portfolios, alerts: alerts}
  }
  ```

  Add a new constructor:
  ```go
  func NewAlertHandlerWithChecker(portfolios PortfolioReader, alerts Store, checker EmailSummarizer) *AlertHandler {
  	return &AlertHandler{portfolios: portfolios, alerts: alerts, checker: checker}
  }
  ```

  Add the `SendSummary` handler (after `Delete`):
  ```go
  // SendSummary implements POST /portfolios/:slug/email-summary.
  func (h *AlertHandler) SendSummary(c fiber.Ctx) error {
  	if h.checker == nil {
  		return writeProblem(c, fiber.StatusServiceUnavailable, "email not configured",
  			"email sending is not configured on this server")
  	}
  	_, p, err := h.resolvePortfolio(c)
  	if err != nil {
  		return err
  	}
  	var body struct {
  		Recipient string `json:"recipient"`
  	}
  	if unmarshalErr := sonic.Unmarshal(c.Body(), &body); unmarshalErr != nil || body.Recipient == "" {
  		return writeProblem(c, fiber.StatusBadRequest, "bad request", "recipient is required")
  	}
  	if sendErr := h.checker.SendSummary(c.Context(), p.ID, body.Recipient); errors.Is(sendErr, ErrEmailNotConfigured) {
  		return writeProblem(c, fiber.StatusServiceUnavailable, "email not configured",
  			"Mailgun API key is not set")
  	} else if sendErr != nil {
  		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", sendErr.Error())
  	}
  	return c.SendStatus(fiber.StatusCreated)
  }
  ```

  Also update `resolvePortfolio` so it references `PortfolioReader` rather than `portfolio.Store` — the method body is unchanged, just the field type already changed above.

- [ ] **Step 3: Fix `stubAlertStore.MarkSent` signature in the test file**

  The correct signature from `alert.Store` is:
  ```go
  func (s stubAlertStore) MarkSent(_ context.Context, _ uuid.UUID, _ time.Time, _ float64) error {
  	panic("unexpected call")
  }
  ```
  Add `"time"` to the test file imports.

- [ ] **Step 4: Run the failing tests**

  ```bash
  cd /Users/jdf/Developer/penny-vault/pv-api && go test ./alert/... -run 'TestSendSummary' -v
  ```
  Expected: compile errors (interfaces and methods not defined yet).

- [ ] **Step 5: Run the tests after implementation**

  ```bash
  go test ./alert/... -v
  ```
  Expected: all tests pass, including the four new `TestSendSummary*` tests.

- [ ] **Step 6: Commit**

  ```bash
  git add alert/handler.go alert/handler_test.go
  git commit -m "feat(alert): add EmailSummarizer interface and SendSummary handler"
  ```

---

## Task 3: Wire through the `api` layer

**Files:**
- Modify: `api/server.go`
- Modify: `api/portfolios.go`

- [ ] **Step 1: Add the stub route to `RegisterPortfolioRoutes` in `api/portfolios.go`**

  In the `RegisterPortfolioRoutes` function, add:
  ```go
  r.Post("/portfolios/:slug/email-summary", stubPortfolio)
  ```
  Place it after the existing `r.Post("/portfolios/:slug/run", stubPortfolio)` line for grouping.

- [ ] **Step 2: Add `AlertChecker` to `api.Config` in `api/server.go`**

  Add the field to the `Config` struct:
  ```go
  AlertChecker alert.EmailSummarizer // optional: if nil, email-summary returns 503
  ```
  Place it after `ProgressHub *progress.Hub`.

  `alert` is already imported in `api/server.go`.

- [ ] **Step 3: Replace `NewAlertHandler` call with `NewAlertHandlerWithChecker` in `NewApp`**

  Find (in `NewApp`):
  ```go
  alertStore := alert.NewPoolStore(conf.Pool)
  alertHandler := alert.NewAlertHandler(portfolioStore, alertStore)
  RegisterAlertRoutesWith(protected, alertHandler)
  ```

  Replace with:
  ```go
  alertStore := alert.NewPoolStore(conf.Pool)
  alertHandler := alert.NewAlertHandlerWithChecker(portfolioStore, alertStore, conf.AlertChecker)
  RegisterAlertRoutesWith(protected, alertHandler)
  ```

- [ ] **Step 4: Add the real route to `RegisterAlertRoutesWith` in `api/portfolios.go`**

  In `RegisterAlertRoutesWith`, add:
  ```go
  r.Post("/portfolios/:slug/email-summary", h.SendSummary)
  ```
  Place it after `r.Delete("/portfolios/:slug/alerts/:alertId", h.Delete)`.

- [ ] **Step 5: Add the stub entry to the `portfolios_test.go` table**

  In `api/portfolios_test.go`, find the `DescribeTable` entries and add:
  ```go
  Entry("email summary", "POST", "/portfolios/adm-standard-aq35/email-summary"),
  ```

- [ ] **Step 6: Run the api tests**

  ```bash
  cd /Users/jdf/Developer/penny-vault/pv-api && go test ./api/... -v
  ```
  Expected: all pass, including the new stub entry.

- [ ] **Step 7: Commit**

  ```bash
  git add api/server.go api/portfolios.go api/portfolios_test.go
  git commit -m "feat(api): register email-summary route and thread AlertChecker through Config"
  ```

---

## Task 4: Wire `cmd/server.go` + final build check

**Files:**
- Modify: `cmd/server.go`

- [ ] **Step 1: Pass `AlertChecker` in the `api.Config` struct literal**

  In `cmd/server.go`, find the `api.NewApp(ctx, api.Config{...})` call. Add:
  ```go
  AlertChecker: checker,
  ```
  Place it after `ProgressHub: hub,`.

  The `checker` variable is the `*alert.Checker` already constructed a few lines earlier:
  ```go
  checker := alert.NewChecker(pool, alertEmail.Config{...})
  ```

- [ ] **Step 2: Verify the project compiles cleanly**

  ```bash
  cd /Users/jdf/Developer/penny-vault/pv-api && go build ./...
  ```
  Expected: no errors.

- [ ] **Step 3: Run the full test suite**

  ```bash
  go test ./... 2>&1 | tail -30
  ```
  Expected: all packages pass (skip packages that require a live DB — those are integration tests and will show "no test files" or be tagged).

- [ ] **Step 4: Commit**

  ```bash
  git add cmd/server.go
  git commit -m "feat(cmd): pass Checker to api.Config for on-demand email summary"
  ```
