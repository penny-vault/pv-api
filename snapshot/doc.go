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

// Package snapshot reads per-portfolio SQLite files produced by strategy
// binaries. The per-portfolio file is the single source of truth for
// backtest output; every derived endpoint (summary, drawdowns, statistics,
// trailing-returns, holdings, performance, transactions) is served by
// opening a snapshot and calling the matching accessor on Reader.
//
// Only a read-only SQLite handle is opened — writes happen in the
// backtest package via atomic rename.
package snapshot
