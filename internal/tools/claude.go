// Package tools provides the tool adapter interface and shared types for keld
// tool integrations (Claude Code, Codex, Gemini).
package tools

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/iancoleman/orderedmap"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/telemetry"
)

// ClaudeAdapter implements the Adapter interface for Claude Code.
type ClaudeAdapter struct{}

// Name returns the internal name for Claude Code.
func (a *ClaudeAdapter) Name() string { return "claude_code" }

// DisplayName returns the human-readable name for Claude Code.
func (a *ClaudeAdapter) DisplayName() string { return "Claude Code" }

// ConfigPath returns the path to Claude Code's settings file (~/.claude/settings.json).
// This uses the user's home directory, not KELD_HOME.
func (a *ClaudeAdapter) ConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".claude", "settings.json")
	}
	return filepath.Join(home, ".claude", "settings.json")
}

// Detect reports whether the ~/.claude directory exists (Claude Code is installed).
func (a *ClaudeAdapter) Detect() bool {
	dir := filepath.Dir(a.ConfigPath())
	_, err := os.Stat(dir)
	return err == nil
}

// Apply merges keld OTEL environment variables and hooks into the Claude Code
// settings JSON. If currentText is nil the config file is absent (created=true).
func (a *ClaudeAdapter) Apply(currentText *string, p SetupParams, replace bool) Plan {
	text := ""
	if currentText != nil {
		text = *currentText
	}

	obj, err := config.LoadJSON(text)
	if err != nil {
		// Return an error plan on invalid JSON
		return Plan{
			Name:       a.Name(),
			ConfigPath: a.ConfigPath(),
			Conflict:   fmt.Sprintf("invalid JSON: %v", err),
		}
	}

	envKeys := config.MergeEnv(obj, telemetry.ClaudeEnv(p))

	// Strip existing keld hooks before re-adding, so re-running setup is
	// idempotent even when the command STRING changes (bare "keld" → pinned
	// absolute path) — otherwise the changed command leaves the old entries and
	// appends new ones (duplicate keld hooks). See RemoveHooksByCommand.
	command := telemetry.HookCommand(p.BinPath, "claude_code")
	config.RemoveHooksByCommand(obj, telemetry.HookCommandSubstr)
	for _, he := range telemetry.ClaudeHookEvents {
		config.AddClaudeHook(obj, he.Event, he.Matcher, command)
	}

	after := config.DumpJSON(obj)

	managed := map[string]any{
		"env_keys":    envKeys,
		"hook_substr": telemetry.HookCommandSubstr,
		"created":     currentText == nil,
	}

	return Plan{
		Name:       a.Name(),
		ConfigPath: a.ConfigPath(),
		AfterText:  after,
		Managed:    managed,
		Summary: []string{
			fmt.Sprintf("set %d OTEL env vars", len(envKeys)),
			"add SessionStart + CwdChanged + UserPromptSubmit hooks",
		},
		Changed: after != (text),
	}
}

// Remove strips keld-managed env vars and hooks from the Claude Code settings JSON.
func (a *ClaudeAdapter) Remove(currentText *string, managed map[string]any) Plan {
	text := ""
	if currentText != nil {
		text = *currentText
	}

	obj, err := config.LoadJSON(text)
	if err != nil {
		return Plan{
			Name:       a.Name(),
			ConfigPath: a.ConfigPath(),
			Conflict:   fmt.Sprintf("invalid JSON: %v", err),
		}
	}

	// Extract env_keys from managed
	var envKeys []string
	if v, ok := managed["env_keys"]; ok {
		switch keys := v.(type) {
		case []string:
			envKeys = keys
		case []any:
			for _, k := range keys {
				if s, ok := k.(string); ok {
					envKeys = append(envKeys, s)
				}
			}
		}
	}

	// Extract hook_substr from managed, fall back to constant
	hookSubstr := telemetry.HookCommandSubstr
	if v, ok := managed["hook_substr"]; ok {
		if s, ok := v.(string); ok && s != "" {
			hookSubstr = s
		}
	}

	config.RemoveSectionKeys(obj, "env", envKeys)
	config.RemoveHooksByCommand(obj, hookSubstr)

	var after string
	if len(obj.Keys()) > 0 {
		after = config.DumpJSON(obj)
	}

	return Plan{
		Name:       a.Name(),
		ConfigPath: a.ConfigPath(),
		AfterText:  after,
		Managed:    managed,
		Summary:    []string{"remove Keld env vars and hooks"},
		Changed:    after != text,
	}
}

// Status reports whether Claude Code is installed (Detect) and configured with
// keld's OTEL env vars and hooks.
func (a *ClaudeAdapter) Status(currentText *string, managed map[string]any) ToolStatus {
	text := ""
	if currentText != nil {
		text = *currentText
	}

	obj, err := config.LoadJSON(text)
	if err != nil {
		return ToolStatus{
			Name:      a.Name(),
			Installed: a.Detect(),
			Detail:    fmt.Sprintf("invalid JSON: %v", err),
		}
	}

	// configured = OTEL_EXPORTER_OTLP_ENDPOINT present in env AND keld hook present
	configured := false
	if envVal, ok := obj.Get("env"); ok {
		configured = hasOTLPEndpoint(envVal) && config.HasHookWithCommand(obj, telemetry.HookCommandSubstr)
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

// hasOTLPEndpoint checks whether the env value (which may be a *orderedmap.OrderedMap
// or orderedmap.OrderedMap) contains the OTEL_EXPORTER_OTLP_ENDPOINT key.
// After JSON unmarshal, orderedmap stores sub-maps as value type orderedmap.OrderedMap
// (not pointer), so both forms are handled.
func hasOTLPEndpoint(v any) bool {
	switch m := v.(type) {
	case *orderedmap.OrderedMap:
		_, found := m.Get("OTEL_EXPORTER_OTLP_ENDPOINT")
		return found
	case orderedmap.OrderedMap:
		_, found := m.Get("OTEL_EXPORTER_OTLP_ENDPOINT")
		return found
	}
	return false
}
