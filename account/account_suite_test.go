// Copyright 2021-2025
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

package account_test

import (
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/jarcoal/httpmock"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAccount(t *testing.T) {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: GinkgoWriter})

	RegisterFailHandler(Fail)

	RegisterFailHandler(Fail)
	RunSpecs(t, "Account Suite")
}

var _ = BeforeSuite(func() {
	// block all HTTP requests
	httpmock.Activate()

	dbUser := os.Getenv("PVAPI_TEST_DB_USER")
	if dbUser == "" {
		log.Debug().Msg("defaulting dbUser to 'postgres'")
		dbUser = "postgres"
	}

	dbHost := os.Getenv("PVAPI_TEST_DB_HOST")
	if dbHost == "" {
		log.Debug().Msg("defaulting dbHost to 'localhost'")
		dbHost = "localhost"
	}

	dbPort := os.Getenv("PVAPI_TEST_DB_PORT")
	if dbPort == "" {
		dbPort = "5432"
	}

	// double check that dbPort is an integer
	_, err := strconv.Atoi(dbPort)
	Expect(err).To(BeNil())

	dbName := os.Getenv("PVAPI_TEST_DB_DBNAME")
	if dbName == "" {
		log.Debug().Msg("defaulting db name to 'pvtest'")
		dbName = "pvtest"
	}

	// set viper configuration
	viper.Set("db.url", fmt.Sprintf("postgresql://%s@%s:%s/%s", dbUser, dbHost, dbPort, dbName))
})

var _ = BeforeEach(func() {
	// remove any mocks
	httpmock.Reset()
})

var _ = AfterSuite(func() {
	httpmock.DeactivateAndReset()
})
