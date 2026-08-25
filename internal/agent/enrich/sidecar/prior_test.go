package sidecar

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// priorResponse is a realistic /analyze body carrying the SESSION PRIOR block:
// the session as it stood BEFORE this window, reported beside the window's own
// answer. Three dimensions publish it (workflow, language, branch — decided by
// measurement over 1,022 windows); the block also carries `clamped`, which this
// client deliberately does not model.
//
// The three shapes that matter are all here: a prior that agrees, a prior the
// window departs from with a value the session never held (`novel`), and a
// SESSION-FIRST window whose prior is `absent` and whose contrast is null on all
// three measures. That last one is 45.1% of real windows.
func priorResponse() map[string]any {
	return map[string]any{
		"schema": 8, "evidence": 180, "session": "d19a4c72",
		"window_start": "2026-08-20T09:03:17Z", "window_end": "2026-08-20T10:03:17Z",
		"workstreams": map[string]any{
			"branch": map[string]any{"value": "feat/ledger", "share": 0.94, "evidence": 62,
				"provenance": "known:tool_inputs"},
			"language": map[string]any{"value": "Python", "share": 0.571, "evidence": 7,
				"provenance": "known:tool_inputs"},
		},
		"inventory": map[string]any{"named_terms": []map[string]any{{"value": "Federico", "n": 2}}},
		"prior": map[string]any{
			"clamped": false,
			"dimensions": map[string]any{
				// The motivating case, as measured: the window is Python 0.571 in
				// a session that is TypeScript 0.886 and gives Python 5.5%.
				"language": map[string]any{
					"value": "TypeScript", "share": 0.886, "evidence": 271,
					"status": "attributed", "agrees": false, "departure": 0.516,
					"novel": false,
				},
				// A skill the session had never run: the phase transition.
				"workflow": map[string]any{
					"value": "superpowers:brainstorming", "share": 1.0, "evidence": 38,
					"status": "attributed", "agrees": false, "departure": 1.0, "novel": true,
				},
				// Session-first: absent, and NOT filled in.
				"branch": map[string]any{
					"value": nil, "share": 0.0, "evidence": 0, "status": "absent",
					"agrees": nil, "departure": nil, "novel": nil,
				},
			},
		},
	}
}

func TestAnalyzeDecodesThePriorBlock(t *testing.T) {
	out, ok := serve(t, priorResponse()).Analyze("/tmp/t.jsonl", "prompt-1", 60)
	if !ok {
		t.Fatal("a response carrying the prior block failed to decode")
	}
	if out.Evidence != 180 || out.Schema != 8 {
		t.Errorf("the digest did not survive the added block: %+v", out)
	}
	lg := out.Prior.Dimensions["language"]
	if lg == nil {
		t.Fatalf("language prior dropped: %+v", out.Prior)
	}
	if lg.Value != "TypeScript" || lg.Share != 0.886 || lg.Evidence != 271 || lg.Status != "attributed" {
		t.Errorf("language prior mangled: %+v", lg)
	}
	if lg.Agrees == nil || *lg.Agrees {
		t.Errorf("agrees = %v, want a pointer to false", lg.Agrees)
	}
	if lg.Departure == nil || *lg.Departure != 0.516 {
		t.Errorf("departure = %v, want 0.516 — the measure that catches the excursion", lg.Departure)
	}
	if lg.Novel == nil || *lg.Novel {
		t.Errorf("novel = %v, want a pointer to false: a TypeScript session that has touched "+
			"Python is not one where Python is absent", lg.Novel)
	}

	// THE POINTERS ARE THE CONTRACT, exactly as they are on Dynamic. A
	// session-first window cannot say whether it agrees, how far it departed, or
	// whether its value is new — there is nothing to compare against. A plain
	// bool/float64 would render all three as false/0.0, which reads as "we
	// compared and it matched", the fallback this whole block refuses to be.
	br := out.Prior.Dimensions["branch"]
	if br == nil || br.Status != "absent" {
		t.Fatalf("branch prior = %+v", br)
	}
	if br.Agrees != nil || br.Departure != nil || br.Novel != nil {
		t.Errorf("a contrast was invented against an empty prior: %+v", br)
	}
	if br.Value != "" || br.Evidence != 0 {
		t.Errorf("an absent prior carries a value: %+v", br)
	}
	if wf := out.Prior.Dimensions["workflow"]; wf == nil || wf.Novel == nil || !*wf.Novel {
		t.Errorf("workflow novelty lost: %+v", wf)
	}
}

