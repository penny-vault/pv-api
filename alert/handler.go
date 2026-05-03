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

package alert

import (
	"context"
	"errors"
	"fmt"

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
	portfolios        PortfolioReader
	alerts            Store
	summarizer        EmailSummarizer
	unsubscribeSecret string
}

func NewAlertHandler(portfolios PortfolioReader, alerts Store) *AlertHandler {
	return &AlertHandler{portfolios: portfolios, alerts: alerts}
}

func NewAlertHandlerWithChecker(portfolios PortfolioReader, alerts Store, summarizer EmailSummarizer, unsubscribeSecret string) *AlertHandler {
	return &AlertHandler{
		portfolios:        portfolios,
		alerts:            alerts,
		summarizer:        summarizer,
		unsubscribeSecret: unsubscribeSecret,
	}
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
	// nil summarizer: handler built without email support (e.g. tests via NewAlertHandler).
	// ErrEmailNotConfigured: non-nil summarizer but no Mailgun API key set at runtime.
	// Both result in 503.
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

// Unsubscribe handles GET /api/alerts/unsubscribe?token=<token>.
// Unauthenticated — the HMAC token is the credential.
func (h *AlertHandler) Unsubscribe(c fiber.Ctx) error {
	if h.unsubscribeSecret == "" {
		return c.Status(fiber.StatusNotFound).SendString("Unsubscribe is not configured.")
	}
	token := string([]byte(c.Query("token")))
	alertID, recipient, err := VerifyUnsubscribeToken(h.unsubscribeSecret, token)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid or expired unsubscribe link.")
	}
	_, err = h.alerts.Get(c.Context(), alertID)
	if errors.Is(err, ErrNotFound) {
		return c.Status(fiber.StatusOK).Type("html").
			SendString("<html><body><p>You have been unsubscribed.</p></body></html>")
	}
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Something went wrong.")
	}
	if err := h.alerts.RemoveRecipient(c.Context(), alertID, recipient); err != nil && !errors.Is(err, ErrNotFound) {
		return c.Status(fiber.StatusInternalServerError).SendString("Something went wrong.")
	}
	html := fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>Unsubscribed</title>
<style>body{font-family:-apple-system,sans-serif;max-width:480px;margin:80px auto;padding:0 24px;color:#0f172a}
h1{color:#0ea5e9}p{color:#64748b}</style></head>
<body><h1>Penny Vault</h1>
<p>You have been unsubscribed from portfolio alerts for portfolio <strong>%s</strong>.</p>
</body></html>`, alertID)
	return c.Status(fiber.StatusOK).Type("html").SendString(html)
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
