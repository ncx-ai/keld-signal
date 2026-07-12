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

// SpoolDir is the on-disk queue of undelivered enrich pointers (hook writes,
// daemon drains). Sibling of models/ under KELD_HOME.
func SpoolDir() string { return filepath.Join(KeldHome(), "spool") }

func APIBase() string {
	if apiOverrideSet {
		return apiOverride
	}
	if v := os.Getenv("KELD_API_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultAPIURL
}
