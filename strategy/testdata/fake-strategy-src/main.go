// A minimal strategy that behaves like a pvbt-compiled strategy binary for
// tests: supports `describe --json` and exits 0. Not a real backtest; this
// exists only so strategy.Install can exercise its clone + build + describe
// path end-to-end.
package main

import (
	"fmt"
	"os"
)

const describeJSON = `{
  "shortcode": "fake",
  "name": "Fake Strategy",
  "description": "Test fixture for pvapi install tests",
  "parameters": [
    {"name": "riskOn", "type": "universe", "default": "SPY", "description": "risk-on universe"}
  ],
  "presets": [
    {"name": "standard", "parameters": {"riskOn": "SPY"}}
  ],
  "schedule": "@monthend",
  "benchmark": "SPY"
}`

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "describe" {
		fmt.Print(describeJSON)
		return
	}
	fmt.Fprintln(os.Stderr, "usage: fake-strategy describe [--json]")
	os.Exit(2)
}
