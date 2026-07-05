// Package tools provides the Gemini CLI adapter for keld tool integrations.
package tools

import (
	"os"
	"path/filepath"

	"github.com/iancoleman/orderedmap"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/telemetry"
)

// GeminiAdapter implements the Adapter interface for Gemini CLI.
type GeminiAdapter struct{}

// Name returns the internal name for Gemini CLI.
func (a *GeminiAdapter) Name() string { return "gemini" }

// DisplayName returns the human-readable name for Gemini CLI.
func (a *GeminiAdapter) DisplayName() string { return "Gemini CLI" }

// ConfigPath returns the path to Gemini CLI's settings file (~/.gemini/settings.json).
// This uses the user's home directory, not KELD_HOME.
func (a *GeminiAdapter) ConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".gemini", "settings.json")
	}
	return filepath.Join(home, ".gemini", "settings.json")
}

// Detect reports whether the ~/.gemini directory exists (parent of config file).
func (a *GeminiAdapter) Detect() bool {
	dir := filepath.Dir(a.ConfigPath())
	_, err := os.Stat(dir)
	return err == nil
}

// Apply sets the telemetry block in the Gemini CLI settings JSON.
// currentText is nil if the config file is absent (created=true).
// replace is accepted for interface parity but Gemini always merges.
func (a *GeminiAdapter) Apply(currentText *string, p SetupParams, replace bool) Plan {
	text := ""
	if currentText != nil {
		text = *currentText
	}

	obj, err := config.LoadJSON(text)
	if err != nil {
		obj = orderedmap.New()
	}

	obj.Set("telemetry", telemetry.GeminiTelemetry(p))
	after := config.DumpJSON(obj)

	return Plan{
		Name:       a.Name(),
		ConfigPath: a.ConfigPath(),
		AfterText:  after,
		Managed: map[string]any{
			"keys":    []string{"telemetry"},
			"created": currentText == nil,
		},
		Summary: []string{"set telemetry block"},
		Changed: after != (text),
	}
}

// Remove strips the keld-managed keys from the Gemini CLI settings JSON.
func (a *GeminiAdapter) Remove(currentText *string, managed map[string]any) Plan {
	text := ""
	if currentText != nil {
		text = *currentText
	}

	obj, err := config.LoadJSON(text)
	if err != nil {
		obj = orderedmap.New()
	}

	// Determine which keys to remove; fall back to ["telemetry"] if not in managed.
	keys := []string{"telemetry"}
	if v, ok := managed["keys"]; ok {
		switch ks := v.(type) {
		case []string:
			keys = ks
		case []any:
			keys = keys[:0]
			for _, k := range ks {
				if s, ok := k.(string); ok {
					keys = append(keys, s)
				}
			}
		}
	}

	for _, k := range keys {
		obj.Delete(k)
	}

	var after string
	if len(obj.Keys()) > 0 {
		after = config.DumpJSON(obj)
	}

	return Plan{
		Name:       a.Name(),
		ConfigPath: a.ConfigPath(),
		AfterText:  after,
		Managed:    managed,
		Summary:    []string{"remove telemetry block"},
		Changed:    after != text,
	}
}

// Status reports whether Gemini CLI is installed (Detect) and configured with
// keld's telemetry (otlpEndpoint present in the telemetry sub-object).
func (a *GeminiAdapter) Status(currentText *string, managed map[string]any) ToolStatus {
	text := ""
	if currentText != nil {
		text = *currentText
	}

	obj, err := config.LoadJSON(text)
	if err != nil {
		obj = orderedmap.New()
	}

	configured := false
	if telVal, ok := obj.Get("telemetry"); ok {
		configured = hasOTLPEndpointGemini(telVal)
	}

	detail := "not configured"
	if configured {
		detail = "configured"
	}

	return ToolStatus{
		Name:       a.Name(),
		Installed:  a.Detect(),
		Configured: configured,
		Detail:     detail,
	}
}

// hasOTLPEndpointGemini checks whether the telemetry value (which may be a
// *orderedmap.OrderedMap or orderedmap.OrderedMap after JSON unmarshal) contains
// the otlpEndpoint key.
func hasOTLPEndpointGemini(v any) bool {
	switch m := v.(type) {
	case *orderedmap.OrderedMap:
		_, found := m.Get("otlpEndpoint")
		return found
	case orderedmap.OrderedMap:
		_, found := m.Get("otlpEndpoint")
		return found
	}
	return false
}
