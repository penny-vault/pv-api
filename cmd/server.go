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

package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/penny-vault/pv-api/api"
	"github.com/penny-vault/pv-api/backtest"
	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/snapshot"
	"github.com/penny-vault/pv-api/sql"
	"github.com/penny-vault/pv-api/strategy"
)

// backtestPortfolioStoreAdapter adapts *portfolio.PoolStore to the
// backtest.PortfolioStore interface. Lives in cmd because it exists
// purely to bridge package boundaries that we deliberately keep
// dependency-free in each direction.
type backtestPortfolioStoreAdapter struct {
	store *portfolio.PoolStore
}

// backtestRunStoreAdapter adapts *portfolio.PoolRunStore to the
// backtest.RunStore interface. CreateRun translates the portfolio.Run
// return type to backtest.RunRow; the other methods delegate directly.
type backtestRunStoreAdapter struct {
	store *portfolio.PoolRunStore
}

func (a backtestRunStoreAdapter) CreateRun(ctx context.Context, portfolioID uuid.UUID, status string) (backtest.RunRow, error) {
	r, err := a.store.CreateRun(ctx, portfolioID, status)
	if err != nil {
		return backtest.RunRow{}, err
	}
	return backtest.RunRow{
		ID:          r.ID,
		PortfolioID: r.PortfolioID,
		Status:      r.Status,
	}, nil
}

func (a backtestRunStoreAdapter) UpdateRunRunning(ctx context.Context, runID uuid.UUID) error {
	return a.store.UpdateRunRunning(ctx, runID)
}

func (a backtestRunStoreAdapter) UpdateRunSuccess(ctx context.Context, runID uuid.UUID, snapshotPath string, durationMs int32) error {
	return a.store.UpdateRunSuccess(ctx, runID, snapshotPath, durationMs)
}

func (a backtestRunStoreAdapter) UpdateRunFailed(ctx context.Context, runID uuid.UUID, errMsg string, durationMs int32) error {
	return a.store.UpdateRunFailed(ctx, runID, errMsg, durationMs)
}

func (a backtestPortfolioStoreAdapter) GetByID(ctx context.Context, id uuid.UUID) (backtest.PortfolioRow, error) {
	p, err := a.store.GetByID(ctx, id)
	if err != nil {
		return backtest.PortfolioRow{}, err
	}
	return backtest.PortfolioRow{
		ID:           p.ID,
		StrategyCode: p.StrategyCode,
		StrategyVer:  p.StrategyVer,
		Parameters:   p.Parameters,
		Benchmark:    p.Benchmark,
		Status:       string(p.Status),
		SnapshotPath: p.SnapshotPath,
	}, nil
}

func (a backtestPortfolioStoreAdapter) SetRunning(ctx context.Context, id uuid.UUID) error {
	return a.store.SetRunning(ctx, id)
}

func (a backtestPortfolioStoreAdapter) SetReady(ctx context.Context, id uuid.UUID, path string, k backtest.SetKpis) error {
	return a.store.SetReady(ctx, id, path,
		k.CurrentValue, k.YtdReturn, k.MaxDrawdown, k.Sharpe, k.Cagr, k.InceptionDate)
}

func (a backtestPortfolioStoreAdapter) SetFailed(ctx context.Context, id uuid.UUID, errMsg string) error {
	return a.store.SetFailed(ctx, id, errMsg)
}

