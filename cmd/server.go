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
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/penny-vault/pvbt/tradecron"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/penny-vault/pv-api/api"
	"github.com/penny-vault/pv-api/backtest"
	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/scheduler"
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

func (a backtestPortfolioStoreAdapter) MarkRunningTx(ctx context.Context, portfolioID, runID uuid.UUID) error {
	return a.store.MarkRunningTx(ctx, portfolioID, runID)
}

func (a backtestPortfolioStoreAdapter) MarkReadyTx(ctx context.Context, portfolioID, runID uuid.UUID,
	snapshotPath string, currentValue, ytdReturn, maxDrawdown, sharpe, cagr float64,
	inceptionDate time.Time, durationMs int32) error {
	return a.store.MarkReadyTx(ctx, portfolioID, runID, snapshotPath,
		currentValue, ytdReturn, maxDrawdown, sharpe, cagr, inceptionDate, durationMs)
}

func (a backtestPortfolioStoreAdapter) MarkFailedTx(ctx context.Context, portfolioID, runID uuid.UUID,
	errMsg string, durationMs int32) error {
	return a.store.MarkFailedTx(ctx, portfolioID, runID, errMsg, durationMs)
}

// dispatcherAdapter wraps *backtest.Dispatcher and translates backtest.ErrQueueFull
// to portfolio.ErrQueueFull so the portfolio handler can return 503 without
// importing the backtest package.
type dispatcherAdapter struct {
	bt *backtest.Dispatcher
}

func (a dispatcherAdapter) Submit(ctx context.Context, portfolioID uuid.UUID) (uuid.UUID, error) {
	id, err := a.bt.Submit(ctx, portfolioID)
	if errors.Is(err, backtest.ErrQueueFull) {
		return uuid.Nil, portfolio.ErrQueueFull
	}
	return id, err
}

// schedulerStoreAdapter adapts *portfolio.PoolStore to scheduler.PortfolioStore,
// translating portfolio.DueContinuous → scheduler.Claim and
// scheduler.NextRunFunc → portfolio.NextRunFunc at the package seam.
type schedulerStoreAdapter struct {
	store *portfolio.PoolStore
}

func (a schedulerStoreAdapter) ClaimDueContinuous(
	ctx context.Context, before time.Time, batchSize int,
	nextRun scheduler.NextRunFunc,
) ([]scheduler.Claim, error) {
	portRun := portfolio.NextRunFunc(nextRun)
	dues, err := a.store.ClaimDueContinuous(ctx, before, batchSize, portRun)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.Claim, len(dues))
	for i, d := range dues {
		out[i] = scheduler.Claim{
			PortfolioID: d.PortfolioID,
			Schedule:    d.Schedule,
			NextRunAt:   d.NextRunAt,
		}
	}
	return out, nil
}

// schedulerDispatcherAdapter wraps *backtest.Dispatcher for the scheduler.
// ErrQueueFull is NOT translated here — scheduler.tickOnce already uses
// errors.Is against backtest.ErrQueueFull directly.
type schedulerDispatcherAdapter struct {
	bt *backtest.Dispatcher
}

func (a schedulerDispatcherAdapter) Submit(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	return a.bt.Submit(ctx, id)
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
			SnapshotsDir:   conf.Backtest.SnapshotsDir,
			MaxConcurrency: conf.Backtest.MaxConcurrency,
			Timeout:        conf.Backtest.Timeout,
			RunnerMode:     conf.Runner.Mode,
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
				return "", fmt.Errorf("%w: %s", backtest.ErrStrategyNoArtifact, code)
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

		// Initialize tradecron with no holiday data (future plan loads real
		// holidays). Required before any @monthend/@quarter* schedule is
		// evaluated anywhere in the process.
		tradecron.SetMarketHolidays(nil)
		log.Info().Msg("tradecron holidays disabled (no data loaded)")

		var schedulerDone chan struct{}
		if conf.Scheduler.Enabled {
			schedCfg := scheduler.Config{
				TickInterval: conf.Scheduler.TickInterval,
				BatchSize:    conf.Scheduler.BatchSize,
			}
			schedCfg.ApplyDefaults()
			if err := schedCfg.Validate(); err != nil {
				log.Fatal().Err(err).Msg("scheduler config")
			}
			sched := scheduler.New(schedCfg,
				schedulerStoreAdapter{store: portfolioStore},
				schedulerDispatcherAdapter{bt: dispatcher},
				scheduler.TradecronNext,
			)
			schedulerDone = make(chan struct{})
			go func() {
				defer close(schedulerDone)
				if err := sched.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
					log.Error().Err(err).Msg("scheduler exited with error")
				}
			}()
			log.Info().
				Dur("tick_interval", schedCfg.TickInterval).
				Int("batch_size", schedCfg.BatchSize).
				Msg("scheduler started")
		} else {
			log.Info().Msg("scheduler disabled")
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
			Dispatcher:     dispatcherAdapter{bt: dispatcher},
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
			if schedulerDone != nil {
				select {
				case <-schedulerDone:
				case <-time.After(5 * time.Second):
					log.Warn().Msg("scheduler drain timeout; proceeding with dispatcher shutdown")
				}
			}
			_ = dispatcher.Shutdown(30 * time.Second)
			shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer shutCancel()
			if err := app.ShutdownWithContext(shutCtx); err != nil {
				return fmt.Errorf("fiber shutdown: %w", err)
			}
			return nil
		}
	},
}
