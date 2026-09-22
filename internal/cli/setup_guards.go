package cli

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/ncx-ai/keld-signal/internal/telemetry"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// The post-write verification, MOVED to internal/telemetry and aliased here so
// this file's callers and its tests read unchanged.
//
// ⚠️ IT MOVED BECAUSE IT GAINED A SECOND CALLER, not for tidiness. The daemon's
// integrations detector now asks the same question before rewriting keld's own
// block in a tool's config it finds drifted — "do not repair what you cannot
// verify" — and two copies of "does the running proxy accept this value" are two
// ways for `keld signal setup` and the daemon to disagree about one machine.
// The measurement behind it is on telemetry.ProbeSecret.
type probeOutcome = telemetry.ProbeOutcome

const (
	probeOK         = telemetry.ProbeOK
	probeUnverified = telemetry.ProbeUnverified
	probeRejected   = telemetry.ProbeRejected
)

func probeTelemetry(endpoint, secret string) (probeOutcome, int) {
	return telemetry.ProbeSecret(endpoint, secret)
}

// newerKeldOnPATH returns the path and version of a `keld` on PATH that reports a
// STRICTLY NEWER build than current, or ("", "").
//
// ⚠️ THIS IS THE 2026-09-18 INCIDENT'S CAUSE RATHER THAN ITS SYMPTOM. The
// mismatched secret was written by a keld 3.0.0-rc.3 living at
// /usr/local/keld/keld — a binary that predates the secret having a file of its
// own, so it minted into agent.json and disagreed with the running proxy. A
// newer binary knows things this one does not; writing every tool's
// configuration from the older of two installs is not something to warn about
// and continue past.
//
// It reuses keldPATHBinaries — the detection `keld signal doctor` already does
// for shadowed installs — rather than walking PATH a second time. An unknown or
// unreadable version is NOT newer: the guard fails toward doing nothing, so a
// source build ("dev") and a version string this cannot parse both pass.
func newerKeldOnPATH(current string) (path, ver string) {
	for _, bin := range keldPATHBinaries() {
		v := keldVersionOf(bin)
		if v == "" {
			continue
		}
		if newer, known := version.Newer(v, current); known && newer {
			return bin, v
		}
	}
	return "", ""
}

// keldVersionOf asks a binary what it is. Cobra prints "<name> version <v>", so
// the last field is the version; anything else yields "" and is treated as
// unknown.
func keldVersionOf(bin string) string {
	cmd := exec.Command(bin, "--version")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// shadowedByNewerKeld renders the refusal. It names the path and the version and
// gives the fix, because "something newer is on PATH" that does not say WHERE is
// a message nobody can act on.
func shadowedByNewerKeld(path, ver, current string) error {
	return fmt.Errorf("refusing to configure tools: %s reports version %s, newer than this binary (%s). "+
		"A newer keld writes the telemetry credential differently, and the older one writing it is how a "+
		"machine ends up with tools holding a secret the running daemon rejects. "+
		"Run `%s signal setup` instead, or remove the stale binary", path, ver, current, path)
}
