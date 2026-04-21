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
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
)

var _ = Describe("DockerRunner", func() {
	var (
		fc     *fakeDocker
		runner *backtest.DockerRunner
	)

	BeforeEach(func() {
		fc = newFakeDocker()
		runner = &backtest.DockerRunner{
			Client:           fc,
			Network:          "pvapi",
			NanoCPUs:         2_000_000_000,
			MemoryBytes:      1 << 30,
			SnapshotsHostDir: "/host/snapshots",
			SnapshotsDir:     "/var/lib/pvapi/snapshots",
		}
	})

	It("runs the strategy image and returns nil on exit 0", func() {
		err := runner.Run(context.Background(), backtest.RunRequest{
			Artifact:     "pvapi-strategy/foo/bar:v1",
			ArtifactKind: backtest.ArtifactImage,
			Args:         []string{"--benchmark", "SPY"},
			OutPath:      "/var/lib/pvapi/snapshots/abc.sqlite.tmp",
			Timeout:      5 * time.Second,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(fc.CreatedCmds).To(HaveLen(1))
		Expect(fc.CreatedCmds[0]).To(Equal([]string{"backtest", "--output", "/var/lib/pvapi/snapshots/abc.sqlite.tmp", "--benchmark", "SPY"}))
		Expect(fc.CreatedHosts).To(HaveLen(1))
		hc := fc.CreatedHosts[0]
		Expect(hc.Resources.NanoCPUs).To(Equal(int64(2_000_000_000)))
		Expect(hc.Resources.Memory).To(Equal(int64(1 << 30)))
		Expect(hc.Mounts).To(HaveLen(1))
		Expect(string(hc.Mounts[0].Type)).To(Equal("bind"))
		Expect(hc.Mounts[0].Source).To(Equal("/host/snapshots"))
		Expect(hc.Mounts[0].Target).To(Equal("/var/lib/pvapi/snapshots"))
	})

	It("wraps non-zero exit in ErrRunnerFailed", func() {
		fc.ContainerExit = 2
		err := runner.Run(context.Background(), backtest.RunRequest{
			Artifact:     "img",
			ArtifactKind: backtest.ArtifactImage,
			OutPath:      "/snap.sqlite.tmp",
			Timeout:      time.Second,
		})
		Expect(err).To(MatchError(backtest.ErrRunnerFailed))
	})

	It("returns ErrArtifactKindMismatch for a binary artifact", func() {
		err := runner.Run(context.Background(), backtest.RunRequest{
			Artifact:     "/path/to/bin",
			ArtifactKind: backtest.ArtifactBinary,
			OutPath:      "/snap.sqlite.tmp",
		})
		Expect(err).To(MatchError(backtest.ErrArtifactKindMismatch))
	})

	It("returns ErrTimedOut when ContainerWait exceeds the deadline", func() {
		fc.WaitDelay = 100 * time.Millisecond
		err := runner.Run(context.Background(), backtest.RunRequest{
			Artifact:     "img",
			ArtifactKind: backtest.ArtifactImage,
			OutPath:      "/snap.sqlite.tmp",
			Timeout:      20 * time.Millisecond,
		})
		Expect(errors.Is(err, backtest.ErrTimedOut)).To(BeTrue())
		Expect(fc.KilledIDs).NotTo(BeEmpty())
	})

	It("uses the Network setting on the host config", func() {
		_ = runner.Run(context.Background(), backtest.RunRequest{
			Artifact: "img", ArtifactKind: backtest.ArtifactImage,
			OutPath: "/snap.sqlite.tmp", Timeout: time.Second,
		})
		Expect(string(fc.CreatedHosts[0].NetworkMode)).To(Equal("pvapi"))
	})
})
