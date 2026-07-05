// hook_config.go — read/write ~/.keld/hook.json for the keld __hook subcommand.
package config

import (
	"bytes"
	"encoding/json"
	"os"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// hookConfigFile is the on-disk JSON structure of ~/.keld/hook.json.
type hookConfigFile struct {
	Endpoint    string `json:"endpoint"`
	IngestToken string `json:"ingest_token"`
}

// SaveHookConfig writes endpoint and ingestToken to ~/.keld/hook.json with
// mode 0600 (2-space indent, trailing newline, no HTML escaping).
func SaveHookConfig(endpoint, ingestToken string) error {
	if err := os.MkdirAll(paths.KeldHome(), 0o755); err != nil {
		return err
	}

	hf := hookConfigFile{
		Endpoint:    endpoint,
		IngestToken: ingestToken,
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(hf); err != nil {
		return err
	}

	return os.WriteFile(paths.HookConfigPath(), buf.Bytes(), 0o600)
}
