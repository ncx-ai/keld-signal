package enrich

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The prior's `status` is the sidecar's `window.REASONS` — the FOUR different
// ways a level can fail to have a dominant value, plus the one way it can
// succeed. The Go side drops a value it does not recognise, so a drift here
// would silently stop publishing a dimension rather than fail loudly. Pinned
// against the file that owns the vocabulary.
func TestPriorStatusVocabularyMatchesTheSidecar(t *testing.T) {
	src, err := os.ReadFile("../../../sidecar/app/analysis/window.py")
	if err != nil {
		t.Fatalf("cannot read the sidecar's window module, so PriorStatuses is unpinned: %v", err)
	}
	m := regexp.MustCompile(`(?ms)^REASONS = \((.*?)\)`).FindStringSubmatch(string(src))
	if m == nil {
		t.Fatal("no `REASONS = (...)` assignment in the sidecar module")
	}
	var want []string
	for _, q := range regexp.MustCompile(`"([a-z_]+)"`).FindAllStringSubmatch(m[1], -1) {
		want = append(want, q[1])
	}
	if len(want) == 0 {
		t.Fatal("parsed no values out of REASONS; the comparison would be vacuous")
	}
	if strings.Join(want, ",") != strings.Join(PriorStatuses, ",") {
		t.Errorf("REASONS drifted:\n python %v\n go     %v", want, PriorStatuses)
	}
}

func TestKnownPriorStatusVocabulary(t *testing.T) {
	for _, s := range PriorStatuses {
		if !KnownPriorStatus(s) {
			t.Errorf("published status %q not recognised", s)
		}
	}
	for _, s := range []string{"", "provisional", "attributed ", "Absent"} {
		if KnownPriorStatus(s) {
			t.Errorf("%q was recognised; an unknown status is version skew and must drop", s)
		}
	}
}

// THE POINTERS ARE THE CONTRACT, asserted at the wire rather than argued for.
// 45.1% of real windows are a session's first and have no prior at all; every
// one of them reports a null contrast. If Agrees/Novel were plain bools and
// Departure a plain float64, all three would marshal as false/false/0 — "we
// compared this window to its session and it matched exactly", stated about a
// window that had no session to compare to.
func TestANullContrastMarshalsAsNullNotAsAgreement(t *testing.T) {
	b, err := json.Marshal(Prior{Value: "", Share: 0, Evidence: 0, Status: "absent"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, forbidden := range []string{`"agrees"`, `"departure"`, `"novel"`} {
		if strings.Contains(got, forbidden) {
			t.Errorf("an absent prior stated %s: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, `"status":"absent"`) {
		t.Errorf("the status was omitted, so the block is a blank rather than a stated absence: %s", got)
	}

	no, dep := false, 0.0
	b, _ = json.Marshal(Prior{Value: "main", Share: 0.9, Evidence: 40, Status: "attributed",
		Agrees: &no, Departure: &dep, Novel: &no})
	got = string(b)
	// A measured false and a measured zero are FACTS and must survive omitempty:
	// `departure: 0.0` says the window looks exactly like its session, which is
	// the twin of the +0.516 case and just as reportable.
	for _, want := range []string{`"agrees":false`, `"departure":0`, `"novel":false`} {
		if !strings.Contains(got, want) {
			t.Errorf("a measured %s was dropped by omitempty: %s", want, got)
		}
	}
}

// The prior travels with the digest because it comes from the same /analyze
// call, and it must never be mistaken for the digest. Two maps, keyed alike,
// converted at one chokepoint — this pins that the pipeline actually carries the
// second one through to the Profile.
func TestThePriorReachesTheProfileWithoutTouchingTheWorkstreams(t *testing.T) {
	yes := true
	dep := 0.516
	an := WindowAnalysis{
		Workstreams: map[string]Labeled{"language": {Value: "Python", Confidence: 0.571}},
		Prior: map[string]Prior{
			"language": {Value: "TypeScript", Share: 0.886, Evidence: 271,
				Status: "attributed", Departure: &dep},
			// The window has no `skill` at all; the prior does. It must stay
			// that way all the way to the Profile.
			"skill": {Value: "superpowers:brainstorming", Share: 1.0, Evidence: 38,
				Status: "attributed", Novel: &yes},
		},
	}
	ex := WorkstreamsExtractor{Analyze: func(string, string, int) (WindowAnalysis, bool) {
		return an, true
	}}
	out, err := ex.Run(&JobContext{TranscriptPath: "/tmp/t.jsonl", PromptID: "p1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, ok := out["prior"].(map[string]Prior)
	if !ok {
		t.Fatalf("the pass published no prior: %+v", out)
	}
	if len(got) != 2 || got["language"].Value != "TypeScript" {
		t.Errorf("prior mangled by the pass: %+v", got)
	}
	ws := out["workstreams"].(map[string]Labeled)
	if _, present := ws["skill"]; present {
		t.Errorf("the prior leaked into the workstreams: %+v", ws)
	}
	if ws["language"].Value != "Python" {
		t.Errorf("the window's own answer was overwritten by its session: %+v", ws)
	}
	// No Producer stamp: a Prior is not a Labeled and has no field for one, and
	// the pass is already attributed for this job through extractor_versions.
	if p := priorFrom(out); len(p) != 2 || p["skill"].Novel == nil || !*p["skill"].Novel {
		t.Errorf("priorFrom lost the block: %+v", p)
	}
}

// A window analysis with no prior publishes NO key. An empty map would read
// downstream as "we looked at the session and it said nothing", which is a
// different fact from a sidecar too old to have looked at all.
func TestNoPriorPublishesNoKey(t *testing.T) {
	ex := WorkstreamsExtractor{Analyze: func(string, string, int) (WindowAnalysis, bool) {
		return WindowAnalysis{Workstreams: map[string]Labeled{"branch": {Value: "main"}}}, true
	}}
	out, err := ex.Run(&JobContext{TranscriptPath: "/tmp/t.jsonl", PromptID: "p1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, present := out["prior"]; present {
		t.Errorf("an empty prior published a key: %+v", out)
	}
	if priorFrom(out) != nil {
		t.Errorf("priorFrom invented a map: %+v", priorFrom(out))
	}
}
