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
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"
)

// DefaultGoVersion is used when a strategy repo's go.mod does not declare a
// go version (or cannot be parsed).
const DefaultGoVersion = "1.24"

// ParseGoVersion reads <dir>/go.mod and returns the value of the `go`
// directive ("1.22", "1.24"), or "" if go.mod is missing / malformed.
// ParseGoVersion never returns a non-nil error — parsing failures degrade
// to the empty-string result so callers can fall back to DefaultGoVersion.
func ParseGoVersion(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod")) //nolint:gosec // dir is an internal path
	if err != nil {
		return "", nil
	}
	f, err := modfile.ParseLax("go.mod", data, nil)
	if err != nil || f.Go == nil {
		return "", nil
	}
	return f.Go.Version, nil
}

// RenderDockerfile returns a two-stage Dockerfile that builds the strategy
// with golang:<goVer>-alpine and copies the resulting binary into
// distroless/static-debian12:nonroot. Empty goVer falls back to
// DefaultGoVersion.
func RenderDockerfile(goVer string) []byte {
	if goVer == "" {
		goVer = DefaultGoVersion
	}
	return []byte(fmt.Sprintf(`# syntax=docker/dockerfile:1.7
FROM golang:%s-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/strategy .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/strategy /strategy
USER nonroot:nonroot
ENTRYPOINT ["/strategy"]
`, goVer))
}
