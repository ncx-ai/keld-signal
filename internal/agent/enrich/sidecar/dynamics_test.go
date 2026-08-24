package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// dynamicsResponse is a realistic /analyze body: the digest, the inventory the
// client must have nowhere for, and the FULL dynamics block the sidecar emits —
// including the parts this client deliberately does not model (the per-side
// value/share/evidence/reason, the three timestamps, the sizer detail).
func dynamicsResponse() map[string]any {
	return map[string]any{
		"schema": 3, "evidence": 180, "session": "d19a4c72",
		"window_start": "2026-08-20T09:03:17Z", "window_end": "2026-08-20T10:03:17Z",
		"workstreams": map[string]any{
			"branch": map[string]any{"value": "feat/ledger", "share": 0.94, "evidence": 62,
				"provenance": "known:tool_inputs"},
		},
		"inventory": map[string]any{"named_terms": []map[string]any{{"value": "Federico", "n": 2}}},
		"dynamics": map[string]any{
			"sizer": "ewma", "slice_minutes": 12.5, "baseline_minutes": 47.5,
			"source": "bin+event", "reconcile_scope": "file",
			"slice_start": "2026-08-20T09:50:47Z", "slice_end": "2026-08-20T10:03:17Z",
			"baseline_start": "2026-08-20T09:03:17Z",
			"sizer_detail": map[string]any{"detected_at": 1755683447, "observations": 11,
				"fires": 1, "level": "branch", "slice_minutes": 12.5},
			"dimensions": map[string]any{
				"branch": map[string]any{
					"status": "compared", "turnover": 0.4, "decay": 0.25,
					"concentration_shift": -0.31, "changed": true, "reading": "switched",
					// The two sides carry a reference-level VALUE. Nothing here may
					// forward them — see TestNothingInTheDynamicsSubtreeCanCarryALevelValue.
					"slice":    map[string]any{"value": "feat/ledger", "share": 1.0, "evidence": 62, "reason": "attributed"},
					"baseline": map[string]any{"value": "main", "share": 1.0, "evidence": 118, "reason": "attributed"},
				},
				"workflow": map[string]any{
					"status": "both_absent", "turnover": nil, "decay": nil,
					"concentration_shift": nil, "changed": false, "reading": nil,
					"slice":    map[string]any{"value": nil, "share": 0.0, "evidence": 0, "reason": "absent"},
					"baseline": map[string]any{"value": nil, "share": 0.0, "evidence": 0, "reason": "absent"},
				},
				"language": map[string]any{
					"status": "slice_thin", "turnover": nil, "decay": nil,
					"concentration_shift": nil, "changed": nil, "reading": nil,
					"slice":    map[string]any{"value": nil, "share": 0.0, "evidence": 2, "reason": "thin"},
					"baseline": map[string]any{"value": "go", "share": 0.8, "evidence": 40, "reason": "attributed"},
				},
				// A dimension the sidecar reported as null (no comparison at all).
				"output_type": nil,
			},
		},
	}
}

func serve(t *testing.T, body map[string]any) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, 5*time.Second)
}

