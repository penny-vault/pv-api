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

package strategy_test

import (
	"context"
	"fmt"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("ImageTag", func() {
	DescribeTable("builds a tag from a GitHub clone URL",
		func(prefix, cloneURL, ver, want string) {
			Expect(strategy.ImageTag(prefix, cloneURL, ver)).To(Equal(want))
		},
		Entry("plain", "pvapi-strategy", "https://github.com/penny-vault/adm", "v1.2.3",
			"pvapi-strategy/penny-vault/adm:v1.2.3"),
		Entry("with .git suffix", "pvapi-strategy", "https://github.com/penny-vault/adm.git", "v1.2.3",
			"pvapi-strategy/penny-vault/adm:v1.2.3"),
		Entry("non-github fallback", "pvapi-strategy", "https://example.com/foo/bar", "abc123",
			"pvapi-strategy/unknown/example.com-foo-bar:abc123"),
	)
})

var _ = Describe("InstallDocker", func() {
	It("clones, builds, describes, and returns the image ref", func() {
		srcRepo := materializeFakeRepo("v1.0.0")
		fc := newFakeDocker()
		destDir := filepath.Join(GinkgoT().TempDir(), "install")

		result, err := strategy.InstallDocker(context.Background(),
			strategy.InstallRequest{
				ShortCode: "fake",
				CloneURL:  "file://" + srcRepo,
				Version:   "v1.0.0",
				DestDir:   destDir,
			},
			strategy.DockerInstallDeps{Client: fc, ImagePrefix: "pvapi-test"},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.ArtifactRef).To(HavePrefix("pvapi-test/unknown/"))
		Expect(result.ShortCode).To(Equal("fake"))
		Expect(fc.CreatedImages).To(HaveLen(1))
	})

	It("returns ErrDockerBuildFailed when ImageBuild fails", func() {
		srcRepo := materializeFakeRepo("v1.0.0")
		fc := newFakeDocker()
		fc.ImageBuildErr = fmt.Errorf("build boom")
		destDir := filepath.Join(GinkgoT().TempDir(), "install-fail")

		_, err := strategy.InstallDocker(context.Background(),
			strategy.InstallRequest{
				ShortCode: "fake",
				CloneURL:  "file://" + srcRepo,
				Version:   "v1.0.0",
				DestDir:   destDir,
			},
			strategy.DockerInstallDeps{Client: fc, ImagePrefix: "pvapi-test"},
		)
		Expect(err).To(MatchError(strategy.ErrDockerBuildFailed))
	})

	It("uses the binary's self-reported short code regardless of the request hint", func() {
		srcRepo := materializeFakeRepo("v1.0.0")
		fc := newFakeDocker()
		fc.DescribeStdout = []byte(`{"shortcode":"different","parameters":[],"presets":[],"schedule":"@monthend","benchmark":"SPY"}`)
		destDir := filepath.Join(GinkgoT().TempDir(), "install-mismatch")

		result, err := strategy.InstallDocker(context.Background(),
			strategy.InstallRequest{
				ShortCode: "hint-ignored",
				CloneURL:  "file://" + srcRepo,
				Version:   "v1.0.0",
				DestDir:   destDir,
			},
			strategy.DockerInstallDeps{Client: fc, ImagePrefix: "pvapi-test"},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.ShortCode).To(Equal("different"))
	})
})
