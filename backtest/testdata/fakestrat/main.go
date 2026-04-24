// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
// Tiny test-only stand-in for a real strategy binary. Reads the
// FAKESTRAT_FIXTURE env variable as a source path and copies it to the
// --output flag. FAKESTRAT_BEHAVIOR=fail exits 1; FAKESTRAT_BEHAVIOR=sleep
// sleeps forever so context cancellation paths can be exercised.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "backtest" {
		fmt.Fprintln(os.Stderr, "fakestrat: expected 'backtest' subcommand")
		os.Exit(2)
	}
	os.Args = append(os.Args[:1], os.Args[2:]...)

	fs := flag.NewFlagSet("fakestrat", flag.ContinueOnError)
	output := fs.String("output", "", "output SQLite path")
	jsonMode := fs.Bool("json", false, "emit JSON progress to stdout")
	// Parse only known flags; unknown flags (strategy parameters) are ignored.
	_ = fs.Parse(os.Args[1:])

	switch os.Getenv("FAKESTRAT_BEHAVIOR") {
	case "fail":
		fmt.Fprintln(os.Stderr, "fakestrat: simulated failure")
		os.Exit(1)
	case "sleep":
		time.Sleep(1 * time.Hour)
	}

	if *jsonMode {
		fmt.Println(`{"type":"progress","step":1,"total_steps":10,"current_date":"2023-01-01","target_date":"2025-01-01","pct":10.0,"elapsed_ms":100,"eta_ms":900,"measurements":100}`)
	}

	if *output == "" {
		fmt.Fprintln(os.Stderr, "fakestrat: --output is required")
		os.Exit(2)
	}
	src := os.Getenv("FAKESTRAT_FIXTURE")
	if src == "" {
		fmt.Fprintln(os.Stderr, "fakestrat: FAKESTRAT_FIXTURE env is required")
		os.Exit(2)
	}

	in, err := os.Open(src)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakestrat: open fixture:", err)
		os.Exit(1)
	}
	defer in.Close()

	out, err := os.Create(*output)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakestrat: create output:", err)
		os.Exit(1)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		fmt.Fprintln(os.Stderr, "fakestrat: copy:", err)
		os.Exit(1)
	}
}
