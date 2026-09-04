package localagent

import (
	"encoding/json"
	"fmt"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// THE TWO HALVES OF AN INSTALL, AS DOCTOR SEES THEM.
//
// ⚠️ `keld` AND THE ANALYSIS SIDECAR SHIP AS SEPARATE ARTIFACTS, and nothing
// compared them until this file. The macOS pkg cannot carry the sidecar past
// Apple's notarization, so the installer fetches it — and an upgrade over an
// earlier install used to keep whatever sidecar was already on disk. Measured:
// a 2.3.0 agent ran ~3 weeks against an Aug 11 sidecar with no `/blocks` route,
// which 404'd, which the block emitter read as "no blocks closed yet". Telemetry
// flowed, blocks stopped, and THIS COMMAND SAID "No problems found." the whole
// time — correctly, because every fact it could reach was fine. See
// docs/superpowers/specs/2026-09-04-sidecar-version-skew-discovery.md.
//
// ⚠️ THIS ONE IS A LIVE PROBE, WHERE THE MODEL AND TELEMETRY STATES READ DISK,
// and the asymmetry is forced rather than sloppy. Presence of a model is a
// filesystem fact, so reading disk makes daemon reachability irrelevant. A
// running sidecar's BUILD is not on disk anywhere the CLI can find it: the
// binary is a ~15,000-file frozen tree whose path the daemon resolved at spawn
// (`sidecarBinPath` searches three directories, and `KELD_SIDECAR_BIN` can
// override all of them), so the only honest source is the process that is
// actually serving. The cost is that an unreachable sidecar yields no answer —
// which is why Known exists and why silence, not a finding, is what an
// unreachable sidecar produces.

// SidecarVersionState is the comparison doctor reports on.
type SidecarVersionState struct {
	// Daemon is this binary's version; Sidecar is what the running sidecar
	// answered on /health.
	Daemon  string
	Sidecar string
	// Known is false when there was nothing conclusive to compare: no daemon,
	// no sidecar, an unreachable one, one too old to carry the field, or either
	// half reporting "dev". ⚠️ AN UNREACHABLE SIDECAR MUST NEVER RENDER AS SKEW
	// — that is the `thin`/`absent` discipline the rest of this package runs on,
	// and inventing a finding here would send someone to reinstall over what is
	// usually just a stopped daemon.
	Known bool
}

// SidecarVersion asks the running sidecar what build it is. Dependencies are
// injected for testability, matching Health.
func SidecarVersion(info *agentcfg.Info, fetchFn func(string) (string, error)) SidecarVersionState {
	s := SidecarVersionState{Daemon: version.CLI}
	url, err := HealthURL(info)
	if err != nil {
		return s // no daemon, or no sidecar this run: nothing to compare
	}
	body, err := fetchFn(url)
	if err != nil {
		return s
	}
	var h struct {
		Version string `json:"version"`
	}
	if json.Unmarshal([]byte(body), &h) != nil {
		return s
	}
	s.Sidecar = h.Version
	// Skew's own rule decides whether this pair is comparable at all, so the
	// daemon and this command can never disagree about what counts as skew.
	_, known := version.Skew(s.Daemon, s.Sidecar)
	s.Known = known
	return s
}

// HealthURL returns the sidecar's /health URL, the sibling of MetricsURL.
// /health rather than /metrics because it is the one route that answers with
// the inference worker down — which is every deterministic-mode machine, i.e.
// every v2 install.
func HealthURL(info *agentcfg.Info) (string, error) {
	u, err := MetricsURL(info)
	if err != nil {
		return "", err
	}
	return u[:len(u)-len("/metrics")] + "/health", nil
}

// ProblemLine returns doctor's finding, or "" when there is nothing to report.
//
// Reports ONLY when the comparison is conclusive AND the two disagree. A silent
// return covers every other case, including the one that looks most like a
// problem: a sidecar that will not answer. See Known.
func (s SidecarVersionState) ProblemLine() string {
	skewed, known := version.Skew(s.Daemon, s.Sidecar)
	if !known || !skewed {
		return ""
	}
	// Names the installer, not `keld signal restart`: restarting changes nothing
	// when the wrong artifact is on disk, and a remedy that cannot work is worse
	// than none — it spends the reader's trust as well as their time.
	return fmt.Sprintf(
		"version skew — keld is %s but the analysis sidecar is %s. They ship separately, "+
			"so a route this version needs may be missing from that sidecar and the work "+
			"goes missing silently (this is how blocks stop while telemetry keeps flowing). "+
			"Re-run the Keld installer to update the sidecar: "+
			"macOS `/usr/local/keld/onboard.command`, Linux `curl -fsSL https://keld.co/install.sh | sh`.",
		s.Daemon, s.Sidecar)
}
