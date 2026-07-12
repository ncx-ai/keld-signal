package clientevents

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func utf8ValidString(s string) bool { return utf8.ValidString(s) }

func TestRedactFieldsAbsolutePathBecomesBasenameOrPathToken(t *testing.T) {
	in := map[string]any{"path": "/home/dg/keld/x.json"}
	out := redactFields(in)

	v, ok := out["path"].(string)
	if !ok {
		t.Fatalf("expected string value for %q, got %T", "path", out["path"])
	}
	if strings.Contains(v, "/home/dg/keld/x.json") {
		t.Fatalf("redacted value still contains verbatim absolute path: %q", v)
	}
	if v != "x.json" && v != "<path>" {
		t.Fatalf("expected basename %q or %q, got %q", "x.json", "<path>", v)
	}
}

func TestRedactFieldsLongProseStringIsRedacted(t *testing.T) {
	long := strings.Repeat("this is a long sentence with many words in it ", 5)
	if len(long) <= maxFieldLen {
		t.Fatalf("test fixture too short: %d bytes", len(long))
	}
	in := map[string]any{"note": long}
	out := redactFields(in)

	if out["note"] != "<redacted>" {
		t.Fatalf("expected <redacted>, got %v", out["note"])
	}
}

func TestRedactFieldsNumbersAndShortEnumsPassUnchanged(t *testing.T) {
	in := map[string]any{
		"reason":   "deadline",
		"status":   503,
		"attempts": 4,
	}
	out := redactFields(in)

	if out["reason"] != "deadline" {
		t.Fatalf("expected reason unchanged, got %v", out["reason"])
	}
	if out["status"] != 503 {
		t.Fatalf("expected status unchanged, got %v", out["status"])
	}
	if out["attempts"] != 4 {
		t.Fatalf("expected attempts unchanged, got %v", out["attempts"])
	}
}

func TestRedactFieldsFakePromptSentenceIsRedacted(t *testing.T) {
	in := map[string]any{
		"prompt": "please refactor the auth module and delete the old tokens table",
	}
	out := redactFields(in)

	if out["prompt"] != "<redacted>" {
		t.Fatalf("expected <redacted>, got %v", out["prompt"])
	}
}

func TestRedactFieldsNilValueDropped(t *testing.T) {
	in := map[string]any{"foo": nil, "bar": "baz"}
	out := redactFields(in)

	if _, ok := out["foo"]; ok {
		t.Fatalf("expected key %q to be dropped for nil value, got %v", "foo", out["foo"])
	}
	if out["bar"] != "baz" {
		t.Fatalf("expected bar unchanged, got %v", out["bar"])
	}
}

func TestRedactFieldsBoolAndDurationPassUnchanged(t *testing.T) {
	in := map[string]any{
		"ok":       true,
		"timeout":  5 * time.Second,
		"fraction": 1.5,
	}
	out := redactFields(in)

	if out["ok"] != true {
		t.Fatalf("expected ok unchanged, got %v", out["ok"])
	}
	if out["timeout"] != 5*time.Second {
		t.Fatalf("expected timeout unchanged, got %v", out["timeout"])
	}
	if out["fraction"] != 1.5 {
		t.Fatalf("expected fraction unchanged, got %v", out["fraction"])
	}
}

func TestRedactFieldsNestedMapRecurses(t *testing.T) {
	in := map[string]any{
		"nested": map[string]any{
			"path": "/var/log/keld/agent.log",
			"code": "E_TIMEOUT",
		},
	}
	out := redactFields(in)

	nested, ok := out["nested"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested map, got %T", out["nested"])
	}
	pathVal, ok := nested["path"].(string)
	if !ok {
		t.Fatalf("expected string path in nested map, got %T", nested["path"])
	}
	if strings.Contains(pathVal, "/var/log/keld/agent.log") {
		t.Fatalf("nested path leaked verbatim: %q", pathVal)
	}
	if nested["code"] != "E_TIMEOUT" {
		t.Fatalf("expected nested code unchanged, got %v", nested["code"])
	}
}

func TestRedactFieldsUnknownTypeDropped(t *testing.T) {
	in := map[string]any{
		"list": []string{"a", "b", "c"},
		"kept": "ok",
	}
	out := redactFields(in)

	if _, ok := out["list"]; ok {
		t.Fatalf("expected unknown type key %q to be dropped, got %v", "list", out["list"])
	}
	if out["kept"] != "ok" {
		t.Fatalf("expected kept unchanged, got %v", out["kept"])
	}
}

