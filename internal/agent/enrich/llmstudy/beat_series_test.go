package llmstudy

import (
	"strings"
	"testing"
)

func beats(n int, changedAt ...int) []Beat {
	ch := map[int]bool{}
	for _, c := range changedAt {
		ch[c] = true
	}
	var out []Beat
	for i := 1; i <= n; i++ {
		out = append(out, Beat{Ordinal: i, Text: "beat " + strings.Repeat("x", i), ChangedSubject: ch[i]})
	}
	return out
}

// ⚠️ THE RETIRED RULE, EXERCISED DIRECTLY. AppendBeat no longer computes ChangedSubject and no
// longer discards a restatement (see its doc: 41 of 42 then 0 of 46, and 0 of 70 discarded), so
// the tests below call the rule where they used to read it off a stored beat. They are kept
// because the rule is kept — as documentation, and because the blind review corpus records what
// earlier rounds decided with it.
func appendJudged(prev []Beat, text string, g BeatGround) []Beat {
	terms := beatSubjectTermsGrounded(text, g)
	return append(prev, Beat{
		Ordinal:        len(prev) + 1,
		Text:           text,
		ChangedSubject: beatChangedSubject(terms, prev),
		SubjectTerms:   sortedTerms(terms),
	})
}

// AppendBeat stores every beat it is given, including one that restates its predecessor. The
// suppression it used to apply discarded 0 of 70 — inert — and a window that repeated itself is a
// fact about the session rather than something to hide.
func TestAppendBeatStoresEveryBeatIncludingARestatement(t *testing.T) {
	var bs []Beat
	bs, ok := AppendBeat(bs, "the March ledger for Meridian\n- the statement was opened", BeatGround{})
	if !ok {
		t.Fatal("the first beat was not stored")
	}
	bs, ok = AppendBeat(bs, "Meridian's March ledger\n- the statement was opened again", BeatGround{})
	if !ok {
		t.Fatal("a restatement must now be stored, not discarded")
	}
	if len(bs) != 2 || bs[1].Ordinal != 2 {
		t.Errorf("ordinals must be contiguous over stored beats: %v", bs)
	}
	for _, b := range bs {
		if b.ChangedSubject {
			t.Errorf("ChangedSubject must no longer be computed on the production path: %+v", b)
		}
	}
}

// ChangedSubject carried no information at all: 47 of 47 beats over three real sessions were
// flagged as a change, because the old rule ran the near-duplicate test (beatsRestate at 0.8)
// against beats that were non-duplicates by construction. SelectBeats samples on this flag, so
// selection for the expensive report was choosing on a constant.
//
// The discrimination that matters is between beats that continue a subject and beats that leave
// it — both of which are stored, since neither restates the other. This fixture is the shape the
// real corpus showed: one subject carried across three beats, then a jump.
func TestChangedSubjectDistinguishesContinuationFromAJump(t *testing.T) {
	var bs []Beat
	for _, text := range []string{
		"Working on the CSV export for the overview page, which delivers five files in one ZIP.",
		"The CSV export now ships summary, spend-over-time and breakdowns from the same ZIP endpoint.",
		"The CSV export is finished and pushed, with the confirmation modal explaining the ZIP.",
		"Fixing the ConfirmDialog nesting warning that RemoveMember and remove-key-button both trip.",
	} {
		bs = appendJudged(bs, text, BeatGround{})
	}
	if !bs[0].ChangedSubject {
		t.Error("the first beat establishes the subject and must be marked")
	}
	for _, i := range []int{1, 2} {
		if bs[i].ChangedSubject {
			t.Errorf("beat %d continues the CSV export subject and must not be marked: %q",
				i+1, bs[i].Text)
		}
	}
	if !bs[3].ChangedSubject {
		t.Errorf("a jump to an unrelated component must be marked: %q", bs[3].Text)
	}
}

