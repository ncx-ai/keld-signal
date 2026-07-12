package clientevents

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// maxFieldLen and maxFieldWords bound what an event Fields *value* may look
// like before it's treated as free text (and therefore a potential leak of
// raw prompt content). Short enums, status codes, and error reasons are at
// most a few words and well under this length; anything resembling a prose
// sentence trips one of the two caps and is fully replaced. maxFieldWords is
// counted with strings.Fields (splits on unicode.IsSpace) so unicode
// whitespace / NBSP-separated prompts are caught too. When in doubt, redact.
const (
	maxFieldLen   = 120
	maxFieldWords = 3

	// maxErrLen caps a RedactError message (in runes, excluding the "<Type>: "
	// prefix) so a verbose wrapped error can never smuggle a large blob of text.
	maxErrLen = 200
)

// pathRe matches an embedded absolute-path token: a path that begins at the
// start of the string or right after whitespace (so a relative token like
// "a/b", where "/" is preceded by a non-space, is NOT matched). It covers
// POSIX absolute paths ("/etc", "/home/u/x.json"), Windows drive paths
// ("C:\..."), and Windows UNC paths ("\\server\share\..."). The leading
// start/whitespace is captured as group 1 so RedactError can preserve it when
// substituting "<path>". A token stops at the first whitespace, so a path
// containing a space is only partially matched — redactFields relies on that
// (any embedded-path match ⇒ redact the WHOLE value) to avoid leaking an
// interior directory word.
var pathRe = regexp.MustCompile(`(^|\s)(?:/\S+|[A-Za-z]:\\\S*|\\\\\S+)`)

// wholePathRe matches when the ENTIRE string is a single absolute-path token
// (no other content, no interior whitespace).
var wholePathRe = regexp.MustCompile(`^(?:/\S+|[A-Za-z]:\\\S*|\\\\\S+)$`)

// wsCollapseRE matches any newline/tab/carriage-return/vertical-form run so a
// multi-line error message collapses to a single line.
var wsCollapseRE = regexp.MustCompile(`[\t\n\r\v\f]+`)

// redactFields returns a NEW map derived from in where every value has been
// allow-listed for publication: numbers, bools, and time.Duration pass
// unchanged; strings go through conservative whole-value redaction
// (redactString); nested maps recurse; anything else (slices, structs,
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
			// Conservative default: unknown/compound types (slices, structs,
			// pointers, ...) are dropped rather than risk publishing something
			// we haven't vetted.
		}
	}
	return out
}

// redactString applies conservative whole-value redaction to a single string:
//  1. any control character (newline/tab/etc.) ⇒ "<redacted>";
//  2. the whole value is one absolute path ⇒ its basename (re-checked by the
//     length/word caps, so a pathological basename still redacts);
//  3. an absolute path embedded among other content ⇒ "<redacted>" (never
//     surgically strip a field value — a space inside a directory name would
//     otherwise leave a fragment behind);
//  4. otherwise apply the free-text caps.
func redactString(s string) string {
	for _, r := range s {
		if unicode.IsControl(r) {
			return "<redacted>"
		}
	}
	if wholePathRe.MatchString(s) {
		return capField(baseName(s))
	}
	if pathRe.MatchString(s) {
		return "<redacted>"
	}
	return capField(s)
}

// capField rejects free text: a value longer than maxFieldLen bytes or with
// more than maxFieldWords whitespace-separated words is replaced wholesale.
func capField(s string) string {
	if len(s) > maxFieldLen || len(strings.Fields(s)) > maxFieldWords {
		return "<redacted>"
	}
	return s
}

// baseName returns the final path segment, splitting on both POSIX ("/") and
// Windows ("\") separators so it works cross-platform regardless of the host
// running the daemon (filepath.Base only splits on the host separator, which
// would leak a whole UNC/Windows path when running on Linux).
func baseName(s string) string {
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		return s[i+1:]
	}
	return s
}

// RedactError reduces an error to a short, path-stripped, rune-safe,
// length-capped class+summary string suitable for publication:
// "<Type>: <redacted message>". Unlike a field value it strips embedded paths
// surgically (→ "<path>") so the useful error class survives, e.g.
// "open <path>: denied". It never returns raw multi-line text or a verbatim
// absolute path.
func RedactError(err error) string {
	if err == nil {
		return ""
	}

	msg := wsCollapseRE.ReplaceAllString(err.Error(), " ")
	// Preserve the captured start/whitespace prefix (group 1) so we don't glue
	// the "<path>" token onto the preceding word.
	msg = pathRe.ReplaceAllString(msg, "$1<path>")

	runes := []rune(msg)
	if len(runes) > maxErrLen {
		msg = string(runes[:maxErrLen]) + "…"
	}

	return fmt.Sprintf("%T: %s", err, msg)
}
