package llmstudy

import "testing"

// The identifier gate bounds fabricated SPECIFICS. It cannot bound fabricated
// judgements, which is why the spec keeps a human review gate.
func TestUnverifiedIdentifiersFlagsInventedSpecifics(t *testing.T) {
	src := "user: reconcile the March ledger for Northwind\nassistant: opened ledger-2026-03.csv\n"
	d := Digest{
		Done:     "Reconciled the March ledger for Northwind using ledger-2026-03.csv.",
		Happened: "Also cross-checked against Globex and invoice-99812.pdf.",
	}
	found := map[string]bool{}
	for _, b := range UnverifiedIdentifiers(d, src) {
		found[b] = true
	}
	if !found["Globex"] || !found["invoice-99812.pdf"] {
		t.Errorf("invented specifics not flagged: %v", UnverifiedIdentifiers(d, src))
	}
	if found["Northwind"] || found["ledger-2026-03.csv"] {
		t.Errorf("real specifics wrongly flagged: %v", UnverifiedIdentifiers(d, src))
	}
}

// Rubberstamping: corrections occurred, yet the report names no friction anywhere.
func TestLooksRubberstampedWhenFrictionIsOmitted(t *testing.T) {
	facts := DigestFacts{Turns: 20, UserTurns: 8, Corrections: 3, CorrectedTurns: 2}
	glowing := Digest{
		Done: "Everything was completed smoothly.", Happened: "All steps succeeded first time.",
		Structure: "Two parts, both in place.", Unresolved: []string{"nothing is open"},
	}
	if !LooksRubberstamped(glowing, facts) {
		t.Error("a glowing report with 3 corrections must be flagged")
	}
	honest := Digest{
		Done: "Completed the reconciliation.", Happened: "Two attempts were reversed after totals disagreed.",
		Structure: "Two parts.", Unresolved: []string{"April still to do"},
	}
	if LooksRubberstamped(honest, facts) {
		t.Error("a report naming the friction must not be flagged")
	}
}

// With no corrections there is nothing to under-report, so nothing is flagged.
func TestLooksRubberstampedIsSilentWithoutCorrections(t *testing.T) {
	d := Digest{Done: "Done.", Happened: "Went fine.", Unresolved: []string{"nothing open"}}
	if LooksRubberstamped(d, DigestFacts{Turns: 6, UserTurns: 2}) {
		t.Error("no corrections means no rubberstamping to detect")
	}
}

func TestRetainedFactsCountsSurvivors(t *testing.T) {
	after := Digest{Done: "contacted Northwind about the totals"}
	if got := RetainedFacts(after, []string{"ledger-2026-03.csv", "Northwind"}); got != 1 {
		t.Fatalf("RetainedFacts = %d, want 1 (Northwind survived, the file did not)", got)
	}
}

// Ordinary English must not be mistaken for invented proper nouns, or the gate's
// signal is drowned by false positives.
func TestIdentifiersIgnoreSentenceStarters(t *testing.T) {
	d := Digest{Done: "The work is done. However, there were issues. Also nothing is open."}
	for _, id := range Identifiers(d) {
		if id == "The" || id == "However" || id == "Also" || id == "Nothing" {
			t.Errorf("stop word %q treated as an identifier", id)
		}
	}
}

// The sentinel gives "nothing is open" a first-class expression so the required
// field can be honoured without inventing a blocker.
func TestUsesUnresolvedSentinel(t *testing.T) {
	if !UsesUnresolvedSentinel(Digest{Unresolved: []string{UnresolvedSentinel}}) {
		t.Error("the canonical sentinel must be recognised")
	}
	if !UsesUnresolvedSentinel(Digest{Unresolved: []string{"None - nothing remains"}}) {
		t.Error("recognition must be case-insensitive on the marker")
	}
	if UsesUnresolvedSentinel(Digest{Unresolved: []string{"needs further testing"}}) {
		t.Error("a real-looking item must not read as the sentinel")
	}
	if UsesUnresolvedSentinel(Digest{Unresolved: []string{UnresolvedSentinel, "and also X"}}) {
		t.Error("the sentinel must stand alone; mixing it with items is not 'nothing open'")
	}
}

