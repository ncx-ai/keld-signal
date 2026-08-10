package llmstudy

import (
	"testing"
)

// The first currency check independent of judgement: a story whose subject contradicts the
// measured record is wrong against a measurement, not an opinion.
func TestBeatContradictingTheRecordIsDetected(t *testing.T) {
	r := SessionRecord{Turns: 40}.WithProject("keld-signal").WithFocus("engineering", "software", 0.9)
	r.Subjects = []string{"digest", "synopsis", "threshold"}

	bad := BeatContradictsRecord("Work has focused on invoice reconciliation and vendor payment terms.", r)
	if len(bad) == 0 {
		t.Error("a beat sharing nothing with the measured subjects was not flagged")
	}
	ok := BeatContradictsRecord("Work has focused on the digest and its thresholds in keld-signal.", r)
	if len(ok) > 0 {
		t.Errorf("a beat consistent with the record was flagged: %v", ok)
	}
}

// Abstain when the record has nothing to check against.
func TestConsistencyAbstainsWithoutSubjects(t *testing.T) {
	if got := BeatContradictsRecord("anything at all", SessionRecord{Turns: 3}); len(got) > 0 {
		t.Errorf("a verdict was returned with no measured subjects: %v", got)
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
