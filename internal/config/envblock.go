// internal/config/envblock.go
package config

import "strings"

// Marker comments that delimit the keld-managed block in .env files.
// These strings are frozen: they must remain byte-identical so that
// already-installed blocks stay detectable. Treat as a backward-compat contract.
const (
	KeldEnvStart = "# >>> keld-managed (do not edit) >>>"
	KeldEnvEnd   = "# <<< keld-managed <<<"
)

// HasEnvBlock reports whether text contains the keld managed block start marker.
func HasEnvBlock(text string) bool {
	return strings.Contains(text, KeldEnvStart)
}

// RemoveEnvBlock removes the keld managed block (including its markers) from
// text. Lines outside the markers are preserved unchanged. Mirrors the pattern
// from StripKeldBlock: uses Split on "\n", preserves all non-block lines
// byte-for-byte, and adds exactly one trailing newline when result is non-empty.
func RemoveEnvBlock(text string) string {
	if !strings.Contains(text, KeldEnvStart) {
		return text
	}
	lines := strings.Split(text, "\n")
	var out []string
	inside := false
	for _, line := range lines {
		if strings.TrimSpace(line) == KeldEnvStart {
			inside = true
			continue
		}
		if inside && strings.TrimSpace(line) == KeldEnvEnd {
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

// UpsertEnvBlock replaces (or inserts) the keld managed block in text with a
// block containing body. If text already has a block it is stripped first, then
// the new block is appended — so re-upserting never duplicates the block.
// Preserves all other lines byte-for-byte, including GEMINI_API_KEY.
// Mirrors the pattern from UpsertKeldBlock.
func UpsertEnvBlock(text, body string) string {
	base := RemoveEnvBlock(text)
	if !strings.HasSuffix(body, "\n") {
		body = body + "\n"
	}
	block := KeldEnvStart + "\n" + body + KeldEnvEnd + "\n"
	if strings.TrimSpace(base) == "" {
		return block
	}
	return base + "\n" + block
}
