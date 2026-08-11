package llmstudy

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// Insights are append-only: repeated re-summarising is what destroys the most
// valuable content, so earlier entries carry forward verbatim.
func TestMergeInsightsIsAppendOnlyAndDeduped(t *testing.T) {
	prev := Digest{Insights: []string{"retry loop was the bottleneck", "staging mirrors prod"}}
	next := Digest{Insights: []string{"staging mirrors prod", "the vendor rate-limits at 100/s"}}
	got := MergeInsights(prev, next)
	if len(got.Insights) != 3 {
		t.Fatalf("want 3 insights (2 prior + 1 new), got %d: %v", len(got.Insights), got.Insights)
	}
	if got.Insights[0] != "retry loop was the bottleneck" {
		t.Errorf("earlier insight must survive verbatim and first, got %q", got.Insights[0])
	}
}

// Unresolved is current state, not history: merging it would accumulate stale
// blockers forever, which is the opposite failure from losing them.
func TestMergeInsightsDoesNotMergeUnresolved(t *testing.T) {
	prev := Digest{Unresolved: []string{"waiting on vendor"}}
	next := Digest{Unresolved: []string{"nothing open"}}
	got := MergeInsights(prev, next)
	if len(got.Unresolved) != 1 || got.Unresolved[0] != "nothing open" {
		t.Fatalf("unresolved must reflect current state, got %v", got.Unresolved)
	}
}

// The other sections come from the new digest, not the old one.
func TestMergeInsightsKeepsNewProse(t *testing.T) {
	prev := Digest{Done: "old", Structure: "old shape"}
	next := Digest{Done: "new", Structure: "new shape"}
	got := MergeInsights(prev, next)
	if got.Done != "new" || got.Structure != "new shape" {
		t.Fatalf("prose must come from the new digest: %+v", got)
	}
}

// CapSections bounds the lists and LEAVES PROSE ALONE. Both halves are asserted in one test
// because the removal is only safe if the list bounds survived it: DefaultListCap is applied to
// what a prompt is shown (priorOpenItems), so dropping it would move prompt size, while prose
// reaches no prompt at all.
//
// The prose half fails against the previous implementation, which is the point of writing it.
// Verified by restoring the seven clipProse calls with their literal former caps and re-running:
// all SEVEN sections fail, each on both assertions, at exactly the caps that used to apply —
// synopsis 645 of 4,000, done/current/why/next 895, happened 1,395, structure 1,595, every one
// of them ending "…". (645 not 650, and 895 not 900, because clipProse reserves a rune for the
// marker and then backs up to a word boundary — the truncation is not even at the number the
// cap names.)
func TestCapSectionsBoundsListsAndLeavesProseAlone(t *testing.T) {
	long := strings.Repeat("word ", 800) // 4,000 runes, past every cap that used to apply
	d := Digest{
		Synopsis: long, Done: long, Happened: long, Structure: long, Current: long,
		Why: long, Next: long,
		Insights: make([]string, 40), Unresolved: make([]string, 40),
	}
	for i := range d.Insights {
		d.Insights[i] = "insight"
		d.Unresolved[i] = "open item"
	}
	got := CapSections(d, 12)
	for name, v := range map[string]string{
		"synopsis": got.Synopsis, "done": got.Done, "happened": got.Happened,
		"structure": got.Structure, "current": got.Current, "why": got.Why, "next": got.Next,
	} {
		if v != long {
			t.Errorf("%s was altered: %d runes of %d, ends %q — prose must be passed through "+
				"untouched", name, len([]rune(v)), len([]rune(long)), lastRunes(v, 24))
		}
		// Stated separately from the equality above so a failure says WHICH defect it is: a
		// truncation mark is the visible half of the one being removed.
		if strings.HasSuffix(strings.TrimSpace(v), "…") {
			t.Errorf("%s was truncated and marked: %q", name, lastRunes(v, 40))
		}
	}
	if len(got.Insights) != 12 || len(got.Unresolved) != 12 {
		t.Errorf("lists not capped: insights=%d unresolved=%d", len(got.Insights), len(got.Unresolved))
	}
}

// lastRunes is the tail of a string for a failure message, in runes so a multi-byte section
// cannot be cut mid-character by the error report itself.
func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// Capping keeps the most recent entries; older ones have already survived several
// refinements.
func TestCapSectionsDropsOldestInsights(t *testing.T) {
	d := Digest{Insights: []string{"first", "second", "third"}}
	got := CapSections(d, 2)
	if len(got.Insights) != 2 || got.Insights[0] != "second" {
		t.Fatalf("want the two most recent, got %v", got.Insights)
	}
}

