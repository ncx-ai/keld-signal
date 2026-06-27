// Package tools provides the tool adapter interface and shared types for keld
// tool integrations (Claude Code, Codex, Gemini).
package tools

import (
	"github.com/ncx-ai/keld-cli/internal/telemetry"
)

// SetupParams is a type alias to telemetry.SetupParams to avoid import cycles
// while keeping the tools package ergonomic.
type SetupParams = telemetry.SetupParams

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
}

// ToolStatus describes the installation and configuration state of a tool.
type ToolStatus struct {
	Name        string
	Installed   bool
	Configured  bool
	Detail      string
}

// Adapter is the interface that tool adapters (Claude Code, Codex, Gemini) must
// implement to support keld integration.
type Adapter interface {
	// Name returns the tool's internal name (e.g., "claude", "codex", "gemini").
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
