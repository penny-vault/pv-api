// Copyright 2021-2024
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

package cache

import "github.com/tidwall/tinylru"

var lru tinylru.LRUG[string, string]

func init() {
	lru.Resize(1024)
}

// Get returns the value for the specified key and a boolean indicating if the
// key existed
func Get(key string) (string, bool) {
	return lru.Get(key)
}

// Set a new value in the cache. Returns the previous value and a boolean
// indicating if the value was replaced
func Set(key, val string) (string, bool) {
	return lru.Set(key, val)
}