// TestCapSectionsBoundsListEntryLength is the fix for task-7b finding (a): CapSections
// bounded entry COUNT (maxList) but never entry LENGTH, so a schema-legal digest with
// DefaultListCap Unresolved items of 2,000 runes each survived it untouched. Measured
// before that fix (by constructing exactly this digest, running it through CapSections,
// and feeding the result into DigestUpdatePromptFrom as `prev` — priorOpenItems embedded
// Unresolved verbatim at the time): the assembled prompt came out at 30,191 runes
// against the 11,000 budget, and the new turn ("a filler turn about the work") did not
// appear anywhere in it.
//
// RETIRED in fix round 3: this test used to also assert against the ASSEMBLED PROMPT
// (budget + new-turn survival) after building it from `capped`. Those two assertions
// are now VACUOUS — an independent review found that reverting the two capEntryLength
// calls this test names and re-running leaves the prompt-level assertions passing
// regardless (10,931 runes, well in budget, new turn present either way): task-7b fix
// round 2 added promptOpenItemCap, which clips every open item to 80 runes FOR THE
// PROMPT (priorOpenItems) independent of whatever CapSections did or did not do to the
// STORED digest, so by the time a prompt is assembled this test's own bug (an uncapped
// capEntryLength) can no longer be observed there — the prompt layer's bound papers
// over the storage layer's absence. There is no way to recalibrate this test to make
// the prompt-level assertions distinguish the two states again: promptOpenItemCap
// intercepts EVERY item regardless of its stored length, structurally, not as a matter
// of degree. So only the assertion that is still live is kept: capEntryLength's actual,
// current contract is bounding what gets STORED, and that is what this test now checks.
// ⚠️ RECALIBRATED for the never-cut-mid-sentence rule (AGENTS.md). capEntryLength now bounds
// via clipEntry, which cuts at a SENTENCE END and keeps an over-long single sentence WHOLE
// rather than amputate it — so DefaultListEntryCap is advisory for exactly one input shape,
// and this test's original input (2,000 undifferentiated "x") is that shape. Both halves are
// asserted now, because the interesting property has become which of the two a given entry
// gets:
//
//   - an entry with sentence structure is bounded, and bounded at a full stop;
//   - an entry that is one un-terminated run is kept whole, because clipping it would store a
//     sentence that was never written. Nothing downstream can overflow on it: the prompt is
//     measured insensitive to stored entry length
//     (TestRefinePromptIsInsensitiveToStoredProseLength), promptOpenItemCap/boundOpenItems
//     intercept every item the prompt embeds, and boundRetainList bounds what Identifiers
//     pulls out of it.
func TestCapSectionsBoundsListEntryLength(t *testing.T) {
	sentences := strings.Repeat("This is one sentence about the close. ", 20) // 740 runes
	prev := Digest{}
	for i := 0; i < DefaultListCap; i++ {
		prev.Unresolved = append(prev.Unresolved, sentences)
		prev.Insights = append(prev.Insights, sentences)
	}
	capped := CapSections(prev, DefaultListCap)
	for _, l := range []struct {
		name string
		v    []string
	}{{"unresolved", capped.Unresolved}, {"insights", capped.Insights}} {
		for i, item := range l.v {
			if n := len([]rune(item)); n > DefaultListEntryCap {
				t.Errorf("%s[%d] not capped: %d runes (cap %d)", l.name, i, n, DefaultListEntryCap)
			}
			if !strings.HasSuffix(item, "."+elisionMark) {
				t.Errorf("%s[%d] was not cut at a sentence end: %q", l.name, i, tailOf(item))
			}
		}
	}
	// The single-sentence case: kept whole, never amputated.
	blob := strings.Repeat("x", 2000)
	whole := CapSections(Digest{Unresolved: []string{blob}}, DefaultListCap)
	if whole.Unresolved[0] != blob {
		t.Errorf("an un-terminated entry was cut to %d runes; it must be kept whole rather "+
			"than stored as a sentence nobody wrote", len([]rune(whole.Unresolved[0])))
	}
}

// tailOf is the last few runes of a string, for an error message about where a cut landed.
func tailOf(s string) string {
	r := []rune(s)
	if len(r) <= 24 {
		return s
	}
	return "..." + string(r[len(r)-24:])
}

// The updated report must build on the new turns and say what changed — checked against
// the instructions the prompt always carries, since (per the beat/retain-list redesign)
// the prior report's own prose is no longer embedded for a specific prior digest to
// appear verbatim against. See TestRefinePromptCarriesNoPriorProse for that contract.
func TestUpdatePromptDemandsRevisionAndExtension(t *testing.T) {
	p := DigestUpdatePrompt(Digest{Done: "x"}, "finance / invoicing", "user: now do April\n", "counts: turns=4 corrections=0\n")
	for _, want := range []string{"now do April", "say what changed"} {
		if !strings.Contains(p, want) {
			t.Errorf("update prompt omits %q", want)
		}
	}
	// Recency-bias guard: earlier material survives unless the new part contradicts it.
	// Deleting the no-shrink rule must not have taken this with it — the no-shrink rule
	// forbade compression outright, this one only forbids dropping something the new
	// part is simply SILENT on, and the two are not the same instruction.
	if !strings.Contains(p, "unless the new part contradicts it") {
		t.Error("update prompt must instruct carry-forward of earlier material")
	}
	if !strings.Contains(p, "Do not drop earlier material simply because the new part does not mention it") {
		t.Error("update prompt must guard against dropping unmentioned material")
	}
	// Structure must be extended, not rewritten.
	if !strings.Contains(p, "EXTEND the picture") {
		t.Error("update prompt must tell structure to extend rather than restart")
	}
}

