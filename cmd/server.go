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

	"github.com/docker/docker/client"
	units "github.com/docker/go-units"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/penny-vault/pvbt/tradecron"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

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
	strategyVer := ""
	if p.StrategyVer != nil {
		strategyVer = *p.StrategyVer
	}
	return backtest.PortfolioRow{
		ID:               p.ID,
		StrategyCode:     p.StrategyCode,
		StrategyVer:      strategyVer,
		StrategyCloneURL: p.StrategyCloneURL,
		Parameters:       p.Parameters,
		Benchmark:        p.Benchmark,
		Status:           string(p.Status),
		SnapshotPath:     p.SnapshotPath,
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
	serverCmd.Flags().String("strategy-ephemeral-dir", "/tmp/pvapi-strategies", "ephemeral build dir for unofficial strategies")
	serverCmd.Flags().Duration("strategy-ephemeral-install-timeout", 60*time.Second, "max time for one ephemeral clone+build")
	serverCmd.Flags().String("runner-docker-socket", "unix:///var/run/docker.sock", "Docker daemon socket URL")
	serverCmd.Flags().String("runner-docker-network", "", "Docker network for backtest containers; empty = daemon default")
	serverCmd.Flags().Float64("runner-docker-cpu-limit", 0.0, "per-container CPU limit in cores; 0 = unlimited")
	serverCmd.Flags().String("runner-docker-memory-limit", "", "per-container memory limit (e.g. 512Mi, 1Gi); empty = unlimited")
	serverCmd.Flags().Duration("runner-docker-build-timeout", 10*time.Minute, "max time for one docker image build")
	serverCmd.Flags().String("runner-docker-image-prefix", "pvapi-strategy", "prefix for strategy image tags")
	serverCmd.Flags().String("runner-docker-snapshots-host-path", "", "host path that maps to backtest.snapshots_dir when pvapi itself runs in docker; empty = snapshots_dir")
	bindPFlagsToViper(serverCmd)

	// The auto-transform in bindPFlagsToViper only handles one dash→dot
	// substitution, so runner.docker.* flags need explicit bindings.
	mustBindPFlag := func(key, flag string) {
		if err := viper.BindPFlag(key, serverCmd.Flags().Lookup(flag)); err != nil {
			panic(err)
		}
	}
	mustBindPFlag("runner.docker.socket", "runner-docker-socket")
	mustBindPFlag("runner.docker.network", "runner-docker-network")
	mustBindPFlag("runner.docker.cpu_limit", "runner-docker-cpu-limit")
	mustBindPFlag("runner.docker.memory_limit", "runner-docker-memory-limit")
	mustBindPFlag("runner.docker.build_timeout", "runner-docker-build-timeout")
	mustBindPFlag("runner.docker.image_prefix", "runner-docker-image-prefix")
	mustBindPFlag("runner.docker.snapshots_host_path", "runner-docker-snapshots-host-path")
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

		var (
			runner          backtest.Runner
			artifactKind    backtest.ArtifactKind
			resolve         backtest.ArtifactResolver
			dockerInstaller strategy.InstallerFunc
		)

		switch conf.Runner.Mode {
		case "host":
			runner = &backtest.HostRunner{}
			artifactKind = backtest.ArtifactBinary
			resolve = func(resolveCtx context.Context, cloneURL, ver string) (string, func(), error) {
				if ver != "" {
					artifact, err := strategyStore.LookupArtifact(resolveCtx, cloneURL, ver)
					if err == nil && artifact != "" {
						return artifact, func() {}, nil
					}
					if err != nil && !errors.Is(err, strategy.ErrNotFound) {
						return "", nil, err
					}
				}
				return strategy.EphemeralBuild(resolveCtx, strategy.EphemeralOptions{
					CloneURL: cloneURL,
					Ver:      ver,
					Dir:      conf.Strategy.EphemeralDir,
					Timeout:  conf.Strategy.EphemeralInstallTimeout,
				})
			}

		case "docker":
			dc, err := client.NewClientWithOpts(
				client.WithHost(conf.Runner.Docker.Socket),
				client.WithAPIVersionNegotiation(),
			)
			if err != nil {
				log.Fatal().Err(err).Msg("docker client")
			}
			var memBytes int64
			if m := conf.Runner.Docker.MemoryLimit; m != "" && m != "0" {
				b, mErr := units.RAMInBytes(m)
				if mErr != nil {
					log.Fatal().Err(mErr).Msg("parse runner.docker.memory_limit")
				}
				memBytes = b
			}
			nanoCPUs := int64(conf.Runner.Docker.CPULimit * 1e9)
			snapHost := conf.Runner.Docker.SnapshotsHostPath
			if snapHost == "" {
				snapHost = conf.Backtest.SnapshotsDir
			}
			runner = &backtest.DockerRunner{
				Client:           dc,
				Network:          conf.Runner.Docker.Network,
				NanoCPUs:         nanoCPUs,
				MemoryBytes:      memBytes,
				SnapshotsHostDir: snapHost,
				SnapshotsDir:     conf.Backtest.SnapshotsDir,
			}
			artifactKind = backtest.ArtifactImage
			resolve = func(resolveCtx context.Context, cloneURL, ver string) (string, func(), error) {
				if ver != "" {
					artifact, err := strategyStore.LookupArtifact(resolveCtx, cloneURL, ver)
					if err == nil && artifact != "" {
						return artifact, func() {}, nil
					}
					if err != nil && !errors.Is(err, strategy.ErrNotFound) {
						return "", nil, err
					}
				}
				return strategy.EphemeralImageBuild(resolveCtx, strategy.DockerEphemeralOptions{
					CloneURL:    cloneURL,
					Ver:         ver,
					Dir:         conf.Strategy.EphemeralDir,
					Timeout:     conf.Strategy.EphemeralInstallTimeout,
					Client:      dc,
					ImagePrefix: conf.Runner.Docker.ImagePrefix,
				})
			}
			dockerInstaller = func(instCtx context.Context, req strategy.InstallRequest) (*strategy.InstallResult, error) {
				return strategy.InstallDocker(instCtx, req, strategy.DockerInstallDeps{
					Client:       dc,
					ImagePrefix:  conf.Runner.Docker.ImagePrefix,
					BuildTimeout: conf.Runner.Docker.BuildTimeout,
				})
			}

		case "kubernetes":
			log.Fatal().Msg("runner.mode = kubernetes lands in plan 9")

		default:
			log.Fatal().Str("mode", conf.Runner.Mode).Msg("unknown runner.mode")
		}

		portfolioAdapter := backtestPortfolioStoreAdapter{store: portfolioStore}
		runAdapter := backtestRunStoreAdapter{store: portfolioStore.PoolRunStore}
		orch := backtest.NewRunner(btCfg, runner, artifactKind, portfolioAdapter, runAdapter, resolve)
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
				GitHubToken:     conf.GitHub.Token,
				SyncInterval:    conf.Strategy.RegistrySyncInterval,
				Concurrency:     conf.Strategy.InstallConcurrency,
				OfficialDir:     conf.Strategy.OfficialDir,
				GitHubOwner:     "penny-vault",
				RunnerMode:      conf.Runner.Mode,
				DockerInstaller: dockerInstaller,
			},
			Dispatcher:     dispatcherAdapter{bt: dispatcher},
			SnapshotOpener: snapshot.Opener{},
			Ephemeral: api.EphemeralConfig{
				Dir:     conf.Strategy.EphemeralDir,
				Timeout: conf.Strategy.EphemeralInstallTimeout,
			},
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
