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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/strategy"
)

// copyTree copies the directory tree rooted at src into dst (dst must not exist).
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src) //nolint:gosec // test helper; path is internal
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck // test helper

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close() //nolint:errcheck // test helper

	_, err = io.Copy(out, in)
	return err
}

// gitInit creates an initial commit in dir containing all current files.
func gitInit(dir string) error {
	run := func(args ...string) error {
		cmd := exec.Command(args[0], args[1:]...) //nolint:gosec // test helper
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %w\n%s", args, err, out)
		}
		return nil
	}
	if err := run("git", "init", "-q", dir); err != nil {
		return err
	}
	if err := run("git", "-C", dir, "add", "."); err != nil {
		return err
	}
	return run("git", "-C", dir, "commit", "-q", "-m", "init")
}

var _ = Describe("EphemeralBuild", func() {
	var tmpRoot string

	BeforeEach(func() {
		var err error
		tmpRoot, err = os.MkdirTemp("", "ephbuild-*")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(tmpRoot) //nolint:errcheck // test cleanup
	})

	Describe("happy path", func() {
		It("clones, builds, and produces a working binary", func(ctx SpecContext) {
			fakeSrc := filepath.Join(
				"testdata", "fake-strategy-src",
			)
			srcDir := filepath.Join(tmpRoot, "fakesrc")
			Expect(copyTree(fakeSrc, srcDir)).To(Succeed())
			Expect(gitInit(srcDir)).To(Succeed())

			binPath, cleanup, err := strategy.EphemeralBuild(ctx, strategy.EphemeralOptions{
				CloneURL:          "file://" + srcDir,
				Dir:               tmpRoot,
				Timeout:           60 * time.Second,
				SkipURLValidation: true,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(cleanup).NotTo(BeNil())
			Expect(binPath).NotTo(BeEmpty())

			_, statErr := os.Stat(binPath)
			Expect(statErr).NotTo(HaveOccurred())

			// Run the built binary and check its output.
			out, runErr := exec.CommandContext(ctx, binPath, "describe", "--json").Output() //nolint:gosec // binPath from EphemeralBuild
			Expect(runErr).NotTo(HaveOccurred())
			Expect(string(out)).To(ContainSubstring(`"shortcode": "fake"`))

			// Cleanup should remove the tempdir.
			buildDir := filepath.Dir(binPath)
			cleanup()
			_, statErr = os.Stat(buildDir)
			Expect(os.IsNotExist(statErr)).To(BeTrue())

			// Second call must not panic or error.
			Expect(cleanup).NotTo(Panic())
		}, NodeTimeout(120*time.Second))
	})

	Describe("URL rejection", func() {
		It("returns ErrInvalidCloneURL for non-allowlisted URLs", func() {
			_, cleanup, err := strategy.EphemeralBuild(context.Background(), strategy.EphemeralOptions{
				CloneURL: "file:///tmp/x",
			})
			Expect(err).To(MatchError(strategy.ErrInvalidCloneURL))
			Expect(cleanup).To(BeNil())
		})
	})
})

var _ = Describe("ValidateCloneURL", func() {
	DescribeTable("accepts canonical GitHub HTTPS URLs",
		func(url string) { Expect(strategy.ValidateCloneURL(url)).To(Succeed()) },
		Entry("owner/repo", "https://github.com/penny-vault/pvbt"),
		Entry("with .git suffix", "https://github.com/penny-vault/pvbt.git"),
		Entry("dashes", "https://github.com/some-owner/some-repo"),
		Entry("dots", "https://github.com/a.b/c.d"),
	)

	DescribeTable("rejects other shapes",
		func(url string) { Expect(strategy.ValidateCloneURL(url)).ToNot(Succeed()) },
		Entry("ssh", "git@github.com:owner/repo.git"),
		Entry("http", "http://github.com/owner/repo"),
		Entry("file", "file:///tmp/repo"),
		Entry("gitlab", "https://gitlab.com/owner/repo"),
		Entry("query string", "https://github.com/owner/repo?x=1"),
		Entry("path beyond repo", "https://github.com/owner/repo/tree/main"),
		Entry("empty", ""),
	)
})
