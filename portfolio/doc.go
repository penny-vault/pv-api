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

// Package portfolio implements the pvapi 3.0 portfolio CRUD slice:
// create / list / get config / patch name / delete. Slug generation
// and create-request validation live here; derived-data endpoints
// (summary / drawdowns / metrics / holdings / measurements / runs)
// stay as 501 stubs in api/ until the backtest runner arrives in
// Plan 5.
package portfolio
