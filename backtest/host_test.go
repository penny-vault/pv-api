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

package backtest_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
)

var (
	fakeStratBin string
	fakeStratSrc string
)

var _ = BeforeSuite(func() {
	dir := GinkgoT().TempDir()
	fakeStratBin = filepath.Join(dir, "fakestrat")
	cmd := exec.Command("go", "build", "-o", fakeStratBin, "./testdata/fakestrat")
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))

	fakeStratSrc = filepath.Join(dir, "fixture.bin")
	Expect(os.WriteFile(fakeStratSrc, []byte("this-is-a-fake-snapshot"), 0o644)).To(Succeed())
})

var _ = Describe("HostRunner", func() {
	var runner *backtest.HostRunner

	BeforeEach(func() {
		runner = &backtest.HostRunner{}
	})

	It("copies the fixture to OutPath on success", func() {
		out := filepath.Join(GinkgoT().TempDir(), "out.sqlite")
		Expect(os.Setenv("FAKESTRAT_FIXTURE", fakeStratSrc)).To(Succeed())
		DeferCleanup(func() { os.Unsetenv("FAKESTRAT_FIXTURE") })

		err := runner.Run(context.Background(), backtest.RunRequest{
			Artifact:     fakeStratBin,
			ArtifactKind: backtest.ArtifactBinary,
			Args:         []string{"--something", "1"},
			OutPath:      out,
			Timeout:      5 * time.Second,
		})
		Expect(err).NotTo(HaveOccurred())

		data, rerr := os.ReadFile(out)
		Expect(rerr).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("this-is-a-fake-snapshot"))
	})

	It("wraps non-zero exit in ErrRunnerFailed with stderr attached", func() {
		out := filepath.Join(GinkgoT().TempDir(), "out.sqlite")
		Expect(os.Setenv("FAKESTRAT_BEHAVIOR", "fail")).To(Succeed())
		DeferCleanup(func() { os.Unsetenv("FAKESTRAT_BEHAVIOR") })
		Expect(os.Setenv("FAKESTRAT_FIXTURE", fakeStratSrc)).To(Succeed())
		DeferCleanup(func() { os.Unsetenv("FAKESTRAT_FIXTURE") })

		err := runner.Run(context.Background(), backtest.RunRequest{
			Artifact:     fakeStratBin,
			ArtifactKind: backtest.ArtifactBinary,
			OutPath:      out,
			Timeout:      5 * time.Second,
		})
		Expect(err).To(MatchError(backtest.ErrRunnerFailed))
		Expect(err.Error()).To(ContainSubstring("simulated failure"))
	})

	It("returns ErrTimedOut when the timeout fires", func() {
		out := filepath.Join(GinkgoT().TempDir(), "out.sqlite")
		Expect(os.Setenv("FAKESTRAT_BEHAVIOR", "sleep")).To(Succeed())
		DeferCleanup(func() { os.Unsetenv("FAKESTRAT_BEHAVIOR") })

		err := runner.Run(context.Background(), backtest.RunRequest{
			Artifact:     fakeStratBin,
			ArtifactKind: backtest.ArtifactBinary,
			OutPath:      out,
			Timeout:      150 * time.Millisecond,
		})
		Expect(err).To(MatchError(backtest.ErrTimedOut))
	})

	It("returns ErrTimedOut when the parent context is cancelled", func() {
		out := filepath.Join(GinkgoT().TempDir(), "out.sqlite")
		Expect(os.Setenv("FAKESTRAT_BEHAVIOR", "sleep")).To(Succeed())
		DeferCleanup(func() { os.Unsetenv("FAKESTRAT_BEHAVIOR") })

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()

		err := runner.Run(ctx, backtest.RunRequest{
			Artifact:     fakeStratBin,
			ArtifactKind: backtest.ArtifactBinary,
			OutPath:      out,
			Timeout:      5 * time.Second,
		})
		Expect(err).To(MatchError(backtest.ErrTimedOut))
	})

	It("tees stdout to ProgressWriter and passes --json when ProgressWriter is set", func() {
		out := filepath.Join(GinkgoT().TempDir(), "out.sqlite")
		Expect(os.Setenv("FAKESTRAT_FIXTURE", fakeStratSrc)).To(Succeed())
		DeferCleanup(func() { os.Unsetenv("FAKESTRAT_FIXTURE") })

		var buf bytes.Buffer
		err := runner.Run(context.Background(), backtest.RunRequest{
			Artifact:       fakeStratBin,
			ArtifactKind:   backtest.ArtifactBinary,
			OutPath:        out,
			Timeout:        5 * time.Second,
			ProgressWriter: &buf,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(buf.String()).To(ContainSubstring(`"type":"progress"`))
	})

	It("returns ErrArtifactKindMismatch when given an image artifact", func() {
		out := filepath.Join(GinkgoT().TempDir(), "out.sqlite")
		err := runner.Run(context.Background(), backtest.RunRequest{
			Artifact:     "some/image:latest",
			ArtifactKind: backtest.ArtifactImage,
			OutPath:      out,
			Timeout:      time.Second,
		})
		Expect(err).To(MatchError(backtest.ErrArtifactKindMismatch))
	})
})
