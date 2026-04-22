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
	"path/filepath"
	"time"
)

// Config is the top-level pvapi configuration shape. New sections are added
// as later plans land (runner, scheduler, ...).
type Config struct {
	DataDir   string `mapstructure:"data_dir"`
	Log       logConf
	DB        dbConf
	Server    serverConf
	Auth      authConf
	GitHub    githubConf
	Strategy  strategyConf
	Backtest  backtestConf
	Runner    runnerConf
	Scheduler schedulerConf
	Mailgun   mailgunConf
}

// dbConf holds the PostgreSQL connection string.
type dbConf struct {
	URL string
}

// serverConf holds HTTP server settings.
type serverConf struct {
	Port         int
	AllowOrigins string `mapstructure:"allow_origins"`
}

// authConf configures the JWT-verification middleware.
type authConf struct {
	JWKSURL  string `mapstructure:"jwks_url"`
	Audience string
	Issuer   string
}

// githubConf holds optional GitHub credentials.
type githubConf struct {
	Token string
}

// strategyConf controls the registry sync and install coordinator.
type strategyConf struct {
	RegistrySyncInterval    time.Duration `mapstructure:"registry_sync_interval"`
	InstallConcurrency      int           `mapstructure:"install_concurrency"`
	OfficialDir             string        `mapstructure:"official_dir"`
	GithubQuery             string        `mapstructure:"github_query"`
	EphemeralDir            string        `mapstructure:"ephemeral_dir"`
	EphemeralInstallTimeout time.Duration `mapstructure:"ephemeral_install_timeout"`
	StatsRefreshTime        string        `mapstructure:"stats_refresh_time"`
	StatsStartDate          string        `mapstructure:"stats_start_date"`
	StatsTickInterval       time.Duration `mapstructure:"stats_tick_interval"`
}

// backtestConf controls the backtest runtime.
type backtestConf struct {
	SnapshotsDir   string        `mapstructure:"snapshots_dir"`
	MaxConcurrency int           `mapstructure:"max_concurrency"`
	Timeout        time.Duration `mapstructure:"timeout"`
}

// runnerConf holds the runner execution-mode setting.
type runnerConf struct {
	Mode   string     `mapstructure:"mode"`
	Docker dockerConf `mapstructure:"docker"`
}

// dockerConf configures DockerRunner + InstallDocker when runner.mode = "docker".
type dockerConf struct {
	Socket            string        `mapstructure:"socket"`
	Network           string        `mapstructure:"network"`
	CPULimit          float64       `mapstructure:"cpu_limit"`
	MemoryLimit       string        `mapstructure:"memory_limit"`
	BuildTimeout      time.Duration `mapstructure:"build_timeout"`
	ImagePrefix       string        `mapstructure:"image_prefix"`
	SnapshotsHostPath string        `mapstructure:"snapshots_host_path"`
}

// mailgunConf holds Mailgun credentials for outbound alert emails.
type mailgunConf struct {
	Domain      string `mapstructure:"domain"`
	APIKey      string `mapstructure:"api_key"`
	FromAddress string `mapstructure:"from_address"`
}

// schedulerConf controls the in-process scheduler that picks up due
// continuous portfolios and submits them to the backtest dispatcher.
type schedulerConf struct {
	TickInterval time.Duration `mapstructure:"tick_interval"`
	BatchSize    int           `mapstructure:"batch_size"`
	Enabled      bool          `mapstructure:"enabled"`
}

var conf Config

// applyDataDirFallbacks fills any unset data directory fields with paths
// derived from Config.DataDir.
func applyDataDirFallbacks(c *Config) {
	base := c.DataDir
	if c.Backtest.SnapshotsDir == "" {
		c.Backtest.SnapshotsDir = filepath.Join(base, "snapshots")
	}
	if c.Strategy.OfficialDir == "" {
		c.Strategy.OfficialDir = filepath.Join(base, "strategies", "official")
	}
	if c.Strategy.EphemeralDir == "" {
		c.Strategy.EphemeralDir = filepath.Join(base, "strategies", "ephemeral")
	}
}
