//go:build !windows

package main

import (
	"fmt"
	"os"
)

// There is no wizard page to embed into off Windows. This stub exists so the
// package builds and tests on the Linux/macOS machines this repo is developed
// on — see kill_other.go.
func panel(o options) int {
	fmt.Fprintln(os.Stderr, "keld-wizard-host: --panel is Windows-only")
	return 2
}
