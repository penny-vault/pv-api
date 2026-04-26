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
	ErrImmutableField          = errors.New("field is not updatable; only `name`, `startDate`, `endDate`, `runRetention` may be patched")
	ErrInvalidRunRetention     = errors.New("run_retention must be >= 1")
)

// validateRunRetention returns ErrInvalidRunRetention when v is non-nil and < 1.
func validateRunRetention(v *int) error {
	if v == nil {
		return nil
	}
	if *v < 1 {
		return ErrInvalidRunRetention
	}
	return nil
}

// ValidateCreate validates and normalises an official-strategy create request.
func ValidateCreate(req CreateRequest, s strategy.Strategy) (CreateRequest, error) {
	norm := req

	if err := validateRunRetention(req.RunRetention); err != nil {
		return CreateRequest{}, err
	}

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
	if err := validateRunRetention(req.RunRetention); err != nil {
		return CreateRequest{}, err
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

// validateDates checks that endDate is not before startDate.
func validateDates(start, end *time.Time) error {
	if start != nil && end != nil && end.Before(*start) {
		return ErrEndBeforeStart
	}
	return nil
}

// ParameterRetype describes a parameter whose declared type changed between
// strategy versions.
type ParameterRetype struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// ParameterDiff is the result of comparing two strategy describes.
// A diff is "compatible" when every change can be applied without losing or
// remapping user input.
type ParameterDiff struct {
	Kept                []string          `json:"kept"`
	AddedWithDefault    []string          `json:"added_with_default"`
	AddedWithoutDefault []string          `json:"added_without_default"`
	Removed             []string          `json:"removed"`
	Retyped             []ParameterRetype `json:"retyped"`
}

// Compatible reports whether the diff can be applied automatically:
// no Removed, no Retyped, no AddedWithoutDefault.
func (d ParameterDiff) Compatible() bool {
	return len(d.Removed) == 0 && len(d.Retyped) == 0 && len(d.AddedWithoutDefault) == 0
}

// DiffParameters classifies every parameter declared on either describe.
//   - Kept: same Name and same Type on both describes.
//   - Retyped: same Name on both describes, different Type.
//   - Removed: declared on old, absent on new.
//   - AddedWithDefault / AddedWithoutDefault: absent on old, declared on new
//     (split by whether new declaration has a non-nil Default).
func DiffParameters(oldDescribe, newDescribe strategy.Describe) ParameterDiff {
	oldByName := make(map[string]strategy.DescribeParameter, len(oldDescribe.Parameters))
	for _, p := range oldDescribe.Parameters {
		oldByName[p.Name] = p
	}
	newByName := make(map[string]strategy.DescribeParameter, len(newDescribe.Parameters))
	for _, p := range newDescribe.Parameters {
		newByName[p.Name] = p
	}

	var d ParameterDiff
	for name, oldP := range oldByName {
		newP, ok := newByName[name]
		if !ok {
			d.Removed = append(d.Removed, name)
			continue
		}
		if oldP.Type != newP.Type {
			d.Retyped = append(d.Retyped, ParameterRetype{Name: name, From: oldP.Type, To: newP.Type})
			continue
		}
		d.Kept = append(d.Kept, name)
	}
	for name, newP := range newByName {
		if _, present := oldByName[name]; present {
			continue
		}
		if newP.Default != nil {
			d.AddedWithDefault = append(d.AddedWithDefault, name)
		} else {
			d.AddedWithoutDefault = append(d.AddedWithoutDefault, name)
		}
	}
	return d
}

// MatchPresetName returns the name of a preset on newDescribe whose
// parameters exactly match current, or nil if none match.
// Comparison is done via canonical-JSON normalisation so that type
// differences (e.g. int vs float64) do not cause spurious mismatches.
func MatchPresetName(current map[string]any, newDescribe strategy.Describe) *string {
	for _, preset := range newDescribe.Presets {
		if presetParametersEqual(preset.Parameters, current) {
			name := preset.Name
			return &name
		}
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
