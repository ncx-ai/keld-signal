// Package tools provides the Gemini CLI adapter for keld tool integrations.
package tools

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/iancoleman/orderedmap"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/telemetry"
)

// GeminiAdapter implements the Adapter interface for Gemini CLI.
//
// Gemini CLI is the first adapter that manages TWO on-disk artifacts:
//   - ~/.gemini/settings.json — telemetry block + hooks.BeforeAgent (via Plan,
//     like every other adapter: Apply returns AfterText, the caller commits it).
//   - ~/.gemini/.env — OTEL header auth + trace-off (via Task 1's
//     config.UpsertEnvBlock/RemoveEnvBlock helpers), which never appears in
//     Plan.AfterText. The Adapter interface only carries one (path, text) pair
//     per Plan and must not change, so Apply/Remove read+write the .env file
//     directly as a side effect instead of staging it for the caller.
//
// Trade-off this implies: cli/setup.go calls adapter.Apply once per tool to
// build a diff/confirmation prompt *before* the user confirms (and unconditionally
// under --dry-run, which never reaches its later write step for settings.json).
// Because the .env write happens inside Apply itself, a declined confirmation
// or a --dry-run run will still have written the .env block. This is accepted
// for now because the write is idempotent and non-destructive (GEMINI_API_KEY
// and all other lines are preserved byte-for-byte) and inert on its own: Gemini
// only emits OTEL once settings.json's telemetry.enabled is also set, which
// only happens once the settings.json Plan is actually committed. Revisit if a
// generic multi-file Plan mechanism is ever introduced.
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

// envPath returns the path to Gemini CLI's env file (~/.gemini/.env), which
// holds the keld-managed OTEL header/trace-off block alongside the user's own
// GEMINI_API_KEY. Derived from ConfigPath's directory (not a separate
// os.UserHomeDir() call) so the two artifacts always move together; since
// os.UserHomeDir honors $HOME on darwin/linux, tests can redirect both files
// at once via t.Setenv("HOME", tmpDir) without any other plumbing.
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
// settings JSON, and writes the keld-managed OTEL block into ~/.gemini/.env
// (created fresh at 0600 if absent, since it holds a secret; existing lines,
// notably GEMINI_API_KEY, are preserved byte-for-byte — see config.UpsertEnvBlock).
// currentText is nil if the settings file is absent (created=true).
// replace is accepted for interface parity but Gemini always merges.
func (a *GeminiAdapter) Apply(currentText *string, p SetupParams, replace bool) Plan {
	text := ptrToStr(currentText)

	obj, err := config.LoadJSON(text)
	if err != nil {
		obj = orderedmap.New()
	}

	obj.Set("telemetry", telemetry.GeminiTelemetry(p))

	cmd := telemetry.HookCommand("gemini")
	config.AddClaudeHook(obj, "BeforeAgent", nil, cmd)

	after := config.DumpJSON(obj)

	envCreated, envErr := a.applyEnvBlock(p)

	managed := map[string]any{
		"keys":        []string{"telemetry"},
		"hook_substr": telemetry.HookCommandSubstr,
		"created":     currentText == nil,
		"env_created": envCreated,
	}

	plan := Plan{
		Name:       a.Name(),
		ConfigPath: a.ConfigPath(),
		AfterText:  after,
		Managed:    managed,
		Summary:    []string{"set telemetry block", "add BeforeAgent hook", "write ~/.gemini/.env OTEL block"},
		Changed:    after != text,
	}
	if envErr != nil {
		plan.Conflict = fmt.Sprintf("couldn't write ~/.gemini/.env: %v", envErr)
	}
	return plan
}

// applyEnvBlock reads ~/.gemini/.env (absent is treated as empty), upserts the
// keld OTEL block into it via config.UpsertEnvBlock (preserving every other
// line, notably GEMINI_API_KEY), and writes the result back at mode 0600.
// Returns whether the file did not previously exist (so Remove can decide
// whether to delete it once emptied) and any I/O error encountered.
func (a *GeminiAdapter) applyEnvBlock(p SetupParams) (created bool, err error) {
	path := a.envPath()

	var envText string
	data, readErr := os.ReadFile(path)
	switch {
	case readErr == nil:
		envText = string(data)
	case os.IsNotExist(readErr):
		created = true
	default:
		return false, readErr
	}

	updated := config.UpsertEnvBlock(envText, telemetry.GeminiEnvBlock(p))
	if updated == envText {
		return created, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return created, err
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		return created, err
	}
	return created, nil
}

// Remove strips the keld-managed keys and BeforeAgent hook from the Gemini CLI
// settings JSON, and strips the keld-managed block from ~/.gemini/.env
// (deleting the file only if keld created it fresh and it is now empty).
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

	envErr := a.removeEnvBlock(managed)

	plan := Plan{
		Name:       a.Name(),
		ConfigPath: a.ConfigPath(),
		AfterText:  after,
		Managed:    managed,
		Summary:    []string{"remove telemetry block", "remove BeforeAgent hook", "remove ~/.gemini/.env OTEL block"},
		Changed:    after != text,
	}
	if envErr != nil {
		plan.Conflict = fmt.Sprintf("couldn't update ~/.gemini/.env: %v", envErr)
	}
	return plan
}

// removeEnvBlock strips the keld-managed block from ~/.gemini/.env, preserving
// every other line (notably GEMINI_API_KEY). A missing file is a no-op. If
// stripping the block leaves the file empty AND keld created the file fresh
// during the corresponding Apply (managed["env_created"] == true), the file is
// deleted instead of left behind as an empty husk.
func (a *GeminiAdapter) removeEnvBlock(managed map[string]any) error {
	path := a.envPath()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	envText := string(data)

	stripped := config.RemoveEnvBlock(envText)
	if stripped == envText {
		return nil
	}

	if stripped == "" {
		if created, _ := managed["env_created"].(bool); created {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}
	}

	return os.WriteFile(path, []byte(stripped), 0o600)
}

// Status reports whether Gemini CLI is installed (Detect) and configured with
// keld's telemetry block, BeforeAgent hook, and .env OTEL block. Configured is
// true only when all three are present; Detail distinguishes a fully-configured
// state from a partial one (useful if the user hand-edits one artifact).
func (a *GeminiAdapter) Status(currentText *string, managed map[string]any) ToolStatus {
	text := ptrToStr(currentText)

	obj, err := config.LoadJSON(text)
	if err != nil {
		obj = orderedmap.New()
	}

	settingsOK := false
	if telVal, ok := obj.Get("telemetry"); ok {
		settingsOK = hasOTLPEndpointGemini(telVal) && config.HasHookWithCommand(obj, telemetry.HookCommandSubstr)
	}

	envOK := false
	if data, err := os.ReadFile(a.envPath()); err == nil {
		envOK = config.HasEnvBlock(string(data))
	}

	configured := settingsOK && envOK

	var detail string
	switch {
	case configured:
		detail = "configured"
	case settingsOK && !envOK:
		detail = "settings configured; .env block missing"
	case envOK && !settingsOK:
		detail = ".env block present; settings not configured"
	default:
		detail = "not configured"
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
