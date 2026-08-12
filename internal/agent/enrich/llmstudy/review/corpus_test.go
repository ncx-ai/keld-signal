package review

import (
	"strings"
	"testing"
)

// fixtureDoc is a miniature of the source document, including the two shapes that break a
// naive parser: a conversation window whose verbatim content contains markdown headings and
// a "---" rule, and a beat that failed to generate and has no output.
const fixtureDoc = "# Inputs and outputs, for review\n\nBlurb.\n\n---\n\n" +
	"# First session\n\n*Software · 8 mined windows*\n\n" +
	"## Beat 1 (window 0 of 8) · marked SUBJECT CHANGED\n\n" +
	"### Input 1 — measured record (counted on device, not generated)\n\n" +
	"```\ncounts: turns=1 user_turns=1 tool_calls=0 corrections=0\nprojects: alpha\nrecurring subjects: widget.go\n```\n\n" +
	"### Input 2 — conversation window (40 runes)\n\n" +
	"```\nuser: fix widget.go please\n\n## A heading inside the window\n\n---\n\nassistant: done\n```\n\n" +
	"### Output\n\n> Fixing widget.go. It now compiles.\n\n---\n\n" +
	"## Beat at window 3 — GENERATION FAILED\n\n```\nretry: gave up after 5 attempt(s)\n```\n\n" +
	"## Beat 2 (window 4 of 8)\n\n" +
	"### Input 1 — measured record (counted on device, not generated)\n\n" +
	"```\ncounts: turns=9 user_turns=3 tool_calls=4 corrections=1\nprojects: alpha\nrecurring subjects: widget.go, gadget.go, alpha/cmd\n```\n\n" +
	"### Input 2 — conversation window (60 runes)\n\n" +
	"```\nuser: now gadget.go\nassistant: gadget.go updated\n```\n\n" +
	"### Output\n\n> Moving on to gadget.go, which is updated\n> and building.\n\n---\n\n" +
	"# Second session\n\n*Accounting · 4 mined windows*\n\n" +
	"## Beat 1 (window 0 of 4) · marked SUBJECT CHANGED\n\n" +
	"### Input 1 — measured record (counted on device, not generated)\n\n" +
	"```\ncounts: turns=2 user_turns=1 tool_calls=0 corrections=0\nprojects: books\nrecurring subjects: ledger.csv\n```\n\n" +
	"### Input 2 — conversation window (30 runes)\n\n" +
	"```\nuser: close out the ledger\n```\n\n" +
	"### Output\n\n> Closing the ledger for the period.\n"

func parseFixture(t *testing.T) Corpus {
	t.Helper()
	c, skipped, err := ParseCorpus(fixtureDoc)
	if err != nil {
		t.Fatalf("ParseCorpus: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1 (the GENERATION FAILED beat)", skipped)
	}
	return c
}

func TestParseCorpusReadsSessionsBeatsAndBothInputs(t *testing.T) {
	c := parseFixture(t)
	if len(c.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(c.Sessions))
	}
	if c.Sessions[0].Title != "First session" || c.Sessions[0].Domain != "Software" {
		t.Errorf("session 0 = %q/%q", c.Sessions[0].Title, c.Sessions[0].Domain)
	}
	if c.Sessions[1].Domain != "Accounting" {
		t.Errorf("session 1 domain = %q, want Accounting", c.Sessions[1].Domain)
	}
	if got := len(c.Items()); got != 3 {
		t.Fatalf("items = %d, want 3", got)
	}
	one := c.Sessions[0].Items[0]
	if one.WindowIndex != 0 || one.WindowCount != 8 || !one.MarkedSubjectChanged {
		t.Errorf("beat 1 coords = %d of %d changed=%v", one.WindowIndex, one.WindowCount, one.MarkedSubjectChanged)
	}
	if !strings.Contains(one.Record, "recurring subjects: widget.go") {
		t.Errorf("record not captured: %q", one.Record)
	}
	if one.Output != "Fixing widget.go. It now compiles." {
		t.Errorf("output = %q", one.Output)
	}
	two := c.Sessions[0].Items[1]
	if two.MarkedSubjectChanged {
		t.Error("beat 2 carries no SUBJECT CHANGED marker but parsed as changed")
	}
	// A blockquote wrapped over two lines is one statement, joined with a single space.
	if two.Output != "Moving on to gadget.go, which is updated and building." {
		t.Errorf("wrapped output = %q", two.Output)
	}
}

