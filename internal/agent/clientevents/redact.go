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

	// maxErrLen is a defensive final cap (in runes) on RedactError's WHOLE
	// "<Type>: <message>" result, in case a pathological type name is long
	// enough to matter — the message itself is already bounded to
	// maxFieldLen/maxFieldWords by safeErrMessage before this applies.
	maxErrLen = 200
)

// pathRe matches an embedded absolute-path token: a path that begins at the
// start of the string or right after a NON-word char (so a relative token
// like "a/b", where "/" is preceded by a word char, is NOT matched, but a
// path behind a quote/equals/paren/colon — `="/…`, `stat "/…`, `cfg(/…`,
// `file://…` — IS caught). It covers POSIX absolute paths ("/etc",
// "/home/u/x.json"), Windows drive paths ("C:\..."), and Windows UNC paths
// ("\\server\share\..."). The leading start/non-word char is CONSUMED and
// captured as group 1, so RedactError must re-emit it (${1}<path>) to avoid
// eating the preceding "/=/( char. A token stops at the first whitespace, so
// a path containing a space is only partially matched — redactFields relies
// on that (any embedded-path match ⇒ redact the WHOLE value) to avoid leaking
// an interior directory word.
var pathRe = regexp.MustCompile(`(^|[^\w])(?:/\S+|[A-Za-z]:\\\S*|\\\\\S+)`)

// wholePathRe matches when the ENTIRE string is a single absolute-path token
// (no other content, no interior whitespace).
var wholePathRe = regexp.MustCompile(`^(?:/\S+|[A-Za-z]:\\\S*|\\\\\S+)$`)

// wsCollapseRE matches any newline/tab/carriage-return/vertical-form run so a
// multi-line error message collapses to a single line.
var wsCollapseRE = regexp.MustCompile(`[\t\n\r\v\f]+`)

// redactedText marks a string as already vetted by RedactError: leading/
// embedded absolute paths surgically replaced with "<path>", the message
// portion word/length-capped exactly like a plain field value, and control
// chars scrubbed. redactFields passes it through AS-IS rather than routing it
// through redactString/capField again — re-applying the free-text word-cap to
// an already-short "<Type>: message" summary is what previously clobbered
// nearly every real error (anything with more than 3 words, or any path) into
// a bare "<redacted>", gutting error telemetry. Only RedactError should ever
// produce a value of this type.
type redactedText string

// redactFields returns a NEW map derived from in where every value has been
// allow-listed for publication: numbers, bools, and time.Duration pass
// unchanged; strings go through conservative whole-value redaction
// (redactString); a redactedText (from RedactError) passes through as-is,
// already vetted; nested maps recurse; anything else (slices, structs,
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
		case redactedText:
			// Already vetted by RedactError. A defensive control-char scan
			// only — no word/length cap here, since that's the exact
			// double-redaction this type exists to avoid.
			s := string(val)
			for _, r := range s {
				if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
					s = "<redacted>"
					break
				}
			}
			out[k] = s
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
		// Control (Cc: \n \t ...) or format (Cf: U+200B ZWSP, U+200D ZWJ, ...)
		// chars — the latter can invisibly stitch a multi-token prompt into one
		// "word", so reject both.
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
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
// "<Type>: <message>". Unlike a plain field value it strips embedded paths
// surgically (→ "<path>") so the useful error class survives, e.g.
// "open <path>: denied", rather than nuking the whole value. The message
// portion is then given the SAME free-text protection a plain field value
// gets (word/length cap) — a message that still reads as prose (or still
// contains a control/format char) after path-stripping is replaced wholesale
// with "<redacted>", but the "<Type>: " prefix always survives so the error
// class remains useful for classification even when the message can't be
// trusted. The returned redactedText is pre-vetted: redactFields passes it
// through unchanged rather than re-running the field free-text cap on it
// (that double-pass was the bug — see the redactedText doc comment).
func RedactError(err error) redactedText {
	if err == nil {
		return redactedText("")
	}
	typeName := fmt.Sprintf("%T", err)

	msg := wsCollapseRE.ReplaceAllString(err.Error(), " ")
	// pathRe consumes the leading start/non-word char into group 1; re-emit it
	// with ${1} (braces required so it isn't parsed as $1<) so only the path
	// itself becomes "<path>" and the preceding "/=/( char is preserved
	// (e.g. `stat "<path>`, `path=<path>`).
	msg = pathRe.ReplaceAllString(msg, "${1}<path>")
	if !safeErrMessage(msg) {
		msg = "<redacted>"
	}

	result := typeName + ": " + msg
	runes := []rune(result)
	if len(runes) > maxErrLen {
		result = string(runes[:maxErrLen]) + "…"
	}

	return redactedText(result)
}

// safeErrMessage reports whether an already path-stripped RedactError message
// is safe to publish verbatim: no control/format character, at most
// maxFieldLen bytes, and at most maxFieldWords whitespace-separated words —
// the identical free-text protection capField applies to a plain field value,
// applied here to the message half of a "<Type>: <message>" summary so a
// prose or prompt-embedded error message can't leak through the error field.
func safeErrMessage(msg string) bool {
	for _, r := range msg {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return false
		}
	}
	return len(msg) <= maxFieldLen && len(strings.Fields(msg)) <= maxFieldWords
}
