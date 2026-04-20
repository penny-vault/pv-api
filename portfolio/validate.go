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

	"github.com/penny-vault/pvbt/tradecron"

	"github.com/penny-vault/pv-api/strategy"
)

// Validation sentinels. Each maps to a distinct error message via
// fmt.Errorf("%w: ...") so callers get a 422 detail while retaining
// errors.Is behavior.
var (
	ErrLiveNotSupported        = errors.New("live mode unavailable")
	ErrScheduleRequired        = errors.New("schedule required for continuous mode")
	ErrScheduleForbidden       = errors.New("schedule forbidden for non-continuous mode")
	ErrInvalidSchedule         = errors.New("invalid schedule")
	ErrStrategyNotReady        = errors.New("strategy not installed")
	ErrStrategyVersionMismatch = errors.New("strategy version not installed")
	ErrUnknownParameter        = errors.New("unknown parameter")
	ErrMissingParameter        = errors.New("missing required parameter")
	ErrInvalidStrategyDescribe = errors.New("strategy describe JSON is malformed")
	ErrUnsupportedMode         = errors.New("unsupported mode")
)

// ValidateCreate runs every check from the spec's "Create-portfolio
// validation" subsection against req + the caller-supplied strategy row.
// On success, it returns a normalized CreateRequest with
// StrategyVer and Benchmark filled from the strategy's describe output
// when the request left them blank.
func ValidateCreate(req CreateRequest, s strategy.Strategy) (CreateRequest, error) {
	norm := req

	if err := validateMode(norm); err != nil {
		return norm, err
	}

	// strategy installed
	if s.InstalledVer == nil || len(s.DescribeJSON) == 0 {
		return norm, fmt.Errorf("%w: %s is still installing — try again shortly", ErrStrategyNotReady, s.ShortCode)
	}

	// strategy version matches installed
	if norm.StrategyVer != "" && norm.StrategyVer != *s.InstalledVer {
		return norm, fmt.Errorf("%w: want %s, installed is %s", ErrStrategyVersionMismatch, norm.StrategyVer, *s.InstalledVer)
	}
	norm.StrategyVer = *s.InstalledVer

	// parameters validate against describe
	var d strategy.Describe
	if err := json.Unmarshal(s.DescribeJSON, &d); err != nil {
		return norm, fmt.Errorf("%w: %w", ErrInvalidStrategyDescribe, err)
	}
	if err := validateParameters(norm.Parameters, d); err != nil {
		return norm, err
	}

	// default benchmark
	if norm.Benchmark == "" {
		norm.Benchmark = d.Benchmark
	}

	return norm, nil
}

// validateMode enforces the live / schedule rules from the spec.
func validateMode(req CreateRequest) error {
	if req.Mode == ModeLive {
		return fmt.Errorf("%w: live trading is not yet supported", ErrLiveNotSupported)
	}
	switch req.Mode {
	case ModeContinuous:
		if req.Schedule == "" {
			return fmt.Errorf("%w", ErrScheduleRequired)
		}
		if _, err := tradecron.New(req.Schedule, tradecron.RegularHours); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidSchedule, err)
		}
	case ModeOneShot:
		if req.Schedule != "" {
			return fmt.Errorf("%w", ErrScheduleForbidden)
		}
	case ModeLive:
		// handled above
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedMode, req.Mode)
	}
	return nil
}

// validateParameters enforces that every declared parameter is present and
// that no unknown parameters are supplied.
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
