package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// AgentBinaryName is the daemon binary the service must be pointed at. It is
// the ONLY program that understands the `run` verb every platform's service
// definition invokes.
const AgentBinaryName = "keld-agent"

// ErrNoAgentBinary reports that the daemon binary could not be located, so no
// service definition was written.
//
// Refusing is the safe direction and the whole point: the alternative is a
// service definition naming something that cannot run, which is strictly worse
// than not restarting at all.
var ErrNoAgentBinary = errors.New("service: cannot find the keld-agent binary; not rewriting the service definition")

// agentExecutable answers which binary the service definition should name.
//
// ⚠️ **THIS EXISTS BECAUSE `keld signal restart` BRICKED THE SERVICE, AND IT
// DID SO SILENTLY.** Start and Restart both re-derived the service definition
// from `os.Executable()` before kickstarting. That is correct when the running
// binary IS the daemon — and `keld signal restart` runs inside the `keld` CLI,
// so the LaunchAgent was rewritten to `/…/keld run`. `keld` has no `run`
// command, so the daemon died on every launch, launchd dutifully respawned it,
// and the machine collected nothing until a human noticed. Observed live on a
// real machine: the plist read `<string>…/keld</string><string>run</string>`
// and the log was `unknown command "run" for "keld"`, repeating.
//
// The two commands are documented as equivalent — `keld signal restart` and
// `keld-agent restart` share a verb table for exactly that reason — so the one
// that is safe and the one that destroys the service were indistinguishable to
// anyone reading either help text.
//
// The rule is therefore: **a restart may not repoint the service.** Three
// sources, in order, and the first is what makes a restart a restart:
//
//  1. the program the EXISTING definition already names — a restart adopts what
//     is installed rather than re-deciding it;
//  2. the running binary, when it is itself the daemon — the ordinary
//     `keld-agent install` path on a machine with nothing installed yet;
//  3. a sibling `keld-agent` beside the running binary — `keld` and
//     `keld-agent` ship together, so this is the CLI installing the daemon.
//
// If none of those produce a daemon binary, it returns ErrNoAgentBinary and the
// caller writes nothing.
//
// installedProgram is the platform's reader for (1) — it returns "" when there
// is no definition on disk or none can be parsed. exeFn is os.Executable,
// injected for tests.
func agentExecutable(installedProgram func() string, exeFn func() (string, error)) (string, error) {
	if installedProgram != nil {
		if p := strings.TrimSpace(installedProgram()); p != "" && isAgentBinary(p) {
			return p, nil
		}
	}

	exe, err := exeFn()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoAgentBinary, err)
	}
	if isAgentBinary(exe) {
		return exe, nil
	}

	// The CLI is installing or restarting on the daemon's behalf. They ship
	// side by side in every layout this project produces (the pkg's
	// /usr/local/keld, `curl | sh`'s ~/.local/bin, a dev tree's ./), so the
	// sibling is where the daemon is.
	sibling := filepath.Join(filepath.Dir(exe), agentFileName())
	if isRegularExecutable(sibling) {
		return sibling, nil
	}
	return "", fmt.Errorf("%w (ran as %s, no %s beside it)", ErrNoAgentBinary, exe, agentFileName())
}

// agentFileName is the daemon's on-disk name for this platform.
func agentFileName() string {
	if runtime.GOOS == "windows" {
		return AgentBinaryName + ".exe"
	}
	return AgentBinaryName
}

// isAgentBinary reports whether path names the daemon.
//
// ⚠️ Matched on the FILE NAME, not on a path prefix, because the daemon lives
// in a different directory in every install layout this project ships — the
// macOS pkg's /usr/local/keld, `curl | sh`'s ~/.local/bin, the frozen tree, a
// developer's `go build` output. A prefix rule would have to know all of them
// and would silently reject a layout invented later. The name is the one thing
// that is the same everywhere, and it is what `reapStaleSidecars` matches on
// for the same reason.
//
// A suffix check would wrongly accept `not-keld-agent`; comparing the base name
// exactly does not.
func isAgentBinary(path string) bool {
	base := filepath.Base(path)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(base, AgentBinaryName+".exe") || strings.EqualFold(base, AgentBinaryName)
	}
	return base == AgentBinaryName
}

func isRegularExecutable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true // no execute bit to consult
	}
	return fi.Mode().Perm()&0o111 != 0
}