// An example in an earlier prompt here leaked verbatim into an unrelated report, so
// the update prompt must carry no worked examples at all.
func TestUpdatePromptCarriesNoWorkedExamples(t *testing.T) {
	p := DigestUpdatePrompt(Digest{}, "x", "user: hi\n", "counts: turns=1\n")
	for _, leaky := range []string{"resolver", "redirects", "Northwind", "checkout-api"} {
		if strings.Contains(p, leaky) {
			t.Errorf("update prompt contains a leakable example token %q", leaky)
		}
	}
}

// Retention instrumentation showed all 7 lost facts vanished while sections had room
// under their caps — the model was recompressing, not being truncated. So the prompt
// hands back the prior report's named specifics as an explicit retain-list.
func TestUpdatePromptCarriesARetainList(t *testing.T) {
	prev := Digest{
		// INSTALL_SCRIPT_URL (underscore-only, no dot/hyphen/internal-cap-before-boundary)
		// used to reach the prompt only via CarryForward's full-JSON embed of this sentence,
		// not via Identifiers — identifierPat has no separator to key on and an underscore
		// blocks its \b, so it was never actually part of the retain-list. Now that
		// CarryForward is gone, the sentinel is InstallScriptURL (internal caps), which
		// Identifiers does extract, so this test again exercises what it claims to.
		Done:     "Edited agent-type-row.tsx and bumped InstallScriptURL.",
		Happened: "Verified against ledger-2026-03.csv.",
	}
	p := DigestUpdatePrompt(prev, "x", "user: continue\n", "counts: turns=9\n")
	if !strings.Contains(p, "SPECIFICS ALREADY REPORTED") {
		t.Fatal("update prompt must carry an explicit retain-list")
	}
	for _, want := range []string{"agent-type-row.tsx", "InstallScriptURL", "ledger-2026-03.csv"} {
		if !strings.Contains(p, want) {
			t.Errorf("retain-list omits %q", want)
		}
	}
}

// A first digest has no specifics, and the section must then be omitted rather than
// printed empty — an empty label invites the model to fill it.
func TestUpdatePromptOmitsRetainListWhenEmpty(t *testing.T) {
	p := DigestUpdatePrompt(Digest{}, "x", "user: hi\n", "counts: turns=1\n")
	if strings.Contains(p, "SPECIFICS ALREADY REPORTED") {
		t.Error("empty retain-list must be omitted")
	}
}

// Fix round 1, finding 1 (CRITICAL): a legacy caller (no RefineInput, no measured
// record to offer) must not get a fabricated SESSION RECORD asserting zero turns and
// zero corrections. SessionRecord.Block() always writes its counts line unconditionally
// regardless of whether anything was actually observed, and digestRules tells the model
// those counts are measured and authoritative — "if corrections occurred the work did
// not go smoothly and the report must say so" — so an unpopulated record would assert
// "nothing happened, no friction" against every REAL fact the caller actually has (the
// old `facts` argument, which has no field on RefineInput to land in and so is correctly
// dropped rather than smuggled in — see DigestUpdatePrompt's doc — but must not be
// replaced by a fabricated substitute either).
func TestLegacyCallerGetsNoFabricatedRecord(t *testing.T) {
	p := DigestUpdatePrompt(Digest{Done: "x"}, "work session", "user: hi\n", "counts: turns=40 corrections=3\n")
	if strings.Contains(p, "SESSION RECORD") {
		t.Errorf("legacy caller must not get a fabricated SESSION RECORD section:\n%s", p)
	}
	if strings.Contains(p, "turns=0") {
		t.Errorf("legacy caller must not assert a fabricated zero-turn count:\n%s", p)
	}
}

// A genuinely populated record still reaches the prompt normally — the fix above gates
// on Populated(), it does not remove the section outright.
func TestPopulatedRecordStillWritesSection(t *testing.T) {
	p := DigestUpdatePromptFrom(Digest{Done: "x"}, RefineInput{
		SessionLabel: "work session", Record: SessionRecord{Turns: 5}, NewTurns: "user: hi\n",
	})
	if !strings.Contains(p, "SESSION RECORD") || !strings.Contains(p, "turns=5") {
		t.Error("a populated record must still be written")
	}
}

