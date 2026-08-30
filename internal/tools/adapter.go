// Package tools provides the tool adapter interface and shared types for keld
// tool integrations (Claude Code, Codex, Gemini).
package tools

import (
	"os"

	"github.com/iancoleman/orderedmap"

	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/telemetry"
)

// SetupParams is a type alias to telemetry.SetupParams to avoid import cycles
// while keeping the tools package ergonomic.
type SetupParams = telemetry.SetupParams

// ExtraFile describes a second on-disk artifact (beyond ConfigPath/AfterText)
// that an adapter's Apply/Remove needs written or deleted. Adapters must stay
// pure: they compute AfterText (or set Delete) by reading the current file
// contents, but never write to disk themselves. The caller (internal/cli's
// setup/uninstall flows) performs the actual write, gated behind the same
// confirm/--dry-run logic that already guards Plan.AfterText.
type ExtraFile struct {
	Path      string
	AfterText string
	Mode      os.FileMode
	Delete    bool
}

// Plan describes the result of an Apply or Remove operation on a tool's
// configuration.
type Plan struct {
	Name       string
	ConfigPath string
	AfterText  string
	Managed    map[string]any
	Summary    []string
	Changed    bool
	Conflict   string // Empty string means no conflict (Python's None)

	// ExtraFile carries a second file (e.g. Gemini's ~/.gemini/.env) whose
	// write must be gated by the same caller-side confirm/--dry-run logic as
	// AfterText. Nil when the adapter manages only one file, or when this
	// Apply/Remove call produced no change to the extra file.
	ExtraFile *ExtraFile
}

// ToolStatus describes the installation and configuration state of a tool.
type ToolStatus struct {
	Name       string
	Installed  bool
	Configured bool
	Detail     string
}

// Adapter is the interface that tool adapters (Claude Code, Codex, Gemini) must
// implement to support keld integration.
type Adapter interface {
	// Name returns the tool's internal name (e.g., "claude_code", "codex", "gemini").
	Name() string

	// DisplayName returns the human-readable display name of the tool.
	DisplayName() string

	// Detect checks whether the tool is detected on the system.
	Detect() bool

	// ConfigPath returns the path to the tool's configuration file.
	ConfigPath() string

	// Apply applies keld telemetry integration to the tool's config.
	// currentText is nil if the config file is absent.
	// replace controls whether to replace the entire config (true) or merge (false).
	Apply(currentText *string, p SetupParams, replace bool) Plan

	// Remove removes keld telemetry integration from the tool's config.
	// currentText is nil if the config file is absent.
	Remove(currentText *string, managed map[string]any) Plan

	// Status returns the current installation and configuration status of the tool.
	// currentText is nil if the config file is absent.
	Status(currentText *string, managed map[string]any) ToolStatus
}

// removeKeldHooks strips keld-owned hook entries from obj during teardown.
//
// ⚠️ IT REMOVES BY TWO RECOGNIZERS, AND THE SECOND IS THE REPAIR. Teardown
// prefers the `hook_substr` RECORDED IN THE MANIFEST at setup time, so that a
// config written by an older keld is torn down by that keld's own recognizer.
// Every manifest written before the HookCommandSubstr fix records the Unix-only
// "keld __hook" — which matches nothing on Windows (see the ⚠️ on that constant)
// — so honouring the manifest ALONE would leave those machines' hooks in place
// forever, uninstall after uninstall. Correcting the constant does not reach
// them; this does.
//
// Removing by both is safe rather than merely convenient: RemoveHooksByCommand
// deletes only entries whose command contains the substring, and both
// recognizers are keld-specific, so a second pass can only remove keld's own
// hooks. The order is irrelevant and a no-match pass is a no-op.
func removeKeldHooks(obj *orderedmap.OrderedMap, managed map[string]any) {
	recorded := ""
	if v, ok := managed["hook_substr"]; ok {
		if s, ok := v.(string); ok && s != "" {
			recorded = s
		}
	}
	if recorded != "" {
		config.RemoveHooksByCommand(obj, recorded)
	}
	if recorded != telemetry.HookCommandSubstr {
		config.RemoveHooksByCommand(obj, telemetry.HookCommandSubstr)
	}
}