func init() {
	rootCmd.AddCommand(serverCmd)

	serverCmd.Flags().Int("server-port", 3000, "port to bind the HTTP server to")
	serverCmd.Flags().String("server-allow-origins", "http://localhost:9000", "single CORS origin to allow; empty disables CORS")
	serverCmd.Flags().String("auth0-jwks-url", "", "Auth0 JWKS URL for JWT verification")
	serverCmd.Flags().String("auth0-audience", "", "Auth0 API audience")
	serverCmd.Flags().String("auth0-issuer", "", "Auth0 issuer URL")
	serverCmd.Flags().String("github-token", "", "GitHub API token; empty uses unauthenticated Search")
	serverCmd.Flags().Duration("strategy-registry-sync-interval", time.Hour, "how often to poll GitHub for strategy updates")
	serverCmd.Flags().Int("strategy-install-concurrency", 2, "maximum concurrent strategy installs")
	serverCmd.Flags().String("strategy-official-dir", "/var/lib/pvapi/strategies/official", "where installed official strategy binaries live")
	serverCmd.Flags().String("strategy-github-query", "owner:penny-vault topic:pvbt-strategy", "GitHub search query for official strategies (owner filter applied client-side)")
	bindPFlagsToViper(serverCmd)
}

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Run the pvapi HTTP server",
	RunE: func(_ *cobra.Command, _ []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		pool := sql.Instance(ctx)

		// Build backtest config from viper and apply defaults.
		btCfg := backtest.Config{
			SnapshotsDir:   viper.GetString("backtest.snapshots_dir"),
			MaxConcurrency: viper.GetInt("backtest.max_concurrency"),
			Timeout:        viper.GetDuration("backtest.timeout"),
			RunnerMode:     viper.GetString("runner.mode"),
		}
		btCfg.ApplyDefaults()
		if err := btCfg.Validate(); err != nil {
			log.Fatal().Err(err).Msg("backtest config")
		}
		if err := os.MkdirAll(btCfg.SnapshotsDir, 0o750); err != nil {
			log.Fatal().Err(err).Msg("mkdir snapshots_dir")
		}

		portfolioStore := portfolio.NewPoolStore(pool)
		strategyStore := strategy.PoolStore{Pool: pool}

		resolve := func(code, ver string) (string, error) {
			s, err := strategyStore.Get(ctx, code)
			if err != nil {
				return "", err
			}
			if s.ArtifactRef == nil || *s.ArtifactRef == "" {
				return "", fmt.Errorf("strategy %s has no installed binary", code)
			}
			return *s.ArtifactRef, nil
		}

		runner := &backtest.HostRunner{}
		portfolioAdapter := backtestPortfolioStoreAdapter{store: portfolioStore}
		runAdapter := backtestRunStoreAdapter{store: portfolioStore.PoolRunStore}
		orch := backtest.NewRunner(btCfg, runner, portfolioAdapter, runAdapter, resolve)
		dispatcher := backtest.NewDispatcher(btCfg, runner, runAdapter, orch.Run)
		dispatcher.Start(ctx)

		if err := backtest.StartupSweep(ctx, btCfg.SnapshotsDir, portfolioStore); err != nil {
			log.Warn().Err(err).Msg("startup sweep")
		}

		app, err := api.NewApp(ctx, api.Config{
			Port:         conf.Server.Port,
			AllowOrigins: conf.Server.AllowOrigins,
			Auth: api.AuthConfig{
				JWKSURL:  conf.Auth0.JWKSURL,
				Audience: conf.Auth0.Audience,
				Issuer:   conf.Auth0.Issuer,
			},
			Pool: pool,
			Registry: api.RegistryConfig{
				GitHubToken:  conf.GitHub.Token,
				SyncInterval: conf.Strategy.RegistrySyncInterval,
				Concurrency:  conf.Strategy.InstallConcurrency,
				OfficialDir:  conf.Strategy.OfficialDir,
				GitHubOwner:  "penny-vault",
			},
			Dispatcher:     dispatcher,
			SnapshotOpener: snapshot.Opener{},
		})
		if err != nil {
			return fmt.Errorf("build app: %w", err)
		}

		errCh := make(chan error, 1)
		addr := fmt.Sprintf(":%d", conf.Server.Port)

		go func() {
			log.Info().Str("addr", addr).Msg("server listening")
			if err := app.Listen(addr, fiber.ListenConfig{DisableStartupMessage: true}); err != nil {
				errCh <- err
			}
			close(errCh)
		}()

		select {
		case err := <-errCh:
			if err != nil {
				return err
			}
			return nil
		case <-ctx.Done():
			log.Info().Msg("shutdown signal received")
			_ = dispatcher.Shutdown(30 * time.Second)
			if err := app.ShutdownWithContext(ctx); err != nil {
				return fmt.Errorf("fiber shutdown: %w", err)
			}
			return nil
		}
	},
}
