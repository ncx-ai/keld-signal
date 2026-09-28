//go:build !windows

package main

import "os/exec"

// No consoles to suppress off Windows; see kill_other.go.
func hideChildWindow(cmd *exec.Cmd) {}
