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

import "time"

// Config is the top-level pvapi configuration shape. New sections are added
// as later plans land (runner, scheduler, ...).
type Config struct {
	Log       logConf
	Server    serverConf
	Auth0     auth0Conf
	GitHub    githubConf
	Strategy  strategyConf
	Backtest  backtestConf
	Runner    runnerConf
	Scheduler schedulerConf
}

// serverConf holds HTTP server settings.
type serverConf struct {
	Port         int
	AllowOrigins string `mapstructure:"allow_origins"`
}

// auth0Conf configures the JWT-verification middleware.
type auth0Conf struct {
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
}

// backtestConf controls the backtest runtime.
type backtestConf struct {
	SnapshotsDir   string        `mapstructure:"snapshots_dir"`
	MaxConcurrency int           `mapstructure:"max_concurrency"`
	Timeout        time.Duration `mapstructure:"timeout"`
}

// runnerConf holds the runner execution-mode setting.
type runnerConf struct {
	Mode string `mapstructure:"mode"`
}

// schedulerConf controls the in-process scheduler that picks up due
// continuous portfolios and submits them to the backtest dispatcher.
type schedulerConf struct {
	TickInterval time.Duration `mapstructure:"tick_interval"`
	BatchSize    int           `mapstructure:"batch_size"`
	Enabled      bool          `mapstructure:"enabled"`
}

var conf Config
