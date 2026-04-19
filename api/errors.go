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

package api

import (
	"errors"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog/log"
)

// Sentinel domain errors. Handlers return these; WriteProblem maps them
// to HTTP status codes. Domain packages export their own errors that wrap
// these sentinels so `errors.Is` matches.
var (
	ErrNotFound       = errors.New("resource not found")
	ErrConflict       = errors.New("resource conflict")
	ErrInvalidParams  = errors.New("invalid parameters")
	ErrNotImplemented = errors.New("not implemented")
)

// Problem is the RFC 7807 body pvapi emits on every error.
type Problem struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// WriteProblem maps err to a status + problem+json body and writes it to c.
// Unknown errors yield 500 with the error string logged (not exposed).
func WriteProblem(c fiber.Ctx, err error) error {
	return writeProblemWithOptionalDetail(c, err, "")
}

// WriteProblemWithDetail writes a problem+json response overriding the
// detail field with the provided text.
func WriteProblemWithDetail(c fiber.Ctx, err error, detail string) error {
	return writeProblemWithOptionalDetail(c, err, detail)
}

func writeProblemWithOptionalDetail(c fiber.Ctx, err error, override string) error {
	status, title := classify(err)

	detail := err.Error()
	if override != "" {
		detail = override
	}

	p := Problem{
		Type:     "about:blank",
		Title:    title,
		Status:   status,
		Detail:   detail,
		Instance: c.Path(),
	}

	if status == fiber.StatusInternalServerError {
		log.Error().Err(err).Str("path", c.Path()).Msg("unexpected handler error")
		p.Detail = ""
	}

	body, marshalErr := sonic.Marshal(p)
	if marshalErr != nil {
		log.Error().Err(marshalErr).Msg("problem marshal failed")
		return c.SendStatus(fiber.StatusInternalServerError)
	}

	c.Set(fiber.HeaderContentType, "application/problem+json")
	return c.Status(status).Send(body)
}

func classify(err error) (int, string) {
	switch {
	case errors.Is(err, ErrInvalidToken):
		return fiber.StatusUnauthorized, "Unauthorized"
	case errors.Is(err, ErrNotFound):
		return fiber.StatusNotFound, "Not Found"
	case errors.Is(err, ErrConflict):
		return fiber.StatusConflict, "Conflict"
	case errors.Is(err, ErrInvalidParams):
		return fiber.StatusUnprocessableEntity, "Unprocessable Entity"
	case errors.Is(err, ErrNotImplemented):
		return fiber.StatusNotImplemented, "Not Implemented"
	default:
		return fiber.StatusInternalServerError, "Internal Server Error"
	}
}