// The three metrics and `changed` are POINTERS because null is a fact here, not
// an absence: a metric is reported only under status "compared", and `changed`
// is a THREE-state answer whose third state ("we cannot say") is the whole
// point of the sidecar's evidence-floor work. A float64/bool would render
// "unknown" as 0.0/false — "we checked, nothing moved" — which is the exact
// misreading dynamics.py exists not to produce.
func TestAnalyzeDecodesTheDynamicsBlock(t *testing.T) {
	out, ok := serve(t, dynamicsResponse()).Analyze("/tmp/t.jsonl", "prompt-1", 60)
	if !ok {
		t.Fatal("a response carrying the dynamics block failed to decode")
	}
	if out.Evidence != 180 || out.Schema != 3 {
		t.Errorf("the digest did not survive the added block: %+v", out)
	}

	br := out.Dynamics.Dimensions["branch"]
	if br == nil {
		t.Fatalf("branch dynamics dropped: %+v", out.Dynamics)
	}
	if br.Status != "compared" || br.Reading != "switched" {
		t.Errorf("status/reading = %q/%q", br.Status, br.Reading)
	}
	if br.Changed == nil || !*br.Changed {
		t.Errorf("changed = %v, want a pointer to true", br.Changed)
	}
	if br.Turnover == nil || *br.Turnover != 0.4 {
		t.Errorf("turnover = %v, want 0.4", br.Turnover)
	}
	if br.Decay == nil || *br.Decay != 0.25 {
		t.Errorf("decay = %v, want 0.25", br.Decay)
	}
	if br.ConcentrationShift == nil || *br.ConcentrationShift != -0.31 {
		t.Errorf("concentration_shift = %v, want -0.31", br.ConcentrationShift)
	}

	// both_absent: `changed` is definitively FALSE (a level that never fired did
	// not change) and must arrive as a non-nil false, distinct from the unknown
	// below. The metrics are null because a share of nothing is not zero.
	wf := out.Dynamics.Dimensions["workflow"]
	if wf == nil || wf.Status != "both_absent" {
		t.Fatalf("workflow dynamics = %+v", wf)
	}
	if wf.Changed == nil || *wf.Changed {
		t.Errorf("both_absent changed = %v, want a pointer to false", wf.Changed)
	}
	if wf.Turnover != nil || wf.Decay != nil || wf.ConcentrationShift != nil {
		t.Errorf("metrics reported outside `compared`: %+v", wf)
	}
	if wf.Reading != "" {
		t.Errorf("reading stated outside `compared`: %q", wf.Reading)
	}

	// slice_thin: `changed` is UNKNOWN — a null that must stay a null.
	lg := out.Dynamics.Dimensions["language"]
	if lg == nil || lg.Status != "slice_thin" {
		t.Fatalf("language dynamics = %+v", lg)
	}
	if lg.Changed != nil {
		t.Errorf("changed = %v, want nil (unknown) for slice_thin", *lg.Changed)
	}

	if d, present := out.Dynamics.Dimensions["output_type"]; !present || d != nil {
		t.Errorf("a null dimension must decode to a nil pointer, got %+v (present=%v)", d, present)
	}
}

// The privacy claim, VERIFIED against the type rather than trusted. Every
// dynamics value that could be a reference level's own string — a branch name, a
// file extension, a skill id, and above all a named TERM lifted from message text
// — lives in the block's per-side `slice`/`baseline` objects, and this client
// must have structurally nowhere to put one. So: walk the whole Dynamics subtree
// and assert its ONLY string-typed fields are the two closed vocabularies
// (status, reading). A field added tomorrow that could carry a level value fails
// here, at the decode boundary, rather than in a code review.
func TestNothingInTheDynamicsSubtreeCanCarryALevelValue(t *testing.T) {
	allowed := map[string]bool{"status": true, "reading": true}
	var walk func(reflect.Type, string, int)
	strings_ := 0
	walk = func(tp reflect.Type, path string, depth int) {
		if depth > 8 {
			t.Fatalf("dynamics type is deeper than the walk: %s", path)
		}
		switch tp.Kind() {
		case reflect.Ptr, reflect.Slice, reflect.Array:
			walk(tp.Elem(), path, depth+1)
		case reflect.Map:
			// Map KEYS are dimension names from a closed set (see
			// enrich.Dynamic's doc); the values are what is walked.
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
			strings_++
			leaf := path[strings.LastIndex(path, ".")+1:]
			if !allowed[leaf] {
				t.Errorf("%s is a string inside the dynamics block: a reference-level "+
					"value (branch name, path, named term) could be decoded into it. "+
					"Only the closed status/reading vocabularies may cross.", path)
			}
		case reflect.Interface:
			t.Errorf("%s is an interface: anything at all decodes into it", path)
		}
	}
	f, ok := reflect.TypeOf(AnalyzeResult{}).FieldByName("Dynamics")
	if !ok {
		t.Fatal("AnalyzeResult has no Dynamics field")
	}
	walk(f.Type, "dynamics", 0)
	if strings_ != len(allowed) {
		t.Fatalf("walk found %d string fields, want %d — the assertions above are "+
			"vacuous if the walk is not reaching the leaves", strings_, len(allowed))
	}
}

