package cli

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/version"
)

// probeOutcome is what the post-write verification found.
type probeOutcome int

const (
	// probeOK — the running proxy accepted the credential just written.
	probeOK probeOutcome = iota
	// probeUnverified — nothing answered on the loopback port. NOT a failure:
	// `keld-agent install` registers and starts the service AFTER setup runs,
	// and the macOS wizard onboards before the daemon exists at all, so the
	// ordinary first install has nothing listening.
	probeUnverified
	// probeRejected — the proxy answered 401. The machine WOULD have been broken.
	probeRejected
)

// probeTimeout bounds the loopback probe. It is one request to 127.0.0.1 against
// a handler that authenticates and returns immediately; a setup run must not sit
// on it if something is wedged.
const probeTimeout = 3 * time.Second

// probeTelemetry POSTs one empty OTLP batch to the running proxy with the
// credential setup just wrote into every tool config.
//
// ⚠️ IT EXISTS BECAUSE SETUP CAN SUCCEED AND LEAVE THE MACHINE BROKEN, AND DID
// (2026-09-18, the maintainer's machine). ~/.keld/agent.json held telemetry
// secret 26908e20…; ~/.codex/config.toml and ~/.claude/settings.json both held
// a5629e92…, written at 17:34 by a keld 3.0.0-rc.3 still on PATH at
// /usr/local/keld/keld. A probe POST to the running proxy with the tools' token
// returned 401. Codex's telemetry was dead and Claude Code was one restart away
// from the same, and nothing said so — every tool config looked correctly
// written, because it was, with the wrong value. One request answers the
// question the file contents cannot.
//
// The batch is `{"resourceLogs":[]}`: valid OTLP carrying no records, so a proxy
// that accepts it forwards nothing.
func probeTelemetry(endpoint, secret string) (probeOutcome, int) {
	if endpoint == "" || secret == "" {
		return probeUnverified, 0
	}
	url := strings.TrimRight(endpoint, "/") + "/v1/logs"
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"resourceLogs":[]}`))
	if err != nil {
		return probeUnverified, 0
	}
	req.Header.Set("Content-Type", "application/json")
	// The shape Claude Code and Codex send. The proxy accepts three; probing with
	// the one two of the three tools use is the closest thing to asking on their
	// behalf.
	req.Header.Set("x-keld-ingest-token", secret)
	resp, err := (&http.Client{Timeout: probeTimeout}).Do(req)
	if err != nil {
		return probeUnverified, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return probeRejected, resp.StatusCode
	}
	return probeOK, resp.StatusCode
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
