// internal/config/merge_toml.go
package config

import (
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/ncx-ai/keld-cli/internal/errs"
)

// Marker comments that delimit the keld-managed block in Codex config.toml.
// These strings must remain byte-identical to KELD_TOML_START / KELD_TOML_END
// in src/keld/config/merge.py so that already-installed blocks stay detectable.
const (
	KeldTOMLStart = "# >>> keld (managed by keld CLI — do not edit between markers)"
	KeldTOMLEnd   = "# <<< keld"
)

// HasKeldBlock reports whether text contains the keld managed block start marker.
func HasKeldBlock(text string) bool {
	return strings.Contains(text, KeldTOMLStart)
}

// StripKeldBlock removes the keld managed block (including its markers) from
// text. Lines outside the markers are preserved unchanged. Mirrors Python's
// strip_keld_block: uses splitlines semantics (Split on "\n"), rstrips trailing
// newlines, and adds exactly one trailing newline when result is non-empty.
func StripKeldBlock(text string) string {
	if !strings.Contains(text, KeldTOMLStart) {
		return text
	}
	lines := strings.Split(text, "\n")
	var out []string
	inside := false
	for _, line := range lines {
		if strings.TrimSpace(line) == KeldTOMLStart {
			inside = true
			continue
		}
		if inside && strings.TrimSpace(line) == KeldTOMLEnd {
			inside = false
			continue
		}
		if !inside {
			out = append(out, line)
		}
	}
	result := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if result == "" {
		return ""
	}
	return result + "\n"
}

// UpsertKeldBlock replaces (or inserts) the keld managed block in text with a
// block containing body. If text already has a block it is stripped first, then
// the new block is appended — so re-upserting never duplicates the block.
// Mirrors Python's upsert_keld_block.
func UpsertKeldBlock(text, body string) string {
	base := StripKeldBlock(text)
	if !strings.HasSuffix(body, "\n") {
		body = body + "\n"
	}
	block := KeldTOMLStart + "\n" + body + KeldTOMLEnd + "\n"
	if strings.TrimSpace(base) == "" {
		return block
	}
	return base + "\n" + block
}

// ValidateTOML returns an errs.Error if text is not valid TOML, nil otherwise.
func ValidateTOML(text string) error {
	var v any
	if err := toml.Unmarshal([]byte(text), &v); err != nil {
		return errs.New("resulting TOML is invalid: %v", err)
	}
	return nil
}

// StripTOMLTable removes a top-level [table] and all its [table.sub] subtables
// from raw TOML text, preserving all other content. No-op if the table is
// absent. Mirrors Python's strip_toml_table (keepends=True line walk).
func StripTOMLTable(text, table string) string {
	var out []string
	dropping := false
	for _, line := range strings.SplitAfter(text, "\n") {
		stripped := strings.TrimSpace(line)
		if strings.HasPrefix(stripped, "[") {
			header := strings.Trim(stripped, "[]")
			header = strings.TrimSpace(header)
			// split on first dot to get top-level segment
			top := strings.SplitN(header, ".", 2)[0]
			top = strings.TrimSpace(top)
			top = strings.Trim(top, "\"'")
			dropping = top == table
		}
		if !dropping {
			out = append(out, line)
		}
	}
	return strings.Join(out, "")
}
