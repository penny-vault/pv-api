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
	"time"

	"github.com/penny-vault/pv-api/strategy"
)

var (
	ErrStrategyNotReady        = errors.New("strategy not installed")
	ErrStrategyVersionMismatch = errors.New("strategy version not installed")
	ErrUnknownParameter        = errors.New("unknown parameter")
	ErrMissingParameter        = errors.New("missing required parameter")
	ErrInvalidStrategyDescribe = errors.New("strategy describe JSON is malformed")
	ErrInvalidDate             = errors.New("invalid date")
	ErrEndBeforeStart          = errors.New("endDate must be on or after startDate")
)

// ValidateCreate validates and normalises an official-strategy create request.
func ValidateCreate(req CreateRequest, s strategy.Strategy) (CreateRequest, error) {
	norm := req

	if s.InstalledVer == nil || len(s.DescribeJSON) == 0 {
		return norm, fmt.Errorf("%w: %s is still installing — try again shortly", ErrStrategyNotReady, s.ShortCode)
	}
	if norm.StrategyVer != "" && norm.StrategyVer != *s.InstalledVer {
		return norm, fmt.Errorf("%w: want %s, installed is %s",
			ErrStrategyVersionMismatch, norm.StrategyVer, *s.InstalledVer)
	}
	norm.StrategyVer = *s.InstalledVer

	var d strategy.Describe
	if err := json.Unmarshal(s.DescribeJSON, &d); err != nil {
		return norm, fmt.Errorf("%w: %w", ErrInvalidStrategyDescribe, err)
	}
	if err := validateParameters(norm.Parameters, d); err != nil {
		return norm, err
	}
	if norm.Benchmark == "" {
		norm.Benchmark = d.Benchmark
	}
	if err := validateDates(norm.StartDate, norm.EndDate); err != nil {
		return norm, err
	}
	return norm, nil
}

// ValidateCreateUnofficial validates an unofficial (clone-URL) strategy create
// request. Skips the install-lifecycle checks.
func ValidateCreateUnofficial(req CreateRequest, d strategy.Describe) (CreateRequest, error) {
	norm := req
	if err := validateParameters(norm.Parameters, d); err != nil {
		return norm, err
	}
	if norm.Benchmark == "" {
		norm.Benchmark = d.Benchmark
	}
	if err := validateDates(norm.StartDate, norm.EndDate); err != nil {
		return norm, err
	}
	return norm, nil
}

// validateDates checks that endDate is not before startDate.
func validateDates(start, end *time.Time) error {
	if start != nil && end != nil && end.Before(*start) {
		return ErrEndBeforeStart
	}
	return nil
}

// validateParameters enforces that every declared parameter is present and no
// unknown parameters are supplied.
func validateParameters(params map[string]any, d strategy.Describe) error {
	declared := make(map[string]struct{}, len(d.Parameters))
	for _, p := range d.Parameters {
		declared[p.Name] = struct{}{}
	}
	for k := range params {
		if _, ok := declared[k]; !ok {
			return fmt.Errorf("%w: %s", ErrUnknownParameter, k)
		}
	}
	for _, p := range d.Parameters {
		if _, present := params[p.Name]; !present {
			return fmt.Errorf("%w: %s", ErrMissingParameter, p.Name)
		}
	}
	return nil
}