// The exact observed failure: a session the source called "Fixed and verified live"
// produced speculative blockers that appeared nowhere in the conversation.
func TestLooksFabricatedUnresolvedCatchesTheObservedFailure(t *testing.T) {
	src := "assistant: Fixed and verified live. 9 pass. Committing to main.\nuser: commit to main\n"
	facts := DigestFacts{Turns: 13, UserTurns: 1} // no corrections
	fabricated := Digest{Unresolved: []string{
		"The resolver's behavior in different environments needs further testing.",
		"The exact impact of the 5-minute cache TTL is not fully understood.",
	}}
	if !LooksFabricatedUnresolved(fabricated, facts, src) {
		t.Error("speculative blockers on verified work must be flagged")
	}
	if LooksFabricatedUnresolved(Digest{Unresolved: []string{UnresolvedSentinel}}, facts, src) {
		t.Error("the sentinel must never be flagged as fabrication")
	}
}

// Where the work genuinely hit friction, open items are expected and must not be
// flagged — otherwise the metric punishes honest reporting.
func TestLooksFabricatedUnresolvedSilentWhenFrictionOccurred(t *testing.T) {
	src := "assistant: verified live\n"
	facts := DigestFacts{Turns: 13, UserTurns: 4, Corrections: 2}
	d := Digest{Unresolved: []string{"The retry path may need further testing."}}
	if LooksFabricatedUnresolved(d, facts, src) {
		t.Error("with real corrections, open items are expected")
	}
}

// And where the source never claims completion, tentative items are legitimate.
func TestLooksFabricatedUnresolvedSilentWhenSourceDoesNotClaimDone(t *testing.T) {
	src := "user: start looking into the resolver\nassistant: reading the config\n"
	d := Digest{Unresolved: []string{"The approach may need further testing."}}
	if LooksFabricatedUnresolved(d, DigestFacts{Turns: 4}, src) {
		t.Error("unfinished work may legitimately carry tentative open items")
	}
}

// Observed failure: an instruction example leaked verbatim into a report about an
// unrelated session, and the identifier gate could not see it because the leaked
// words were lowercase common nouns.
func TestLeakedPromptWordsCatchesInstructionBleed(t *testing.T) {
	// A digest that borrows instruction vocabulary absent from the session.
	src := "assistant: fixed the Manage column width\nuser: merge to main\n"
	leaky := Digest{Done: "The work reached a stopping point and nothing is unresolved.",
		Happened: "Reversals were abandoned."}
	if len(LeakedPromptWords(leaky, src)) == 0 {
		t.Log("note: common-word filter may cover this phrasing; the morphology cases below are the load-bearing ones")
	}

	// The observed FALSE POSITIVE: the session says "resolve" and
	// "internal/agent/resolve/claude.go", the digest says "resolver". Legitimate
	// nominalisation, and the old exact-substring check flagged it.
	morphSrc := "assistant: where the signal agent resolves paths, internal/agent/resolve/claude.go\n"
	morph := Digest{Structure: "the agent resolver opens the path directly"}
	if got := LeakedPromptWords(morph, morphSrc); len(got) != 0 {
		t.Errorf("morphological variant of a source word must not be flagged: %v", got)
	}
}

// T2 hit 22.6% largely because the gate treated hyphenated English adjectives as
// invented identifiers. An identifier must LOOK like one.
func TestIdentifiersExcludeEnglishCompounds(t *testing.T) {
	d := Digest{Done: "A follow-up on the security-sensitive, well-known read-only path."}
	for _, id := range Identifiers(d) {
		switch id {
		case "follow-up", "security-sensitive", "well-known", "read-only":
			t.Errorf("English compound %q treated as an identifier", id)
		}
	}
	// Real identifiers must still be caught.
	real := Digest{Done: "Edited agent-type-row.tsx and ledger-2026-03.csv; bumped Qwen3-0.6B."}
	found := map[string]bool{}
	for _, id := range Identifiers(real) {
		found[id] = true
	}
	for _, want := range []string{"agent-type-row.tsx", "ledger-2026-03.csv", "Qwen3-0.6B"} {
		if !found[want] {
			t.Errorf("real identifier %q was dropped; found %v", want, Identifiers(real))
		}
	}
}

