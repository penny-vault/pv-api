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

package cmd

import (
	"bytes"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func TestRunnerDockerConfig(t *testing.T) {
	v := viper.New()
	v.SetConfigType("toml")
	err := v.ReadConfig(bytes.NewBufferString(`
[runner]
mode = "docker"
  [runner.docker]
  socket       = "tcp://daemon:2375"
  network      = "pvapi"
  cpu_limit    = 2.0
  memory_limit = "1Gi"
  build_timeout = "7m"
  image_prefix = "pvapi-strat"
  snapshots_host_path = "/srv/snapshots"
`))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var c Config
	if err := v.Unmarshal(&c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Runner.Mode != "docker" {
		t.Errorf("mode = %q; want docker", c.Runner.Mode)
	}
	if c.Runner.Docker.Socket != "tcp://daemon:2375" {
		t.Errorf("socket = %q", c.Runner.Docker.Socket)
	}
	if c.Runner.Docker.CPULimit != 2.0 {
		t.Errorf("cpu_limit = %v", c.Runner.Docker.CPULimit)
	}
	if c.Runner.Docker.MemoryLimit != "1Gi" {
		t.Errorf("memory_limit = %q", c.Runner.Docker.MemoryLimit)
	}
	if c.Runner.Docker.BuildTimeout != 7*time.Minute {
		t.Errorf("build_timeout = %v", c.Runner.Docker.BuildTimeout)
	}
	if c.Runner.Docker.SnapshotsHostPath != "/srv/snapshots" {
		t.Errorf("snapshots_host_path = %q", c.Runner.Docker.SnapshotsHostPath)
	}
}
