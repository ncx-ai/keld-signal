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
const EnvWorkstreamsFile = "KELD_PROJECTS_FILE"

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
