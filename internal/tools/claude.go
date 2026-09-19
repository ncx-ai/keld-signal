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

	// ⚠️ THE KEYS ARE THE SAME LIST IN BOTH POSITIONS, AND THAT IS WHAT MAKES
	// THE SWITCH REVERSIBLE. With the lane on they are merged in; with it off
	// they are taken back out, so a machine an earlier keld configured loses
	// the block on its next apply rather than keeping a stale exporter nobody
	// asked for. Either way the manifest records the full list, so
	// `keld signal uninstall` strips it whichever position the machine was in.
	envKeys := telemetry.ClaudeEnvKeys()
	if p.ToolOTLP {
		config.MergeEnv(obj, telemetry.ClaudeEnv(p))
	} else {
		config.RemoveSectionKeys(obj, "env", envKeys)
	}

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

	otel := otelOffSummary
	if p.ToolOTLP {
		otel = fmt.Sprintf("set %d OTEL env vars", len(envKeys))
	}

	return Plan{
		Name:       a.Name(),
		ConfigPath: a.ConfigPath(),
		AfterText:  after,
		Managed:    managed,
		Summary: []string{
			otel,
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

	config.RemoveSectionKeys(obj, "env", envKeys)
	removeKeldHooks(obj, managed)

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

// otelOffSummary is the line every adapter's setup summary prints for the OTLP
// lane while the switch is off. Shared so the three adapters say the same
// thing, and worded as the ACTION because that is what the apply performs on a
// machine an earlier keld configured: a summary reading "set 6 OTEL env vars"
// beside a write that removes them is the control-says-one-thing-does-another
// failure this page exists to prevent.
const otelOffSummary = "remove Keld's OTEL settings (extended tool telemetry is off)"

// Status reports whether Claude Code is installed (Detect) and configured for
// keld.
//
// ⚠️ **CONFIGURED IS THE HOOK, NOT THE OTEL BLOCK, SINCE THE OTLP LANE BECAME
// OPT-IN.** It used to require both. With `tool_otlp` off — the default — keld
// deliberately writes no OTEL block, so demanding one would report every
// correctly-configured machine as unconfigured: the detector would re-apply the
// adapter on every poll forever, and `keld signal doctor` would print a drift
// finding on a healthy install. The OTLP lane is reported separately, in
// ToolStatus.OTLP, which is a fact about the file rather than a verdict on it.
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

	configured := config.HasHookWithCommand(obj, telemetry.HookCommandSubstr)
	otlp := false
	if envVal, ok := obj.Get("env"); ok {
		otlp = hasOTLPEndpoint(envVal)
	}

	detail := "not configured"
	if configured {
		detail = "configured"
	}

	return ToolStatus{
		Name:       a.Name(),
		Installed:  a.Detect(),
		Configured: configured,
		OTLP:       otlp,
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
