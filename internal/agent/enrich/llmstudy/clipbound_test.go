package llmstudy

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// A long real shell command, the shape that made this rule necessary: 93.9% of the corpus's
// `command` arguments exceed toolArgCap, and the old clip(v, 80) cut them mid-token.
const longCommand = `cd /home/dg/keld/keld-signal && grep -rn "distinctiveToken\|distinctiveTerms\|subjectTokens" --include="*.go" internal/`

// TestToolArgIsCutAtAnArgumentBoundary is the headline case. A truncated shell token is a
// FALSE identifier, and window text is not decoration: it is T2's verification reference and
// the source SessionRecord.Observe extracts verbatim-verified Subjects from, so half a token
// becomes an authoritative "subject" that never existed.
//
// Fails before the fix: clip(v, 80) returns
// `cd /home/dg/keld/keld-signal && grep -rn "distinctiveToken\|distinctiveTerms\|subjec`,
// whose last token is a fabricated one.
func TestToolArgIsCutAtAnArgumentBoundary(t *testing.T) {
	got := toolLine("Bash", rawArgs(`{"command":`+quote(longCommand)+`}`))
	if n := runeLen(got); n > len("Bash ")+toolArgCap {
		t.Fatalf("tool line is %d runes, over name+%d: %q", n, toolArgCap, got)
	}
	if !strings.HasSuffix(got, " "+elisionMark) {
		t.Errorf("a clipped argument must say it was clipped: %q", got)
	}
	// Every token that survived must be a token the transcript actually contained.
	for _, tok := range strings.Fields(strings.TrimSuffix(got, " "+elisionMark)) {
		if tok == "Bash" {
			continue
		}
		if !strings.Contains(longCommand, tok) {
			t.Errorf("token %q is not in the original command — the clip manufactured it", tok)
		}
	}
	// A single token longer than the cap is dropped, not sliced: there is no boundary inside
	// it, and half of it is the false identifier this rule is about.
	blob := strings.Repeat("A", 200)
	if got := toolLine("Grep", rawArgs(`{"pattern":`+quote(blob)+`}`)); strings.Contains(got, "AAAA") {
		t.Errorf("an over-long single token was sliced rather than dropped: %q", got)
	}
	// And an argument that fits is returned untouched, spacing included.
	if got := toolLine("Read", rawArgs(`{"file_path":"/a/b/digest_refine.go"}`)); got != "Read digest_refine.go" {
		t.Errorf("a fitting argument was altered: %q", got)
	}
}

// TestTurnTextIsCutAtASentenceEnd pins the window's own bound, including the forward allowance
// that makes it affordable: retreating to the last sentence end inside the budget cost 40.3% of
// the coarse view's content and 10.4% of the mined turns'; reaching forward to the next one costs
// +2.5% and +7.7%, so the delimiter-respecting cut carries slightly MORE text than the rune-count
// cut it replaces. See clipAllowancePct.
//
// The budget is therefore a SHAPING number, not a hard bound — the real context limits are
// asserted on the assembled prompt (assertPromptWithinBudget, assertBeatPromptWithinBudget) — so
// this asserts the allowance rather than the budget.
//
// Fails before the fix: clip() returns exactly PerTurnChars runes ending mid-word.
func TestTurnTextIsCutAtASentenceEnd(t *testing.T) {
	long := strings.Repeat("The close is reconciled against the ledger for Meridian. ", 40)
	got := clipTurn(long, 1200)
	if n, limit := runeLen(got), 1200+1200*clipAllowancePct/100; n > limit {
		t.Fatalf("clipTurn returned %d runes, past the %d-rune allowance", n, limit)
	}
	if !strings.HasSuffix(got, "."+elisionMark) {
		t.Errorf("turn text was not cut at a full stop: %q", tailOf(got))
	}
	// A line break is the fallback when there is no sentence end.
	lines := strings.Repeat("Read foo.go\n", 200)
	got = clipTurn(lines, 100)
	if !strings.HasSuffix(got, "Read foo.go"+elisionMark) {
		t.Errorf("line-structured text was not cut at a line break: %q", tailOf(got))
	}
	// Neither available: the text is a blob, and half a blob is a fabricated specific. It is
	// dropped to the marker rather than sliced.
	if got := clipTurn(strings.Repeat("q", 5000), 240); got != elisionMark {
		t.Errorf("a delimiter-free blob was sliced: %.40q...", got)
	}
	// Text that fits is untouched.
	if got := clipTurn("short and whole", 1200); got != "short and whole" {
		t.Errorf("fitting text was altered: %q", got)
	}
	// The allowance reaches FORWARD, and only that far. A sentence ending just past the budget
	// is kept; one ending far past it is not, and the cut retreats or drops instead.
	near := strings.Repeat("x", 110) + ". tail"
	if got := clipTurn(near, 100); !strings.HasSuffix(got, "."+elisionMark) || runeLen(got) < 100 {
		t.Errorf("a sentence ending just past the budget was not reached: %d runes, %q",
			runeLen(got), tailOf(got))
	}
	far := strings.Repeat("y", 400) + ". tail"
	if got := clipTurn(far, 100); runeLen(got) > 100 {
		t.Errorf("the allowance reached %d runes for a boundary 400 runes out; it must retreat "+
			"or drop instead", runeLen(got))
	}
}

