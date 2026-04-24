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
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/strategy"
)

// materializeFakeRepo copies strategy/testdata/fake-strategy-src/* into a
// fresh tempdir, `git init`s it, commits, tags, and returns the directory
// path. Callers can clone from `file://<path>`.
func materializeFakeRepo(tag string) string {
	srcDir, err := filepath.Abs("testdata/fake-strategy-src")
	Expect(err).NotTo(HaveOccurred())

	dst := GinkgoT().TempDir()

	// Copy source tree
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	Expect(err).NotTo(HaveOccurred())

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dst
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
	}

	run("init", "-q", "-b", "main")
	run("add", ".")
	run("commit", "-q", "-m", "initial")
	run("tag", tag)

	return dst
}

var _ = Describe("Install", func() {
	It("clones a pinned tag, builds, and parses describe", func() {
		repo := materializeFakeRepo("v1.0.0")
		dst := GinkgoT().TempDir()

		result, err := strategy.Install(context.Background(), strategy.InstallRequest{
			ShortCode: "fake",
			CloneURL:  "file://" + repo,
			Version:   "v1.0.0",
			DestDir:   dst,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.BinPath).To(BeARegularFile())
		Expect(result.ShortCode).To(Equal("fake"))

		var d strategy.Describe
		Expect(json.Unmarshal(result.DescribeJSON, &d)).To(Succeed())
		Expect(d.ShortCode).To(Equal("fake"))
		Expect(d.Presets).To(HaveLen(1))
		Expect(d.Presets[0].Name).To(Equal("standard"))
	})

	It("fails with a useful error when the tag does not exist", func() {
		repo := materializeFakeRepo("v1.0.0")

		_, err := strategy.Install(context.Background(), strategy.InstallRequest{
			ShortCode: "fake",
			CloneURL:  "file://" + repo,
			Version:   "v9.9.9",
			DestDir:   GinkgoT().TempDir(),
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("v9.9.9"))
	})

	It("uses the binary's self-reported short code regardless of the request hint", func() {
		repo := materializeFakeRepo("v1.0.0")

		result, err := strategy.Install(context.Background(), strategy.InstallRequest{
			ShortCode: "hint-ignored",
			CloneURL:  "file://" + repo,
			Version:   "v1.0.0",
			DestDir:   GinkgoT().TempDir(),
		})
		Expect(err).NotTo(HaveOccurred())
		// The fake binary reports "fake" as its short code; the hint is ignored.
		Expect(result.ShortCode).To(Equal("fake"))
		Expect(result.BinPath).To(ContainSubstring("fake.bin"))
	})
})