// TestRefinePromptCarriesNoPriorProse pins the core contract of this redesign: the
// previous report's PROSE is never embedded — it is replaced by the beat series — while a
// genuinely named specific still reaches the model via the deterministic retain-list.
// Both halves are asserted together: checking only absence would also be satisfied by a
// prompt that dropped the retain-list entirely, which is not what "compression, not loss"
// means.
//
// The prose sentinels are deliberately lowercase, multi-word phrases, not identifier-shaped
// tokens. Identifiers only extracts identifier-SHAPED candidates (internal capitals,
// digits, paths, dotted names) — an all-caps single-word sentinel would be picked up by the
// retain-list ON PURPOSE, the same way a real constant name or acronym would be, so a test
// built on that shape would be asserting the opposite of what the retain-list is for.
func TestRefinePromptCarriesNoPriorProse(t *testing.T) {
	prev := Digest{
		Synopsis:  "a month-end close for Meridian",
		Done:      "the vendor invoice backlog was cleared out this week",
		Happened:  "a reconciliation mismatch was traced to a duplicate posting",
		Structure: "the ledger now separates suspense entries from posted ones",
		Current:   "x", Why: "y", Next: "z",
		Insights:   []string{"an insight about the close process"},
		Unresolved: []string{"UNIQUEOPEN"},
	}
	in := RefineInput{
		SessionLabel: "work session",
		Record:       SessionRecord{Turns: 20}.WithProject("meridian"),
		Beats:        []Beat{{Ordinal: 1, Text: "work began on the March close", ChangedSubject: true}},
		NewTurns:     "user: now do April\n",
	}
	p := DigestUpdatePromptFrom(prev, in)

	// The beat series plus the retain-list replace the prose: none of the previous
	// report's own PROSE sentences may be embedded. Synopsis is the highest-value case
	// here — it is the one field CarryForward existed specifically to preserve verbatim
	// (see the deleted function's own doc), so if any field were still being smuggled
	// through whole, this is the one most likely to be it.
	absent := append([]string{prev.Synopsis, prev.Done, prev.Happened, prev.Structure}, prev.Insights...)
	for _, prose := range absent {
		if strings.Contains(p, prose) {
			t.Errorf("prior prose %q is still embedded", prose)
		}
	}
	// A genuine named specific still survives via the retain-list.
	if !strings.Contains(p, "Meridian") {
		t.Error("retain-list of named specifics is missing")
	}
	// Open items are handed over verbatim BY DESIGN (priorOpenItems is an accounting
	// requirement, not a prose leak) — this is not the same claim as the one above.
	if !strings.Contains(p, "UNIQUEOPEN") {
		t.Error("prior open items must still be accounted for")
	}
	if !strings.Contains(p, "March close") {
		t.Error("the beat series is missing")
	}
	if !strings.Contains(p, "turns=20") {
		t.Error("the measured record is missing")
	}
}

// The measured record comes first: everything after it is indicative or evidence, and the
// ordering is load-bearing — a model shown authoritative counts first holds its prose
// consistent with them.
func TestRecordPrecedesNarrative(t *testing.T) {
	in := RefineInput{
		SessionLabel: "work session",
		Record:       SessionRecord{Turns: 9},
		Beats:        []Beat{{Ordinal: 1, Text: "LADDERMARK"}},
		NewTurns:     "user: WINDOWMARK\n",
	}
	p := DigestUpdatePromptFrom(Digest{Done: "x"}, in)
	rec, bea, win := strings.Index(p, "turns=9"), strings.Index(p, "LADDERMARK"), strings.Index(p, "WINDOWMARK")
	// strings.Index returns -1 on a miss, and -1 < bea would satisfy the ordering check
	// below even if the record were missing entirely — this must fail loudly instead of
	// passing for the wrong reason.
	if rec < 0 || bea < 0 || win < 0 {
		t.Fatalf("expected all three markers present, got positions record=%d beats=%d window=%d", rec, bea, win)
	}
	if !(rec < bea && bea < win) {
		t.Errorf("want record < beats < window, got %d %d %d", rec, bea, win)
	}
}

// The no-shrink rule contradicted deliberate compression and had to go.
func TestNoShrinkRuleIsRemoved(t *testing.T) {
	p := DigestUpdatePromptFrom(Digest{Done: "x"}, RefineInput{SessionLabel: "s", NewTurns: "user: hi\n"})
	for _, gone := range []string{"must not become shorter", "Refinement ADDS"} {
		if strings.Contains(p, gone) {
			t.Errorf("the no-shrink rule survives: %q", gone)
		}
	}
}

// RESTORED. These two tests were added in eab4b32 and DELETED by 0cb7415, which rewrote this
// file; the cumulative range diff hid it, because an add and a later remove net to zero. The
// consequence was that restoring the buggy oldest-end eviction loop left the ENTIRE suite
// green — fix 3 was unpinned for two rounds. Re-verified on restoration by making that revert
// again: the first test fails ("one oversized entry deleted the whole retain-list: 60 entries
// in, 0 out") and the second still passes, which is the point of having both.
//
// TestBoundRetainListDropsTheOffendingEntryNotEverythingOlder is the fix for task-7b fix
// round 4 (finding 3). The eviction pass dropped only from the OLDEST end, so a single
// entry longer than retainListMaxTotal could never itself be removed: the loop shrank the
// list from the far end while the offending entry survived every iteration, until
// `len(kept) > 0` failed and the whole retain-list came back EMPTY.
//
// That is the worst possible outcome of a bound: the retain-list is the ONLY channel
// carrying the prior report's named specifics into a refinement, so this silently deleted
// the entire fact-retention anchor — over one long path-shaped name, which is exactly the
// kind of identifier real transcripts produce.
//
// Every length here derives from the enforced constants: retainListMaxCount-1 ordinary
// specifics (so the count bound is not what is being tested) plus one entry of
// retainListMaxTotal+1 runes, at the NEWEST end, where the old loop could never reach it.
//
// Confirmed by restoring the oldest-end eviction loop and re-running: "one oversized entry
// deleted the whole retain-list: 60 entries in, 0 out". The sibling test below still passes
// under that revert, which is the point of having both — the old loop was correct for the
// ordinary case and wrong only for the entry it could not evict.
func TestBoundRetainListDropsTheOffendingEntryNotEverythingOlder(t *testing.T) {
	var named []string
	for i := 0; i < retainListMaxCount-1; i++ {
		named = append(named, fmt.Sprintf("Id%06d", i))
	}
	oversized := strings.Repeat("a", retainListMaxTotal+1)
	named = append(named, oversized)

	got := boundRetainList(named)
	if len(got) == 0 {
		t.Fatalf("one oversized entry deleted the whole retain-list: %d entries in, 0 out", len(named))
	}
	if n := retainListJoinedLen(got); n > retainListMaxTotal {
		t.Errorf("retain-list joins to %d runes, over the %d-rune bound", n, retainListMaxTotal)
	}
	for _, s := range got {
		if s == oversized {
			t.Error("the oversized entry survived — it cannot fit and must be the one dropped")
		}
	}
	// The ordinary specifics all fit within the bound together (59 x 8 runes plus 58
	// separators = 588 of 700), so every one of them must survive: the fix must drop the
	// offending entry ONLY, not take a share of the innocent ones with it.
	if len(got) != retainListMaxCount-1 {
		t.Errorf("want all %d ordinary specifics kept, got %d: %v",
			retainListMaxCount-1, len(got), got)
	}
}

