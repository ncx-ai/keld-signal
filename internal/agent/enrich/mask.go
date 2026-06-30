package enrich

import "strings"

// Mask returns a redacted hint for a sensitive value. It never returns the full
// value. Emails keep the domain; other values keep at most the last 4 chars
// when the value is long enough to make the tail non-identifying.
func Mask(label, value string) string {
	if label == "email" {
		if at := strings.LastIndex(value, "@"); at >= 0 {
			return "***@" + value[at+1:]
		}
	}
	const tail = 4
	if len(value) <= tail+2 {
		return "***"
	}
	return "…" + value[len(value)-tail:]
}
