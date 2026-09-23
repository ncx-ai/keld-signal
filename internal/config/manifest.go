// internal/config/manifest.go
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// HookRecord records the installed Keld hook. All three fields are preserved
// for backward-compat with manifests written by the old Python CLI.
type HookRecord struct {
	Path    string `json:"path"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// ToolManifest records what Keld configured for one tool.
type ToolManifest struct {
	Name       string         `json:"name"`
	ConfigPath string         `json:"config_path"`
	Managed    map[string]any `json:"managed"`
	BackupPath *string        `json:"backup_path"`
	// ConfiguredAt is when KELD last wrote this tool's config, and it is the
	// only record of that instant anywhere on the machine.
	//
	// ⚠️ THE TOOL'S OWN CONFIG MTIME IS NOT THAT INSTANT, and reading it as one
	// made a tool nobody had touched ask for a restart. Measured on the
	// maintainer's machine 2026-09-18: Codex writes `hooks.state` trust entries
	// into its own config.toml at session start (observed 20:56) and Claude Code
	// rewrote settings.json on its own (observed 21:21). Both moved the mtime
	// the integrations rule was treating as "when Signal configured this tool",
	// so the row flipped to `restart_required` off the tool's own housekeeping.
	//
	// Written by every path that applies an adapter — `keld signal setup` and
	// the daemon's integrations detector, which share `integrations.ApplyEntry`.
	// A POINTER, and nil on every manifest written before this field existed:
	// those machines fall back to the manifest file's mtime and then to the
	// tool config's, in that order (see integrations.ReadWiring). A zero time
	// would be indistinguishable from "keld wrote this at the epoch".
	ConfiguredAt *time.Time `json:"configured_at"`
}

// Manifest is Keld's own state file (~/.keld/manifest.json).
// Optional scalars are pointers so they serialize as JSON null when unset,
// matching Python's to_dict output exactly (no omitempty).
type Manifest struct {
	Endpoint *string                 `json:"endpoint"`
	Actor    *string                 `json:"actor"`
	Tools    map[string]ToolManifest `json:"tools"`
	Hook     *HookRecord             `json:"hook"`
}

// LoadManifest reads ~/.keld/manifest.json. If the file is missing, a fresh
// Manifest with a non-nil empty Tools map is returned (no error).
func LoadManifest() (*Manifest, error) {
	data, err := os.ReadFile(paths.ManifestPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Manifest{Tools: map[string]ToolManifest{}}, nil
		}
		return nil, err
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m.Tools == nil {
		m.Tools = map[string]ToolManifest{}
	}
	return &m, nil
}

// Save writes the manifest to ~/.keld/manifest.json with 2-space indent, a
// trailing newline, and no HTML escaping — matching Python's json.dumps output.
func (m *Manifest) Save() error {
	if err := os.MkdirAll(paths.KeldHome(), 0o755); err != nil {
		return err
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return err
	}
	// json.Encoder.Encode already appends a newline; the result is correct.
	return os.WriteFile(paths.ManifestPath(), buf.Bytes(), 0o644)
}