// The trap this package documents twice: distinctiveToken admits ANY lowercase word of 7+
// characters, which is why SynopsisLag can match on "remains"/"whether" and why T12's beat
// subject terms came out as gerunds and adverbs. A novelty count built on that vocabulary would
// call a reworded beat a new subject. Two beats naming the same things in different English must
// not be a subject change.
func TestChangedSubjectIgnoresOrdinaryEnglish(t *testing.T) {
	first := "Working on the CSV export for the overview page, which delivers five files in one ZIP."
	second := "The CSV export endpoint is progressing steadily, although the ZIP assembly " +
		"remains unfinished and consequently the overview download is currently unavailable."
	if terms := beatSubjectTerms(second); len(terms) == 0 {
		t.Fatalf("fixture names nothing, so it cannot exercise the rule: %v", terms)
	}
	bs := appendJudged(nil, first, BeatGround{})
	bs = appendJudged(bs, second, BeatGround{})
	if bs[1].ChangedSubject {
		t.Errorf("long ordinary English words must not read as a new subject: %q", bs[1].Text)
	}
	for _, bad := range []string{"progressing", "steadily", "although", "remains",
		"consequently", "currently", "unavailable", "unfinished"} {
		if beatSubjectTerms(second)[bad] {
			t.Errorf("%q is ordinary English and must not be a subject term", bad)
		}
	}
}

// A beat that names nothing concrete AND was prompted by a turn that names nothing either cannot
// be judged, and is reported unchanged rather than guessed at — the same "continuity is the
// default" rule SubjectShifted follows. This is the flag's remaining lower bound (6 of 27 beats in
// the regenerated corpus), and it is pinned so the abstention stays visible rather than being
// mistaken for a verdict.
func TestChangedSubjectAbstainsWhenNothingIsNamedAtAll(t *testing.T) {
	bs := appendJudged(nil, "Reconciling the March ledger for Meridian against the statement.", BeatGround{})
	abstaining := "Designing a schema that allows free-form prose while preventing " +
		"rubberstamping, resting on measurable evidence rather than judgement."
	ground := BeatGround{Turn: "how does it look so far?"}
	if n := len(beatSubjectTermsGrounded(abstaining, ground)); n != 0 {
		t.Fatalf("fixture names %d subjects, so it does not exercise abstention", n)
	}
	bs = appendJudged(bs, abstaining, ground)
	if bs[1].ChangedSubject {
		t.Errorf("a beat naming nothing must not be reported as a subject change: %q", bs[1].Text)
	}
}

// The under-reporting defect, on the pair it was reported on. Both beats are verbatim from the
// shipped dump of this repo's own session, with the user turns that prompted their windows: beat 1
// is about finding CPU-friendly alternatives to GLiNER2, beat 2 about removing a prompt-truncation
// confound from the study design. Different subjects. The text-only rule missed it because the two
// beats share GLiNER2 and only one term ("LLM") was novel; the grounded turn supplies the second.
func TestChangedSubjectUsesTheGroundedTurn(t *testing.T) {
	first := "Exploring CPU-friendly language models for text classification and NER that " +
		"match GLINER2’s footprint and performance, focusing on lightweight, open-source " +
		"alternatives from Hugging Face."
	firstTurn := "besides gliner2, which doesn't need specialized hardware or GPU's, is there " +
		"any other language model out there that could be used for text classification?"
	second := "Fixing the LLM classifier study design to ensure it properly isolates model " +
		"performance from input changes. The spec has been updated to feed only the LLM arms " +
		"with the mined conversation window, while control and arm C receive full production " +
		"prompts, removing the confound where GLiNER2 would lose due to prompt truncation."
	secondTurn := "I installed lamma.cpp"

	bs := appendJudged(nil, first, BeatGround{Turn: firstTurn})
	ungrounded := appendJudged(bs, second, BeatGround{})
	if ungrounded[1].ChangedSubject {
		t.Fatal("the text-only rule now flags this pair, so it no longer demonstrates the gap " +
			"the grounded term set closes")
	}
	got := appendJudged(bs, second, BeatGround{Turn: secondTurn})
	if !got[1].ChangedSubject {
		t.Errorf("the grounded turn must carry this subject change: terms=%v", got[1].SubjectTerms)
	}
}

