package creddetect

import (
	"math"
	"strings"
)

// Span is a detected credential location (half-open [Start,End)).
type Span struct {
	RuleID string
	Start  int
	End    int
}

// Detect returns credential spans in text. A rule's regex runs only if one of
// its keywords is present (pre-filter); a match below the rule's entropy floor
// (when >0) is dropped. Overlapping spans are de-duplicated (first match wins).
func Detect(text string) []Span {
	lower := strings.ToLower(text)
	var out []Span
	for _, r := range Rules() {
		if r.Regex.String() == "" {
			// Path-only gitleaks rules (e.g. pkcs12-file) carry no "regex" key,
			// only a "path" key for filename matching; Task 2's loader compiles
			// the missing field as an empty pattern, which matches the empty
			// string at every offset. Such rules have no content-matching
			// semantics, so skip them here rather than flag every position.
			continue
		}
		if !keywordPresent(lower, r.Keywords) {
			continue
		}
		for _, loc := range r.Regex.FindAllStringSubmatchIndex(text, -1) {
			s, e := loc[0], loc[1]
			if r.SecretGroup > 0 && 2*r.SecretGroup+1 < len(loc) && loc[2*r.SecretGroup] >= 0 {
				s, e = loc[2*r.SecretGroup], loc[2*r.SecretGroup+1]
			}
			if r.Entropy > 0 && shannon(text[s:e]) < r.Entropy {
				continue
			}
			if !overlaps(out, s, e) {
				out = append(out, Span{RuleID: r.ID, Start: s, End: e})
			}
		}
	}
	return out
}

// keywordPresent reports whether any keyword occurs in the (lowercased) text.
// Empty keyword list ⇒ the rule is unconditional (rare in gitleaks).
func keywordPresent(lower string, kws []string) bool {
	if len(kws) == 0 {
		return true
	}
	for _, k := range kws {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

func overlaps(spans []Span, s, e int) bool {
	for _, x := range spans {
		if s < x.End && x.Start < e {
			return true
		}
	}
	return false
}

// shannon returns the Shannon entropy (bits/char) of s.
func shannon(s string) float64 {
	if s == "" {
		return 0
	}
	var freq [256]float64
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	n := float64(len(s))
	h := 0.0
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}