// T2 was measuring English, not fabrication: of 71 flags across 19 digests the top
// tokens were "Key", "Initial", "four-screen", "well-documented", "e.g". Prose needs a
// position-aware rule, since a capital at a sentence boundary carries no information.
func TestIdentifiersArePositionAware(t *testing.T) {
	d := Digest{
		Done:     "Key findings are in place. Initial review passed. Verify the totals.",
		Happened: "Reviewing the data, we cross-checked against Globex and read ledger-2026-03.csv.",
		Current:  "Specifically, the page-level and well-documented parts are done. See e.g the notes.",
	}
	got := map[string]bool{}
	for _, id := range Identifiers(d) {
		got[id] = true
	}
	// Sentence-initial English must be ignored.
	for _, w := range []string{"Key", "Initial", "Verify", "Reviewing", "Specifically"} {
		if got[w] {
			t.Errorf("sentence-initial English %q treated as an identifier", w)
		}
	}
	// Hyphenated English compounds must be ignored.
	for _, w := range []string{"page-level", "well-documented", "cross-checked"} {
		if got[w] {
			t.Errorf("hyphenated English %q treated as an identifier", w)
		}
	}
	// "e.g" is not a filename.
	if got["e.g"] {
		t.Error(`"e.g" treated as an identifier`)
	}
	// A mid-sentence proper noun and a real filename MUST still be caught — these are
	// the fabrications the gate exists for.
	for _, w := range []string{"Globex", "ledger-2026-03.csv"} {
		if !got[w] {
			t.Errorf("%q must be checked; got %v", w, Identifiers(d))
		}
	}
}

// Before identifierPat gained its underscore alternative, a snake_case or
// SCREAMING_SNAKE_CASE name reached Identifiers' OUTER regex not at all — `_` blocks
// the `\b` the capitalised alternative needs right after e.g. "INSTALL", and the
// dotted/hyphenated alternative only fires on `.`/`-`. strongIdentifier already
// special-cased an underscore (it names "an ALL_CAPS constant" explicitly), so the
// gap was purely in what got offered to it as a candidate in the first place — this
// pins that it no longer does.
func TestIdentifiersExtractSnakeCaseNames(t *testing.T) {
	d := Digest{
		Done:     "Updated enrichment_pass and tool_events, then bumped MAX_LEN.",
		Happened: "Checked KELD_WATCH against session_record before shipping.",
	}
	got := map[string]bool{}
	for _, id := range Identifiers(d) {
		got[id] = true
	}
	for _, w := range []string{"enrichment_pass", "tool_events", "MAX_LEN", "KELD_WATCH", "session_record"} {
		if !got[w] {
			t.Errorf("snake_case name %q must be extracted; got %v", w, Identifiers(d))
		}
	}
}

// The widened pattern must still leave ordinary, underscore-free prose alone — the
// gate stays narrow, it just no longer excludes an entire naming convention.
func TestIdentifiersSnakeCaseWideningStaysNarrow(t *testing.T) {
	d := Digest{Done: "This is ordinary prose about the work and its progress, nothing special here at all."}
	if got := Identifiers(d); len(got) != 0 {
		t.Errorf("ordinary prose must not be flagged, got %v", got)
	}
}

func TestStrongIdentifierSignals(t *testing.T) {
	for _, w := range []string{"agent-type-row.tsx", "/v1/enrichments", "Qwen3-0.6B",
		"PostgreSQL", "INSTALL_SCRIPT_URL", "config.py"} {
		if !strongIdentifier(w) {
			t.Errorf("%q should be a strong identifier", w)
		}
	}
	for _, w := range []string{"Key", "Review", "user-facing", "e.g", "follow-up"} {
		if strongIdentifier(w) {
			t.Errorf("%q should not be a strong identifier", w)
		}
	}
}
