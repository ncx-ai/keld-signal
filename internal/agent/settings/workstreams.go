package settings

import (
	"encoding/json"
	"fmt"
	"os"
)

// RemoteWorkstream is one org project definition, distributed via the settings
// document (or KELD_PROJECTS_FILE while Atlas does not serve the key).
// Descriptions flow DOWN to the device; only project IDs ever flow up.
type RemoteWorkstream struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Team        string   `json:"team,omitempty"`
	Repos       []string `json:"repos,omitempty"`
	Keywords    []string `json:"keywords,omitempty"`
	TicketKey   string   `json:"ticket_key,omitempty"`
}

// EnvWorkstreamsFile points at a local JSON array of RemoteWorkstream — the mock
// path for tests and the smoke runbook. It WINS over the remote key so a
// local run is reproducible regardless of org state.
const EnvWorkstreamsFile = "KELD_WORKSTREAMS_FILE"

// EnvWorkstreamsFileLegacy is the pre-rename name, still honoured when the new
// one is unset: runbooks, smoke scripts and MDM payloads outlive a release.
const EnvWorkstreamsFileLegacy = "KELD_PROJECTS_FILE"

// WorkstreamsFileFromEnv returns the workstream-list file the environment names
// and the variable that named it — the new name first, then the legacy one.
// ("", "") when neither is set.
func WorkstreamsFileFromEnv() (path, name string) {
	if p := os.Getenv(EnvWorkstreamsFile); p != "" {
		return p, EnvWorkstreamsFile
	}
	if p := os.Getenv(EnvWorkstreamsFileLegacy); p != "" {
		return p, EnvWorkstreamsFileLegacy
	}
	return "", ""
}

// LoadWorkstreamsFile reads a strict JSON array of project definitions.
// A missing or malformed file is an error, never an empty list — silence
// here would make "attribution never ran" indistinguishable from "no
// projects defined".
func LoadWorkstreamsFile(path string) ([]RemoteWorkstream, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []RemoteWorkstream
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("projects file %s: %w", path, err)
	}
	return out, nil
}
