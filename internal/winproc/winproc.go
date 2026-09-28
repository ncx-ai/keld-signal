//go:build !windows

// Package winproc keeps spawned console programs from putting a window on the
// screen. Everywhere except Windows that costs nothing and the helpers are
// no-ops, so callers never need a build tag of their own.
package winproc

import "os/exec"

// Hide is a no-op off Windows.
func Hide(*exec.Cmd) {}
