package llmstudy

import (
	"testing"
)

// The first currency check independent of judgement: a story whose subject contradicts the
// measured record is wrong against a measurement, not an opinion.
func TestBeatContradictingTheRecordIsDetected(t *testing.T) {
	r := SessionRecord{Turns: 40}.WithProject("keld-signal").WithFocus("engineering", "software", 0.9)
	r.Subjects = []string{"digest", "synopsis", "threshold"}

	bad, checked := BeatContradictsRecord("Work has focused on invoice reconciliation and vendor payment terms.", r)
	if !checked {
		t.Error("a record with enough subjects abstained instead of rendering a verdict")
	}
	if len(bad) == 0 {
		t.Error("a beat sharing nothing with the measured subjects was not flagged")
	}
	ok, checked := BeatContradictsRecord("Work has focused on the digest and its thresholds in keld-signal.", r)
	if !checked {
		t.Error("a record with enough subjects abstained instead of rendering a verdict")
	}
	if len(ok) > 0 {
		t.Errorf("a beat consistent with the record was flagged: %v", ok)
	}
}

// Abstain when the record has nothing to check against. checked must go false, not merely
// terms==nil: a caller computing a rate as flagged/checked cannot tell "nothing wrong" from
// "nothing measured" apart if abstention and a clean verdict both collapse to the same nil —
// exactly the false-confidence failure this study exists to catch.
func TestConsistencyAbstainsWithoutSubjects(t *testing.T) {
	got, checked := BeatContradictsRecord("anything at all", SessionRecord{Turns: 3})
	if checked {
		t.Error("abstention was reported as a rendered verdict (checked=true) with no measured subjects")
	}
	if len(got) > 0 {
		t.Errorf("a verdict was returned with no measured subjects: %v", got)
	}
}

// distinctiveTerms must emit the TRIMMED spelling, or a subject sitting right before a
// sentence boundary can never match a clean word list. subjectTokens keeps '.', '-', '_', '/'
// attached to a token (they can be part of an identifier), so "threshold." reached
// BeatContradictsRecord as a token distinct from the record's plain "threshold" — a beat is
// short sentences, so a subject noun immediately before a period, comma, or parenthesis is
// the ORDINARY case, not an edge case. Pins the class across all three punctuation shapes,
// plus the negative direction: a genuinely absent term must still be flagged regardless.
func TestBeatContradictsRecordTrimsTrailingPunctuation(t *testing.T) {
	r := SessionRecord{Turns: 40}
	r.Subjects = []string{"digest", "synopsis", "threshold"}

	consistent := []string{
		"Work continues on the threshold.",              // sentence-final period
		"Work continues on the threshold, still going.", // comma-final
		"Work continues on the threshold) as planned.",  // parenthesis-final
	}
	for _, beat := range consistent {
		got, checked := BeatContradictsRecord(beat, r)
		if !checked {
			t.Errorf("%q: abstained despite a record with enough subjects", beat)
		}
		if len(got) > 0 {
			t.Errorf("%q: a subject the record holds (modulo trailing punctuation) was flagged: %v", beat, got)
		}
	}

	// Negative direction: punctuation-trimming must not swallow a real contradiction.
	got, checked := BeatContradictsRecord("Work continues on invoicing.", r)
	if !checked {
		t.Error("abstained on a beat the record should have been able to check")
	}
	if len(got) == 0 {
		t.Error("a genuinely absent subject was not flagged")
	}
}

// Observed in real output: a next inventing schema fields never discussed. T7 only inspects
// unresolved, so nothing caught it.
func TestFabricatedNextIsDetected(t *testing.T) {
	src := "user: add a synopsis section to the digest\nassistant: added it\n"
	d := Digest{Next: "Define a schema with fields for ToolName, CallID, InputPayload and Timestamp."}
	if got := FabricatedNext(d, src); len(got) == 0 {
		t.Error("invented specifics in next were not flagged")
	}
	d2 := Digest{Next: "Extend the synopsis section and re-measure the digest thresholds."}
	if got := FabricatedNext(d2, src); len(got) > 0 {
		t.Errorf("a grounded next was flagged: %v", got)
	}
}

// RetainedFacts hand-enumerated the prose fields and so never saw synopsis — the same defect
// ProseFields was introduced to fix.
func TestRetainedFactsSeesEverySection(t *testing.T) {
	d := Digest{Synopsis: "the Meridian close"}
	if got := RetainedFacts(d, []string{"Meridian"}); got != 1 {
		t.Errorf("a fact present only in synopsis was not counted: %d", got)
	}
}
