// Copyright 2021-2024
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
	"strings"

	"github.com/penny-vault/pv-api/account"
	"github.com/penny-vault/pv-api/api"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "pv-api",
	Short: "Run the pv-api HTTP service",
	Run: func(_ *cobra.Command, args []string) {
		ctx := context.Background()
		account.SetupPlaid(conf.Plaid)
		app := api.CreateFiberApp(ctx, conf.Server)

		// shutdown cleanly on interrupt
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		go func() {
			sig := <-c // block until signal is read
			fmt.Printf("Received signal: '%s'; shutting down...\n", sig.String())
			if err := app.Shutdown(); err != nil {
				log.Fatal().Err(err).Msg("fiber app shutdown failed")
			}
		}()

		// listen for connections
		err := app.Listen(fmt.Sprintf(":%d", conf.Server.Port))
		if err != nil {
			log.Fatal().Err(err).Msg("app.Listen returned an error")
		}
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.config/pvapi.toml)")

	// logging flags
	rootCmd.PersistentFlags().String("log-level", "info", "set logging level. one of debug, error, fatal, info, panic, trace, or warn")
	rootCmd.PersistentFlags().String("log-output", "stdout", "set log output. a filename, stdout, or stderr)")
	rootCmd.PersistentFlags().Bool("log-pretty", true, "pretty print log output (default is JSON output)")
	rootCmd.PersistentFlags().Bool("log-report-caller", true, "print the filename and line number of the log statement that caused the message")

	// plaid flags
	rootCmd.Flags().String("plaid-client-id", "", "private identifier issued by plaid")
	rootCmd.Flags().String("plaid-secret", "", "private key issued by plaid")
	rootCmd.Flags().String("plaid-environment", "sandbox", "plaid environment to connet to: {'sandbox', 'production'}")

	// server flags
	rootCmd.Flags().String("server-allow-origins", "http://localhost:8080, https://www.pennyvault.com, https://beta.pennyvault.com, https://pennyvault.app", "list of allowed CORS origins")
	rootCmd.Flags().String("server-jwks-url", "", "URL of JWKS used to sign tokens")
	rootCmd.Flags().Int("server-port", 3000, "port to bind HTTP server to")
	rootCmd.Flags().String("server-user-info-url", "", "URL of user info service used to retrieve the users profile")

	// bind flags to viper names
	bindPFlagsToViper(rootCmd)
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {
		// Find home directory.
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		// Search config in home directory with name ".pvapi.toml".
		viper.AddConfigPath("/etc/") // path to look for the config file in
		viper.AddConfigPath(fmt.Sprintf("%s/.config", home))
		viper.AddConfigPath(".")
		viper.SetConfigType("toml")
		viper.SetConfigName("pvapi")
	}

	viper.SetEnvPrefix("pvapi")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	viper.AutomaticEnv() // read in environment variables

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err != nil {
		log.Error().Stack().Err(err).Msg("error reading config file")
		os.Exit(1)
	}

	// unmarshal to config instance
	if err := viper.Unmarshal(&conf); err != nil {
		log.Panic().Err(err).Msg("error reading config into the config struct")
	}

	setupLogging(conf.Log)
	log.Info().Str("ConfigFile", viper.ConfigFileUsed()).Msg("loaded config file")
}
