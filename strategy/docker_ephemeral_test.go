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
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("EphemeralImageBuild", func() {
	It("clones, builds, and returns an ephemeral image ref", func() {
		srcRepo := materializeFakeRepo("v1.0.0")
		fc := newFakeDocker()

		ref, cleanup, err := strategy.EphemeralImageBuild(context.Background(), strategy.DockerEphemeralOptions{
			CloneURL:          "file://" + srcRepo,
			Ver:               "v1.0.0",
			Dir:               GinkgoT().TempDir(),
			SkipURLValidation: true,
			Client:            fc,
			ImagePrefix:       "pvapi-ephem",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(ref).To(HavePrefix("pvapi-ephem/ephemeral/"))
		Expect(cleanup).NotTo(BeNil())

		// cleanup removes image + tempdir; idempotent.
		cleanup()
		Expect(fc.RemovedImages).To(ContainElement(ref))
		cleanup() // second call is a no-op
		Expect(fc.RemovedImages).To(HaveLen(1))
	})

	It("removes the tempdir when ImageBuild fails", func() {
		srcRepo := materializeFakeRepo("v1.0.0")
		fc := newFakeDocker()
		fc.ImageBuildErr = fmt.Errorf("nope")

		parent := GinkgoT().TempDir()
		ref, cleanup, err := strategy.EphemeralImageBuild(context.Background(), strategy.DockerEphemeralOptions{
			CloneURL:          "file://" + srcRepo,
			Ver:               "v1.0.0",
			Dir:               parent,
			SkipURLValidation: true,
			Client:            fc,
			ImagePrefix:       "pvapi-ephem",
		})
		Expect(err).To(HaveOccurred())
		Expect(ref).To(Equal(""))
		Expect(cleanup).To(BeNil())

		entries, _ := os.ReadDir(parent)
		Expect(entries).To(BeEmpty(), "tempdir should have been removed")
	})

	It("rejects a non-allowlisted URL when SkipURLValidation is false", func() {
		fc := newFakeDocker()
		_, _, err := strategy.EphemeralImageBuild(context.Background(), strategy.DockerEphemeralOptions{
			CloneURL:    "https://gitlab.com/foo/bar",
			Ver:         "v1",
			Dir:         GinkgoT().TempDir(),
			Client:      fc,
			ImagePrefix: "pvapi-ephem",
		})
		Expect(err).To(MatchError(strategy.ErrInvalidCloneURL))
	})
})
