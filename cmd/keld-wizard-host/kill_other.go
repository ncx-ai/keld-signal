//go:build !windows

package main

import (
	"os"
	"syscall"
)

// The helper only ever runs on Windows — it exists for an Inno wizard page. These
// stubs keep `go test ./...` and `go build ./...` working on the Linux and macOS
// machines this repo is also developed on, so a change here cannot be discovered
// broken only in the Windows CI job.

func limitChildrenToOurLifetime() {}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
