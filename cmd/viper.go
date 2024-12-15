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
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

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
