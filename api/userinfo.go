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

package api

import (
	"fmt"
	"io"
	"net/http"

	"github.com/penny-vault/pv-api/cache"
	"github.com/penny-vault/pv-api/pkginfo"
	"github.com/rs/zerolog/log"

	json "github.com/bytedance/sonic"
)

type UserInfo struct {
	Email             string                       `json:"email"`
	EmailVerified     bool                         `json:"email_verfied"`
	FamilyName        string                       `json:"family_name"`
	Gender            string                       `json:"gender"`
	GivenName         string                       `json:"given_name"`
	Locale            string                       `json:"locale"`
	Name              string                       `json:"name"`
	Picture           string                       `json:"picture"`
	PreferredUsername string                       `json:"preferred_username"`
	Subject           string                       `json:"sub"`
	UpdatedAt         int                          `json:"updated_at"`
	Roles             map[string]map[string]string `json:"urn:zitadel:iam:org:project:roles"`
}

// LookupUserInfo reads user info from redis or loads it from the userinfo
// endpoint
func LookupUserInfo(subject string, token string) UserInfo {
	if serverConfig.UserInfoUrl == "" {
		log.Panic().Msg("userInfoUrl not initialized. Call CreateFiberApp before calling LookupUserInfo and ensure that server.user_info_url is set in your settings file")
	}

	var userInfo UserInfo

	if userInfoJson, ok := cache.Get(subject); ok {
		err := json.Unmarshal([]byte(userInfoJson), &userInfo)
		if err != nil {
			log.Error().Err(err).Msg("problem")
		}

		return userInfo
	}

	client := &http.Client{}
	req, err := http.NewRequest("GET", serverConfig.UserInfoUrl, nil)
	if err != nil {
		log.Error().Err(err).Str("user_info_url", serverConfig.UserInfoUrl).Msg("parsing user info json failed")
	}

	req.Header.Set("authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Set("user-agent", "pvapi "+pkginfo.Version)

	resp, err := client.Do(req)
	if err != nil {
		log.Error().Err(err).Str("user_info_url", serverConfig.UserInfoUrl).Msg("requesting userinfo failed")
	}

	defer resp.Body.Close()

	bodyJson, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error().Err(err).Str("user_info_url", serverConfig.UserInfoUrl).Msg("reading user info body failed")
	}

	err = json.Unmarshal(bodyJson, &userInfo)
	if err != nil {
		log.Error().Err(err).Str("user_info_url", serverConfig.UserInfoUrl).Msg("parsing user info json failed")
	}

	cache.Set(subject, string(bodyJson))

	return userInfo
}
