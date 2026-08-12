package llmstudy

import (
	"strings"
	"testing"
)

// The guard's whole claim is that it measures a FACT. These are the facts it must get right, and
// the non-facts it must not start measuring.
func TestBeatAnchorIsOccurrenceNotJudgement(t *testing.T) {
	const window = "user: open fa-register.csv and check it against the depreciation schedule\n" +
		"assistant: I read app/main.py and reran the exporter, which came back empty for that " +
		"date range\n"
	cases := []struct {
		name  string
		entry string
		want  string // the anchoring term, or "" for unanchored
	}{
		{"path", "app/main.py was read end to end", "app/main.py"},
		{"dotted filename", "fa-register.csv was opened", "fa-register.csv"},
		{"case differs", "FA-Register.csv was opened", "FA-Register.csv"},
		{"trailing punctuation", "the exporter wrote app/main.py.", "app/main.py"},
		// The measured false drop the DF gate produced: every noun of this entry is in the
		// window, and all of them are popular across the corpus (export .485, rows .636,
		// empty .758, range .394). Popularity is not evidence about this entry.
		{"ordinary nouns, all present", "the export came back empty for that date range", "export"},
		{"named in neither", "the Ganymede migration was signed off", ""},
		{"function words only", "it was then done with that", ""},
	}
	for _, c := range cases {
		got := beatAnchorIn(c.entry, window, "counts: turns=4\n")
		if (got.Term == "") != (c.want == "") || (c.want != "" && !strings.EqualFold(got.Term, c.want)) {
			t.Errorf("%s: anchor(%q) = %q, want %q", c.name, c.entry, got.Term, c.want)
		}
	}
}

// Occurrence is a SUBSTRING test, which is the lenient direction and deliberately so: scoring a
// plural or a morphological variant as a fabrication is a mistake this study has already made.
func TestBeatAnchorAcceptsAMorphologicalVariant(t *testing.T) {
	const window = "user: rerun the exporter for fa-register.csv\n"
	if got := beatAnchorIn("the export was rerun", window, ""); got.Term == "" {
		t.Error("an entry naming a term the evidence contains was reported unanchored")
	}
}

// The two sides are reported separately: an entry anchored only in the RECORD is the signature of
// an event whose antecedent fell on the other side of a window boundary, which is the measurable
// cost of disjoint windows and the first thing the review round is asked to report.
func TestBeatAnchorReportsWhichSideAnchoredIt(t *testing.T) {
	const window = "user: rerun the exporter\n"
	const record = "counts: turns=9\nsubjects: fa-register.csv, depreciation\n"
	if got := beatAnchorIn("the exporter was rerun", window, record); !got.InWindow {
		t.Errorf("an entry anchored in its own window must be reported against it: %+v", got)
	}
	got := beatAnchorIn("fa-register.csv was reconciled", window, record)
	if got.Term == "" || got.InWindow {
		t.Errorf("an entry anchored only in the record must be reported as such: %+v", got)
	}
}

// A term is not decided by corpus popularity, and this is the measurement that settled it. With
// the DF gate, "the export came back empty for a date range holding no rows" was DROPPED — every
// noun of it in the window, all of them popular in an engineering corpus — while "the issue was
// discussed and identified" anchored on a bare verb (continued .000, discussed .091,
// identified .242 against export .485, rows .636, empty .758).
func TestBeatAnchorTermsDoNotDependOnTheCorpusTable(t *testing.T) {
	entry := "the export came back empty for that date range"
	restore := withDocFreq(newDocFreq([][]string{{"export"}, {"export"}, {"export"}}))
	warm := beatAnchorTerms(entry)
	restore()
	restore = withDocFreq(nil)
	cold := beatAnchorTerms(entry)
	restore()
	if len(warm) != len(cold) {
		t.Errorf("the term set moved with the corpus table: %v vs %v", warm, cold)
	}
	if len(cold) == 0 {
		t.Fatalf("an entry of ordinary nouns must still offer terms: %v", cold)
	}
	for _, w := range []string{"that", "with", "then"} {
		for _, term := range cold {
			if strings.EqualFold(term, w) {
				t.Errorf("%q is a function word and must not be a term: %v", w, cold)
			}
		}
	}
}