// One novel STRONG identifier is enough; one novel weak proper-noun candidate is not. A path,
// dotted filename or versioned token names one thing and nothing else, which is why requiring two
// of them made "a confirmation modal on the CSV download" a continuation of a ConfirmDialog fix. A
// bare capitalised word is thin evidence and still needs a second.
func TestChangedSubjectAcceptsOneStrongIdentifierButNotOneWeakName(t *testing.T) {
	base := "Fixing the ConfirmDialog nesting warning that RemoveMember trips."
	bs := appendJudged(nil, base, BeatGround{})

	strong := appendJudged(bs, "Adding a confirmation modal to the export in overview.tsx.", BeatGround{})
	if !strong[1].ChangedSubject {
		t.Errorf("one novel strong identifier must mark a change: terms=%v", strong[1].SubjectTerms)
	}

	weak := appendJudged(bs, "Still chasing the same nesting warning, now under Halberd.", BeatGround{})
	if weak[1].ChangedSubject {
		t.Errorf("one novel weak proper-noun candidate must not mark a change: terms=%v",
			weak[1].SubjectTerms)
	}
}

// The grounded half is bounded. A user turn can be a pasted file or diff, and an unbounded ground
// would let one paste supply enough terms to decide the flag on its own — the same reasoning
// maxSubjectTermLen records for the record's subjects.
func TestGroundedSubjectTermsAreBounded(t *testing.T) {
	var paste strings.Builder
	paste.WriteString("here is the whole file: ")
	for i := 0; i < 40; i++ {
		paste.WriteString("internal/agent/enrich/mod")
		paste.WriteByte(byte('a' + i%26))
		paste.WriteString(".go ")
	}
	terms := beatSubjectTermsGrounded("Working on the mask enforcement.", BeatGround{Turn: paste.String()})
	if len(terms) > maxBeatGroundTerms+2 {
		t.Errorf("a pasted turn contributed %d terms, past the cap of %d", len(terms), maxBeatGroundTerms)
	}
	// Deterministic: order of appearance, not map order.
	for i := 0; i < 5; i++ {
		again := beatSubjectTermsGrounded("Working on the mask enforcement.", BeatGround{Turn: paste.String()})
		if len(again) != len(terms) {
			t.Fatalf("term set is not stable across runs: %d vs %d", len(again), len(terms))
		}
		for k := range again {
			if !terms[k] {
				t.Fatalf("term set is not stable across runs: %q appeared", k)
			}
		}
	}
}

// GroundOf takes the turn that PROMPTED the window — its last user turn (Mine emits one window per
// user prompt). Taking the first would ground every beat in the window's oldest turn, which is the
// previous subject.
func TestGroundOfTakesThePromptingTurn(t *testing.T) {
	w := Window{Turns: []Turn{
		{Role: RoleUser, Text: "reconcile the bank statement"},
		{Role: RoleAssistant, Text: "done, two differences"},
		{Role: RoleUser, Text: "now do the AP accruals for Calder"},
		{Role: RoleTool, Text: "Read(gl-mar.csv)"},
	}}
	if got := GroundOf(w).Turn; got != "now do the AP accruals for Calder" {
		t.Errorf("want the last user turn, got %q", got)
	}
	if got := GroundOf(Window{}).Turn; got != "" {
		t.Errorf("a window with no user turn must ground on nothing, got %q", got)
	}
}

// A return to a subject the session has already covered is history repeating, not a change —
// the semantics the old doc claimed and the old code could not deliver, since it compared only
// against near-duplicates.
func TestChangedSubjectTreatsAReturnAsUnchanged(t *testing.T) {
	var bs []Beat
	for _, text := range []string{
		"Fixing the budget saliency in members-by-team.tsx so the amount reads on mint rows.",
		"Working on the CSV export for the overview page, which delivers five files in one ZIP.",
		"Back on members-by-team.tsx, where the budget saliency still clashes with the pill.",
	} {
		bs = appendJudged(bs, text, BeatGround{})
	}
	if !bs[1].ChangedSubject {
		t.Errorf("the CSV export is a new subject: %q", bs[1].Text)
	}
	if bs[2].ChangedSubject {
		t.Errorf("returning to a covered subject is not a change: %q", bs[2].Text)
	}
}