// AnalyzeLabeled is the chokepoint the pipeline consumes, and it converts both
// halves of one /analyze call: what the window CONTAINS and how it is CHANGING.
func TestAnalyzeLabeledForwardsTheDynamics(t *testing.T) {
	got, ok := serve(t, dynamicsResponse()).AnalyzeLabeled("/tmp/t.jsonl", "prompt-1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if got.Workstreams["branch"].Value != "feat/ledger" {
		t.Errorf("workstreams half lost: %+v", got.Workstreams)
	}
	if len(got.Dynamics) != 3 {
		t.Fatalf("want branch/workflow/language, got %+v", got.Dynamics)
	}
	if _, present := got.Dynamics["output_type"]; present {
		t.Error("a null dimension must be OMITTED, not published as a zero Dynamic: " +
			"a status of \"\" reads downstream as a real comparison outcome")
	}
	br := got.Dynamics["branch"]
	if br.Status != "compared" || br.Reading != "switched" || br.Turnover == nil || *br.Turnover != 0.4 {
		t.Errorf("branch dynamic mangled in conversion: %+v", br)
	}
	if br.Changed == nil || !*br.Changed {
		t.Errorf("changed lost in conversion: %v", br.Changed)
	}
	// The three states survive the conversion, not just the decode: false where
	// the answer is definitively no, and NIL where it is unknown. A conversion
	// that defaulted the unknown to false would publish "we checked, nothing
	// moved" about a dimension nobody could compare.
	if c := got.Dynamics["workflow"].Changed; c == nil || *c {
		t.Errorf("both_absent changed = %v, want a pointer to false", c)
	}
	if c := got.Dynamics["language"].Changed; c != nil {
		t.Errorf("slice_thin changed = %v, want nil (unknown)", *c)
	}
	if d := got.Dynamics["language"]; d.Turnover != nil || d.Decay != nil || d.ConcentrationShift != nil {
		t.Errorf("metrics invented outside `compared`: %+v", d)
	}
}

// A status or reading this binary does not know is a SIDECAR VERSION SKEW: the
// sidecar is frozen and shipped separately from keld-agent (an older one can sit
// in ~/.local/bin indefinitely), and both fields are closed published
// vocabularies gated by enrich.SchemaVersion. Forwarding an unrecognised value
// would put a label in Atlas that no consumer's vocabulary contains — the same
// rule that keeps masked spans to matched vocabulary ids only. The whole
// dimension is dropped rather than half-published, because a reading without its
// status (or a status nobody can read) is not interpretable.
func TestAnalyzeLabeledDropsAnUnknownDynamicsVocabulary(t *testing.T) {
	body := dynamicsResponse()
	dims := body["dynamics"].(map[string]any)["dimensions"].(map[string]any)
	dims["branch"].(map[string]any)["reading"] = "consolidating"
	dims["language"].(map[string]any)["status"] = "slice_sparse"

	got, ok := serve(t, body).AnalyzeLabeled("/tmp/t.jsonl", "prompt-1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if _, present := got.Dynamics["branch"]; present {
		t.Errorf("an unknown READING was published: %+v", got.Dynamics["branch"])
	}
	if _, present := got.Dynamics["language"]; present {
		t.Errorf("an unknown STATUS was published: %+v", got.Dynamics["language"])
	}
	if _, present := got.Dynamics["workflow"]; !present {
		t.Error("a skewed sibling took down the dimensions that were readable")
	}
	// The digest half is unaffected: dynamics vocabulary skew must not cost the
	// facet that has been publishing since before this block existed.
	if got.Workstreams["branch"].Value != "feat/ledger" {
		t.Errorf("workstreams half dropped with the dynamic: %+v", got.Workstreams)
	}
}

// A response with no dynamics block at all (an older sidecar, or /analyze on a
// window with no series) is a SUCCESS with no dynamics — not a failure, and not
// an empty map that would publish as "we compared and found nothing".
func TestAnalyzeLabeledWithoutADynamicsBlock(t *testing.T) {
	got, ok := serve(t, map[string]any{
		"schema": 3, "evidence": 5, "session": "d19a4c72",
		"workstreams": map[string]any{
			"branch": map[string]any{"value": "main", "share": 1.0, "evidence": 5,
				"provenance": "known:tool_inputs"},
		},
	}).AnalyzeLabeled("/tmp/t.jsonl", "prompt-1", 60)
	if !ok {
		t.Fatal("a response without dynamics must not report failure")
	}
	if got.Dynamics != nil {
		t.Errorf("want nil dynamics, got %+v", got.Dynamics)
	}
	if got.Workstreams["branch"].Value != "main" {
		t.Errorf("workstreams half lost: %+v", got.Workstreams)
	}
}
