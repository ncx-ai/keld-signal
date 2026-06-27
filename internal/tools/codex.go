// Package tools provides the Codex adapter for keld tool integrations.
package tools

import (
	"os"
	"path/filepath"
	"reflect"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/ncx-ai/keld-cli/internal/config"
	"github.com/ncx-ai/keld-cli/internal/telemetry"
)

// CodexAdapter implements the Adapter interface for OpenAI Codex CLI.
type CodexAdapter struct{}

// Name returns the internal name for Codex.
func (a *CodexAdapter) Name() string { return "codex" }

// DisplayName returns the human-readable name for Codex.
func (a *CodexAdapter) DisplayName() string { return "Codex" }

// ConfigPath returns the path to Codex's config file (~/.codex/config.toml).
// This uses the user's home directory, NOT KELD_HOME.
func (a *CodexAdapter) ConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".codex", "config.toml")
	}
	return filepath.Join(home, ".codex", "config.toml")
}

// Detect reports whether the ~/.codex directory exists (parent of config file).
func (a *CodexAdapter) Detect() bool {
	dir := filepath.Dir(a.ConfigPath())
	_, err := os.Stat(dir)
	return err == nil
}

// Apply merges keld OTEL config and hooks into the Codex config.toml.
// currentText is nil if the config file is absent (created=true).
// replace controls whether to attempt replacing a conflicting [otel] table.
func (a *CodexAdapter) Apply(currentText *string, p SetupParams, replace bool) Plan {
	body := telemetry.CodexBlockBody(p, "codex")
	after := config.UpsertKeldBlock(ptrToStr(currentText), body)

	if err := config.ValidateTOML(after); err != nil {
		if replace {
			current := ptrToStr(currentText)
			stripped := config.StripTOMLTable(current, "otel")

			// Safety check: verify the strip removed ONLY the [otel] table and nothing
			// else. Parse both the current text (minus otel) and the stripped text via
			// go-toml and compare with reflect.DeepEqual, mirroring codex.py's use of
			// tomllib.loads and dict equality.
			var parsedCurrent map[string]any
			var parsedStripped map[string]any
			safeToReplace := false
			if errC := toml.Unmarshal([]byte(current), &parsedCurrent); errC == nil {
				if errS := toml.Unmarshal([]byte(stripped), &parsedStripped); errS == nil {
					delete(parsedCurrent, "otel")
					safeToReplace = reflect.DeepEqual(parsedStripped, parsedCurrent)
				}
			}

			if !safeToReplace {
				return Plan{
					Name:       a.Name(),
					ConfigPath: a.ConfigPath(),
					AfterText:  current,
					Managed:    map[string]any{},
					Summary:    []string{},
					Changed:    false,
					Conflict: "Keld couldn't safely replace the [otel] section in " +
						"~/.codex/config.toml without affecting other settings — " +
						"resolve it manually.",
				}
			}

			after = config.UpsertKeldBlock(stripped, body)
			if err2 := config.ValidateTOML(after); err2 != nil {
				return Plan{
					Name:       a.Name(),
					ConfigPath: a.ConfigPath(),
					AfterText:  current,
					Managed:    map[string]any{},
					Summary:    []string{},
					Changed:    false,
					Conflict: "Keld couldn't replace the conflicting section in " +
						"~/.codex/config.toml: " + err2.Error(),
				}
			}

			return Plan{
				Name:       a.Name(),
				ConfigPath: a.ConfigPath(),
				AfterText:  after,
				Managed:    map[string]any{"block": true, "created": currentText == nil},
				Summary:    []string{"replace your existing [otel] with Keld's [otel] + hooks block"},
				Changed:    after != ptrToStr(currentText),
			}
		}

		// Not replace: return a conflict explaining the issue.
		return Plan{
			Name:       a.Name(),
			ConfigPath: a.ConfigPath(),
			AfterText:  ptrToStr(currentText),
			Managed:    map[string]any{},
			Summary:    []string{},
			Changed:    false,
			Conflict: "your ~/.codex/config.toml can't be safely modified by Keld " +
				"(it already defines conflicting settings, e.g. an [otel] table): " + err.Error(),
		}
	}

	// Happy path: TOML is valid.
	return Plan{
		Name:       a.Name(),
		ConfigPath: a.ConfigPath(),
		AfterText:  after,
		Managed:    map[string]any{"block": true, "created": currentText == nil},
		Summary:    []string{"add [otel] + SessionStart/PreToolUse hooks block"},
		Changed:    after != ptrToStr(currentText),
	}
}

// Remove strips the keld-managed block from the Codex config.toml.
func (a *CodexAdapter) Remove(currentText *string, managed map[string]any) Plan {
	after := config.StripKeldBlock(ptrToStr(currentText))
	return Plan{
		Name:       a.Name(),
		ConfigPath: a.ConfigPath(),
		AfterText:  after,
		Managed:    managed,
		Summary:    []string{"remove Keld block"},
		Changed:    after != ptrToStr(currentText),
	}
}

// Status reports whether Codex is installed (Detect) and configured with keld's block.
func (a *CodexAdapter) Status(currentText *string, managed map[string]any) ToolStatus {
	text := ptrToStr(currentText)
	configured := config.HasKeldBlock(text)
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

// ptrToStr dereferences a string pointer, returning "" for nil.
func ptrToStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
