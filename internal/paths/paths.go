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

func AuthPath() string              { return filepath.Join(KeldHome(), "auth.json") }
func ManifestPath() string          { return filepath.Join(KeldHome(), "manifest.json") }
func HookConfigPath() string        { return filepath.Join(KeldHome(), "hook.json") }
func AgentInfoPath() string         { return filepath.Join(KeldHome(), "agent.json") }
func AgentConfigPath() string       { return filepath.Join(KeldHome(), "agent-config.json") }
func DebugLogPath() string          { return filepath.Join(KeldHome(), "agent.log") }
func StateDir() string              { return filepath.Join(KeldHome(), "state") }
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

// ClientEventsSpoolDir is where the clientevents Reporter spools batches that
// failed to POST to Atlas (e.g. Atlas unreachable), for a later drain sweep.
func ClientEventsSpoolDir() string { return filepath.Join(SpoolDir(), "clientevents") }

func APIBase() string {
	if apiOverrideSet {
		return apiOverride
	}
	if v := os.Getenv("KELD_API_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultAPIURL
}