// The oversized entry must not be able to hide behind the count bound either: an entry
// beyond retainListMaxCount is dropped for being old (tailN), and one over
// retainListMaxTotal is dropped for being long. Neither may take the rest with it.
func TestBoundRetainListPrefersTheNewestThatFit(t *testing.T) {
	var named []string
	for i := 0; i < retainListMaxCount*2; i++ {
		named = append(named, fmt.Sprintf("Id%06d", i))
	}
	got := boundRetainList(named)
	if n := retainListJoinedLen(got); n > retainListMaxTotal {
		t.Errorf("retain-list joins to %d runes, over the %d-rune bound", n, retainListMaxTotal)
	}
	if len(got) > retainListMaxCount {
		t.Errorf("retain-list kept %d entries, over the %d bound", len(got), retainListMaxCount)
	}
	// Newest-first: the very last input entry is the newest state and must be present,
	// while the very first has already survived several refinements.
	if got[len(got)-1] != named[len(named)-1] {
		t.Errorf("the newest specific was dropped: last kept %q, newest input %q",
			got[len(got)-1], named[len(named)-1])
	}
}

// packIdentifiers fills n runes with sequential, GLOBALLY DISTINCT identifier-shaped
// tokens and NOTHING else — no filler prose at all. This is deliberately the OPPOSITE
// density from packSpecifics (2 specifics + filler words per call): an independent
// review found that packSpecifics' filler ("word word word...") is not identifier-shaped
// at all (no digit, no internal capital, no separator), so it contributes nothing to
// Identifiers()' output and every packSpecifics-based worst-case measurement of the
// retain-list badly UNDERSTATES what a digest whose sections are genuinely, densely full
// of names can produce — 94 distinct identifiers / 938 runes from packSpecifics, against
// several times that from the identical rune budget filled this way. Every token here
// contains a digit, so strongIdentifier accepts it unconditionally regardless of
// sentence position — this is the true worst case for Identifiers' OWN output, not an
// artifact of how specifics happen to be phrased.
//
// It replaced a packSpecifics helper (2 named specifics plus filler WORDS per call), which is
// what the "94 distinct identifiers / 938 runes" figures quoted in digest_refine.go's docs
// were measured with. That helper is gone with the construction it served — nothing called it
// after the worst case was rebuilt — but its numbers are kept in those docs because they are
// the evidence for why density, not length, is what a retain-list worst case turns on.
func packIdentifiers(next *int, n int) string {
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(fmt.Sprintf("Id%06d ", *next))
		*next++
	}
	r := []rune(b.String())
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// TestWorstCasePromptOnBothPaths is the honest worst-case re-derivation — the fifth on this
// branch, because the previous four were each wrong the same way: a test certified a bound
// using a number the code did not enforce.
//
// Rules this construction follows, all of them lessons from those four:
//
//   - EVERY length comes from an enforced constant. Subjects at maxSubjectTermLen x
//     MaxRecordSubjects, the recency anchor at maxRecentSubjects tokens of maxSubjectTermLen,
//     open items at DefaultListCap x promptOpenItemCap (fed MORE than DefaultListCap, since
//     the first prev out of CreateDigestWithView is unbounded — priorOpenItems is what holds
//     the count), the retain-list at retainListMaxCount/retainListMaxTotal, beats at
//     MaxBeatSelection x BeatCap, the view over SessionViewCap, the label at
//     sessionLabelCap, turning points at MaxRecordTurningPoints. Where a dimension has NO
//     enforced bound it is named as such at its construction site (SessionRecord.Projects'
//     per-entry length; DigestFacts' Topics/Entities counts) — see the closing note below on
//     what that means for "worst case". PROSE IS NOW ONE OF THOSE: CapSections clips none of
//     it, so densePrev's sections are a measured stored size (storedProseRunes and friends,
//     the former caps) rather than a bound. Prose is not a free variable for THIS prompt,
//     which is the point of TestRefinePromptIsInsensitiveToStoredProseLength: at ten times
//     these sizes the total, the window and the retain-list are identical to the rune.
//   - Identifiers are GLOBALLY DISTINCT, from one shared counter, so Identifiers' exact-string
//     dedup cannot understate the retain-list the way a per-field counter did (68 runes
//     measured across a whole Digest, against 1,126 once the names are distinct).
//   - Identifier DENSITY, not just length: packIdentifiers fills every rune with an
//     identifier-shaped token. Filler prose contributes nothing to Identifiers' output, so a
//     filler-based construction understates the retain-list by an order of magnitude
//     (measured here: Identifiers(prev) alone returns tokens joining to over 15,000 runes,
//     more than the entire prompt budget, before boundRetainList bounds it).
//   - BOTH paths are probed, not just refine. The create path has no beat ladder to yield and
//     an unbounded caller-supplied facts block, so it fails differently.
//
// What the numbers mean: the TOTAL is not really a free variable, because fitTurns fills
// whatever room is left with conversation — so a healthy worst case sits just under the
// budget by construction, and the number that carries information is the WINDOW. A total
// under budget with a starved window is the silent failure; both are asserted.
func TestWorstCasePromptOnBothPaths(t *testing.T) {
	t.Run("refine", func(t *testing.T) {
		// worstCaseOpenItems exceeds DefaultListCap deliberately: a schema-legal first prev
		// straight out of CreateDigestWithView has no maxItems on Unresolved, so the count
		// bound that actually holds is priorOpenItems' own.
		const worstCaseOpenItems = 40
		prev := densePrev(worstCaseOpenItems)
		in := realisticRefineInput()

		p := DigestUpdatePromptFrom(prev, in)
		total := len([]rune(p))
		// CONTENT, notice stripped — the quantity the backstop and both reservations
		// upstream actually promise; counting the notice inflated every margin by 97.
		window := len([]rune(windowOf(promptWindow(t, p, windowHeader, updateSectionsMarker))))
		t.Logf("WORST CASE refine: total %d runes (budget %d, margin %d), window content %d "+
			"(floor %d, margin %d)", total, DefaultPromptCharBudget,
			DefaultPromptCharBudget-total, window, MinTurnChars, window-MinTurnChars)
		logContributors(t, p, prev, in, window)
		if total > DefaultPromptCharBudget {
			t.Errorf("worst-case refine prompt %d runes exceeds the %d-rune budget", total, DefaultPromptCharBudget)
		}
		if window < MinTurnChars {
			t.Errorf("worst-case refine window starved to %d runes of content, below the %d-rune floor", window, MinTurnChars)
		}
	})

	t.Run("create", func(t *testing.T) {
		// The create path's own maximum: label at its cap, a large measured-facts block
		// (unbounded — realisticFactsBlock names that), an over-cap session view, and more
		// turns than any remaining room.
		p := DigestCreatePromptWithView(
			strings.Repeat("a real session label about the work underway ", 5),
			strings.Repeat("user: filler turn about the close\nassistant: acknowledged\n", 4000),
			strings.Repeat("user: an early turn about the work\n", 400),
			realisticFactsBlock())
		total := len([]rune(p))
		window := len([]rune(windowOf(promptWindow(t, p, createWindowHeader, createSectionsMarker))))
		t.Logf("WORST CASE create: total %d runes (budget %d, margin %d), window content %d "+
			"(floor %d, margin %d); largest single contributor is the facts block at %d runes, "+
			"ahead of the instructional tail at %d", total, DefaultPromptCharBudget,
			DefaultPromptCharBudget-total, window, MinTurnChars, window-MinTurnChars,
			len([]rune(realisticFactsBlock())), createTailLen())
		if total > DefaultPromptCharBudget {
			t.Errorf("worst-case create prompt %d runes exceeds the %d-rune budget", total, DefaultPromptCharBudget)
		}
		if window < MinTurnChars {
			t.Errorf("worst-case create window starved to %d runes of content, below the %d-rune floor", window, MinTurnChars)
		}
	})
}

