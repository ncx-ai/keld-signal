package clientevents

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// maxFieldLen and maxFieldSpaces bound what an event Fields value may look
// like before it's considered free text (and therefore a potential leak of
// raw prompt content). Short enums/status codes/error reasons have zero or
// one space and are well under this length; anything resembling a prose
// sentence trips one of the two caps and is fully replaced. When in doubt,
// redact.
const (
	maxFieldLen    = 120
	maxFieldSpaces = 3

	// maxErrLen caps a RedactError message (excluding the "<Type>: " prefix)
	// so a verbose wrapped error can never smuggle a large blob of text.
	maxErrLen = 200
)

// absPathRE matches embedded absolute filesystem path substrings so they can
// be stripped from a string before it's published. Covers POSIX paths (a "/"
// followed by non-space chars, requiring at least one more "/" or a "." so
// bare "/" or short one-off tokens aren't over-eagerly treated as paths) and
// Windows drive-letter paths ("C:\...").
var absPathRE = regexp.MustCompile(`(?:[A-Za-z]:\\[^\s]*)|(?:/[^\s]*[/.][^\s]*)`)

// wsCollapseRE matches any run of whitespace that includes a newline or tab,
// so a multi-line error message collapses to a single line.
var wsCollapseRE = regexp.MustCompile(`[\t\n\r]+`)

// stripPaths replaces every embedded absolute path substring in s with the
// literal token "<path>". It never leaves a verbatim absolute path behind.
func stripPaths(s string) string {
	return absPathRE.ReplaceAllString(s, "<path>")
}

// redactFields returns a NEW map derived from in where every value has been
// allow-listed for publication: numbers, bools, and time.Duration pass
// unchanged; strings are path-stripped and rejected as free text when too
// long or too "spacey"; nested maps recurse; anything else (slices, structs,
// pointers, ...) is dropped. The input map is never mutated. This is the
// privacy gate for client events — conservative by design: when in doubt,
// redact.
func redactFields(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		switch val := v.(type) {
		case bool,
			int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64,
			float32, float64,
			time.Duration:
			out[k] = val
		case string:
			out[k] = redactString(val)
		case map[string]any:
			out[k] = redactFields(val)
		default:
			// Conservative default: unknown/compound types are dropped rather
			// than risk publishing something we haven't vetted.
		}
	}
	return out
}

// redactString applies the path-stripping + free-text rejection rules to a
// single string value.
func redactString(s string) string {
	original := s
	stripped := stripPaths(s)

	// If the entire value was a single absolute path, prefer the basename of
	// the original for debuggability; the verbatim absolute path is gone
	// either way.
	if stripped == "<path>" {
		return filepath.Base(original)
	}

	if len(stripped) > maxFieldLen || strings.Count(stripped, " ") > maxFieldSpaces {
		return "<redacted>"
	}
	return stripped
}

// RedactError reduces an error to a short, path-stripped, length-capped
// class+summary string suitable for publication: "<Type>: <redacted message>".
// It never returns raw multi-line text or a verbatim absolute path.
func RedactError(err error) string {
	if err == nil {
		return ""
	}

	msg := wsCollapseRE.ReplaceAllString(err.Error(), " ")
	msg = stripPaths(msg)

	truncated := false
	if len(msg) > maxErrLen {
		msg = msg[:maxErrLen]
		truncated = true
	}
	if truncated {
		msg += "…"
	}

	return fmt.Sprintf("%T: %s", err, msg)
}
