package llmstudy

import (
	"strings"
	"testing"
)

func TestDigestSchemaIsStrictAndComplete(t *testing.T) {
	s := DigestSchema()
	if s["additionalProperties"] != false {
		t.Error("schema must be strict so the model cannot invent sections")
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema has no properties")
	}
	want := []string{"synopsis", "done", "happened", "structure", "insights", "current", "why", "next", "unresolved"}
	for _, f := range want {
		if _, ok := props[f]; !ok {
			t.Errorf("schema missing required section %q", f)
		}
	}
	req, ok := s["required"].([]string)
	if !ok || len(req) != len(want) {
		t.Fatalf("required = %v, want all %d sections", s["required"], len(want))
	}
	for _, f := range []string{"insights", "unresolved"} {
		if props[f].(map[string]any)["type"] != "array" {
			t.Errorf("%s must be an array", f)
		}
	}
	for _, f := range []string{"synopsis", "done", "happened", "structure", "current", "why", "next"} {
		if props[f].(map[string]any)["type"] != "string" {
			t.Errorf("%s must be a string", f)
		}
	}
}

// unresolved defeats rubberstamping structurally: a required field the model must
// address means an all-positive report cannot validate.
func TestUnresolvedIsRequired(t *testing.T) {
	for _, r := range DigestSchema()["required"].([]string) {
		if r == "unresolved" {
			return
		}
	}
	t.Fatal("unresolved must be required, or an all-positive report validates")
}

// The digest must serve accountants and marketers, not only engineers.
func TestDigestPromptIsDomainNeutral(t *testing.T) {
	facts := FactsFrom(Signals{Turns: 4, UserTurns: 2}, nil).
		WithPlace("", "", "Q2 close").WithFocus("finance", "fin", 0.9)
	p := DigestCreatePrompt("finance / invoicing", "user: reconcile the ledger\n", facts.Block())
	for _, b := range []string{"codebase", "test suite", "deploy", "repository", "compile"} {
		if strings.Contains(strings.ToLower(p), b) {
			t.Errorf("prompt mentions %q — not domain-neutral", b)
		}
	}
	for _, want := range []string{"reconcile the ledger", "turns=4", "unresolved", "function=fin"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt omits %q", want)
		}
	}
}

// The measured context must be presented as binding, or the prose can contradict it
// and rubberstamping becomes unmeasurable.
func TestDigestPromptTreatsFactsAsAuthoritative(t *testing.T) {
	facts := FactsFrom(Signals{Turns: 9, UserTurns: 3, Corrections: 3}, nil)
	p := DigestCreatePrompt("x", "user: hi\n", facts.Block())
	for _, want := range []string{"authoritative", "corrections=3", "did not go smoothly"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt must bind the prose to the counts; missing %q", want)
		}
	}
}

func TestValidateDigestRejectsEmptyProseAndEmptyUnresolved(t *testing.T) {
	full := Digest{Synopsis: "y", Done: "d", Happened: "h", Structure: "s", Current: "c",
		Why: "w", Next: "n", Unresolved: []string{"nothing open"}}
	if p := ValidateDigest(full); len(p) != 0 {
		t.Fatalf("a complete digest must validate, got %v", p)
	}
	if p := ValidateDigest(Digest{Synopsis: "y", Done: "d", Happened: "h", Structure: "s",
		Current: "c", Why: "w", Next: "n"}); len(p) != 1 {
		t.Errorf("empty unresolved must be flagged, got %v", p)
	}
	// Seven prose sections now: synopsis leads them.
	missing := ValidateDigest(Digest{Unresolved: []string{"x"}})
	if len(missing) != 7 {
		t.Errorf("all seven prose sections must be flagged when empty, got %v", missing)
	}
}

// Real output showed two repeatable defects: sections restating each other, and
// assistant-centric phrasing ("the assistant modified X") that reads wrongly in a
// manager's report. Both are addressed in the prompt, so both are pinned here.
func TestDigestPromptForbidsRestatementAndAssistantVoice(t *testing.T) {
	p := DigestCreatePrompt("x", "user: hi\n", FactsFrom(Signals{Turns: 3}, nil).Block())
	for _, want := range []string{
		"Write about the WORK",
		"never who typed what",
		"Nothing in\n    these instructions is subject matter",
		"must add something the others do not",
		"QUESTIONS to answer, not text to copy",
		"Describe the SUBJECT, not",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing guidance %q", want)
		}
	}
}

