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
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// setViperDefaults registers package-wide viper defaults. Called once from
// root.go's init() so they are in place before any config file or env var
// override runs.
func setViperDefaults() {
	viper.SetDefault("data_dir", "/var/lib/pvapi")
	viper.SetDefault("backtest.max_concurrency", 0)
	viper.SetDefault("backtest.timeout", "15m")
	viper.SetDefault("runner.mode", "host")
	viper.SetDefault("runner.docker.socket", "unix:///var/run/docker.sock")
	viper.SetDefault("runner.docker.network", "")
	viper.SetDefault("runner.docker.cpu_limit", 0.0)
	viper.SetDefault("runner.docker.memory_limit", "")
	viper.SetDefault("runner.docker.build_timeout", 10*time.Minute)
	viper.SetDefault("runner.docker.image_prefix", "pvapi-strategy")
	viper.SetDefault("runner.docker.snapshots_host_path", "")
	viper.SetDefault("scheduler.tick_interval", "60s")
	viper.SetDefault("scheduler.batch_size", 32)
	viper.SetDefault("scheduler.enabled", true)
	viper.SetDefault("strategy.ephemeral_install_timeout", 60*time.Second)
	viper.SetDefault("strategy.stats_refresh_time", "17:00")
	viper.SetDefault("strategy.stats_start_date", "2010-01-01")
	viper.SetDefault("strategy.stats_tick_interval", "5m")
	viper.SetDefault("mailgun.domain", "")
	viper.SetDefault("mailgun.api_key", "")
	viper.SetDefault("mailgun.from_address", "Penny Vault <no-reply@mg.pennyvault.com>")
}

func bindPFlagsToViper(cmd *cobra.Command) {
	cmd.LocalFlags().VisitAll(bindPFlag)
}

func bindPFlag(flag *pflag.Flag) {
	// this transforms names like web-port-number into web.port_number for
	// the viper config
	name := strings.Replace(flag.Name, "-", ".", 1) // replace the first dash with a dot
	name = strings.ReplaceAll(name, "-", "_")       // replace all remaining dashes with underscore

	if err := viper.BindPFlag(name, flag); err != nil {
		log.Panic().
			Err(err).
			Str("flag", flag.Name).
			Str("transformed", name).
			Msg("binding pflag to viper name failed")
	}
}