// AppendBeat is the second gate on the BOUND, and the bound is now whole entries rather than a
// sentence boundary: a bulleted beat holds no sentence terminators, so the prose clip that used
// to sit here would return "" and lose every beat.
func TestAppendBeatBoundsAtWholeLinesAndMarksTheDrop(t *testing.T) {
	var bs []Beat
	bs, ok := AppendBeat(bs, "the March close\n- the bank statement was opened", BeatGround{})
	if !ok {
		t.Fatal("a beat inside the cap must be stored")
	}
	if got := bs[0].Text; got != "the March close\n- the bank statement was opened" {
		t.Errorf("a beat inside the cap was altered: %q", got)
	}
	line := "- " + strings.Repeat("x", 120) + "\n"
	over := "the March close\n" + strings.Repeat(line, BeatCap/runeLen(line)+2)
	bs, ok = AppendBeat(bs, over, BeatGround{})
	if !ok {
		t.Fatal("an over-cap beat must be bounded, not discarded")
	}
	last := bs[len(bs)-1].Text
	if runeLen(last) > BeatCap {
		t.Errorf("stored beat is %d runes, over BeatCap %d", runeLen(last), BeatCap)
	}
	if !strings.Contains(last, "dropped to fit the beat cap") {
		t.Errorf("the drop is not marked: %q", last)
	}
	if _, ok := AppendBeat(bs, "tiny", BeatGround{}); ok {
		t.Error("a beat under the floor must not be stored")
	}
}

func TestSelectBeatsKeepsFirstAndLatest(t *testing.T) {
	got := SelectBeats(beats(30), 6)
	if got[0].Ordinal != 1 {
		t.Errorf("the first beat was dropped: %v", got[0])
	}
	if got[len(got)-1].Ordinal != 30 {
		t.Errorf("the latest beat was dropped: %v", got[len(got)-1])
	}
	if len(got) > 6 {
		t.Errorf("cap exceeded: %d", len(got))
	}
}

// SelectBeats no longer samples on ChangedSubject: the flag carried no information (41 of 42
// true, then 0 of 46), so "the interesting ones" were whichever the flag happened to be true for.
// Selection is the first, the latest, and even spacing between them — a statement about the
// transcript rather than about a heuristic.
func TestSelectBeatsIgnoresTheRetiredSubjectFlag(t *testing.T) {
	flagged := SelectBeats(beats(30, 9, 18), 6)
	plain := SelectBeats(beats(30), 6)
	if len(flagged) != len(plain) {
		t.Fatalf("the flag changed how many beats were selected: %d vs %d", len(flagged), len(plain))
	}
	for i := range flagged {
		if flagged[i].Ordinal != plain[i].Ordinal {
			t.Errorf("the flag changed the selection at %d: %d vs %d",
				i, flagged[i].Ordinal, plain[i].Ordinal)
		}
	}
}

func TestSelectBeatsFallsBackToSpacing(t *testing.T) {
	got := SelectBeats(beats(30), 6)
	if len(got) != 6 {
		t.Fatalf("want the cap filled by spacing, got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Ordinal <= got[i-1].Ordinal {
			t.Fatalf("must be chronological and unique: %v", got)
		}
	}
}

