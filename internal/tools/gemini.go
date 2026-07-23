// Package tools provides the Gemini CLI adapter for keld tool integrations.
package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iancoleman/orderedmap"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/telemetry"
)

// GeminiAdapter implements the Adapter interface for Gemini CLI.
//
// keld manages one artifact: ~/.gemini/settings.json — the telemetry block
// (with the ingest token carried in otlpEndpoint's ?token= query, since gemini
// can't reliably carry an auth header — see telemetry.endpointWithToken) plus
// hooks.BeforeAgent. It's carried as Plan.AfterText like every other adapter:
// Apply/Remove compute it, the caller commits it on confirm.
//
// keld does NOT write ~/.gemini/.env. keld <= v0.11.0 put an OTEL auth *header*
// block there, but gemini only honors that .env in "trusted" workspaces, so the
// token never reached Atlas in a normal directory (401 "missing ingest token").
// The token now rides in settings.json instead. For upgrading installs, Apply
// and Remove still emit a Plan.ExtraFile that *strips* any legacy keld block
// from ~/.gemini/.env (preserving GEMINI_API_KEY and every other line) so the
// stale block — and the token sitting in it — doesn't linger. The caller writes
// ExtraFile under the same confirm/--dry-run gate that guards AfterText.
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

// envPath returns the path to Gemini CLI's env file (~/.gemini/.env). keld no
// longer writes it, but reads it to clean up a legacy keld block on upgrade.
// Derived from ConfigPath's directory so the two paths always move together;
// since os.UserHomeDir honors $HOME on darwin/linux, tests can redirect both
// via t.Setenv("HOME", tmpDir).
func (a *GeminiAdapter) envPath() string {
	return filepath.Join(filepath.Dir(a.ConfigPath()), ".env")
}

// Detect reports whether the ~/.gemini directory exists (parent of config file).
func (a *GeminiAdapter) Detect() bool {
	dir := filepath.Dir(a.ConfigPath())
	_, err := os.Stat(dir)
	return err == nil
}

// Apply sets the telemetry block and the BeforeAgent hook in the Gemini CLI
// settings JSON, and (for upgrading installs) computes a Plan.ExtraFile that
// strips any legacy keld-managed block from ~/.gemini/.env. It never writes;
// the caller commits AfterText and ExtraFile under its own confirm/--dry-run
// gate. currentText is nil if the settings file is absent (created=true).
// replace is accepted for interface parity but Gemini always merges.
func (a *GeminiAdapter) Apply(currentText *string, p SetupParams, replace bool) Plan {
	text := ptrToStr(currentText)

	obj, err := config.LoadJSON(text)
	if err != nil {
		obj = orderedmap.New()
	}

	obj.Set("telemetry", telemetry.GeminiTelemetry(p))

	cmd := telemetry.HookCommand(p.BinPath, "gemini")
	config.AddClaudeHook(obj, "BeforeAgent", nil, cmd)

	after := config.DumpJSON(obj)

	envFile, envErr := a.buildEnvCleanupFile()

	managed := map[string]any{
		"keys":        []string{"telemetry"},
		"hook_substr": telemetry.HookCommandSubstr,
		"created":     currentText == nil,
	}

	summary := []string{"set telemetry block", "add BeforeAgent hook"}
	if envFile != nil {
		summary = append(summary, "remove legacy ~/.gemini/.env OTEL block")
	}

	plan := Plan{
		Name:       a.Name(),
		ConfigPath: a.ConfigPath(),
		AfterText:  after,
		Managed:    managed,
		Summary:    summary,
		Changed:    after != text,
		ExtraFile:  envFile,
	}
	// Review note (Minor, left as-is): this overloads plan.Conflict — which
	// elsewhere means "a keld block already exists and differs from what we'd
	// write" — to also carry a plain .env read I/O error. Plan has no dedicated
	// error field today, so Conflict is reused as the least-bad option.
	if envErr != nil {
		plan.Conflict = fmt.Sprintf("couldn't read ~/.gemini/.env: %v", envErr)
	}
	return plan
}

// buildEnvCleanupFile reads ~/.gemini/.env and, if it contains a legacy
// keld-managed block (written by keld <= v0.11.0, when the ingest token rode in
// an OTEL header line there), returns an *ExtraFile that removes it —
// preserving every other line, notably GEMINI_API_KEY — for the caller to
// write. It performs no writes. keld no longer writes any .env block, so this
// is pure migration cleanup: a no-op (nil) when the file is absent or carries
// no keld block. If stripping the block leaves nothing behind, the ExtraFile is
// a delete instead of a rewrite. Returns any read I/O error.
func (a *GeminiAdapter) buildEnvCleanupFile() (*ExtraFile, error) {
	path := a.envPath()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	envText := string(data)

	if !config.HasEnvBlock(envText) {
		return nil, nil
	}

	stripped := config.RemoveEnvBlock(envText)
	if strings.TrimSpace(stripped) == "" {
		return &ExtraFile{Path: path, Delete: true}, nil
	}
	return &ExtraFile{Path: path, AfterText: stripped, Mode: 0o600}, nil
}

// Remove strips the keld-managed keys and BeforeAgent hook from the Gemini CLI
// settings JSON, and (like Apply) emits a Plan.ExtraFile that strips any legacy
// keld block from ~/.gemini/.env. The caller commits ExtraFile under its own
// confirm gate.
func (a *GeminiAdapter) Remove(currentText *string, managed map[string]any) Plan {
	text := ptrToStr(currentText)

	obj, err := config.LoadJSON(text)
	if err != nil {
		obj = orderedmap.New()
	}

	// Determine which top-level keys to remove; fall back to ["telemetry"] if not in managed.
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

	hookSubstr := telemetry.HookCommandSubstr
	if v, ok := managed["hook_substr"]; ok {
		if s, ok := v.(string); ok && s != "" {
			hookSubstr = s
		}
	}
	config.RemoveHooksByCommand(obj, hookSubstr)

	var after string
	if len(obj.Keys()) > 0 {
		after = config.DumpJSON(obj)
	}

	envFile, envErr := a.buildEnvCleanupFile()

	summary := []string{"remove telemetry block", "remove BeforeAgent hook"}
	if envFile != nil {
		summary = append(summary, "remove legacy ~/.gemini/.env OTEL block")
	}

	plan := Plan{
		Name:       a.Name(),
		ConfigPath: a.ConfigPath(),
		AfterText:  after,
		Managed:    managed,
		Summary:    summary,
		Changed:    after != text,
		ExtraFile:  envFile,
	}
	if envErr != nil {
		plan.Conflict = fmt.Sprintf("couldn't read ~/.gemini/.env: %v", envErr)
	}
	return plan
}

// Status reports whether Gemini CLI is installed (Detect) and configured with
// keld's telemetry block and BeforeAgent hook. keld no longer manages
// ~/.gemini/.env, so it plays no part in the configured check.
func (a *GeminiAdapter) Status(currentText *string, managed map[string]any) ToolStatus {
	text := ptrToStr(currentText)

	obj, err := config.LoadJSON(text)
	if err != nil {
		obj = orderedmap.New()
	}

	configured := false
	if telVal, ok := obj.Get("telemetry"); ok {
		configured = hasOTLPEndpointGemini(telVal) && config.HasHookWithCommand(obj, telemetry.HookCommandSubstr)
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
