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
// Gemini CLI manages TWO on-disk artifacts:
//   - ~/.gemini/settings.json — telemetry block + hooks.BeforeAgent, carried
//     as Plan.AfterText like every other adapter: Apply/Remove compute it,
//     the caller commits it on confirm.
//   - ~/.gemini/.env — OTEL header auth + trace-off (via config.UpsertEnvBlock/
//     RemoveEnvBlock), carried as Plan.ExtraFile. Apply/Remove only *read* the
//     current .env (to preserve GEMINI_API_KEY and any other lines
//     byte-for-byte) and compute the new text; they never write it. The
//     caller (internal/cli/setup.go, internal/cli/uninstall.go) writes
//     ExtraFile to disk under the same confirm/--dry-run gate that guards
//     AfterText, so a --dry-run run or a declined confirmation leaves the
//     real ~/.gemini/.env untouched.
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
// settings JSON, and computes (without writing) the keld-managed OTEL block
// for ~/.gemini/.env, returned via Plan.ExtraFile (created fresh at 0600 if
// absent, since it holds a secret; existing lines, notably GEMINI_API_KEY,
// are preserved byte-for-byte — see config.UpsertEnvBlock). The caller is
// responsible for actually writing ExtraFile to disk, under its own
// confirm/--dry-run gate. currentText is nil if the settings file is absent
// (created=true). replace is accepted for interface parity but Gemini always
// merges.
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

	envFile, envCreated, envErr := a.buildEnvApplyFile(p)

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
		ExtraFile:  envFile,
	}
	// Review note (Minor, left as-is): this overloads plan.Conflict — which
	// elsewhere in this Plan means "a keld block already exists and differs
	// from what we'd write" — to also carry a plain .env read I/O error.
	// Plan has no dedicated error field today, so Conflict is reused as the
	// least-bad option; a real fix would add one, which is out of scope for
	// this change.
	if envErr != nil {
		plan.Conflict = fmt.Sprintf("couldn't read ~/.gemini/.env: %v", envErr)
	}
	return plan
}

// buildEnvApplyFile reads ~/.gemini/.env (absent is treated as empty) and
// upserts the keld OTEL block into it via config.UpsertEnvBlock (preserving
// every other line, notably GEMINI_API_KEY), returning the result as an
// *ExtraFile for the caller to write — this function itself performs no
// writes. Returns nil for ef when the computed text is unchanged (nothing to
// write). Also returns whether the file did not previously exist (so Remove
// can later decide whether to delete it once emptied) and any I/O error
// encountered while reading.
func (a *GeminiAdapter) buildEnvApplyFile(p SetupParams) (ef *ExtraFile, created bool, err error) {
	path := a.envPath()

	var envText string
	data, readErr := os.ReadFile(path)
	switch {
	case readErr == nil:
		envText = string(data)
	case os.IsNotExist(readErr):
		created = true
	default:
		return nil, false, readErr
	}

	updated := config.UpsertEnvBlock(envText, telemetry.GeminiEnvBlock(p))
	if updated == envText {
		return nil, created, nil
	}

	return &ExtraFile{Path: path, AfterText: updated, Mode: 0o600}, created, nil
}

// Remove strips the keld-managed keys and BeforeAgent hook from the Gemini CLI
// settings JSON, and computes (without writing) the stripped ~/.gemini/.env
// text, returned via Plan.ExtraFile (with Delete set instead if keld created
// the file fresh and it is now empty). The caller is responsible for actually
// writing/deleting ExtraFile, under its own confirm gate.
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

	envFile, envErr := a.buildEnvRemoveFile(managed)

	plan := Plan{
		Name:       a.Name(),
		ConfigPath: a.ConfigPath(),
		AfterText:  after,
		Managed:    managed,
		Summary:    []string{"remove telemetry block", "remove BeforeAgent hook", "remove ~/.gemini/.env OTEL block"},
		Changed:    after != text,
		ExtraFile:  envFile,
	}
	// Review note (Minor, left as-is): see the matching note in Apply — this
	// reuses plan.Conflict for a plain .env read I/O error rather than a
	// real config-conflict; a dedicated error field would be cleaner but is
	// out of scope here.
	if envErr != nil {
		plan.Conflict = fmt.Sprintf("couldn't read ~/.gemini/.env: %v", envErr)
	}
	return plan
}

// buildEnvRemoveFile reads ~/.gemini/.env and computes the text with the
// keld-managed block stripped out, preserving every other line (notably
// GEMINI_API_KEY) — it performs no writes itself. A missing file is a no-op
// (nil, nil). If stripping the block leaves the file empty AND keld created
// the file fresh during the corresponding Apply (managed["env_created"] ==
// true), the returned ExtraFile has Delete set instead of AfterText, so the
// caller removes the file rather than leaving an empty husk behind.
func (a *GeminiAdapter) buildEnvRemoveFile(managed map[string]any) (*ExtraFile, error) {
	path := a.envPath()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	envText := string(data)

	stripped := config.RemoveEnvBlock(envText)
	if stripped == envText {
		return nil, nil
	}

	if stripped == "" {
		if created, _ := managed["env_created"].(bool); created {
			return &ExtraFile{Path: path, Delete: true}, nil
		}
	}

	return &ExtraFile{Path: path, AfterText: stripped, Mode: 0o600}, nil
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
