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
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	json "github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v2"
	"github.com/jarcoal/httpmock"
	"github.com/joho/godotenv"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pv-api/account"
	"github.com/penny-vault/pv-api/api"
)

type linkTokenResponse struct {
	Token string `json:"token"`
}

// the following is a test credential that has been generated only
// for the purpose of testing thus there is no risk including it
// here
var token = "eyJhbGciOiJSUzI1NiIsImtpZCI6IlpMZUtHV2RZRHBEYXR4SV9LV2l3ckozYmlqM0U4b08yd3lKZmFmV2ozUW89IiwidHlwIjoiSldUIn0.eyJzdWIiOiJ0ZXN0LXVzZXIiLCJqdGkiOiIyMzQxNDM1IiwibmJmIjoxNzM0MjM2MzM2LCJleHAiOjMyNTAyMjM3NTM2LCJpYXQiOjE3MzQyMzYzMzYsImlzcyI6InBlbm55dmF1bHQiLCJhdWQiOiJwZW5ueXZhdWx0In0.PHfXZTe72KsKHWIKL75XFaC5ebzHpANi9t7NgNIIJVUZf9FrteLBIVK3pScrqrwzlJLTOqO52K-wrpDy02rbIEgVk2lm5zbSahUHTj8z32b033NTade4a_JHkSLwWIllrPtyMKe6BwRdcsrq3YPZjW0GgTwHRxysUqkNt33XqJGcPxOvnuYUeSYJwUdCk47EBLnu531nBIY6VtPHknVV-pBxcXlaN-wMK4SayBYI6S7ya_Jw6-gQZunKFim13ulbrxQhrKpswIrITJA5ku6IQ-QBJwvmPKXucUFF-1wgPihAMiNySo1SIZ86Ou_zyQHM-axwq76PZmD_5oPJAExDHg"

var _ = Describe("Plaid", func() {
	var (
		app         *fiber.App
		plaidConfig account.PlaidConfig
	)

	BeforeEach(func() {
		config := api.Config{
			JwksURL:     "http://testhost/jwks",
			UserInfoURL: "http://testhost/userinfo",
		}

		httpmock.RegisterNoResponder(httpmock.InitialTransport.RoundTrip)

		httpmock.RegisterResponder("GET", config.JwksURL,
			httpmock.NewStringResponder(200, `{"use":"sig","kty":"RSA","kid":"ZLeKGWdYDpDatxI_KWiwrJ3bij3E8oO2wyJfafWj3Qo=","alg":"RS256","n":"rTdFQTjoCtXQ-t02rRhOtncx7JZD7cc73ZK1lqXd4zuWkAUaStDyKDHfNzJBFdYrHZgl8lh7WY9mNrMcVbbVvWXPQvXadpv7gSxnLaH5SFcIoAZGQgAM7pDm4kR_fywdVAkaXtQ7tvudfjtxYhXQGMNbr74W_w2_mRrWbVtbmWf7OSzT1ZBZ42zZ5ejibAD-K27KYnMp9uC6aS9yX7wIO6NRswS2nkq8Bj-uD7yE7CMkM2kXHj_iu7B21tikmZ0D0FOpoJoVNlGoWCdmbzsZ44Npdl5H-QZKmy2oJx5kUsDVeVh8Ve1elCIXlbm14ti38vR7nPcUOhRDhT5TxJvbSQ","e":"AQAB"}`))

		httpmock.RegisterResponder("GET", config.UserInfoURL,
			httpmock.NewStringResponder(200, `{
    "email": "test@user.com",
    "email_verified": true,
    "family_name": "Test",
    "gender": "male",
    "given_name": "First",
    "locale": "en",
    "name": "First Test",
    "picture": "https://apic",
    "preferred_username": "test@user.com",
    "sub": "test-user",
    "updated_at": 1726977059,
    "urn:zitadel:iam:org:project:35345966944:roles": {
        "plaid": {
            "3255": "app.auth.test.com"
        }
    },
    "urn:zitadel:iam:org:project:roles": {
        "plaid": {
            "3255": "app.auth.test.com"
        }
    }
}`))

		err := godotenv.Load("../.env")
		Expect(err).To(BeNil())

		plaidConfig = account.PlaidConfig{
			ClientID:    os.Getenv("PVAPI_PLAID_CLIENT_ID"),
			Secret:      os.Getenv("PVAPI_PLAID_SECRET"),
			Environment: "sandbox",
		}

		// Make sure the following are configured - otherwise the test is guaranteed to fail
		Expect(plaidConfig.ClientID).ToNot(Equal(""), "Plaid ClientID must be set in environment as PVAPI_PLAID_CLIENT_ID")
		Expect(plaidConfig.Secret).ToNot(Equal(""), "Plaid Secret must be set in environment as PVAPI_PLAID_SECRET")

		account.SetupPlaid(plaidConfig)

		app = api.CreateFiberApp(context.Background(), config)
	})

	When("a link token is created", func() {
		It("should return the link token", func() {
			req, err := http.NewRequest("POST", "/api/v1/plaid/link", nil)
			Expect(err).To(BeNil())

			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

			res, err := app.Test(req)
			Expect(err).To(BeNil())

			Expect(res.StatusCode).To(Equal(201))

			data, err := io.ReadAll(res.Body)
			Expect(err).To(BeNil())

			var linkTokenResp linkTokenResponse
			err = json.Unmarshal(data, &linkTokenResp)
			Expect(err).To(BeNil())

			Expect(linkTokenResp).ToNot(Equal(""))
		})
	})
})