// The privacy claim for THIS block, verified against the type. Unlike the
// dynamics subtree — which may carry no level value at all — the prior's `value`
// IS a reference level, and that is deliberate: `prior.PRIOR_DIMENSIONS` is
// derived from the sidecar's ALLOCATION list, so a prior can only ever name a
// value that already publishes in `workstreams` beside it (a branch, a language,
// a skill), never `named_terms`, which is the one level read from message TEXT
// and has held real person names.
//
// So the assertion is not "no strings" but "exactly two, and they are the ones
// argued for": `value` (an allocation level, same class as Workstream.Value) and
// `status` (a closed vocabulary). A third string field — a `top` list, an
// example, a reason — fails here rather than in a review.
func TestThePriorSubtreeCarriesOnlyAnAllocationValueAndAClosedStatus(t *testing.T) {
	allowed := map[string]bool{"value": true, "status": true}
	found := 0
	var walk func(reflect.Type, string, int)
	walk = func(tp reflect.Type, path string, depth int) {
		if depth > 8 {
			t.Fatalf("prior type is deeper than the walk: %s", path)
		}
		switch tp.Kind() {
		case reflect.Ptr, reflect.Slice, reflect.Array, reflect.Map:
			walk(tp.Elem(), path, depth+1)
		case reflect.Struct:
			for i := 0; i < tp.NumField(); i++ {
				f := tp.Field(i)
				tag := strings.Split(f.Tag.Get("json"), ",")[0]
				if tag == "" {
					tag = strings.ToLower(f.Name)
				}
				walk(f.Type, path+"."+tag, depth+1)
			}
		case reflect.String:
			found++
			leaf := path[strings.LastIndex(path, ".")+1:]
			if !allowed[leaf] {
				t.Errorf("%s is an unaccounted-for string inside the prior block: only an "+
					"allocation-level `value` and the closed `status` vocabulary may cross", path)
			}
		case reflect.Interface:
			t.Errorf("%s is an interface: anything at all decodes into it", path)
		}
	}
	f, ok := reflect.TypeOf(AnalyzeResult{}).FieldByName("Prior")
	if !ok {
		t.Fatal("AnalyzeResult has no Prior field")
	}
	walk(f.Type, "prior", 0)
	if found != len(allowed) {
		t.Fatalf("walk found %d string fields, want %d — the assertions above are vacuous "+
			"if the walk is not reaching the leaves", found, len(allowed))
	}
}

// THE RULE, at the conversion chokepoint: CONTRAST, NEVER FALLBACK. A prior for
// a dimension the window could not attribute must reach `Prior` and must not
// reach `Workstreams`. Inheriting it would launder "we do not know" into
// something confident — the defect MIN_EVIDENCE exists to prevent, and one this
// project has paid for twice (activity_type's `transform`: predicted 36 times,
// right zero).
func TestAnUnattributedWindowIsNeverFilledInFromItsPrior(t *testing.T) {
	body := priorResponse()
	// The window has no `workflow` value at all; its prior has an emphatic one.
	body["workstreams"].(map[string]any)["workflow"] = nil

	got, ok := serve(t, body).AnalyzeLabeled("/tmp/t.jsonl", "prompt-1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if l, present := got.Workstreams["workflow"]; present {
		t.Errorf("the window's workflow was filled in from the session prior: %+v — "+
			"an unattributed window stays unattributed", l)
	}
	if p, present := got.Prior["workflow"]; !present || p.Value != "superpowers:brainstorming" {
		t.Errorf("the prior itself was dropped: %+v (present=%v)", p, present)
	}
	// ... and the dimensions the window DID attribute are untouched by any of it.
	if got.Workstreams["language"].Value != "Python" {
		t.Errorf("digest half mangled: %+v", got.Workstreams)
	}
}

