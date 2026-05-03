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
	"context"
	"encoding/json"
	"errors"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
)

// ReadStore is the subset of Store operations the handler uses.
type ReadStore interface {
	List(ctx context.Context) ([]Strategy, error)
	Get(ctx context.Context, shortCode string) (Strategy, error)
}

// Handler serves the GET /strategies endpoints.
type Handler struct {
	store ReadStore
}

// NewHandler constructs a handler backed by the given read-only store.
func NewHandler(store ReadStore) *Handler {
	return &Handler{store: store}
}

// List implements GET /strategies.
func (h *Handler) List(c fiber.Ctx) error {
	rows, err := h.store.List(c.Context())
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	out := make([]strategyView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toView(r))
	}
	body, err := sonic.Marshal(out)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	c.Set(fiber.HeaderContentType, "application/json")
	return c.Status(fiber.StatusOK).Send(body)
}

// Get implements GET /strategies/{shortCode}.
func (h *Handler) Get(c fiber.Ctx) error {
	shortCode := c.Params("shortCode")
	row, err := h.store.Get(c.Context(), shortCode)
	if errors.Is(err, ErrNotFound) {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "strategy not found: "+shortCode)
	}
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}

	body, err := sonic.Marshal(toView(row))
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	c.Set(fiber.HeaderContentType, "application/json")
	return c.Status(fiber.StatusOK).Send(body)
}

// strategyView is the JSON shape returned by the handler. Mirrors the
// OpenAPI Strategy schema. Kept in this package to avoid pulling in the
// openapi package.
type strategyView struct {
	ShortCode          string    `json:"shortCode"`
	RepoOwner          string    `json:"repoOwner"`
	RepoName           string    `json:"repoName"`
	CloneURL           string    `json:"cloneUrl,omitempty"`
	IsOfficial         bool      `json:"isOfficial"`
	OwnerSub           *string   `json:"ownerSub,omitempty"`
	Description        *string   `json:"description,omitempty"`
	Categories         []string  `json:"categories,omitempty"`
	Stars              *int      `json:"stars,omitempty"`
	InstallState       string    `json:"installState"`
	InstalledVer       *string   `json:"installedVer,omitempty"`
	LastAttemptedVer   *string   `json:"lastAttemptedVer,omitempty"`
	InstallError       *string   `json:"installError,omitempty"`
	InstalledAt        *string   `json:"installedAt,omitempty"`
	Describe           *Describe `json:"describe,omitempty"`
	CAGR               *float64  `json:"cagr,omitempty"`
	MaxDrawdown        *float64  `json:"maxDrawDown,omitempty"`
	Sharpe             *float64  `json:"sharpe,omitempty"`
	Sortino            *float64  `json:"sortino,omitempty"`
	UlcerIndex         *float64  `json:"ulcerIndex,omitempty"`
	Beta               *float64  `json:"beta,omitempty"`
	Alpha              *float64  `json:"alpha,omitempty"`
	StdDev             *float64  `json:"stdDev,omitempty"`
	TaxCostRatio       *float64  `json:"taxCostRatio,omitempty"`
	OneYearReturn      *float64  `json:"oneYearReturn,omitempty"`
	YtdReturn          *float64  `json:"ytdReturn,omitempty"`
	BenchmarkYtdReturn *float64  `json:"benchmarkYtdReturn,omitempty"`
}

func toView(s Strategy) strategyView {
	v := strategyView{
		ShortCode:          s.ShortCode,
		RepoOwner:          s.RepoOwner,
		RepoName:           s.RepoName,
		CloneURL:           s.CloneURL,
		IsOfficial:         s.IsOfficial,
		OwnerSub:           s.OwnerSub,
		Description:        s.Description,
		Categories:         s.Categories,
		Stars:              s.Stars,
		InstallState:       string(s.DeriveInstallState()),
		InstalledVer:       s.InstalledVer,
		LastAttemptedVer:   s.LastAttemptedVer,
		InstallError:       s.InstallError,
		CAGR:               s.CAGR,
		MaxDrawdown:        s.MaxDrawdown,
		Sharpe:             s.Sharpe,
		Sortino:            s.Sortino,
		UlcerIndex:         s.UlcerIndex,
		Beta:               s.Beta,
		Alpha:              s.Alpha,
		StdDev:             s.StdDev,
		TaxCostRatio:       s.TaxCostRatio,
		OneYearReturn:      s.OneYearReturn,
		YtdReturn:          s.YtdReturn,
		BenchmarkYtdReturn: s.BenchmarkYtdReturn,
	}
	if s.InstalledAt != nil {
		t := s.InstalledAt.UTC().Format("2006-01-02T15:04:05Z")
		v.InstalledAt = &t
	}
	if len(s.DescribeJSON) > 0 {
		var d Describe
		if err := json.Unmarshal(s.DescribeJSON, &d); err == nil {
			v.Describe = &d
		}
	}
	return v
}

// writeProblem emits an RFC 7807 problem+json body. Mirrors the api package's
// helper but is local to strategy/ so this package doesn't import api/.
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