// logContributors prints what each named block of a refine prompt actually costs, so "the
// worst case is N runes" is never reported without saying WHERE the runes went.
//
// Each figure is recomputed from the same function the assembly used, not measured off the
// finished string by landmark — the mistake fix round 4 removed from the backstop. beats and
// view are reported as a residual because fitDiscretionary's inputs are internal to the
// assembly; that residual also carries the fixed intro and the section headings, and is
// labelled accordingly rather than attributed to beats alone.
func logContributors(t *testing.T, p string, prev Digest, in RefineInput, window int) {
	t.Helper()
	type part struct {
		name string
		n    int
	}
	retain := retainListJoinedLen(boundRetainList(Identifiers(prev)))
	open := len([]rune(strings.Join(priorOpenItems(prev), "\n  ")))
	parts := []part{
		{"instructional tail (updateTailLen)", updateTailLen()},
		{"conversation window (fitTurns)", window},
		{"session record block", len([]rune(in.Record.Block()))},
		{"open-item accounting", open},
		{"retain-list (Identifiers, bounded)", retain},
		{"recency anchor", len([]rune(recentSubjectsOf(in.NewTurns)))},
		// clipTurn, matching the assembly. Using clipProse here would report a size the
		// production path no longer produces — the shape of decalibration this branch keeps
		// paying for, where a test computes its expectation with a different function.
		{"session label", len([]rune(clipTurn(in.SessionLabel, sessionLabelCap)))},
	}
	sum := 0
	for _, x := range parts {
		sum += x.n
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].n > parts[j].n })
	for _, x := range parts {
		t.Logf("  %-38s %5d runes", x.name, x.n)
	}
	t.Logf("  %-38s %5d runes (beats + whole-session view + intro + all section headings)",
		"residual", len([]rune(p))-sum)
	t.Logf("  LARGEST single contributor: %s at %d runes (%.0f%% of the prompt)",
		parts[0].name, parts[0].n, 100*float64(parts[0].n)/float64(len([]rune(p))))
	t.Logf("  unbounded, for the record: Identifiers(prev) offered %d runes of retain-list "+
		"before boundRetainList cut it to %d",
		retainListJoinedLen(Identifiers(prev)), retain)
	// How much slack the FLOOR really has: the window's own margin plus whatever the two
	// discretionary claimants would still give up before it. A window margin read on its own
	// is a step-function sample — the mistake an earlier round's "+255" reported.
	t.Logf("  discretionary still present (would yield to the window first): beats=%v view=%v",
		strings.Contains(p, beatsHeader), strings.Contains(p, viewHeader))
}

