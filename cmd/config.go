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

// Config is the top-level pvapi configuration shape. New sections are added
// as later plans land (runner, strategy, scheduler, ...).
type Config struct {
	Log    logConf
	Server serverConf
	Auth0  auth0Conf
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

var conf Config
