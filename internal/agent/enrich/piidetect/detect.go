// Package piidetect detects concrete personal-identifier tokens in text with
// pure Go: no model, no network. It is the deterministic counterpart to
// creddetect (which covers credentials) for the entity types whose format is
// regular — ssn, credit_card, email — so the sensitivity facet can reach its
// two highest classes (phi, pci) with no NER available.
//
// PRECISION IS THE REQUIREMENT, NOT RECALL. A false "phi" on an org dashboard
// is alarming and erodes trust; a miss is not. Every detector therefore
// validates structure (Luhn + issuer prefix for cards, the SSA's invalid-range
// rules for SSNs) and every match passes the IsWellKnown gate, because
// developer transcripts are saturated with published test and example values.
//
// Phone numbers are deliberately NOT detected — see the note on Detect.
package piidetect

import (
	"regexp"
	"strings"
)

// Span is a detected PII location (half-open [Start,End)) with the entity
// label it maps to. Labels are values of enrich.SensitiveEntityLabels; this
// package introduces none of its own.
type Span struct {
	Label string
	Start int
	End   int
}

// Patterns are anchored on word boundaries and use only bounded repetition, so
// they are linear under RE2 and cannot be walked into a pathological state by a
// crafted prompt.
var (
	// A 13-19 digit run, optionally single-separated by space or hyphen. The
	// pattern is a candidate finder only: Luhn + issuer validation below does
	// the real work.
	reCard = regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)
	// SSNs are matched ONLY in the canonical dashed form. See Detect.
	reSSN = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)
	// Conservative address: alnum-initial local part, 1-4 dot-separated domain
	// labels, alphabetic TLD of 2+. This rejects "lodash@4.17.21" (numeric
	// TLD), "user@host" (no TLD) and "@types/node" (no local part).
	reEmail = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9._%+\-]{0,63}@(?:[A-Za-z0-9](?:[A-Za-z0-9\-]{0,30}[A-Za-z0-9])?\.){1,4}[A-Za-z]{2,24}\b`)
)

// Detect returns the PII spans in text, most-severe class first so that an
// overlap resolves to the higher severity. Spans never overlap.
//
// Two deliberate omissions:
//
//   - PHONE. A bare digit run is indistinguishable from a port, a version, a
//     timestamp or an id, and the shapes phones take across locales are broad
//     enough that any pattern with useful recall also fires on all of those.
//     No variant reached a precision this facet can carry, so phone recall
//     stays entirely with the NER, where a false positive costs "pii" — the
//     lowest class — rather than a fabricated one.
//   - UNDASHED SSNs. A bare nine-digit run matches order numbers, account ids
//     and ticket ids constantly, and requiring a nearby "ssn"/"social security"
//     keyword does not fix it: in a text that discusses SSNs the nearest digit
//     run is very often an account or record id, so the keyword gate converts
//     the most common false positive into the most severe class (phi). The
//     canonical dashed form is how an SSN is written when it is written down;
//     everything else is left to the NER, and deterministic mode declares the
//     residual gap via SensitivityExtractor.Degraded.
func Detect(text string) []Span {
	var out []Span
	for _, loc := range reCard.FindAllStringIndex(text, -1) {
		s, e := loc[0], loc[1]
		if dottedNumeric(text, s, e) {
			continue // a version string / dotted-quad segment, not a card
		}
		v := text[s:e]
		if !validCard(digitsOnly(v)) || IsWellKnown("credit_card", v) {
			continue
		}
		out = appendNonOverlapping(out, Span{Label: "credit_card", Start: s, End: e})
	}
	for _, loc := range reSSN.FindAllStringIndex(text, -1) {
		s, e := loc[0], loc[1]
		if charAt(text, s-1) == '-' || charAt(text, e) == '-' {
			continue // part of a longer hyphenated serial, not a standalone SSN
		}
		v := text[s:e]
		if !validSSN(digitsOnly(v)) || IsWellKnown("ssn", v) {
			continue
		}
		out = appendNonOverlapping(out, Span{Label: "ssn", Start: s, End: e})
	}
	for _, loc := range reEmail.FindAllStringIndex(text, -1) {
		s, e := loc[0], loc[1]
		if IsWellKnown("email", text[s:e]) {
			continue
		}
		out = appendNonOverlapping(out, Span{Label: "email", Start: s, End: e})
	}
	return out
}

// appendNonOverlapping drops a span that overlaps one already accepted. Callers
// add classes in descending severity, so the higher class wins the region.
func appendNonOverlapping(spans []Span, s Span) []Span {
	for _, x := range spans {
		if s.Start < x.End && x.Start < s.End {
			return spans
		}
	}
	return append(spans, s)
}

// dottedNumeric reports whether the match sits inside a dotted numeric sequence
// (a version string, a long decimal, an IP-like token): the character just
// outside is '.' and the one beyond it is a digit. A sentence-ending period is
// not affected, since a digit must follow the dot.
func dottedNumeric(text string, s, e int) bool {
	if charAt(text, s-1) == '.' && isDigit(charAt(text, s-2)) {
		return true
	}
	return charAt(text, e) == '.' && isDigit(charAt(text, e+1))
}

func charAt(s string, i int) byte {
	if i < 0 || i >= len(s) {
		return 0
	}
	return s[i]
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func digitsOnly(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if isDigit(s[i]) {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// validSSN applies the Social Security Administration's structural rules to a
// 9-digit string: area 000, 666 and 900-999 are never issued, group 00 is never
// issued, and serial 0000 is never issued.
func validSSN(d string) bool {
	if len(d) != 9 {
		return false
	}
	area, group, serial := d[0:3], d[3:5], d[5:9]
	if area == "000" || area == "666" || area[0] == '9' {
		return false
	}
	if group == "00" || serial == "0000" {
		return false
	}
	return true
}

// validCard requires BOTH a Luhn-valid checksum and an issuer prefix/length
// combination from a real card network. Digits alone are not a card number:
// Luhn on its own admits one in ten random digit runs.
func validCard(d string) bool {
	return brandOK(d) && luhn(d)
}

func luhn(d string) bool {
	n := len(d)
	if n < 13 || n > 19 {
		return false
	}
	sum := 0
	for i := 0; i < n; i++ {
		v := int(d[n-1-i] - '0')
		if i%2 == 1 {
			if v *= 2; v > 9 {
				v -= 9
			}
		}
		sum += v
	}
	return sum%10 == 0
}

// brandOK reports whether d's issuer identification number and length match a
// major card network. Ranges follow the ISO/IEC 7812 assignments in use today.
func brandOK(d string) bool {
	n := len(d)
	switch {
	case d[0] == '4': // Visa
		return n == 13 || n == 16 || n == 19
	case between(d, 2, 51, 55), between(d, 4, 2221, 2720): // Mastercard
		return n == 16
	case between(d, 2, 34, 34), between(d, 2, 37, 37): // American Express
		return n == 15
	case strings.HasPrefix(d, "6011"), between(d, 2, 65, 65),
		between(d, 3, 644, 649), between(d, 6, 622126, 622925): // Discover
		return n >= 16 && n <= 19
	case between(d, 4, 3528, 3589): // JCB
		return n >= 16 && n <= 19
	case between(d, 3, 300, 305), strings.HasPrefix(d, "3095"),
		between(d, 2, 36, 36), between(d, 2, 38, 39): // Diners Club
		return n >= 14 && n <= 19
	}
	return false
}

// between reports whether d's first width digits, read as a number, fall in
// [lo,hi].
func between(d string, width, lo, hi int) bool {
	if len(d) < width {
		return false
	}
	v := 0
	for i := 0; i < width; i++ {
		v = v*10 + int(d[i]-'0')
	}
	return v >= lo && v <= hi
}