// densePrev builds a prior report with every prose section at the stored size these
// budget tests are calibrated to (storedProseRunes and friends — formerly the enforced prose
// caps, now just sizes; see their doc), DefaultListCap
// insights, and `unresolved` open items — every rune of it a GLOBALLY DISTINCT
// identifier-shaped token, from one shared counter, so Identifiers' dedup cannot understate
// what the retain-list would carry.
func densePrev(unresolved int) Digest { return densePrevScaled(unresolved, 1) }

// densePrevScaled is densePrev with every prose section multiplied by `scale`.
//
// It exists because prose length is now unbounded: nothing in the code stops a stored section
// from being ten times the size the old caps allowed, so a budget claim that only ever tries
// the old sizes is asserting a bound over inputs the code no longer restricts — the exact
// defect ("a test certifies a bound the code does not enforce") this branch hit five times.
// scale 1 keeps every calibrated figure where it was measured; the larger scale is used by
// TestRefinePromptIsInsensitiveToStoredProseLength.
func densePrevScaled(unresolved, scale int) Digest {
	var id int
	prev := Digest{
		Synopsis:  packIdentifiers(&id, storedSynopsisRunes*scale),
		Done:      packIdentifiers(&id, storedProseRunes*scale),
		Happened:  packIdentifiers(&id, storedHappenedRunes*scale),
		Structure: packIdentifiers(&id, storedStructureRunes*scale),
		Current:   packIdentifiers(&id, storedProseRunes*scale),
		Why:       packIdentifiers(&id, storedProseRunes*scale),
		Next:      packIdentifiers(&id, storedProseRunes*scale),
	}
	for i := 0; i < DefaultListCap; i++ {
		prev.Insights = append(prev.Insights, packIdentifiers(&id, DefaultListEntryCap))
	}
	for i := 0; i < unresolved; i++ {
		prev.Unresolved = append(prev.Unresolved, packIdentifiers(&id, DefaultListEntryCap))
	}
	return prev
}

