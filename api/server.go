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
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/penny-vault/pv-api/strategy"
)

// Config-validation errors for startRegistrySync.
var (
	ErrRegistrySyncInterval = errors.New("RegistryConfig.SyncInterval must be > 0")
	ErrRegistryOfficialDir  = errors.New("RegistryConfig.OfficialDir must not be empty")
	ErrRegistryGitHubOwner  = errors.New("RegistryConfig.GitHubOwner must not be empty")
)

// Config holds HTTP-layer configuration.
type Config struct {
	Port         int
	AllowOrigins string
	Auth         AuthConfig
	Registry     RegistryConfig
	Pool         *pgxpool.Pool // optional: if set, real handlers mount; otherwise stubs
}

// RegistryConfig configures the strategy registry sync and its install
// coordinator.
type RegistryConfig struct {
	GitHubToken  string
	SyncInterval time.Duration
	Concurrency  int
	OfficialDir  string
	GitHubOwner  string // "penny-vault" in prod
	CacheDir     string // GitHub Search cache directory
}

// NewApp builds a Fiber v3 app with pvapi's middleware stack and routes.
// /healthz is public; every other route is mounted under the auth middleware.
// ctx controls the JWK cache and (if the pool is non-nil) the strategy
// sync goroutine. When a non-nil pool is supplied, the registry sync is
// started in the background.
func NewApp(ctx context.Context, conf Config) (*fiber.App, error) {
	app := fiber.New(fiber.Config{
		JSONEncoder: sonic.Marshal,
		JSONDecoder: sonic.Unmarshal,
	})

	app.Use(requestIDMiddleware())
	app.Use(timerMiddleware())

	if conf.AllowOrigins != "" {
		app.Use(cors.New(cors.Config{
			AllowOrigins: []string{conf.AllowOrigins},
		}))
	}

	app.Use(loggerMiddleware())

	app.Get("/healthz", Healthz)

	auth, err := NewAuthMiddleware(ctx, conf.Auth)
	if err != nil {
		return nil, fmt.Errorf("build auth middleware: %w", err)
	}
	protected := app.Group("", auth)
	RegisterPortfolioRoutes(protected)

	if conf.Pool != nil {
		store := strategy.PoolStore{Pool: conf.Pool}
		RegisterStrategyRoutesWith(protected, NewStrategyHandler(store))

		if err := startRegistrySync(ctx, store, conf.Registry); err != nil {
			return nil, fmt.Errorf("start registry sync: %w", err)
		}
	} else {
		RegisterStrategyRoutes(protected)
	}

	return app, nil
}

// startRegistrySync spins off a goroutine that runs the strategy.Syncer
// on conf.SyncInterval. Runs independently of the HTTP server; errors are
// logged but never propagated.
func startRegistrySync(ctx context.Context, store strategy.Store, conf RegistryConfig) error {
	if conf.SyncInterval <= 0 {
		return ErrRegistrySyncInterval
	}
	if conf.OfficialDir == "" {
		return ErrRegistryOfficialDir
	}
	if conf.GitHubOwner == "" {
		return ErrRegistryGitHubOwner
	}
	cacheDir := conf.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(conf.OfficialDir, ".cache")
	}

	discovery := func(ctx context.Context) ([]strategy.Listing, error) {
		return strategy.DiscoverOfficial(ctx, strategy.DiscoverOptions{
			CacheDir:    cacheDir,
			ExpectOwner: conf.GitHubOwner,
			Token:       conf.GitHubToken,
		})
	}

	syncer := strategy.NewSyncer(store, strategy.SyncerOptions{
		Discovery:   discovery,
		ResolveVer:  strategy.ResolveVerWithGit,
		Installer:   strategy.Install,
		OfficialDir: conf.OfficialDir,
		Concurrency: conf.Concurrency,
		Interval:    conf.SyncInterval,
	})
	go func() {
		_ = syncer.Run(ctx)
	}()
	return nil
}