func TestRedactFieldsDoesNotMutateInput(t *testing.T) {
	in := map[string]any{
		"path": "/home/dg/secret/data.json",
		"note": "short",
	}
	_ = redactFields(in)

	if in["path"] != "/home/dg/secret/data.json" {
		t.Fatalf("input map was mutated: path = %v", in["path"])
	}
	if in["note"] != "short" {
		t.Fatalf("input map was mutated: note = %v", in["note"])
	}
}

func TestRedactFieldsNilAndEmptyInput(t *testing.T) {
	if out := redactFields(nil); out == nil {
		// nil result is acceptable per spec, as long as it never panics on use.
		if len(out) != 0 {
			t.Fatalf("expected empty result for nil input, got %v", out)
		}
	}
	out := redactFields(map[string]any{})
	if len(out) != 0 {
		t.Fatalf("expected empty result for empty input, got %v", out)
	}
}

func TestRedactErrorStripsPathAndHasTypeName(t *testing.T) {
	err := fmt.Errorf("open /home/u/secret.txt: denied")
	got := string(RedactError(err))

	if strings.Contains(got, "/home/u/secret.txt") {
		t.Fatalf("RedactError leaked verbatim path: %q", got)
	}
	if !strings.HasPrefix(got, "*errors.errorString") && !strings.Contains(got, ":") {
		t.Fatalf("expected type-name prefix, got %q", got)
	}
	wantType := fmt.Sprintf("%T", err)
	if !strings.HasPrefix(got, wantType+": ") {
		t.Fatalf("expected prefix %q, got %q", wantType+": ", got)
	}
}

func TestRedactErrorNilReturnsEmpty(t *testing.T) {
	if got := RedactError(nil); got != "" {
		t.Fatalf("expected empty string for nil error, got %q", got)
	}
}

func TestRedactFieldsControlCharsRedacted(t *testing.T) {
	cases := map[string]string{
		"newlines": "fix\nthe\nbug\nin\nprod\nauth\ntokens",
		"tabs":     "col1\tcol2\tcol3\tcol4",
		"lone_nl":  "short\n",
	}
	for name, val := range cases {
		out := redactFields(map[string]any{"v": val})
		if out["v"] != "<redacted>" {
			t.Fatalf("%s: expected <redacted>, got %q", name, out["v"])
		}
	}
}

func TestRedactFieldsSingleSegmentPathsNotVerbatim(t *testing.T) {
	for _, p := range []string{"/etc", "/tmp", "/secret"} {
		out := redactFields(map[string]any{"v": p})
		v, _ := out["v"].(string)
		if v == p {
			t.Fatalf("single-segment path leaked verbatim: %q", v)
		}
		if strings.ContainsAny(v, "/") {
			t.Fatalf("expected basename or <redacted>, got %q", v)
		}
	}
}

func TestRedactFieldsUNCPathNotVerbatim(t *testing.T) {
	unc := `\\server\share\file.txt`
	out := redactFields(map[string]any{"v": unc})
	v, _ := out["v"].(string)
	if strings.Contains(v, `\\server\share`) {
		t.Fatalf("UNC path leaked verbatim: %q", v)
	}
}

func TestRedactFieldsPathWithEmbeddedSpaceRedactedWhole(t *testing.T) {
	out := redactFields(map[string]any{"v": "/home/dg/My Documents/secret.txt"})
	v, _ := out["v"].(string)
	if strings.Contains(v, "Documents") || strings.Contains(v, "secret.txt") {
		t.Fatalf("path fragment leaked: %q", v)
	}
	if v != "<redacted>" {
		t.Fatalf("expected whole-value <redacted>, got %q", v)
	}
}

func TestRedactFieldsWindowsPathWithSpaceRedactedWhole(t *testing.T) {
	out := redactFields(map[string]any{"v": `C:\Users\My Docs\x.txt`})
	v, _ := out["v"].(string)
	if strings.Contains(v, "Docs") || strings.Contains(v, "x.txt") {
		t.Fatalf("windows path fragment leaked: %q", v)
	}
	if v != "<redacted>" {
		t.Fatalf("expected whole-value <redacted>, got %q", v)
	}
}

func TestRedactFieldsLoneCleanPathBecomesBasename(t *testing.T) {
	out := redactFields(map[string]any{"v": "/home/dg/keld/x.json"})
	if out["v"] != "x.json" {
		t.Fatalf("expected basename x.json, got %q", out["v"])
	}
}

