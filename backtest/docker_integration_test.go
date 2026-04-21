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

//go:build integration
// +build integration

package backtest_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/docker/docker/client"

	"github.com/penny-vault/pv-api/strategy"
)

func TestEphemeralImageBuild_IntegrationBootstrap(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "DockerIntegration Suite")
}

var _ = Describe("EphemeralImageBuild (real daemon)", func() {
	It("builds + runs describe end-to-end", func() {
		dc, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		Expect(err).NotTo(HaveOccurred())

		repo := itMakeLocalGitRepo(GinkgoT(), "../strategy/testdata/fake-strategy-src", "v1.0.0")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		ref, cleanup, berr := strategy.EphemeralImageBuild(ctx, strategy.DockerEphemeralOptions{
			CloneURL:          "file://" + repo,
			Ver:               "v1.0.0",
			Dir:               GinkgoT().TempDir(),
			SkipURLValidation: true,
			Client:            dc,
			ImagePrefix:       "pvapi-it",
		})
		Expect(berr).NotTo(HaveOccurred())
		DeferCleanup(cleanup)

		out, rerr := exec.CommandContext(ctx, "docker", "run", "--rm", ref, "describe", "--json").Output()
		Expect(rerr).NotTo(HaveOccurred())
		Expect(string(out)).To(ContainSubstring(`"shortCode": "fake"`))
	})
})

// itMakeLocalGitRepo copies src into a tempdir, runs git init/add/commit/tag,
// and returns the repo path. Prefixed `it` to avoid colliding with any
// helper of the same name elsewhere in the backtest_test package.
func itMakeLocalGitRepo(t GinkgoTInterface, src, tag string) string {
	dir := t.TempDir()
	itCopyTree(t, src, dir)
	for _, args := range [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "add", "."},
		{"git", "-c", "user.email=ci@pvapi", "-c", "user.name=CI", "commit", "-m", "init"},
		{"git", "tag", tag},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		Expect(cmd.Run()).To(Succeed(), "step: %v", args)
	}
	return dir
}

func itCopyTree(t GinkgoTInterface, src, dst string) {
	Expect(filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(target, b, info.Mode())
	})).To(Succeed())
}