// TestMinedWindowHasNoHalfSentences drives the real miner, because clipTurn being correct is
// only useful if buildWindow actually calls it.
func TestMinedWindowHasNoHalfSentences(t *testing.T) {
	long := strings.Repeat("The reconciliation ran against the March ledger. ", 60)
	recs := []record{
		{role: RoleUser, text: "start the close", id: "p1"},
		{role: RoleAssistant, text: long},
		{role: RoleUser, text: long, id: "p2"},
	}
	w := buildWindow("s", recs, 2, MineOpts{K: 12, PerTurnChars: 1200, WindowChars: 12000})
	for i, turn := range w.Turns {
		if runeLen(turn.Text) < 1200 {
			continue
		}
		if !strings.HasSuffix(turn.Text, "."+elisionMark) {
			t.Errorf("turn %d ends mid-clause: %q", i, tailOf(turn.Text))
		}
	}
	if !strings.HasSuffix(w.Target, "."+elisionMark) {
		t.Errorf("the target turn ends mid-clause: %q", tailOf(w.Target))
	}
}

// TestOpenItemsAreWholeOrAbsent is the site with the worst consequence: the block's own
// instruction is "account for EVERY one, in exactly one place", and the items were being
// amputated mid-clause first.
//
// Fails before the fix: each 133-rune item comes back as
// "The server-side entity storage does not apply redaction, creating a potential…".
func TestOpenItemsAreWholeOrAbsent(t *testing.T) {
	real := []string{
		"waiting on vendor confirmation of the rollback window",
		"The server-side entity storage does not apply redaction, creating a potential inconsistency between stored and shown.",
		"User feedback indicates the current design is still messy overall, so the visual hierarchy needs another pass.",
	}
	got := priorOpenItems(Digest{Unresolved: real})
	if len(got) != len(real) {
		t.Fatalf("got %d items for %d real ones: %v", len(got), len(real), got)
	}
	for i := range real {
		if got[i] != real[i] {
			t.Errorf("item %d was altered:\n  had %q\n  got %q", i, real[i], got[i])
		}
	}
	// The pathological case drops WHOLE items and says how many.
	var big []string
	for i := 0; i < DefaultListCap; i++ {
		big = append(big, strings.Repeat("z", 300))
	}
	got = priorOpenItems(Digest{Unresolved: big})
	total := 0
	for _, it := range got {
		total += runeLen(it)
	}
	if total > promptOpenItemsMaxTotal {
		t.Errorf("open-item block is %d runes, over the %d bound the per-item clip already implied",
			total, promptOpenItemsMaxTotal)
	}
	if !strings.HasPrefix(got[0], "(") || !strings.Contains(got[0], "omitted") {
		t.Errorf("items were dropped without saying so: first entry %q", got[0])
	}
	for _, it := range got[1:] {
		if runeLen(it) != 300 {
			t.Errorf("a kept item was clipped to %d runes; kept items must be whole", runeLen(it))
		}
	}
}

