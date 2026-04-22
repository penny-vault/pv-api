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

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolStore adapts *pgxpool.Pool to the Store interface.
type PoolStore struct {
	Pool *pgxpool.Pool
}

func (p PoolStore) List(ctx context.Context) ([]Strategy, error) {
	return List(ctx, p.Pool)
}

func (p PoolStore) Get(ctx context.Context, shortCode string) (Strategy, error) {
	return Get(ctx, p.Pool, shortCode)
}

func (p PoolStore) GetByCloneURL(ctx context.Context, cloneURL string) (Strategy, error) {
	return GetByCloneURL(ctx, p.Pool, cloneURL)
}

func (p PoolStore) Upsert(ctx context.Context, s Strategy) error {
	return Upsert(ctx, p.Pool, s)
}

func (p PoolStore) MarkSuccess(ctx context.Context, shortCode, version, kind, ref string, describe []byte) error {
	return MarkSuccess(ctx, p.Pool, shortCode, version, kind, ref, describe)
}

func (p PoolStore) MarkFailure(ctx context.Context, shortCode, version, errText string) error {
	return MarkFailure(ctx, p.Pool, shortCode, version, errText)
}

func (p PoolStore) LookupArtifact(ctx context.Context, cloneURL, ver string) (string, error) {
	return LookupArtifact(ctx, p.Pool, cloneURL, ver)
}

// StatsStore is the persistence contract for the StatsRefresher.
type StatsStore interface {
	Get(ctx context.Context, shortCode string) (Strategy, error)
	ListInstalled(ctx context.Context) ([]Strategy, error)
	UpdateStats(ctx context.Context, shortCode string, r StatsResult) error
	MarkStatsError(ctx context.Context, shortCode, errText string) error
}

func (p PoolStore) ListInstalled(ctx context.Context) ([]Strategy, error) {
	return ListInstalled(ctx, p.Pool)
}

func (p PoolStore) UpdateStats(ctx context.Context, shortCode string, r StatsResult) error {
	return UpdateStats(ctx, p.Pool, shortCode, r)
}

func (p PoolStore) MarkStatsError(ctx context.Context, shortCode, errText string) error {
	return MarkStatsError(ctx, p.Pool, shortCode, errText)
}

var _ StatsStore = PoolStore{}
var _ Store = PoolStore{}
