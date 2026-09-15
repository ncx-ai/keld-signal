//go:build !windows

package main

// There is no wizard to serve off Windows; see kill_other.go.
func clipboardText() string { return "" }
