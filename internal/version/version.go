// Package version holds the CLI version string, injected at build time.
package version

// CLI is the current version of the keld CLI. Overridden by ldflags at release
// time (e.g. -X github.com/ncx-ai/keld-signal/internal/version.CLI=1.2.3).
var CLI = "dev"