// TestWindowKeepsItsFloorAtTheBoundary is the fix for finding 3: before fitDiscretionary
// existed, only the whole-session view ever yielded room to the recent window
// (clipSessionViewFor already reserved MinTurnChars against ITSELF) — the beat series
// was written at full size unconditionally. So a full 12-item open-item list
// (load-bearing) plus a full 12-beat series (previously NOT discretionary in practice)
// could exhaust the budget before the window's own reserve was ever considered, leaving
// fitTurns nothing to work with but its own omitted-turns notice: current, why, next and
// unresolved — the four sections the window is the ONLY evidence for — would then be
// written from zero conversation, silently, because the assembled prompt was still under
// budget by the only measure anyone was checking.
// Parameters are deliberately TIGHT, not generous: no session view at all (so there is
// nothing left for the view to yield — the beat count alone has to get this right), and
// an item length chosen to sit exactly on a decision boundary rather than comfortably
// inside a k that would be chosen either way. Fix round 2 found that the first version
// of this test (150-rune items plus a whole-session view) left 72 runes of slack —
// enough that fitDiscretionary's THEN-undercounted overhead estimate (it omitted
// beatsHeader and windowHeader, the literal headings the real assembly writes around
// the content being budgeted) still happened to pick the same beat count either way, so
// the window landed above the floor regardless and the test could not tell the two
// versions of fitDiscretionary apart. A test with that much slack survives the bug
// instead of catching it.
//
// RECALIBRATED TWICE, and the second time is the point. Fix round 2 added
// promptOpenItemCap (80), which silently clipped this test's original 124-rune items down
// to ~80 and moved the overhead it exercised, so it passed the header-omission revert
// either way (window 1,637 both ways) — a 25-line docstring describing a dead test. Round 3
// re-derived itemLen as 53. Then the budget went 11,000 -> 14,000, which decalibrated it
// AGAIN: it reported 4,612 runes (margin 3,012) and the revert left it PASSING. Same
// finding, twice, for the same reason — the construction's total pressure was fixed while
// the budget under it moved.
//
// So the third calibration does not rest on itemLen alone. 12 beats plus 12 short open
// items cannot reach the boundary at a 14,000 budget AT ALL: even at itemLen 80, the
// largest promptOpenItemCap admits, the margin is +2,600. The load-bearing pressure a real
// refinement carries is what closes that gap, and it is added here in the form the design
// actually produces it — the SESSION RECORD block, the retain-list from an
// identifier-dense prev (densePrev, the shared builder the worst-case test uses), and the
// focus-shift recency anchor. All three are load-bearing, none is discretionary, and none
// of them is a number this test invents: they come from realisticRefineInput and densePrev.
// The session view is still absent, which is what keeps this a test of the BEAT-count
// decision specifically: there is nothing else left to yield.
//
// RE-MEASURED A THIRD TIME, and the figures below are the current ones. The two quoted here
// before ("reverted: clipped to 1,589 … fixed: 1,788, margin 188", and the scan set "breaches at
// 9, 10, 15, 30, 45, 50, 60 and 80") were measured in task-7b fix round 4 and then decalibrated
// by the BEAT work, which raised BeatCap 200 -> 512: this test's whole pressure is
// MaxBeatSelection x BeatCap, so tripling a beat changes what fitDiscretionary is choosing
// between and moves every window figure. The test kept DETECTING the bug throughout, which is
// why nothing caught the drift — a docstring can go stale while the assertion stays live, and
// that is the softer half of this branch's signature defect rather than a separate one.
//
// Re-scanned 1..80 in the current state, by removing runeLen(windowHeader) and
// runeLen(beatsHeader) from fitDiscretionary's overhead, running the full scan, then restoring
// them:
//   - FIXED never breaches at any itemLen 1-80 (smallest window 1,604 of content). At itemLen 9
//     it is 2,033 runes, margin 433.
//   - REVERTED breaches at itemLen 1-10 and 46-54, and survives everywhere else. At itemLen 9 it
//     is clipped to 1,501 runes of content, 99 below the floor, and this test fails.
//
// The scan is recorded rather than summarised because the step function is the whole reason a
// single sample misleads. Note what it now says about the calibration: **9 is no longer the
// smallest divergent value** — every value from 1 upward diverges — so itemLen is currently a
// comfortable choice rather than a knife-edge one. It is left at 9 because it is inside the
// divergent band with 5 values of slack on each side, and because moving it would be a change
// nothing measured asked for. The band, not the point, is what has to be re-derived after any
// change to prompt size, BeatCap, or the budget.
func TestWindowKeepsItsFloorAtTheBoundary(t *testing.T) {
	// 9 is not a round number: it was originally the smallest item length (scanned from 1) at
	// which the fixed and reverted overhead accounting settle on different beat counts AND the
	// difference actually breaches the floor on the reverted side. It is no longer the smallest
	// — see the re-scan in the docstring — but it is still inside the divergent band.
	const itemLen = 9
	// A bare panic stack mid-suite is not a legible result, so the backstop's panic is
	// recovered into a failure that names THIS mechanism — the reverted accounting trips it
	// before the assertion below is ever reached.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("fitDiscretionary chose a beat count that starved the window past its "+
				"floor — it is not accounting for the headings the assembly writes around the "+
				"content it is budgeting: %v", r)
		}
	}()
	// densePrev(0) supplies prose at every section's cap, identifier-dense, so
	// Identifiers(prev) fills the retain-list to its bound: load-bearing pressure the real
	// refinement carries. Its own open items are replaced below so itemLen stays the knob.
	prev := densePrev(0)
	for i := 0; i < DefaultListCap; i++ {
		prev.Unresolved = append(prev.Unresolved, strings.Repeat("z", itemLen))
	}
	beats := make([]Beat, MaxBeatSelection)
	for i := range beats {
		beats[i] = Beat{Ordinal: i + 1, Text: strings.Repeat("w", BeatCap), ChangedSubject: i%2 == 0}
	}
	// The record, the label at its cap and the focus-shift anchor come from the shared
	// realistic input rather than being re-invented here, so this test cannot drift away
	// from what a refinement really carries the way its first two calibrations did.
	realistic := realisticRefineInput()
	in := RefineInput{
		SessionLabel: realistic.SessionLabel,
		Record:       realistic.Record,
		Beats:        beats,
		// No SessionView: nothing left for the view to yield, so the beat-count decision
		// alone has to get the floor right — the failure mode this test targets.
		NewTurns: realistic.NewTurns,
		Why:      TriggerFocusShift,
	}
	p, window := updatePromptAndWindow(prev, in)

	// The window fitTurns produced, not a landmark search of the finished prompt, and its
	// CONTENT rather than content-plus-notice — the same quantity the backstop checks.
	content := windowOf(window)
	got := len([]rune(content))
	t.Logf("window content: %d runes (floor %d, margin %d); total %d runes",
		got, MinTurnChars, got-MinTurnChars, len([]rune(p)))
	if got < MinTurnChars {
		t.Errorf("window starved to %d runes of content, below the documented floor of %d — "+
			"beats did not yield enough room; window content: %.80q...", got, MinTurnChars, content)
	}
}
