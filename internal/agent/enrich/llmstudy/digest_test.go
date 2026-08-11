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

// Length is the model's business, and it is stated where the model can act on it.
//
// CapSections used to enforce it after the fact, clipping each section at a rune count. That is
// removed (see its doc), which leaves length unsaid unless the prompt says it — so the section
// descriptions carry it, in sentences, and say explicitly that the figures are guides. A guide
// the writer can weigh is a different instruction from a cut applied to a finished paragraph:
// only one of them can produce "…" mid-clause.
//
// Both paths are checked. digestSections is shared between create and refine, and a guide that
// reached only the first report would leave every refinement writing to no guidance at all.
func TestPromptStatesLengthAsGuidanceNotALimit(t *testing.T) {
	for name, p := range map[string]string{
		"create": DigestCreatePrompt("work session", "user: reconcile the ledger\n", "counts: turns=4\n"),
		"refine": DigestUpdatePrompt(Digest{Done: "x"}, "work session", "user: now do April\n", "counts: turns=8\n"),
	} {
		if !strings.Contains(p, "GUIDES, not limits") {
			t.Errorf("%s prompt does not say the lengths are guides", name)
		}
		if !strings.Contains(p, "Nothing cuts your answer short") {
			t.Errorf("%s prompt does not tell the model its answer is not truncated", name)
		}
		// One guide per prose section that used to be governed by a cap instead. The synopsis's
		// was always here; the other six had a rune cap and no wording at all.
		for _, want := range []string{
			"Three or four sentences", // synopsis
			"A few sentences.",        // done, next
			"A sentence or two.",      // current, why
			"and more when there was real difficulty to account for", // happened
			"Build the fullest picture the conversation supports",    // structure
		} {
			if !strings.Contains(p, want) {
				t.Errorf("%s prompt omits the length guide %q", name, want)
			}
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
// ⚠️ RE-CALIBRATED, and the figures this docstring used to quote were wrong. It said
// "reverting viewOverhead to b.Len()+createTailLen() … room measured 1542 runes, 58 below
// the 1600 floor. Test fails. With the fix restored, room measures 1605". That was true at
// the 11,000 budget. The 11,000 -> 14,000 raise decalibrated it: room measured 4554 BOTH
// ways, bit-identical, and reverting the fix left the test PASSING — the SIXTH instance in
// this branch of a test certifying a bound the code does not enforce, and the third caused
// by that budget raise (the two refine-path ones were fixed; this create-path one was never
// re-checked).
//
// The mechanism that disarmed it is worth naming, because it is not "the margin got
// comfortable". clipSessionViewFor sizes the view as
// min(SessionViewCap, budget-overhead-MinTurnChars-notice). The header terms this test
// guards only appear in `overhead`, so they can only change the view's size while the ROOM
// term is the smaller of the two. At 14,000 with a ~3,300-rune facts block the room term was
// ~8,800 and the view was SessionViewCap-bound, in which regime the headers are provably
// irrelevant — precisely the "where the bug is invisible" regime the old fixture comment
// named, which it had silently drifted into.
//
// So the fixture is now sized FROM THE LIVE CONSTANTS rather than from a literal repeat
// count, and the room-bound regime is asserted as a PRECONDITION with t.Fatal. A future
// budget/cap change can therefore make this test fail loudly, but it cannot silently disarm
// it again.
//
// Re-verified by revert, and the failure MODE has changed since this test was written:
// restored, view 1,554 runes (under SessionViewCap, so room-bound) and room 1,705 (105 above
// the floor — clipSessionViewFor's own omittedNotice reservation plus clipProse's ellipsis
// slack) — PASSES. Reverted, it fails inside DigestCreatePromptWithView, on
// assertPromptWithinBudget: "conversation window was clipped to 1449 runes of content, below
// the 1600-rune floor". The backstop landed after this test and now catches the same defect
// first, so the revert panics rather than reaching the room assertion below. Both are
// failures and the panic is the more precise one; the room measurement is kept because it
// isolates header accounting from fitTurns' boundary trim, which the backstop's figure mixes.
func TestCreatePathAccountsForItsOwnHeadersBeforeComputingRoom(t *testing.T) {
	base := "counts: turns=900 corrections=3 tool_calls=400 domain=engineering function=software-development "
	// The VIEW's own room, not SessionViewCap, must be the binding constraint on its size —
	// only in that regime does the header omission change anything at all. That needs the
	// FIXED part of the prompt to be large enough that budget-overhead-floor-notice falls
	// below SessionViewCap, and this derives the threshold from the constants that decide it
	// rather than from a repeat count that a budget change can invalidate.
	fixedNeeded := DefaultPromptCharBudget - SessionViewCap - MinTurnChars -
		runeLen(omittedNotice) - createTailLen() -
		runeLen(createViewHeader) - runeLen(createWindowHeader)
	// A margin past the crossover so the view is comfortably room-bound, and the facts block
	// carries it because it is the one caller-supplied section on this path with no cap.
	const roomBoundMargin = 400
	pad := fixedNeeded + roomBoundMargin - len(base)
	if pad < 0 {
		pad = 0
	}
	facts := base + strings.Repeat("extra=1 ", pad/8+1)
	// Long enough that the view is clipped to fill its allotted room exactly, rather than
	// passing through short and making the header accounting moot.
	sessionView := strings.Repeat("user: an early turn about the work in this session, discussing details\n", 100)
	turns := strings.Repeat("user: hi\n", 400)

	p := DigestCreatePromptWithView("work session", turns, sessionView, facts)

	bytePos := strings.Index(p, createWindowHeader)
	if bytePos < 0 {
		t.Fatal("createWindowHeader missing from prompt")
	}
	// PRECONDITION, not an assertion about the fix: a SessionViewCap-bound view makes this
	// test vacuous. Fatal rather than skip — a test that can no longer see its own bug must
	// say so, which is the whole lesson of the decalibration above.
	viewStart := strings.Index(p, createViewHeader)
	if viewStart < 0 {
		t.Fatal("createViewHeader missing from prompt — the view was dropped entirely")
	}
	viewLen := runeLen(p[viewStart+len(createViewHeader) : bytePos])
	t.Logf("view content: %d runes (SessionViewCap %d)", viewLen, SessionViewCap)
	if viewLen >= SessionViewCap {
		t.Fatalf("the view is SessionViewCap-bound at %d runes, so this test cannot see the "+
			"header-accounting bug it exists to guard — re-calibrate the fixture (see the "+
			"docstring: this is how the 11,000 -> 14,000 budget raise disarmed it)", viewLen)
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