// TestClippedViewSaysItWasClipped: a shorter input that says nothing about being shorter is the
// silent-drop failure one level up from a mid-sentence cut.
func TestClippedViewSaysItWasClipped(t *testing.T) {
	view := strings.Repeat("user: an early turn about the Meridian close.\n", 200)
	got := clipLines(view, 900)
	if n := runeLen(got); n > 900 {
		t.Fatalf("clipLines returned %d runes over a 900 budget", n)
	}
	if !strings.HasSuffix(got, viewOmittedNotice) {
		t.Errorf("a clipped view did not say so: %q", tailOf(got))
	}
	// Whole lines only: every line kept is a line the view contained.
	for _, ln := range strings.Split(strings.TrimSuffix(got, viewOmittedNotice), "\n") {
		if ln == "" {
			continue
		}
		if !strings.Contains(view, ln+"\n") {
			t.Errorf("line %q is not a whole line of the view", ln)
		}
	}
	if got := clipLines(view[:40], 900); got != view[:40] {
		t.Error("a view that fits must be returned untouched")
	}
}

// TestSubjectTermsAreDroppedNotTruncated pins the property maxSubjectTermLen's doc claims and
// no test asserted: the existing guards only check that no term EXCEEDS the cap, which a
// truncating implementation would satisfy while handing the model a path that does not exist
// inside a block labelled authoritative.
func TestSubjectTermsAreDroppedNotTruncated(t *testing.T) {
	long := "docs/superpowers/specs/2026-07-05-keld-agent-loadtest-and-memory-eviction-design.md"
	if runeLen(long) <= maxSubjectTermLen {
		t.Fatalf("the fixture path is %d runes, not over the %d-rune cap", runeLen(long), maxSubjectTermLen)
	}
	text := "We touched " + long + " twice while reconciling Meridian."
	w := Window{Turns: []Turn{{RoleUser, text}}}
	for _, s := range RecentSubjects(w, 1) {
		if strings.HasPrefix(long, s) && s != long {
			t.Errorf("anchor carries a TRUNCATED path %q — a path that does not exist", s)
		}
	}
	rec := SessionRecord{}.Observe(w, Signals{Turns: 1, UserTurns: 1})
	for _, s := range rec.Subjects {
		if strings.HasPrefix(long, s) && s != long {
			t.Errorf("record carries a TRUNCATED path %q as a verbatim-verified subject", s)
		}
	}
}

// TestEntryClipPrefersASentenceOverTheCap states the one place where the rule and a cap
// disagree, so the choice is visible rather than inferred from behaviour.
func TestEntryClipPrefersASentenceOverTheCap(t *testing.T) {
	twoSentences := strings.Repeat("A sentence about the ledger. ", 20)
	got := clipEntry(twoSentences, 300)
	if runeLen(got) > 300 || !strings.HasSuffix(got, "."+elisionMark) {
		t.Errorf("an entry with sentence structure must be cut at a full stop inside the cap: %q", tailOf(got))
	}
	oneLongClause := strings.Repeat("and then ", 60)
	if got := clipEntry(oneLongClause, 300); got != oneLongClause {
		t.Errorf("a single un-terminated clause must be kept whole, got %d runes", runeLen(got))
	}
}

// rawArgs parses a tool_use input object the way parseRecord does, so toolLine is exercised
// through its real input type rather than a hand-built map.
func rawArgs(s string) map[string]json.RawMessage {
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		panic(err)
	}
	return out
}

// quote renders a string as a JSON string literal, so a fixture command carrying backslashes
// and quotes reaches toolLine exactly as the transcript held it.
func quote(s string) string { return strconv.Quote(s) }