func TestAnalyzeLabeledForwardsThePrior(t *testing.T) {
	got, ok := serve(t, priorResponse()).AnalyzeLabeled("/tmp/t.jsonl", "prompt-1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if len(got.Prior) != 3 {
		t.Fatalf("want branch/language/workflow, got %+v", got.Prior)
	}
	lg := got.Prior["language"]
	if lg.Value != "TypeScript" || lg.Status != "attributed" || lg.Evidence != 271 {
		t.Errorf("language prior mangled in conversion: %+v", lg)
	}
	if lg.Departure == nil || *lg.Departure != 0.516 {
		t.Errorf("departure lost in conversion: %v", lg.Departure)
	}
	if a := got.Prior["language"].Agrees; a == nil || *a {
		t.Errorf("agrees = %v, want a pointer to false", a)
	}
	// The nulls survive the conversion, not just the decode.
	if b := got.Prior["branch"]; b.Agrees != nil || b.Departure != nil || b.Novel != nil {
		t.Errorf("a contrast was invented for a session-first window: %+v", b)
	}
	if b := got.Prior["branch"]; b.Status != "absent" {
		t.Errorf("an absent prior lost its status: %+v — a blank a reader takes for a bug is "+
			"what someone eventually fills in", b)
	}
}

// A status this binary does not know is SIDECAR VERSION SKEW — the sidecar is
// frozen and shipped separately from keld-agent, so an older or newer one can
// sit in ~/.local/bin indefinitely. `status` is a closed published vocabulary
// gated by enrich.SchemaVersion; forwarding an unrecognised value would put a
// label in Atlas that no consumer's vocabulary contains.
//
// The whole DIMENSION drops, not half of it: `departure: 0.516` without a
// readable status is a number a reader cannot place — is the session attributed
// at all? — which is the same "uninterpretable in half" argument convertDynamics
// and convertEffort already make.
func TestAnalyzeLabeledDropsAnUnknownPriorStatus(t *testing.T) {
	body := priorResponse()
	dims := body["prior"].(map[string]any)["dimensions"].(map[string]any)
	dims["language"].(map[string]any)["status"] = "provisional"

	got, ok := serve(t, body).AnalyzeLabeled("/tmp/t.jsonl", "prompt-1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if p, present := got.Prior["language"]; present {
		t.Errorf("an unknown STATUS was published: %+v", p)
	}
	if _, present := got.Prior["workflow"]; !present {
		t.Error("a skewed sibling took down the dimensions that were readable")
	}
	// The digest half is unaffected: prior vocabulary skew must not cost the
	// facet that has been publishing since before this block existed.
	if got.Workstreams["branch"].Value != "feat/ledger" {
		t.Errorf("workstreams half dropped with the prior: %+v", got.Workstreams)
	}
}

// A sidecar too old to compute the block sends nothing, and the pass must then
// publish NO key rather than an empty object — "we looked at the session and it
// said nothing" is a different fact from "nobody looked". Same nil-not-empty
// rule convertDynamics, convertActs and convertEffort each follow.
func TestASidecarWithNoPriorBlockPublishesNoPrior(t *testing.T) {
	body := priorResponse()
	delete(body, "prior")
	got, ok := serve(t, body).AnalyzeLabeled("/tmp/t.jsonl", "prompt-1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if got.Prior != nil {
		t.Errorf("Prior = %+v, want nil for a sidecar that sent no block", got.Prior)
	}
	if got.Workstreams["branch"].Value != "feat/ledger" {
		t.Errorf("the digest was lost with the missing block: %+v", got.Workstreams)
	}
}

// A null dimension is not a zero prior: publishing one would state `status: ""`
// and `evidence: 0`, a real-looking answer nobody computed.
func TestANullPriorDimensionIsOmittedNotZeroed(t *testing.T) {
	body := priorResponse()
	body["prior"].(map[string]any)["dimensions"].(map[string]any)["workflow"] = nil
	got, ok := serve(t, body).AnalyzeLabeled("/tmp/t.jsonl", "prompt-1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if p, present := got.Prior["workflow"]; present {
		t.Errorf("a null dimension was published as a zero Prior: %+v", p)
	}
	if len(got.Prior) != 2 {
		t.Errorf("the surviving dimensions were dropped too: %+v", got.Prior)
	}
}

var _ = enrich.Prior{}
