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

package strategy

import (
	"net/url"
	"strings"
)

// ImageTag returns "<prefix>/<owner>/<repo>:<ver>" for a canonical
// https://github.com/<owner>/<repo>(.git)? URL. Non-GitHub URLs (only
// reachable when the caller has set SkipURLValidation) fall through to
// "<prefix>/unknown/<slugified host+path>:<ver>".
func ImageTag(prefix, cloneURL, ver string) string {
	u, err := url.Parse(cloneURL)
	if err == nil && u.Host == "github.com" {
		path := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 2 {
			return prefix + "/" + parts[0] + "/" + parts[1] + ":" + ver
		}
	}
	slug := cloneURL
	if err == nil && u.Host != "" {
		slug = u.Host + u.Path
	}
	slug = strings.ReplaceAll(slug, "/", "-")
	slug = strings.ReplaceAll(slug, ":", "-")
	return prefix + "/unknown/" + slug + ":" + ver
}
