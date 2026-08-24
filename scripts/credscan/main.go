// Command credscan measures the deterministic credential detector
// (internal/agent/enrich/enrich.CredentialSpans, i.e. creddetect + its
// placeholder gate) over a corpus of real prompts.
//
// It is the credential analogue of scripts/pii_precision.py, and it exists for
// the same reason: a recall number on a 42-row synthetic fixture says nothing
// about whether a detector floods real developer prose. Three precision claims
// on this branch that were not corpus-measured were all wrong.
//
// PRIVACY. Input is real prompt text on stdin and it never leaves this process
// intact. Each finding is reduced, before it is written, to:
//
//	mask   first/last two characters ("po…5s") — enough to tell a leaked
//	       password from a UUID, not enough to be the credential
//	shape  character classes (D/a/A, punctuation literal) — carries no content
//	length, rule id, and the surrounding scheme/host for URI findings, which
//	       are not the secret
//
// The raw matched substring is never printed and never written.
//
// Usage:
//
//	go run ./scripts/credscan < prompts.ndjson > findings.ndjson
//
// stdin  NDJSON, one {"id": <string>, "text": <string>} per line.
// stdout NDJSON, one finding per line, plus a final {"kind":"summary",...}.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/creddetect"
)

type input struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type finding struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Label  string `json:"label"`
	Mask   string `json:"mask"`
	Shape  string `json:"shape"`
	Length int    `json:"length"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

type summary struct {
	Kind          string         `json:"kind"`
	Prompts       int            `json:"prompts"`
	PromptsHit    int            `json:"prompts_hit"`
	Spans         int            `json:"spans"`
	SpansByLabel  map[string]int `json:"spans_by_label"`
	PromptsByLabl map[string]int `json:"prompts_by_label"`
}

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 64<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	enc := json.NewEncoder(out)

	s := summary{Kind: "summary", SpansByLabel: map[string]int{}, PromptsByLabl: map[string]int{}}
	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		var rec input
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			fmt.Fprintf(os.Stderr, "skipping unparseable line: %v\n", err)
			continue
		}
		s.Prompts++
		// creddetect.Detect + IsPlaceholder is exactly what
		// enrich.CredentialSpans does; it is inlined here because Detect
		// carries the RULE ID and CredentialSpans flattens every rule to the
		// single label "api_key". Which rule fires is the entire diagnostic
		// value of this measurement.
		var spans []creddetect.Span
		for _, sp := range creddetect.Detect(rec.Text) {
			if creddetect.IsPlaceholder(rec.Text[sp.Start:sp.End]) {
				continue
			}
			spans = append(spans, sp)
		}
		if len(spans) > 0 {
			s.PromptsHit++
		}
		seen := map[string]bool{}
		for _, sp := range spans {
			v := rec.Text[sp.Start:sp.End]
			label := sp.RuleID
			s.Spans++
			s.SpansByLabel[label]++
			if !seen[label] {
				seen[label] = true
				s.PromptsByLabl[label]++
			}
			if err := enc.Encode(finding{
				Kind: "finding", ID: rec.ID, Label: label,
				Mask: mask(v), Shape: shape(v), Length: len(v), Start: sp.Start, End: sp.End,
			}); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
	}
	if err := in.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := enc.Encode(s); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// mask renders first/last two characters, or less when the value is too short
// to spare them. Same bands as scripts/pii_precision.py.
func mask(v string) string {
	r := []rune(v)
	switch n := len(r); {
	case n <= 4:
		return "…"
	case n <= 8:
		return string(r[0]) + "…" + string(r[n-1])
	default:
		return string(r[:2]) + "…" + string(r[n-2:])
	}
}

// shape renders the value's character classes and carries none of its content.
func shape(v string) string {
	const cap = 44
	var b strings.Builder
	for _, c := range v {
		switch {
		case unicode.IsDigit(c):
			b.WriteRune('D')
		case unicode.IsLower(c):
			b.WriteRune('a')
		case unicode.IsUpper(c):
			b.WriteRune('A')
		case c == '\n':
			b.WriteRune('\\')
		default:
			b.WriteRune(c)
		}
	}
	s := b.String()
	if r := []rune(s); len(r) > cap {
		return string(r[:cap]) + "…"
	}
	return s
}
