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
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/penny-vault/pv-api/alert"
	"github.com/penny-vault/pv-api/backtest"
	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/progress"
	"github.com/penny-vault/pv-api/snapshot"
	"github.com/penny-vault/pv-api/strategy"
)

// Config-validation errors for startRegistrySync.
var (
	ErrRegistrySyncInterval = errors.New("RegistryConfig.SyncInterval must be > 0")
	ErrRegistryOfficialDir  = errors.New("RegistryConfig.OfficialDir must not be empty")
	ErrRegistryGitHubOwner  = errors.New("RegistryConfig.GitHubOwner must not be empty")
)

// EphemeralConfig holds the ephemeral-build settings forwarded to
// DescribeHandler and portfolio.Handler.
type EphemeralConfig struct {
	Dir     string
	Timeout time.Duration
}

// Config holds HTTP-layer configuration.
type Config struct {
	Port              int
	AllowOrigins      string
	Auth              AuthConfig
	Registry          RegistryConfig
	Pool              *pgxpool.Pool        // optional: if set, real handlers mount; otherwise stubs
	Dispatcher        portfolio.Dispatcher // optional: if nil, /runs POST returns 501
	SnapshotOpener    portfolio.SnapshotOpener
	ProgressHub       *progress.Hub
	AlertChecker      alert.EmailSummarizer // optional: if nil, email-summary returns 503
	UnsubscribeSecret string               // optional: HMAC secret for unsubscribe tokens
	Ephemeral         EphemeralConfig
}

// RegistryConfig configures the strategy registry sync and its install
// coordinator.
type RegistryConfig struct {
	GitHubToken     string
	SyncInterval    time.Duration
	Concurrency     int
	OfficialDir     string
	GitHubOwner     string // "penny-vault" in prod
	CacheDir        string // GitHub Search cache directory
	RunnerMode      string
	DockerInstaller strategy.InstallerFunc
	// Stats configuration
	StatsRefreshTime  string        // US Eastern "HH:MM"; default "17:00"
	StatsStartDate    time.Time     // backtest start; default 2010-01-01
	StatsTickInterval time.Duration // ticker cadence; default 5m
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
		IdleTimeout: 5 * time.Second,
	})

	app.Use(requestIDMiddleware())
	app.Use(timerMiddleware())

	if conf.AllowOrigins != "" {
		origins := strings.Split(conf.AllowOrigins, ",")
		for i, o := range origins {
			origins[i] = strings.TrimSpace(o)
		}
		app.Use(cors.New(cors.Config{
			AllowOrigins: origins,
		}))
	}

	app.Use(loggerMiddleware())

	app.Get("/healthz", Healthz)
	RegisterDocsRoutes(app)

	auth, err := NewAuthMiddleware(ctx, conf.Auth)
	if err != nil {
		return nil, fmt.Errorf("build auth middleware: %w", err)
	}
	protected := app.Group("/api/v3", auth)

	if conf.Pool != nil {
		portfolioStore := portfolio.NewPoolStore(conf.Pool)
		strategyStore := strategy.PoolStore{Pool: conf.Pool}
		opener := conf.SnapshotOpener
		if opener == nil {
			opener = snapshot.Opener{}
		}
		ephOpts := strategy.EphemeralOptions{
			Dir:     conf.Ephemeral.Dir,
			Timeout: conf.Ephemeral.Timeout,
		}
		portfolioHandler := portfolio.NewHandler(
			portfolioStore, strategyStore, opener, conf.Dispatcher,
			strategy.EphemeralBuild,
			strategy.ValidateCloneURL,
			ephOpts,
		)
		if conf.ProgressHub != nil {
			portfolioHandler.WithHub(conf.ProgressHub)
		}
		RegisterPortfolioRoutesWith(protected, portfolioHandler)
		alertStore := alert.NewPoolStore(conf.Pool)
		alertHandler := alert.NewAlertHandlerWithChecker(portfolioStore, alertStore, conf.AlertChecker, conf.UnsubscribeSecret)
		RegisterAlertRoutesWith(protected, alertHandler)
		RegisterPublicAlertRoutesWith(app, alertHandler)
		RegisterStrategyRoutesWith(protected, NewStrategyHandler(
			strategyStore,
			strategy.EphemeralBuild,
			strategy.ValidateCloneURL,
			ephOpts,
		))

		if err := startRegistrySync(ctx, strategyStore, strategyStore, conf.Registry); err != nil {
			return nil, fmt.Errorf("start registry sync: %w", err)
		}
	} else {
		RegisterPortfolioRoutes(protected)
		RegisterStrategyRoutes(protected)
	}

	return app, nil
}

// hostStatRunner adapts backtest.HostRunner to strategy.StatRunner.
type hostStatRunner struct{}

func (h hostStatRunner) Run(ctx context.Context, req strategy.StatRunRequest) error {
	return (&backtest.HostRunner{}).Run(ctx, backtest.RunRequest{
		Artifact:     req.Artifact,
		ArtifactKind: backtest.ArtifactBinary,
		Args:         req.Args,
		OutPath:      req.OutPath,
	})
}

// snapshotKpis reads KPI metrics from a snapshot file at the given path.
func snapshotKpis(ctx context.Context, path string) (strategy.StatKpis, error) {
	reader, err := snapshot.Open(path)
	if err != nil {
		return strategy.StatKpis{}, fmt.Errorf("open stats snapshot: %w", err)
	}
	defer func() { _ = reader.Close() }()
	kpis, err := reader.Kpis(ctx)
	if err != nil {
		return strategy.StatKpis{}, fmt.Errorf("read stats kpis: %w", err)
	}
	return strategy.StatKpis{
		CAGR:        kpis.Cagr,
		MaxDrawdown: kpis.MaxDrawdown,
		Sharpe:      kpis.Sharpe,
	}, nil
}

// startRegistrySync spins off a goroutine that runs the strategy.Syncer
// on conf.SyncInterval. Runs independently of the HTTP server; errors are
// logged but never propagated.
func startRegistrySync(ctx context.Context, store strategy.Store, statsStore strategy.StatsStore, conf RegistryConfig) error {
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

	statsRefresher, err := strategy.NewStatsRefresher(
		statsStore,
		hostStatRunner{},
		snapshotKpis,
		strategy.StatsRefresherConfig{
			StartDate:    conf.StatsStartDate,
			RefreshTime:  conf.StatsRefreshTime,
			TickInterval: conf.StatsTickInterval,
		},
	)
	if err != nil {
		return fmt.Errorf("building stats refresher: %w", err)
	}

	syncer := strategy.NewSyncer(store, strategy.SyncerOptions{
		Discovery:       discovery,
		ResolveVer:      strategy.ResolveVerWithGit,
		Installer:       strategy.Install,
		DockerInstaller: conf.DockerInstaller,
		RunnerMode:      conf.RunnerMode,
		OfficialDir:     conf.OfficialDir,
		Concurrency:     conf.Concurrency,
		Interval:        conf.SyncInterval,
		Stats:           statsRefresher,
	})
	go func() { _ = syncer.Run(ctx) }()
	go func() { statsRefresher.Run(ctx) }()
	return nil
}