func TestRedactErrorSingleSegmentPathStripped(t *testing.T) {
	got := string(RedactError(fmt.Errorf("open /etc: denied")))
	if strings.Contains(got, "/etc") {
		t.Fatalf("single-segment path leaked in error: %q", got)
	}
	if !strings.Contains(got, "<path>") {
		t.Fatalf("expected <path> token, got %q", got)
	}
}

func TestRedactErrorUNCPathStripped(t *testing.T) {
	got := string(RedactError(fmt.Errorf(`read \\server\share\f.txt: timeout`)))
	if strings.Contains(got, `\\server\share`) {
		t.Fatalf("UNC path leaked in error: %q", got)
	}
}

// TestRedactErrorLongMessageRedactedNotTruncated proves a message so long it
// would otherwise need truncation is instead fully redacted by the same
// free-text cap a plain field value gets (word/length cap on the MESSAGE),
// rather than leaking a large verbatim (if truncated) blob of the original
// error text. The type prefix still survives and the result stays valid
// UTF-8 (no mid-rune slicing) either way.
func TestRedactErrorLongMessageRedactedNotTruncated(t *testing.T) {
	err := errors.New(strings.Repeat("é", 400))
	got := string(RedactError(err))
	if strings.Contains(got, "é") {
		t.Fatalf("long message leaked verbatim content: %q", got)
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("expected message to be <redacted>, got %q", got)
	}
	wantType := fmt.Sprintf("%T", err)
	if !strings.HasPrefix(got, wantType+": ") {
		t.Fatalf("expected type prefix %q to survive, got %q", wantType+": ", got)
	}
	if !utf8ValidString(got) {
		t.Fatalf("result is not valid UTF-8: %q", got)
	}
}

func TestRedactErrorPathsAtNonWordBoundaries(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		leak string
	}{
		{"quoted", `read "/abs/path": denied`, "/abs/path"},
		{"key_value", "path=/home/u/secret.txt", "/home/u/secret.txt"},
		{"paren", "cfg(/home/u/secret.txt) invalid", "/home/u/secret.txt"},
		{"file_url", "file:///home/u/secret.txt", "/home/u/secret.txt"},
		{"windows_quoted", `stat "C:\Users\x.txt": nope`, `C:\Users\x.txt`},
	}
	for _, c := range cases {
		got := string(RedactError(fmt.Errorf("%s", c.msg)))
		if strings.Contains(got, c.leak) {
			t.Fatalf("%s: leaked verbatim path %q in %q", c.name, c.leak, got)
		}
		if !strings.Contains(got, "<path>") {
			t.Fatalf("%s: expected <path> token, got %q", c.name, got)
		}
	}
}

func TestRedactErrorQuotedPathKeepsQuote(t *testing.T) {
	got := string(RedactError(fmt.Errorf("%s", `read "/abs/path": denied`)))
	if strings.Contains(got, "/abs/path") {
		t.Fatalf("leaked path: %q", got)
	}
	if !strings.Contains(got, `"`) {
		t.Fatalf("expected the quote char retained, got %q", got)
	}
}

func TestRedactFieldsPathsAtNonWordBoundariesRedacted(t *testing.T) {
	cases := map[string]string{
		"key_value":      "path=/home/u/secret.txt",
		"quoted":         `open "/home/u/secret.txt"`,
		"file_url":       "file:///home/u/secret.txt",
		"windows_quoted": `"C:\Users\x.txt"`,
	}
	for name, v := range cases {
		out := redactFields(map[string]any{"v": v})
		got, _ := out["v"].(string)
		if strings.Contains(got, "secret.txt") || strings.Contains(got, `x.txt`) {
			t.Fatalf("%s: leaked path fragment in %q", name, got)
		}
		if strings.Contains(got, "/home/u") || strings.Contains(got, `C:\Users`) {
			t.Fatalf("%s: leaked verbatim path in %q", name, got)
		}
	}
}

func TestRedactFieldsRelativePathNotTreatedAsAbsolute(t *testing.T) {
	out := redactFields(map[string]any{"v": "a/b/c.go"})
	if out["v"] != "a/b/c.go" {
		t.Fatalf("relative path should pass unchanged, got %q", out["v"])
	}
}

func TestRedactFieldsZeroWidthCharRedacted(t *testing.T) {
	// U+200B ZERO WIDTH SPACE (category Cf) between tokens would otherwise let
	// a multi-token prompt read as a single "word".
	out := redactFields(map[string]any{"v": "fix​the​bug​now"})
	if out["v"] != "<redacted>" {
		t.Fatalf("expected <redacted> for zero-width-delimited value, got %q", out["v"])
	}
}