// Strong identifiers are reported first. This changes no decision — anchoring is an OR over every
// term — and exists so the audit line names the informative term rather than the first one.
func TestBeatAnchorTermsReportStrongIdentifiersFirst(t *testing.T) {
	got := beatAnchorTerms("the bank reconciliation moved 1,650.55 into fa-register.csv")
	if len(got) < 3 {
		t.Fatalf("want the identifiers and the ordinary nouns, got %v", got)
	}
	for _, want := range []string{"1,650.55", "fa-register.csv"} {
		var found bool
		for _, term := range got[:2] {
			if term == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q is a strong identifier and must be offered first: %v", want, got)
		}
	}
}

// The split is per entry, and it reports which term each survivor was anchored by.
func TestAnchorBeatEventsSplitsPerEntry(t *testing.T) {
	const window = "user: open fa-register.csv\nassistant: read app/main.py\n"
	kept, dropped, anchors := anchorBeatEvents([]string{
		"fa-register.csv was opened",
		"the Ganymede migration was signed off",
		"app/main.py was read",
	}, window, "")
	if len(kept) != 2 || len(dropped) != 1 || len(anchors) != 2 {
		t.Fatalf("kept %v dropped %v anchors %v", kept, dropped, anchors)
	}
	if anchors[0].Term != "fa-register.csv" || anchors[1].Term != "app/main.py" {
		t.Errorf("anchors do not name the terms the entries were kept on: %v", anchors)
	}
	if !strings.Contains(dropped[0], "Ganymede") {
		t.Errorf("the wrong entry was dropped: %v", dropped)
	}
}

// unverifiedSpecifics is recorded, never enforced, and it admits STRONG IDENTIFIERS ONLY.
// Identifiers' regex is what flagged "Key", "Initial" and "e.g" at 22.6%; the weak proper-noun
// route flags the same words whenever a model capitalises one mid-sentence, measured on this
// fixture. A strong identifier names one thing and nothing else.
func TestUnverifiedSpecificsNamesOnlyAbsentNames(t *testing.T) {
	const evidence = "user: open fa-register.csv\nassistant: read app/main.py\n"
	got := unverifiedSpecifics("fa-register.csv was reconciled against ganymede-2.tsx, e.g. "+
		"the Initial Key rows", evidence)
	if !strings.Contains(strings.ToLower(strings.Join(got, " ")), "ganymede-2.tsx") {
		t.Errorf("an absent name was not reported: %v", got)
	}
	for _, ordinary := range []string{"e.g", "Initial", "Key"} {
		for _, term := range got {
			if strings.EqualFold(term, ordinary) {
				t.Errorf("%q is the 22.6%% over-detection and must not be reported: %v",
					ordinary, got)
			}
		}
	}
	if len(unverifiedSpecifics("fa-register.csv was opened", evidence)) != 0 {
		t.Error("a present name was reported as unverified")
	}
}

// ⚠️ THE SENTINEL DEFECT, AS A TEST. Both inputs carry words this harness put there — the
// window's role labels and its hole marker, the record's field labels and count keys — and every
// one of them is in EVERY window by construction. An entry anchoring on those would be anchoring
// on the instrument, which is exactly how leak detection came to flag only the sentinel the model
// is instructed to emit.
func TestAnchoringNeverMatchesTheHarnessOwnWords(t *testing.T) {
	window := "user: rerun it\n" + beatOmittedNotice + "assistant: done\n"
	record := "counts: turns=31 user_turns=10 tool_calls=9 corrections=1\n" +
		"recurring subjects: Northwind, 1,400.00\n"
	for _, entry := range []string{
		"the assistant reran the export",                  // role label
		"turns since the previous update were omitted",    // hole marker
		"the context was covered by a later window",       // hole marker
		"corrections were made to the recurring subjects", // record labels and keys
	} {
		if got := beatAnchorIn(entry, window, record); got.Term != "" {
			t.Errorf("%q anchored on the harness's own word %q", entry, got.Term)
		}
	}
	// And the material a person or a tool actually produced still anchors.
	if got := beatAnchorIn("the Northwind provision was applied", window, record); got.Term == "" {
		t.Error("a measured record value no longer anchors")
	}
	if got := beatAnchorIn("1,400.00 remained unexplained", window, record); got.Term == "" {
		t.Error("a measured amount no longer anchors")
	}
}
