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
	"fmt"
	"strings"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
)

var _ = Describe("ProgressHub", func() {
	var (
		hub   *backtest.ProgressHub
		runID uuid.UUID
	)

	BeforeEach(func() {
		hub = backtest.NewProgressHub()
		runID = uuid.New()
	})

	Describe("Subscribe before any Publish", func() {
		It("returns a live channel that receives subsequent publishes", func() {
			events, unsub := hub.Subscribe(runID)
			defer unsub()

			msg := backtest.ProgressMessage{Type: "progress", Step: 1, TotalSteps: 100, Pct: 1.0}
			hub.Publish(runID, msg)

			evt := <-events
			Expect(evt.Progress).NotTo(BeNil())
			Expect(evt.Progress.Step).To(Equal(int64(1)))
		})
	})

	Describe("Subscribe after several Publishes", func() {
		It("replays the buffered messages then receives live messages", func() {
			for i := 1; i <= 5; i++ {
				hub.Publish(runID, backtest.ProgressMessage{Type: "progress", Step: int64(i), TotalSteps: 10})
			}

			events, unsub := hub.Subscribe(runID)
			defer unsub()

			for i := 1; i <= 5; i++ {
				evt := <-events
				Expect(evt.Progress).NotTo(BeNil())
				Expect(evt.Progress.Step).To(Equal(int64(i)))
			}
		})
	})

	Describe("circular buffer overflow (>20 messages)", func() {
		It("keeps only the most recent 20 messages", func() {
			for i := 1; i <= 25; i++ {
				hub.Publish(runID, backtest.ProgressMessage{Type: "progress", Step: int64(i)})
			}

			events, unsub := hub.Subscribe(runID)
			defer unsub()

			first := <-events
			Expect(first.Progress.Step).To(Equal(int64(6)))
		})
	})

	Describe("Complete before Subscribe", func() {
		It("returns a pre-closed channel with the terminal event", func() {
			hub.Complete(runID, "success", "")

			events, _ := hub.Subscribe(runID)
			evt := <-events
			Expect(evt.Terminal).NotTo(BeNil())
			Expect(evt.Terminal.Status).To(Equal("success"))

			_, ok := <-events
			Expect(ok).To(BeFalse())
		})
	})

	Describe("Complete after Subscribe", func() {
		It("sends the terminal event to active subscribers", func() {
			events, unsub := hub.Subscribe(runID)
			defer unsub()

			hub.Complete(runID, "failed", "strategy exited 1")

			var terminal *backtest.TerminalEvent
			for evt := range events {
				if evt.Terminal != nil {
					terminal = evt.Terminal
					break
				}
			}
			Expect(terminal).NotTo(BeNil())
			Expect(terminal.Status).To(Equal("failed"))
			Expect(terminal.Error).To(Equal("strategy exited 1"))
		})
	})

	Describe("multiple subscribers", func() {
		It("all receive the same events", func() {
			const n = 3
			channels := make([]<-chan backtest.Event, n)
			unsubs := make([]func(), n)
			for i := range channels {
				channels[i], unsubs[i] = hub.Subscribe(runID)
				defer unsubs[i]()
			}

			hub.Publish(runID, backtest.ProgressMessage{Type: "progress", Step: 42})
			for _, ch := range channels {
				evt := <-ch
				Expect(evt.Progress.Step).To(Equal(int64(42)))
			}
		})
	})
})

var _ = Describe("progressLineWriter", func() {
	var (
		hub   *backtest.ProgressHub
		runID uuid.UUID
	)

	BeforeEach(func() {
		hub = backtest.NewProgressHub()
		runID = uuid.New()
	})

	It("publishes a complete progress line", func() {
		events, unsub := hub.Subscribe(runID)
		defer unsub()

		w := backtest.NewProgressLineWriter(hub, runID)
		line := `{"type":"progress","step":10,"total_steps":100,"current_date":"2023-01-01","target_date":"2025-01-01","pct":10.0,"elapsed_ms":500,"eta_ms":4500,"measurements":1000}` + "\n"
		_, err := w.Write([]byte(line))
		Expect(err).NotTo(HaveOccurred())

		evt := <-events
		Expect(evt.Progress).NotTo(BeNil())
		Expect(evt.Progress.Step).To(Equal(int64(10)))
		Expect(evt.Progress.Pct).To(BeNumerically("~", 10.0, 0.001))
	})

	It("buffers a partial line and publishes when newline arrives", func() {
		events, unsub := hub.Subscribe(runID)
		defer unsub()

		w := backtest.NewProgressLineWriter(hub, runID)
		line := `{"type":"progress","step":5,"total_steps":100,"current_date":"2023-01-01","target_date":"2025-01-01","pct":5.0,"elapsed_ms":100,"eta_ms":1900,"measurements":50}`
		half := len(line) / 2
		_, _ = w.Write([]byte(line[:half]))
		Consistently(events, "50ms").ShouldNot(Receive())
		_, _ = w.Write([]byte(line[half:] + "\n"))
		Eventually(events, "100ms").Should(Receive())
	})

	It("discards malformed JSON lines", func() {
		events, unsub := hub.Subscribe(runID)
		defer unsub()

		w := backtest.NewProgressLineWriter(hub, runID)
		_, _ = w.Write([]byte("not json\n"))
		Consistently(events, "50ms").ShouldNot(Receive())
	})

	It("discards lines with type != progress", func() {
		events, unsub := hub.Subscribe(runID)
		defer unsub()

		w := backtest.NewProgressLineWriter(hub, runID)
		_, _ = w.Write([]byte(`{"type":"info","message":"starting"}` + "\n"))
		Consistently(events, "50ms").ShouldNot(Receive())
	})

	It("handles multiple lines in a single write", func() {
		events, unsub := hub.Subscribe(runID)
		defer unsub()

		w := backtest.NewProgressLineWriter(hub, runID)
		var sb strings.Builder
		for i := 1; i <= 3; i++ {
			fmt.Fprintf(&sb, `{"type":"progress","step":%d,"total_steps":10,"current_date":"2023-01-01","target_date":"2025-01-01","pct":%d.0,"elapsed_ms":100,"eta_ms":900,"measurements":%d}`+"\n", i, i*10, i*100)
		}
		_, _ = w.Write([]byte(sb.String()))
		for i := 1; i <= 3; i++ {
			evt := <-events
			Expect(evt.Progress.Step).To(Equal(int64(i)))
		}
	})
})
