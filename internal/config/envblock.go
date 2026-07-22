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
//
// Intentional behaviors:
//   - On replace, the block is relocated to end-of-file: all other lines
//     (anywhere before or after the old block) are preserved, but the block
//     itself always reappears at EOF rather than in its previous position.
//     This is deliberate — no data loss occurs, only relocation.
//   - body is expected to always be an internally-constructed value (the
//     Gemini OTEL block), never raw/untrusted external input. The marker-line
//     guard below is defense-in-depth for that contract, not a promise that
//     arbitrary untrusted body content is otherwise safe to pass in.
//   - Block separators are written as "\n" (LF) unconditionally, even when
//     the input predominantly uses CRLF. Making the freshly-written block
//     follow the input's dominant line ending was considered, but doing so
//     safely would require reasoning about mixed line endings across the
//     whole file and risks the byte-exact preservation guarantee this helper
//     exists for — not worth it for a cosmetic win, so it's left as LF.
func UpsertEnvBlock(text, body string) string {
	base := RemoveEnvBlock(text)
	if !strings.HasSuffix(body, "\n") {
		body = body + "\n"
	}
	// Defensive guard (issue 3): body is always internally-constructed today,
	// but if a future body ever contained a line matching one of the markers
	// verbatim, it would desynchronize RemoveEnvBlock's inside/outside state
	// on a later pass (e.g. an embedded end-marker line closes the block
	// early, spilling the remaining body and the real end-marker out as
	// top-level lines). Drop any such line before building the block so that
	// only the two real marker lines we emit below can ever match.
	body = stripMarkerLines(body)
	block := KeldEnvStart + "\n" + body + KeldEnvEnd + "\n"
	if strings.TrimSpace(base) == "" {
		return block
	}
	return base + "\n" + block
}

// stripMarkerLines drops any line in body whose trimmed form is exactly one
// of the keld env markers, preserving all other lines (and the trailing
// newline) unchanged. This keeps a malformed/adversarial body from
// introducing a stray marker line that could be mistaken for a real block
// boundary on a later RemoveEnvBlock/UpsertEnvBlock pass.
func stripMarkerLines(body string) string {
	if !strings.Contains(body, KeldEnvStart) && !strings.Contains(body, KeldEnvEnd) {
		return body
	}
	lines := strings.Split(body, "\n")
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == KeldEnvStart || trimmed == KeldEnvEnd {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