// The "(subject changed)" annotation went with the flag that produced it. It only ever said what
// a retired heuristic thought, and it cost runes in the one prompt tier with 4 of them to spare.
func TestRenderBeatsDoesNotAnnotateTheRetiredFlag(t *testing.T) {
	out := RenderBeats([]Beat{
		{Ordinal: 1, Text: "the ledger\n- the statement was opened", ChangedSubject: true},
		{Ordinal: 4, Text: "the ledger\n- two differences were found", ChangedSubject: false},
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("one line per beat, got %d: %q", len(lines), out)
	}
	for _, l := range lines {
		if strings.Contains(l, "subject changed") {
			t.Errorf("a retired heuristic is still annotated in the report's input: %q", l)
		}
	}
	if !strings.Contains(lines[0], "[1] the ledger - the statement was opened") {
		t.Errorf("a beat must render whole on one line: %q", lines[0])
	}
}

func TestRenderEmptyBeatsIsEmpty(t *testing.T) {
	if RenderBeats(nil) != "" {
		t.Error("an empty series must render nothing, not a header")
	}
}

// Two consecutive beats over unchanged work reword the same verb — "reconciling" then
// "reconciliation" — because beats regenerate from an overlapping window every few turns.
// insightsMatch, measured on longer "insight" text, scores this pair at 0.667 (4 of 6 shared
// words), below the shared 0.8 threshold: plural-only stemming doesn't fold a gerund onto its
// nominalisation. beatsRestate must, via beatStem's wider stemming, or a beat series fills up
// with reworded duplicates of the same subject.
func TestBeatsRestateFoldsGerundAndNominalisation(t *testing.T) {
	a := "The work is reconciling the March ledger for Meridian."
	b := "Work continues on Meridian's March ledger reconciliation."
	if !beatsRestate(a, b) {
		t.Errorf("a gerund/nominalisation restatement must be caught: %q / %q", a, b)
	}
}

// A stemmer wide enough to catch a gerund/nominalisation pair must not go so far that it
// collapses genuinely different subjects — a dropped beat is lost history, which is worse
// than keeping a near-duplicate.
func TestBeatsRestateKeepsDistinctSubjectsApart(t *testing.T) {
	a := "The work is reconciling the March ledger for Meridian."
	b := "The work has moved to the AR ageing provision policy."
	if beatsRestate(a, b) {
		t.Errorf("distinct subjects must not be treated as a restatement: %q / %q", a, b)
	}
}

// The gerund/nominalisation of an -ate verb ("migrating"/"migration") is the regular, far more
// common counterpart to "reconciling"/"reconciliation"'s irregular case — routine in both
// engineering and business prose describing recurring work. beatStem must fold both onto the
// same root, not just the one instance the brief's fixture happened to use, or a beat series
// about any of these ten verbs still fills with near-duplicates. communicate/negotiate are
// -icate/-iate verbs, where the "ic"/"i" belongs to the stem rather than the suffix, so they
// exercise the longer "icating"/"iating" branches rather than the plain "ating" one the other
// eight take.
func TestBeatsRestateFoldsAteVerbFamily(t *testing.T) {
	pairs := []struct{ gerund, nominal string }{
		{"migrating", "migration"},
		{"creating", "creation"},
		{"generating", "generation"},
		{"operating", "operation"},
		{"negotiating", "negotiation"},
		{"communicating", "communication"},
		{"evaluating", "evaluation"},
		{"terminating", "termination"},
		{"calculating", "calculation"},
		{"allocating", "allocation"},
	}
	for _, p := range pairs {
		if got := beatStem(p.gerund); got != beatStem(p.nominal) {
			t.Errorf("%q -> %q, %q -> %q: must converge to the same stem",
				p.gerund, got, p.nominal, beatStem(p.nominal))
		}
	}
}

// Two beats can share an -ate verb family and still be about different subjects. A stemmer
// aggressive enough to fold gerund onto nominalisation (see TestBeatsRestateFoldsAteVerbFamily)
// must not go on to fold THESE together on the strength of the shared verb alone — that would
// be the over-stemming false positive the coordinator called the expensive direction: a dropped
// beat is lost history nothing can recover.
func TestBeatsRestateKeepsSharedVerbFamilySubjectsApart(t *testing.T) {
	a := "The team is migrating the reporting pipeline to the new data warehouse."
	b := "The team is migrating customer accounts to the new billing system."
	if beatsRestate(a, b) {
		t.Errorf("sharing a verb family must not be enough to collapse distinct subjects: %q / %q", a, b)
	}
	c := "We are evaluating the new vendor contract terms this quarter."
	d := "We are evaluating candidate resumes for the open engineering role."
	if beatsRestate(c, d) {
		t.Errorf("sharing a verb family must not be enough to collapse distinct subjects: %q / %q", c, d)
	}
}

// SelectBeats is exported with a caller-supplied max and documents it as a hard cap, but the
// pick set used to be seeded with the first and last index before any cap check — so max=1
// still returned 2. With only one slot, the invariant of keeping both the first beat and the
// latest can't both hold; the latest is kept, since a single-beat summary is "where things
// stand now", not the origin.
func TestSelectBeatsCapHoldsAtSmallMax(t *testing.T) {
	all := beats(30)
	if got := SelectBeats(all, 1); len(got) != 1 {
		t.Fatalf("max=1 must return exactly 1 beat, got %d: %v", len(got), got)
	} else if got[0].Ordinal != 30 {
		t.Errorf("max=1 must keep the latest beat, got ordinal %d", got[0].Ordinal)
	}
	got := SelectBeats(all, 2)
	if len(got) != 2 {
		t.Fatalf("max=2 must return exactly 2 beats, got %d: %v", len(got), got)
	}
	if got[0].Ordinal != 1 || got[1].Ordinal != 30 {
		t.Errorf("max=2 must be exactly the first and latest, got %v", got)
	}
}
