package alert

import (
	"context"
	"errors"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/types"
)

// PortfolioReader is the subset of portfolio.Store the alert package needs.
type PortfolioReader interface {
	Get(ctx context.Context, ownerSub, slug string) (portfolio.Portfolio, error)
}

// EmailSummarizer sends a one-off summary email for a portfolio.
type EmailSummarizer interface {
	SendSummary(ctx context.Context, portfolioID uuid.UUID, recipient string) error
}

type AlertHandler struct {
	portfolios  PortfolioReader
	alerts      Store
	summarizer  EmailSummarizer
}

func NewAlertHandler(portfolios PortfolioReader, alerts Store) *AlertHandler {
	return &AlertHandler{portfolios: portfolios, alerts: alerts}
}

func NewAlertHandlerWithChecker(portfolios PortfolioReader, alerts Store, summarizer EmailSummarizer) *AlertHandler {
	return &AlertHandler{portfolios: portfolios, alerts: alerts, summarizer: summarizer}
}

func (h *AlertHandler) Create(c fiber.Ctx) error {
	ownerSub, p, err := h.resolvePortfolio(c)
	if err != nil {
		return err
	}
	_ = ownerSub

	var body struct {
		Frequency  string   `json:"frequency"`
		Recipients []string `json:"recipients"`
	}
	if err := sonic.Unmarshal(c.Body(), &body); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "invalid body", err.Error())
	}
	if !validFrequency(body.Frequency) {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "invalid frequency",
			"frequency must be one of: scheduled_run, daily, weekly, monthly")
	}
	if len(body.Recipients) == 0 {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "recipients required",
			"at least one recipient is required")
	}

	a, err := h.alerts.Create(c.Context(), p.ID, body.Frequency, body.Recipients)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return c.Status(fiber.StatusCreated).JSON(toView(a))
}

func (h *AlertHandler) List(c fiber.Ctx) error {
	_, p, err := h.resolvePortfolio(c)
	if err != nil {
		return err
	}
	alerts, err := h.alerts.List(c.Context(), p.ID)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	out := make([]alertView, 0, len(alerts))
	for _, a := range alerts {
		out = append(out, toView(a))
	}
	return c.JSON(out)
}

func (h *AlertHandler) Update(c fiber.Ctx) error {
	_, p, err := h.resolvePortfolio(c)
	if err != nil {
		return err
	}
	alertID, err := uuid.Parse(c.Params("alertId"))
	if err != nil {
		return writeProblem(c, fiber.StatusBadRequest, "Bad Request", "invalid alertId")
	}
	existing, err := h.alerts.Get(c.Context(), alertID)
	if errors.Is(err, ErrNotFound) {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "alert not found")
	}
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	if existing.PortfolioID != p.ID {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "alert not found")
	}

	var body struct {
		Frequency  string   `json:"frequency"`
		Recipients []string `json:"recipients"`
	}
	if err := sonic.Unmarshal(c.Body(), &body); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "invalid body", err.Error())
	}

	freq := existing.Frequency
	if body.Frequency != "" {
		if !validFrequency(body.Frequency) {
			return writeProblem(c, fiber.StatusUnprocessableEntity, "invalid frequency",
				"frequency must be one of: scheduled_run, daily, weekly, monthly")
		}
		freq = body.Frequency
	}
	recips := existing.Recipients
	if len(body.Recipients) > 0 {
		recips = body.Recipients
	}

	updated, err := h.alerts.Update(c.Context(), alertID, freq, recips)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return c.JSON(toView(updated))
}

func (h *AlertHandler) Delete(c fiber.Ctx) error {
	_, p, err := h.resolvePortfolio(c)
	if err != nil {
		return err
	}
	alertID, err := uuid.Parse(c.Params("alertId"))
	if err != nil {
		return writeProblem(c, fiber.StatusBadRequest, "Bad Request", "invalid alertId")
	}
	existing, err := h.alerts.Get(c.Context(), alertID)
	if errors.Is(err, ErrNotFound) {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "alert not found")
	}
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	if existing.PortfolioID != p.ID {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "alert not found")
	}
	if err := h.alerts.Delete(c.Context(), alertID); err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// SendSummary implements POST /portfolios/:slug/email-summary.
func (h *AlertHandler) SendSummary(c fiber.Ctx) error {
	if h.summarizer == nil {
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
	if err := sonic.Unmarshal(c.Body(), &body); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "invalid body", err.Error())
	}
	if body.Recipient == "" {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "recipient required",
			"recipient is required")
	}
	if sendErr := h.summarizer.SendSummary(c.Context(), p.ID, body.Recipient); errors.Is(sendErr, ErrEmailNotConfigured) {
		return writeProblem(c, fiber.StatusServiceUnavailable, "email not configured",
			"Mailgun API key is not set")
	} else if sendErr != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", sendErr.Error())
	}
	return c.SendStatus(fiber.StatusCreated)
}

func (h *AlertHandler) resolvePortfolio(c fiber.Ctx) (string, portfolio.Portfolio, error) {
	ownerSub, ok := c.Locals(types.AuthSubjectKey{}).(string)
	if !ok || ownerSub == "" {
		return "", portfolio.Portfolio{}, writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", "missing subject")
	}
	slug := string([]byte(c.Params("slug")))
	p, err := h.portfolios.Get(c.Context(), ownerSub, slug)
	if errors.Is(err, portfolio.ErrNotFound) {
		return "", portfolio.Portfolio{}, writeProblem(c, fiber.StatusNotFound, "Not Found", "portfolio not found: "+slug)
	}
	if err != nil {
		return "", portfolio.Portfolio{}, writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return ownerSub, p, nil
}

type alertView struct {
	ID          uuid.UUID `json:"id"`
	PortfolioID uuid.UUID `json:"portfolioId"`
	Frequency   string    `json:"frequency"`
	Recipients  []string  `json:"recipients"`
	LastSentAt  *string   `json:"lastSentAt"`
}

func toView(a Alert) alertView {
	v := alertView{
		ID:          a.ID,
		PortfolioID: a.PortfolioID,
		Frequency:   a.Frequency,
		Recipients:  a.Recipients,
	}
	if a.LastSentAt != nil {
		s := a.LastSentAt.Format("2006-01-02T15:04:05Z")
		v.LastSentAt = &s
	}
	return v
}

func validFrequency(f string) bool {
	switch f {
	case FrequencyScheduledRun, FrequencyDaily, FrequencyWeekly, FrequencyMonthly:
		return true
	}
	return false
}

func writeProblem(c fiber.Ctx, status int, title, detail string) error {
	return c.Status(status).JSON(fiber.Map{"title": title, "detail": detail})
}