// TestCreatePathAccountsForItsOwnHeadersBeforeComputingRoom is the fix for task-7b
// finding (c): DigestCreatePromptWithView called
// clipSessionViewFor(sessionView, b.Len()+createTailLen()) and only THEN wrote its own
// "\n\nWHOLE SESSION so far..." header — the same header-omission bug already fixed on
// the refine path in commit 1531ef0, still live here. The same gap existed a second time
// on this path: "\n\nMOST RECENT PART OF THE CONVERSATION, in detail:\n" (this path's
// equivalent of the refine path's windowHeader) is ALSO written between the view and the
// turns, and its length was never subtracted either — omitting it left the window able
// to land short even with the view's own header fixed. clipSessionViewFor's MinTurnChars
// reservation is meant to be a floor on the TURNS CONTENT alone (see its doc and
// fitDiscretionary's, which draws the identical distinction for windowHeader on the
// refine path), so any header written between the view and the turns is overhead on top
// of that reservation, not something the floor is supposed to absorb.
//
// Measures the ROOM actually left for the turns — the point where fitTurns is invoked,
// immediately after createWindowHeader — rather than the post-fitTurns window content.
// fitTurns' own line-boundary trim (task-7b finding (b), digest_fit.go) applies
// unconditionally once room already sits below MinTurnChars, so once room is pushed
// under the floor by ANY margin the actual trimmed window can lose a further, unrelated
// amount that has nothing to do with header accounting. Measuring room directly isolates
// this fix from that one.
//
// Confirmed by reverting viewOverhead to `b.Len()+createTailLen()` (both header-length
// terms dropped) and re-running: room measured 1542 runes, 58 below the 1600 floor. Test
// fails. With the fix restored, room measures 1605, 5 above the floor — the small
// residual slack either side of an exact 1600 is clipProse's own ellipsis reservation on
// the view content (see clipProse's doc) and sub-rune rounding from the header text's
// em-dash, both unrelated to the header-omission bug this test guards, hence the
// tolerance below rather than an exact-equality assertion.
func TestCreatePathAccountsForItsOwnHeadersBeforeComputingRoom(t *testing.T) {
	base := "counts: turns=900 corrections=3 tool_calls=400 domain=engineering function=software-development "
	// Large enough that the VIEW's own room, not SessionViewCap, is the binding
	// constraint on its size — only in that regime does the header omission matter,
	// since a cap-bound view is sized the same whether or not the headers are accounted.
	facts := base + strings.Repeat("extra=1 ", 400)
	// Long enough that the view is clipped to fill its allotted room exactly, rather than
	// passing through short and making the header accounting moot.
	sessionView := strings.Repeat("user: an early turn about the work in this session, discussing details\n", 100)
	turns := strings.Repeat("user: hi\n", 400)

	p := DigestCreatePromptWithView("work session", turns, sessionView, facts)

	bytePos := strings.Index(p, createWindowHeader)
	if bytePos < 0 {
		t.Fatal("createWindowHeader missing from prompt")
	}
	// Rune count, not the raw byte index: several of the fixed strings ahead of it
	// (e.g. the "MEASURED COUNTS" header) contain an em-dash, a multi-byte rune, and
	// DefaultPromptCharBudget/MinTurnChars are both rune budgets.
	before := len([]rune(p[:bytePos])) + len([]rune(createWindowHeader))
	room := DefaultPromptCharBudget - (before + createTailLen())
	t.Logf("room for turns: %d (floor %d, margin %d)", room, MinTurnChars, room-MinTurnChars)

	const tolerance = 10 // clipProse's ellipsis reservation + the em-dash byte/rune gap, not this bug
	if room < MinTurnChars-tolerance {
		t.Errorf("room for turns starved to %d, more than %d below the %d floor — the create "+
			"path's own headers were not fully accounted for before sizing the view", room, tolerance, MinTurnChars)
	}
}
