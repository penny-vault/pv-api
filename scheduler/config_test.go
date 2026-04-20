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

package scheduler_test

import (
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/scheduler"
)

var _ = Describe("Config", func() {
	Describe("ApplyDefaults", func() {
		It("fills in TickInterval=60s when zero", func() {
			c := scheduler.Config{}
			c.ApplyDefaults()
			Expect(c.TickInterval).To(Equal(60 * time.Second))
		})

		It("fills in BatchSize=32 when zero", func() {
			c := scheduler.Config{}
			c.ApplyDefaults()
			Expect(c.BatchSize).To(Equal(32))
		})

		It("does not overwrite non-zero values", func() {
			c := scheduler.Config{TickInterval: 5 * time.Second, BatchSize: 4}
			c.ApplyDefaults()
			Expect(c.TickInterval).To(Equal(5 * time.Second))
			Expect(c.BatchSize).To(Equal(4))
		})
	})

	Describe("Validate", func() {
		It("rejects negative TickInterval", func() {
			c := scheduler.Config{TickInterval: -1 * time.Second, BatchSize: 32}
			Expect(errors.Is(c.Validate(), scheduler.ErrInvalidTickInterval)).To(BeTrue())
		})

		It("rejects negative BatchSize", func() {
			c := scheduler.Config{TickInterval: time.Second, BatchSize: -1}
			Expect(errors.Is(c.Validate(), scheduler.ErrInvalidBatchSize)).To(BeTrue())
		})

		It("accepts positive values", func() {
			c := scheduler.Config{TickInterval: time.Second, BatchSize: 1}
			Expect(c.Validate()).To(Succeed())
		})
	})
})