func TestRedactErrorCollapsesMultilineAndTruncates(t *testing.T) {
	msg := "line one\nline two\ttabbed\n" + strings.Repeat("x", 400)
	err := errors.New(msg)
	got := string(RedactError(err))

	if strings.Contains(got, "\n") || strings.Contains(got, "\t") {
		t.Fatalf("expected no raw newlines/tabs, got %q", got)
	}
	if len(got) > maxErrLen+len(fmt.Sprintf("%T", err))+10 {
		t.Fatalf("expected result to be capped, got length %d: %q", len(got), got)
	}
	// The message is well past maxFieldWords once newlines/tabs collapse to
	// spaces, so it gets the SAME free-text redaction a plain field value
	// would — proving the multi-line collapse doesn't accidentally let a long
	// prose-shaped message slip through unredacted.
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("expected message to be <redacted>, got %q", got)
	}
}

// --- Regression coverage for the double-redaction bug: RedactError's output,
// once stored in an event's fields map, previously went through redactFields
// -> redactString -> capField A SECOND time, and capField's free-text cap
// (max 3 words) replaced nearly every real "<Type>: <message>" summary with a
// bare "<redacted>", silently gutting error telemetry. These tests exercise
// the FULL pipeline (RedactError followed by redactFields) rather than
// RedactError alone, since that's the only way to reproduce the bug.

// TestRedactErrorSurvivesFullPipelineWhenShort proves a short, simple error
// summary is no longer clobbered by the second (field-level) redaction pass:
// it must come out exactly as RedactError produced it, not "<redacted>".
func TestRedactErrorSurvivesFullPipelineWhenShort(t *testing.T) {
	err := errors.New("connection refused")
	out := redactFields(map[string]any{"error": RedactError(err)})

	got, ok := out["error"].(string)
	if !ok {
		t.Fatalf("expected string error field, got %T: %v", out["error"], out["error"])
	}
	if got == "<redacted>" {
		t.Fatalf("short multi-word error summary was clobbered by double-redaction: %q", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Fatalf("expected message to survive, got %q", got)
	}
	wantType := fmt.Sprintf("%T", err)
	if !strings.HasPrefix(got, wantType+": ") {
		t.Fatalf("expected type prefix %q, got %q", wantType+": ", got)
	}
}

// TestRedactErrorProseMessageRedactedThroughFullPipeline proves a prompt-
// shaped error message can't leak prompt content through the error field: the
// message half is redacted (by RedactError itself, and NOT un-redacted by the
// second pass), while the type prefix — useful for classification — survives.
func TestRedactErrorProseMessageRedactedThroughFullPipeline(t *testing.T) {
	err := fmt.Errorf("could not classify the following user prompt about quarterly financial planning and layoffs")
	out := redactFields(map[string]any{"error": RedactError(err)})

	got, ok := out["error"].(string)
	if !ok {
		t.Fatalf("expected string error field, got %T: %v", out["error"], out["error"])
	}
	for _, word := range []string{"financial", "layoffs", "quarterly", "planning"} {
		if strings.Contains(got, word) {
			t.Fatalf("prose word %q leaked through the error field: %q", word, got)
		}
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("expected the message to be <redacted>, got %q", got)
	}
	wantType := fmt.Sprintf("%T", err)
	if !strings.HasPrefix(got, wantType+": ") {
		t.Fatalf("expected type prefix %q to survive, got %q", wantType+": ", got)
	}
}

// TestRedactErrorPathNeverLeaksThroughFullPipeline proves a path embedded in
// an error message never appears verbatim anywhere in the fully-redacted
// pipeline output (RedactError followed by redactFields).
func TestRedactErrorPathNeverLeaksThroughFullPipeline(t *testing.T) {
	err := fmt.Errorf("open /home/u/secret.txt: denied")
	out := redactFields(map[string]any{"error": RedactError(err)})

	got, ok := out["error"].(string)
	if !ok {
		t.Fatalf("expected string error field, got %T: %v", out["error"], out["error"])
	}
	if strings.Contains(got, "/home/u/secret.txt") {
		t.Fatalf("path-containing error leaked verbatim path through full pipeline: %q", got)
	}
	if strings.Contains(got, "secret.txt") {
		t.Fatalf("path-containing error leaked a path fragment through full pipeline: %q", got)
	}
}
