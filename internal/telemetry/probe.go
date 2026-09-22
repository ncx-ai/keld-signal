package telemetry

import (
	"net/http"
	"strings"
	"time"
)

// ProbeOutcome is what asking the running loopback proxy about a credential
// found.
type ProbeOutcome int

const (
	// ProbeOK — the running proxy accepted the credential.
	ProbeOK ProbeOutcome = iota
	// ProbeUnverified — nothing answered on the loopback port. NOT a failure
	// for `keld signal setup`: `keld-agent install` registers and starts the
	// service AFTER setup runs, and the macOS wizard onboards before the daemon
	// exists at all, so the ordinary first install has nothing listening.
	//
	// ⚠️ It IS a refusal for the daemon's own repair path, and the asymmetry is
	// the same one auto-update draws against install.sh: setup has a human
	// reading its output who can act on "could not verify", and a background
	// poll rewriting a tool's config unattended does not.
	ProbeUnverified
	// ProbeRejected — the proxy answered 401/403. The machine WOULD have been
	// broken.
	ProbeRejected
)

// ProbeTimeout bounds the probe. It is one request to 127.0.0.1 against a
// handler that authenticates and returns immediately; neither a setup run nor a
// detector poll must sit on it if something is wedged.
const ProbeTimeout = 3 * time.Second

// ProbeSecret POSTs one empty OTLP batch to the running proxy with the
// credential a caller is about to write (or has just written) into tool configs.
//
// ⚠️ IT EXISTS BECAUSE A WRITE CAN SUCCEED AND LEAVE THE MACHINE BROKEN, AND DID
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
//
// ⚠️ IT LIVES HERE RATHER THAN IN internal/cli BECAUSE TWO CALLERS ASK IT. The
// CLI asks after writing every tool's config; the daemon's integrations detector
// asks BEFORE rewriting keld's own block in one. Two copies of "does the running
// proxy accept this value" are two ways for setup and the daemon to disagree
// about the same machine.
func ProbeSecret(endpoint, secret string) (ProbeOutcome, int) {
	if endpoint == "" || secret == "" {
		return ProbeUnverified, 0
	}
	url := strings.TrimRight(endpoint, "/") + "/v1/logs"
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"resourceLogs":[]}`))
	if err != nil {
		return ProbeUnverified, 0
	}
	req.Header.Set("Content-Type", "application/json")
	// The shape Claude Code and Codex send. The proxy accepts three; probing with
	// the one two of the three tools use is the closest thing to asking on their
	// behalf.
	req.Header.Set("x-keld-ingest-token", secret)
	resp, err := (&http.Client{Timeout: ProbeTimeout}).Do(req)
	if err != nil {
		return ProbeUnverified, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ProbeRejected, resp.StatusCode
	}
	return ProbeOK, resp.StatusCode
}
