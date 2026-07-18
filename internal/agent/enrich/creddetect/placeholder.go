package creddetect

import (
	"regexp"
	"strings"
)

// Placeholder/template shapes that are NEVER real secrets. Conservative by
// design: a real secret must not match (that would be recall loss).
var (
	// Bracket/brace/var templates: <...>, ${...}, {{...}}, %...%.
	reTemplate = regexp.MustCompile(`^(<.*>|\$\{?[A-Za-z0-9_]+\}?|\{\{.*\}\}|%[A-Za-z0-9_]+%)$`)
	// All-caps token of only A-Z/_/digits with NO lowercase (e.g. API_KEY,
	// SECRET_HERE, REPLACE_WITH_YOUR_TOKEN). The caller further requires an
	// underscore or mask char before treating a match as a placeholder, so a real
	// all-caps key without underscores (AKIAIOSFODNN7EXAMPLE) is NOT gated.
	reAllCaps = regexp.MustCompile(`^[A-Z0-9_]*[A-Z_][A-Z0-9_*]*$`)
	// Runs of mask chars (>=3): ****, xxxx, XXXX, ellipsis, bullets.
	reMaskRun = regexp.MustCompile(`(\*{3,}|[xX]{4,}|…{3,}|•{3,})`)
	// "YOUR_"/"MY_"/"THE_" pronoun prefixes (case-insensitive).
	rePronounPrefix = regexp.MustCompile(`(?i)^(your|my|the)[_\-]`)
	// Placeholder-ish words, matched as the WHOLE (lowercased) value.
	placeholderWords = map[string]bool{
		"placeholder": true, "example": true, "redacted": true, "changeme": true,
		"change_me": true, "todo": true, "dummy": true, "fake": true, "sample": true,
		"your_token_here": true, "your-token-here": true, "token": true, "secret": true,
	}
)

// IsPlaceholder reports whether s is a placeholder / redacted / template value
// rather than a real secret. Conservative: keys on placeholder SHAPE and the
// absence of secret-like entropy (mixed case + digits), so real credentials
// return false. Used as a precision gate so a detected sensitive-entity span
// whose text is a placeholder does not trigger the secrets class.
func IsPlaceholder(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return true
	}
	if reTemplate.MatchString(t) {
		return true
	}
	if reMaskRun.MatchString(t) {
		return true
	}
	if rePronounPrefix.MatchString(t) {
		return true
	}
	if placeholderWords[strings.ToLower(t)] {
		return true
	}
	// An all-caps-only token (no lowercase) is a placeholder ONLY if it carries an
	// underscore or mask char — real all-caps keys are long alnum without them.
	if reAllCaps.MatchString(t) && strings.ContainsAny(t, "_*") && !strings.ContainsRune(t, ' ') {
		return true
	}
	return false
}