// The GENERATION FAILED block sits between beat 1 and beat 2 and carries a fenced retry
// message of its own. The first version of this parser left that fence in the buffer, so it
// became beat 2's "measured record" and beat 2's real record became its window: a packet
// whose evidence belonged to a different item, silently. Every input is therefore asserted
// here, not just the ones the heading advertises.
func TestParseCorpusIsNotDerailedByAFailedBeatsOwnFence(t *testing.T) {
	c := parseFixture(t)
	two := c.Sessions[0].Items[1]
	if strings.Contains(two.Record, "gave up") || strings.Contains(two.Window, "gave up") {
		t.Fatalf("the failed beat's retry fence leaked into beat 2:\nrecord=%q\nwindow=%q", two.Record, two.Window)
	}
	if !strings.Contains(two.Record, "corrections=1") || !strings.Contains(two.Record, "gadget.go, alpha/cmd") {
		t.Errorf("beat 2 record = %q", two.Record)
	}
	if !strings.Contains(two.Window, "user: now gadget.go") {
		t.Errorf("beat 2 window = %q", two.Window)
	}
	// The same structural invariant, over every item: a record that does not begin with the
	// counts line, or an empty window, means the two inputs have been shifted by one.
	for _, it := range c.Items() {
		if !strings.HasPrefix(it.Record, "counts: turns=") {
			t.Errorf("beat %d of %q: record does not start with the counts line: %q", it.Ordinal, it.SessionTitle, firstLine(it.Record))
		}
		if strings.TrimSpace(it.Window) == "" {
			t.Errorf("beat %d of %q: empty window", it.Ordinal, it.SessionTitle)
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// A window is quoted transcript, so it can contain anything markdown means something by.
// The parser must keep it whole: a window cut at its own "## " heading is a different piece
// of evidence from the one the writer saw, and the reviewer would be judging a fabrication
// the packaging introduced.
func TestParseCorpusKeepsAWindowWhoseContentLooksLikeMarkdown(t *testing.T) {
	c := parseFixture(t)
	w := c.Sessions[0].Items[0].Window
	for _, want := range []string{"user: fix widget.go please", "## A heading inside the window", "---", "assistant: done"} {
		if !strings.Contains(w, want) {
			t.Errorf("window lost %q; got:\n%s", want, w)
		}
	}
}

func TestFindAndPrecedingAddressItemsByProvenance(t *testing.T) {
	c := parseFixture(t)
	it, err := c.Find("First session", 2)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if it.WindowIndex != 4 {
		t.Errorf("found the wrong beat: window %d", it.WindowIndex)
	}
	if prev := c.Preceding(it); len(prev) != 1 || prev[0].Ordinal != 1 {
		t.Errorf("Preceding = %+v, want just beat 1", prev)
	}
	if prev := c.Preceding(c.Sessions[1].Items[0]); len(prev) != 0 {
		t.Errorf("Preceding of a session's first beat = %d items, want 0", len(prev))
	}
	if _, err := c.Find("First session", 9); err == nil {
		t.Error("Find on a missing ordinal returned no error")
	}
	if _, err := c.Find("No such session", 1); err == nil {
		t.Error("Find on a missing session returned no error")
	}
}

func TestParseCorpusRejectsAnUnclosedFence(t *testing.T) {
	if _, _, err := ParseCorpus(fixtureDoc + "\n```\nunterminated\n"); err == nil {
		t.Fatal("an unclosed fence parsed cleanly")
	}
}
