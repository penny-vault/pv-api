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
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "pvapi",
	Short: "Penny Vault API",
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	setViperDefaults()
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.config/pvapi.toml)")

	rootCmd.PersistentFlags().String("log-level", "info", "set logging level. one of debug, error, fatal, info, panic, trace, or warn")
	rootCmd.PersistentFlags().String("log-output", "stdout", "set log output. a filename, stdout, or stderr")
	rootCmd.PersistentFlags().Bool("log-pretty", true, "pretty print log output (default is JSON output)")
	rootCmd.PersistentFlags().Bool("log-report-caller", false, "print the filename and line number of the log statement that caused the message")

	bindPFlagsToViper(rootCmd)
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		viper.AddConfigPath("/etc/")
		viper.AddConfigPath(fmt.Sprintf("%s/.config", home))
		viper.AddConfigPath(".")
		viper.SetConfigType("toml")
		viper.SetConfigName("pvapi")
	}

	viper.SetEnvPrefix("pvapi")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		// config file is optional — env vars and flags are still honored
		log.Debug().Err(err).Msg("no config file loaded")
	}

	if err := viper.Unmarshal(&conf); err != nil {
		log.Panic().Err(err).Msg("error reading config into the config struct")
	}

	setupLogging(conf.Log)

	if file := viper.ConfigFileUsed(); file != "" {
		log.Info().Str("ConfigFile", file).Msg("loaded config file")
	}
}
