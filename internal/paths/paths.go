package paths

import (
	"os"
	"path/filepath"
	"strings"
)

const DefaultAPIURL = "https://atlas.keld.co"

var (
	apiOverride    string
	apiOverrideSet bool
)

func SetAPIBaseOverride(url string) {
	if url == "" {
		apiOverride, apiOverrideSet = "", false
		return
	}
	apiOverride, apiOverrideSet = strings.TrimRight(url, "/"), true
}

func APIBaseOverride() string { return apiOverride }

func KeldHome() string {
	if v := os.Getenv("KELD_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".keld")
}

func AuthPath() string        { return filepath.Join(KeldHome(), "auth.json") }
func ManifestPath() string    { return filepath.Join(KeldHome(), "manifest.json") }
func HookConfigPath() string  { return filepath.Join(KeldHome(), "hook.json") }
func AgentInfoPath() string   { return filepath.Join(KeldHome(), "agent.json") }
func AgentConfigPath() string { return filepath.Join(KeldHome(), "agent-config.json") }
func DebugLogPath() string    { return filepath.Join(KeldHome(), "agent.log") }
func AgentLogDir() string     { return filepath.Join(KeldHome(), "logs") }
func AgentStdoutLog() string  { return filepath.Join(AgentLogDir(), "agent.out.log") }
func AgentStderrLog() string  { return filepath.Join(AgentLogDir(), "agent.err.log") }
func StateDir() string        { return filepath.Join(KeldHome(), "state") }

// PromptLengthsPath holds the streaming prompt-length distribution the daemon
// uses to size enrichment's input truncation (see agent/enrich/lenstat).
// Lengths only — never prompt text.
func PromptLengthsPath() string     { return filepath.Join(StateDir(), "prompt-lengths.json") }
func BackupsDir() string            { return filepath.Join(KeldHome(), "backups") }
func ModelsDir(model string) string { return filepath.Join(KeldHome(), "models", model) }
func InstallIDPath() string         { return filepath.Join(KeldHome(), "install-id") }

// ReauthMarkerPath is the local "re-authentication required" marker written
// by the daemon self-heal reauther when the CLI token itself is gone/revoked
// (Onboarding 401/403, or no auth.json). Its presence is the only local
// visibility channel once Atlas can no longer be reached with the stored
// credentials; `keld-agent status` / `keld signal status`/`doctor` read it.
func ReauthMarkerPath() string { return filepath.Join(KeldHome(), "reauth-required") }

// ReauthRequired reports whether the daemon has written the local
// "re-authentication required" marker (see ReauthMarkerPath) and, if so,
// returns its contents (a human message + timestamp). Any error reading the
// file (missing, unreadable, etc.) is treated as "not required" — this is a
// best-effort, read-only surface; the daemon alone manages the marker.
func ReauthRequired() (bool, string) {
	data, err := os.ReadFile(ReauthMarkerPath())
	if err != nil {
		return false, ""
	}
	return true, string(data)
}

// SpoolDir is the on-disk queue of undelivered enrich pointers (hook writes,
// daemon drains). Sibling of models/ under KELD_HOME.
func SpoolDir() string { return filepath.Join(KeldHome(), "spool") }

// UpdateDir holds the auto-update marker and any staged-but-uncommitted work.
func UpdateDir() string { return filepath.Join(KeldHome(), "update") }

// UpdateStatePath is the auto-update marker: which version was applied, what
// it displaced, and whether it has been confirmed. Read at every daemon start.
func UpdateStatePath() string { return filepath.Join(UpdateDir(), "state.json") }

// WatchDir holds the transcript watcher's persisted per-file byte cursors.
// Sibling of spool/ and models/ under KELD_HOME.
func WatchDir() string { return filepath.Join(KeldHome(), "watch") }

// ClientEventsSpoolDir is where the clientevents Reporter spools batches that
// failed to POST to Atlas (e.g. Atlas unreachable), for a later drain sweep.
func ClientEventsSpoolDir() string { return filepath.Join(SpoolDir(), "clientevents") }

// TelemetrySpoolDir is where the loopback telemetry proxy spools OTLP batches
// that could not reach Atlas. Its OWN directory: a shared one would cross-post
// bodies between routes, which the features path already documents as a trap.
func TelemetrySpoolDir() string { return filepath.Join(SpoolDir(), "telemetry") }

// FeaturesSpoolDir is where THE SIGNAL-EMBEDDINGS PATH's reporter spools
// batches that failed to POST, for a later drain sweep.
//
// ⚠️ A DIRECTORY OF ITS OWN, NOT A SHARE WITH clientevents. A drain sweep
// re-posts every *.json it finds to ITS OWN endpoint, so two paths sharing a
// spool dir would post each other's bodies to each other's routes — feature
// vectors to /v1/signal/client-events and back — where each is a 400, which
// the transport classifies as permanent and deletes. The separation is what
// makes the drain sweeps independent, and it also keeps the two drop-oldest
// caps from evicting each other's batches: these rows are ~1.4 KB against a
// client event's tens of bytes.
func FeaturesSpoolDir() string { return filepath.Join(SpoolDir(), "features") }

func APIBase() string {
	if apiOverrideSet {
		return apiOverride
	}
	if v := os.Getenv("KELD_API_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultAPIURL
}
