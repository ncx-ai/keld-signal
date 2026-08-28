package publish

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/retry"
)

func TestBuildIncludesPromptChars(t *testing.T) {
	e := Build(queue.Job{Source: "claude_code"}, enrich.Profile{}, "who@x.test", false, 71, time.Unix(0, 0))
	if e.PromptChars != 71 {
		t.Fatalf("PromptChars = %d, want 71", e.PromptChars)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"prompt_chars":71`)) {
		t.Fatalf("wire missing prompt_chars: %s", b)
	}
}

func TestBuildOmitsZeroPromptChars(t *testing.T) {
	b, err := json.Marshal(Build(queue.Job{Source: "claude_code"}, enrich.Profile{}, "who@x.test", false, 0, time.Unix(0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("prompt_chars")) {
		t.Fatalf("zero count should be omitted: %s", b)
	}
}

func TestBuildShapeAndNoRawText(t *testing.T) {
	p := enrich.Run("key sk-live-ABCDEF0123456789 and write a function", "claude_code", enrich.Meta{}, enrichtest.NewFake())
	j := queue.Job{Source: "claude_code", Scheme: "prompt_id", ID: "X", SessionID: "S", Origin: "hook", Version: "2.1"}
	e := Build(j, p, "dg@keld.co", false, 0, time.Unix(0, 0).UTC())

	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "sk-live-ABCDEF0123456789") {
		t.Fatalf("raw secret leaked into payload: %s", b)
	}
	if e.Source.ID != "claude_code" || e.Correlation.ID != "X" {
		t.Fatalf("wire shape wrong: %+v", e)
	}
}

func TestSendPostsHeadersAndBody(t *testing.T) {
	var gotToken, gotActor, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("x-keld-ingest-token")
		gotActor = r.Header.Get("x-keld-actor")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	pub := New(srv.URL, func() string { return "tok123" }, "dg@keld.co")
	err := pub.Send(Enrichment{Source: Source{ID: "claude_code"}, Correlation: Correlation{ID: "X"}})
	if err != nil {
		t.Fatal(err)
	}
	if gotToken != "tok123" {
		t.Fatalf("token header wrong: %q", gotToken)
	}
	// x-keld-actor is deprecated: the publisher must never send it.
	if gotActor != "" {
		t.Fatalf("x-keld-actor must not be sent, got %q", gotActor)
	}
	if !strings.Contains(gotBody, `"claude_code"`) {
		t.Fatalf("body missing source: %s", gotBody)
	}
}

func TestSendErrorsOn500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	if err := New(srv.URL, func() string { return "t" }, "a").Send(Enrichment{}); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestSendErrorsAreTypedStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	err := New(srv.URL, func() string { return "t" }, "a").Send(Enrichment{})
	var se *retry.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("expected *retry.StatusError, got %T (%v)", err, err)
	}
	if se.Code != 401 {
		t.Fatalf("StatusError.Code = %d, want 401", se.Code)
	}
}

func TestBuildDropsEntityTextWhenDisabled(t *testing.T) {
	p := enrich.Profile{Entities: []enrich.Entity{{Label: "org", Text: "AcmeCorpSecret", Start: 0, End: 14}}}
	j := queue.Job{Source: "claude_code", Scheme: "prompt_id", ID: "X"}
	b, _ := json.Marshal(Build(j, p, "a", false, 0, time.Unix(0, 0).UTC()))
	if strings.Contains(string(b), "AcmeCorpSecret") {
		t.Fatalf("entity text must be dropped when disabled: %s", b)
	}
}

func TestBuildKeepsEntityTextWhenEnabled(t *testing.T) {
	p := enrich.Profile{Entities: []enrich.Entity{{Label: "language", Text: "golang", Start: 0, End: 6}}}
	j := queue.Job{Source: "claude_code", Scheme: "prompt_id", ID: "X"}
	b, _ := json.Marshal(Build(j, p, "a", true, 0, time.Unix(0, 0).UTC()))
	if !strings.Contains(string(b), "golang") {
		t.Fatalf("entity text should be present when enabled: %s", b)
	}
}

func TestBuildCarriesJobCategoryFields(t *testing.T) {
	// The deterministic backend abstains on these facets (no keyword priors),
	// so build a Profile literal with known values for all five job-category
	// fields directly rather than relying on enrich.Run — this asserts the
	// Build mapping specifically and deterministically, independent of the
	// classification backend's behavior.
	p := enrich.Profile{
		Activity:      enrich.Labeled{Value: "generate", Confidence: 0.9},
		Personal:      enrich.Labeled{Value: "work", Confidence: 0.9},
		FunctionGuess: enrich.Labeled{Value: "eng", Confidence: 0.9},
		Subcategory:   enrich.Labeled{Value: "eng.dev", Confidence: 0.9},
		SubcategoryAlt: []enrich.Labeled{
			{Value: "eng.test", Confidence: 0.4},
		},
	}
	e := Build(queue.Job{Source: "claude_code"}, p, "a@b.test", false, 0, time.Now())

	if e.Activity != p.Activity {
		t.Errorf("Activity = %+v, want %+v", e.Activity, p.Activity)
	}
	if e.Personal != p.Personal {
		t.Errorf("Personal = %+v, want %+v", e.Personal, p.Personal)
	}
	if e.FunctionGuess != p.FunctionGuess {
		t.Errorf("FunctionGuess = %+v, want %+v", e.FunctionGuess, p.FunctionGuess)
	}
	if e.Subcategory != p.Subcategory {
		t.Errorf("Subcategory = %+v, want %+v", e.Subcategory, p.Subcategory)
	}
	if len(e.SubcategoryAlt) != 1 || e.SubcategoryAlt[0] != p.SubcategoryAlt[0] {
		t.Errorf("SubcategoryAlt = %+v, want %+v", e.SubcategoryAlt, p.SubcategoryAlt)
	}
}

func TestBuildCarriesWorkstreamsAndNoWindowMetadata(t *testing.T) {
	// A REALISTIC profile: the payload legitimately contains "session_id" and
	// "sensitivity_spans", so a guard on the substrings "session"/"span" would
	// only be testing that the fixture left them empty.
	p := enrich.Profile{
		Workstreams: map[string]enrich.Labeled{
			"project": {Value: "keld-signal", Confidence: 0.812, Producer: "workstreams-v6"},
		},
		SensitivitySpans: []enrich.Entity{{Label: "api_key", Start: 4, End: 24, Confidence: 1, Masked: "[REDACTED:api_key]"}},
	}
	e := Build(queue.Job{Source: "claude_code", SessionID: "453451c2-ab12"}, p, "dg@keld.co", false, 0, time.Unix(0, 0))
	if e.Workstreams["project"].Value != "keld-signal" {
		t.Fatalf("dimension not copied onto the wire shape: %+v", e.Workstreams)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"workstreams":{"project":{"value":"keld-signal","confidence":0.812,"producer":"workstreams-v6"}}`) {
		t.Errorf("dimension missing from payload: %s", s)
	}
	// The analysis's own window metadata and text-derived inventory must not be
	// reachable from here: Profile has no field for them by construction. These
	// tokens appear nowhere in a legitimate payload, so the guard fails only if
	// something actually starts forwarding them.
	for _, forbidden := range []string{"window_start", "window_end", "inventory", "named_terms", "prompt_text"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("payload leaked %q: %s", forbidden, s)
		}
	}
}

// The dynamics block on the wire. Both halves ship: the STATED READING and the
// three numbers it was computed from.
//
// That choice is measured, not assumed. `~/keld/refseries-context/experiment/`
// scored three arms on the same windows: a 16 KB window characterisation of raw
// numbers came in at -3.3/-20.0 on synthesis accuracy — WORSE than emitting
// nothing — while a digest scored +36.7. The digest was not number-free (it
// carries "742 recorded tool references", "100%", "x26.2 usual"); what it did was
// LABEL each number and state the conclusion, and every one of the 14
// full-document failures was the tempo question, where the reader was handed
// `engineer_messages: 5` / `assistant_messages: 84` and left to divide. So the
// arm that won is conclusion + labelled numbers, which is exactly this shape: a
// closed-vocabulary `reading` plus keyed `turnover`/`decay`/
// `concentration_shift` (in JSON the key IS the label). What does NOT ship is the
// unlabelled remainder of the sidecar's block — the per-side value/share/
// evidence/reason, the timestamps, the sizer detail — which is the part that made
// the losing arm 16 KB.
func TestBuildCarriesTheDynamicsReadingAndItsNumbers(t *testing.T) {
	changed, turnover, decay, shift := false, 0.42, 0.0, -0.31
	p := enrich.Profile{
		Workstreams: map[string]enrich.Labeled{
			"branch": {Value: "feat/ledger", Confidence: 1, Producer: "workstreams-v8"},
		},
		Dynamics: map[string]enrich.Dynamic{
			"branch": {Status: "compared", Reading: "widening", Changed: &changed,
				Turnover: &turnover, Decay: &decay, ConcentrationShift: &shift},
			"skill": {Status: "both_absent", Changed: &changed},
		},
	}
	e := Build(queue.Job{Source: "claude_code"}, p, "dg@keld.co", false, 0, time.Unix(0, 0))
	if e.Dynamics["branch"].Reading != "widening" {
		t.Fatalf("dynamics not copied onto the wire shape: %+v", e.Dynamics)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"branch":{"status":"compared","reading":"widening","changed":false,"turnover":0.42,"decay":0,"concentration_shift":-0.31}`) {
		t.Errorf("the reading and its numbers are not both on the wire: %s", s)
	}
	// decay=0.0 is a MEASUREMENT ("nothing left this window"), not an absence, so
	// it must survive omitempty — which is why the metrics are pointers.
	if !strings.Contains(s, `"decay":0`) {
		t.Errorf("a measured zero was omitted, which reads as `not compared`: %s", s)
	}
	// Outside `compared` the metrics are null and the reading is unstated; the
	// three-state `changed` still says False, because a level that never fired
	// did not change.
	if !strings.Contains(s, `"skill":{"status":"both_absent","changed":false}`) {
		t.Errorf("an uncompared dimension published metrics or a reading: %s", s)
	}
}

// A dimension whose `changed` is UNKNOWN (slice_absent: a quiet slice and an
// abandoned dimension are indistinguishable) must publish no `changed` at all —
// not false, which reads as "we checked, nothing moved".
func TestBuildKeepsAnUnknownChangedUnstated(t *testing.T) {
	p := enrich.Profile{Dynamics: map[string]enrich.Dynamic{
		"language": {Status: "slice_absent"},
	}}
	b, err := json.Marshal(Build(queue.Job{}, p, "", false, 0, time.Unix(0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"changed"`) {
		t.Errorf("an unknown `changed` was published as a decision: %s", b)
	}
	if !strings.Contains(string(b), `"language":{"status":"slice_absent"}`) {
		t.Errorf("the status must still be stated, so the silence is readable: %s", b)
	}
}

func TestBuildOmitsAbsentDynamics(t *testing.T) {
	b, err := json.Marshal(Build(queue.Job{Source: "claude_code"}, enrich.Profile{}, "dg@keld.co", false, 0, time.Unix(0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "dynamics") {
		t.Fatalf("no comparison must mean an absent key, not an empty object: %s", b)
	}
}

func TestBuildOmitsAbsentWorkstreams(t *testing.T) {
	b, err := json.Marshal(Build(queue.Job{Source: "claude_code"}, enrich.Profile{}, "dg@keld.co", false, 0, time.Unix(0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "workstreams") {
		t.Fatalf("no dimensions must mean an absent key, not an empty object: %s", b)
	}
}

// TestBuildCarriesFacetsSkipped: facets_skipped is the companion that keeps
// pipeline_status honest, so it has to reach Atlas with it. Without it, a
// deterministic-mode profile stops saying "partial" and says nothing at all
// about the seven facets it does not carry — a silently thinner payload, which
// is the same defect one level up (AGENTS.md: dropping must be VISIBLE).
func TestBuildCarriesFacetsSkipped(t *testing.T) {
	p := enrich.Profile{PipelineStatus: "enriched", FacetsSkipped: []string{"task_type", "domain_entities"}}
	e := Build(queue.Job{}, p, "", false, 0, time.Now())
	if len(e.FacetsSkipped) != 2 || e.FacetsSkipped[0] != "task_type" || e.FacetsSkipped[1] != "domain_entities" {
		t.Fatalf("facets_skipped = %v, want the profile's", e.FacetsSkipped)
	}

	b, err := json.Marshal(Build(queue.Job{}, enrich.Profile{PipelineStatus: "enriched"}, "", false, 0, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("facets_skipped")) {
		t.Fatalf("nothing was skipped; the key must be absent: %s", b)
	}
}

// TestBuildCarriesFacetsDegraded: a pass that RAN but with half its evidence
// unavailable (sensitivity in deterministic mode: credential layer yes, NER no)
// is not skipped, so facets_skipped cannot carry it — and a sensitivity of
// "none" from a half-blind pass is a confident negative nobody checked. The
// qualification only helps if it reaches Atlas with the value it qualifies.
func TestBuildCarriesFacetsDegraded(t *testing.T) {
	p := enrich.Profile{PipelineStatus: "enriched", FacetsDegraded: []string{"sensitivity"}}
	e := Build(queue.Job{}, p, "", false, 0, time.Now())
	if len(e.FacetsDegraded) != 1 || e.FacetsDegraded[0] != "sensitivity" {
		t.Fatalf("facets_degraded = %v, want the profile's", e.FacetsDegraded)
	}

	b, err := json.Marshal(Build(queue.Job{}, enrich.Profile{PipelineStatus: "enriched"}, "", false, 0, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("facets_degraded")) {
		t.Fatalf("nothing was degraded; the key must be absent: %s", b)
	}
}

// TestBuildNeverCarriesRawPII pins the privacy invariant across the PII scan
// layer: the sidecar finds ssn/credit_card/email with no model involved and
// returns OFFSETS ONLY, so the Go side is the sole place the value is ever
// resolved — and those spans must reach the wire masked, exactly like the NER's
// and creddetect's. Fixtures are synthetic — a Luhn-valid card under a real IIN
// with an invented body, a structurally valid SSN on no example list, and an
// invented address at a domain that is not RFC-reserved (reserved ones are
// excluded by design, so they could not prove anything here).
func TestBuildNeverCarriesRawPII(t *testing.T) {
	const (
		card  = "4539871234567895"
		ssn   = "321-54-9876"
		email = "dana.rivers@northwind-logistics.co.uk"
	)
	text := "charge " + card + ", ssn " + ssn + ", receipt to " + email

	// nil Model: deterministic mode, where the scan is the only PII source.
	p := enrich.Run(text, "claude_code", enrich.Meta{}, nil,
		enrich.WithPIIScanner(enrichtest.NewScan()))
	if p.Sensitivity.Value != "phi" {
		t.Fatalf("premise: sensitivity = %+v, want phi", p.Sensitivity)
	}
	if len(p.SensitivitySpans) != 3 {
		t.Fatalf("premise: spans = %+v, want three", p.SensitivitySpans)
	}

	b, err := json.Marshal(Build(queue.Job{Source: "claude_code"}, p, "who@x.test", false, len(text), time.Unix(0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{card, ssn, email, "dana.rivers", "4539 8712"} {
		if bytes.Contains(b, []byte(raw)) {
			t.Fatalf("published payload contains raw PII %q: %s", raw, b)
		}
	}
	if !bytes.Contains(b, []byte(`"label":"ssn"`)) {
		t.Fatalf("payload lost the masked ssn span: %s", b)
	}
}

// TestBuildPublishesNoSpeechAct pins the v9 removal at the WIRE. speech_act was
// a published field; dropping it is a published-vocabulary change (schema v8 →
// v9) and the only surface that matters to a consumer is the JSON.
func TestBuildPublishesNoSpeechAct(t *testing.T) {
	e := Build(queue.Job{Source: "claude_code"}, enrich.Profile{SchemaVersion: enrich.SchemaVersion}, "a@b.test", false, 0, time.Now())
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "speech_act") {
		t.Fatalf("published enrichment still carries speech_act: %s", b)
	}
}

// Build carries the effort block through to the wire. Named separately from the
// dynamics/workstreams cases because it is a POINTER: a nil-vs-zeroed mistake
// here publishes `authored_bytes` absent and every count 0, which reads as "the
// window authored nothing" rather than "nobody looked".
func TestBuildCarriesTheEffortBlock(t *testing.T) {
	bytesAuthored, share := int64(6520), 0.542
	p := enrich.Profile{
		Effort: &enrich.Effort{
			AuthoredBytes: &bytesAuthored, AuthoringTurns: 3, AuthoredStatus: "attributed",
			FastShare: &share, Gaps: 41, Tempo: "steered", TempoStatus: "attributed",
		},
	}
	e := Build(queue.Job{ID: "j1"}, p, "actor", false, 0, time.Now())
	if e.Effort == nil {
		t.Fatal("effort dropped by Build")
	}
	if e.Effort.AuthoredBytes == nil || *e.Effort.AuthoredBytes != 6520 ||
		e.Effort.AuthoringTurns != 3 {
		t.Errorf("diff magnitude mangled: %+v", e.Effort)
	}
	if e.Effort.Tempo != "steered" || e.Effort.FastShare == nil || *e.Effort.FastShare != 0.542 {
		t.Errorf("tempo mangled: %+v", e.Effort)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"effort"`, `"authored_bytes":6520`, `"authoring_turns":3`,
		`"fast_share":0.542`, `"gaps":41`, `"tempo":"steered"`, `"tempo_status":"attributed"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("wire missing %s: %s", want, b)
		}
	}
}

// A profile with no effort block publishes no key at all — never an object whose
// counts are 0 and whose statuses are "".
func TestBuildOmitsAnAbsentEffortBlock(t *testing.T) {
	e := Build(queue.Job{ID: "j1"}, enrich.Profile{}, "actor", false, 0, time.Now())
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"effort"`) {
		t.Errorf("an absent effort block was published: %s", b)
	}
}

// The PHYSICAL ACTS inventory reaches the wire. It is the one inventory dimension
// of /analyze that publishes: `action` is derived from tool NAMES and shell argv
// against a closed 22-value table, never from message text, which is exactly what
// keeps `named_terms` on-device (see enrich.Act and the sidecar's
// workstreams.payload docstring).
func TestBuildCarriesThePhysicalActsInventory(t *testing.T) {
	p := enrich.Profile{PhysicalActs: []enrich.Act{
		{Value: "read", N: 41}, {Value: "edit", N: 12}, {Value: "run a service", N: 2},
	}}
	e := Build(queue.Job{ID: "j1"}, p, "actor", false, 0, time.Now())
	if len(e.PhysicalActs) != 3 {
		t.Fatalf("acts inventory dropped by Build: %+v", e.PhysicalActs)
	}
	if e.PhysicalActs[0].Value != "read" || e.PhysicalActs[0].N != 41 {
		t.Errorf("acts mangled: %+v", e.PhysicalActs)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	// PLURAL on the wire, with counts and no shares: an inventory makes no
	// dominance claim, which is the measured reason this is not an eighth
	// workstream (coverage 0.185 as one; see enrich.Acts).
	for _, want := range []string{`"physical_acts"`, `{"value":"read","n":41}`,
		`{"value":"edit","n":12}`, `{"value":"run a service","n":2}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("wire missing %s: %s", want, b)
		}
	}
	// Scoped to the acts array, since `confidence` legitimately appears on every
	// Labeled facet beside it. An entry is a COUNT and nothing else: there is no
	// denominator to divide by, because the acts do not partition the hour.
	acts, err := json.Marshal(e.PhysicalActs)
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{`"share"`, `"confidence"`, `"evidence"`, `"provenance"`} {
		if strings.Contains(string(acts), unwanted) {
			t.Errorf("an inventory entry must carry no %s: %s", unwanted, acts)
		}
	}
}

// The SESSION PRIOR reaches the wire, and reaches it BESIDE the window's own
// answer rather than inside it. This is the whole design expressed at the last
// place it could be broken: a consumer reading this row sees `workstreams` with
// no `skill` — the window could not attribute one — and `prior.skill`
// naming what the session was doing. The two must never be merged.
func TestBuildCarriesTheSessionPriorWithoutFillingInTheWindow(t *testing.T) {
	no, dep, yes := false, 0.516, true
	p := enrich.Profile{
		Workstreams: map[string]enrich.Labeled{"language": {Value: "Python", Confidence: 0.571}},
		Prior: map[string]enrich.Prior{
			"language": {Value: "TypeScript", Share: 0.886, Evidence: 271,
				Status: "attributed", Agrees: &no, Departure: &dep, Novel: &no},
			// The window has NO skill. The prior has one, and it stays here.
			"skill": {Value: "superpowers:brainstorming", Share: 1.0, Evidence: 38,
				Status: "attributed", Novel: &yes},
			// A session's first window: absent, and nothing invented for it.
			"branch": {Status: "absent"},
		},
	}
	e := Build(queue.Job{ID: "j1"}, p, "actor", false, 0, time.Now())
	if len(e.Prior) != 3 {
		t.Fatalf("prior dropped by Build: %+v", e.Prior)
	}
	if _, present := e.Workstreams["skill"]; present {
		t.Errorf("the window's skill was filled in from the session: %+v — an "+
			"unattributed window stays unattributed", e.Workstreams)
	}
	if e.Workstreams["language"].Value != "Python" {
		t.Errorf("the window's own answer was overwritten by its session: %+v", e.Workstreams)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"prior"`, `"value":"TypeScript"`, `"departure":0.516`,
		`"agrees":false`, `"novel":true`, `"status":"absent"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("wire missing %s: %s", want, b)
		}
	}
	// A session-first window states its absence and INVENTS NOTHING. If Agrees,
	// Departure and Novel were plain bool/float64 they would marshal here as
	// false/0/false — "we compared this window to its session and it matched" —
	// about a comparison nobody could make.
	one, err := json.Marshal(e.Prior["branch"])
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{`"agrees"`, `"departure"`, `"novel"`, `"value"`} {
		if strings.Contains(string(one), unwanted) {
			t.Errorf("an absent prior stated %s: %s", unwanted, one)
		}
	}
}

// Absent, not an empty object. `"prior":{}` would read as "we looked at the
// session and it said nothing", which is a different fact from a sidecar too old
// to have looked — the same distinction workstreams, dynamics and physical_acts
// already keep.
func TestBuildOmitsAnEmptySessionPrior(t *testing.T) {
	for name, p := range map[string]enrich.Profile{
		"nil":   {},
		"empty": {Prior: map[string]enrich.Prior{}},
	} {
		e := Build(queue.Job{ID: "j1"}, p, "actor", false, 0, time.Now())
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"prior"`) {
			t.Errorf("%s: an empty prior was published: %s", name, b)
		}
	}
}

// Absent, not an empty list. `"physical_acts":[]` would read as "we analysed the
// hour and it did nothing" — the same distinction the workstreams and dynamics
// keys already keep.
func TestBuildOmitsAnEmptyPhysicalActsInventory(t *testing.T) {
	for name, p := range map[string]enrich.Profile{
		"nil":   {},
		"empty": {PhysicalActs: []enrich.Act{}},
	} {
		e := Build(queue.Job{ID: "j1"}, p, "actor", false, 0, time.Now())
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"physical_acts"`) {
			t.Errorf("%s: an empty acts inventory was published: %s", name, b)
		}
	}
}

// The three FILE-PATH inventories, and the cut-visibility map beside them,
// reach the wire the same way PhysicalActs does — same call, same shape, but
// an OPEN vocabulary (a file path is not a member of a closed table): what
// makes publishing them acceptable is `reconcile()`'s measured
// workspace-relative guarantee, not a lookup (see enrich.PathCount).
func TestBuildCarriesTheFilePathInventoriesAndInventoryOmitted(t *testing.T) {
	p := enrich.Profile{
		Files:            []enrich.PathCount{{Value: "internal/agent/daemon/daemon.go", N: 5}},
		Directories:      []enrich.PathCount{{Value: "internal/agent/daemon", N: 5}},
		Components:       []enrich.PathCount{{Value: "internal/agent/daemon", N: 5}},
		InventoryOmitted: map[string]int{"files": 5},
	}
	e := Build(queue.Job{ID: "j1"}, p, "actor", false, 0, time.Now())
	if len(e.Files) != 1 || len(e.Directories) != 1 || len(e.Components) != 1 {
		t.Fatalf("path inventories dropped by Build: %+v", e)
	}
	if len(e.InventoryOmitted) != 1 || e.InventoryOmitted["files"] != 5 {
		t.Fatalf("inventory_omitted dropped by Build: %+v", e.InventoryOmitted)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"files":[{"value":"internal/agent/daemon/daemon.go","n":5}]`,
		`"directories":[{"value":"internal/agent/daemon","n":5}]`,
		`"components":[{"value":"internal/agent/daemon","n":5}]`,
		`"inventory_omitted":{"files":5}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("wire missing %s: %s", want, b)
		}
	}
}

// Absent, not an empty list/map — same rule PhysicalActs already keeps.
func TestBuildOmitsEmptyFilePathInventoriesAndInventoryOmitted(t *testing.T) {
	for name, p := range map[string]enrich.Profile{
		"nil": {},
		"empty": {
			Files: []enrich.PathCount{}, Directories: []enrich.PathCount{},
			Components: []enrich.PathCount{}, InventoryOmitted: map[string]int{},
		},
	} {
		e := Build(queue.Job{ID: "j1"}, p, "actor", false, 0, time.Now())
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		for _, unwanted := range []string{`"files"`, `"directories"`, `"components"`, `"inventory_omitted"`} {
			if strings.Contains(string(b), unwanted) {
				t.Errorf("%s: an empty %s was published: %s", name, unwanted, b)
			}
		}
	}
}

// The four IDENTIFIER-shaped inventories reach the wire the same way the
// path inventories do — same call, same shape, but each gated per entry by its
// own structural rule at the sidecar decode boundary rather than a lookup
// table (see enrich.NameCount).
func TestBuildCarriesTheIdentifierInventories(t *testing.T) {
	p := enrich.Profile{
		HarnessTools:    []enrich.NameCount{{Value: "Bash", N: 30}},
		Programs:        []enrich.NameCount{{Value: "git", N: 9}},
		ExternalSystems: []enrich.NameCount{{Value: "github.com", N: 4}},
		Integrations:    []enrich.NameCount{{Value: "notion-fetch", N: 1}},
		NamedTerms:      []enrich.NameCount{{Value: "Federico", N: 2}},
	}
	e := Build(queue.Job{ID: "j1"}, p, "actor", false, 0, time.Now())
	if len(e.HarnessTools) != 1 || len(e.Programs) != 1 || len(e.ExternalSystems) != 1 ||
		len(e.Integrations) != 1 || len(e.NamedTerms) != 1 {
		t.Fatalf("identifier inventories dropped by Build: %+v", e)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"harness_tools":[{"value":"Bash","n":30}]`,
		`"programs":[{"value":"git","n":9}]`,
		`"external_systems":[{"value":"github.com","n":4}]`,
		`"integrations":[{"value":"notion-fetch","n":1}]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("wire missing %s: %s", want, b)
		}
	}
}

// Absent, not an empty list — same rule the path inventories already keep.
func TestBuildOmitsEmptyIdentifierInventories(t *testing.T) {
	for name, p := range map[string]enrich.Profile{
		"nil": {},
		"empty": {
			HarnessTools: []enrich.NameCount{}, Programs: []enrich.NameCount{},
			ExternalSystems: []enrich.NameCount{}, Integrations: []enrich.NameCount{},
			NamedTerms: []enrich.NameCount{},
		},
	} {
		e := Build(queue.Job{ID: "j1"}, p, "actor", false, 0, time.Now())
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		for _, unwanted := range []string{`"harness_tools"`, `"programs"`, `"external_systems"`, `"integrations"`} {
			if strings.Contains(string(b), unwanted) {
				t.Errorf("%s: an empty %s was published: %s", name, unwanted, b)
			}
		}
	}
}

// The LAST FOUR inventories reach the wire, and their keys are asserted as exact
// JSON so a renamed field fails here rather than silently publishing a key no
// Atlas consumer reads. `shell_verbs` carries a MULTI-WORD value deliberately:
// keeping the subcommand is the whole of its advantage over `programs`, and a
// gate that dropped it would leave this list looking merely sparse.
func TestBuildCarriesTheLastFourInventories(t *testing.T) {
	p := enrich.Profile{
		FileTypes:  []enrich.NameCount{{Value: ".tsx", N: 12}},
		ShellVerbs: []enrich.NameCount{{Value: "git rebase", N: 7}},
		Subagents:  []enrich.NameCount{{Value: "general-purpose", N: 4}},
		McpServers: []enrich.NameCount{{Value: "notion", N: 5}},
	}
	e := Build(queue.Job{ID: "j1"}, p, "actor", false, 0, time.Now())
	if len(e.FileTypes) != 1 || len(e.ShellVerbs) != 1 || len(e.Subagents) != 1 ||
		len(e.McpServers) != 1 {
		t.Fatalf("the last four inventories were dropped by Build: %+v", e)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"file_types":[{"value":".tsx","n":12}]`,
		`"shell_verbs":[{"value":"git rebase","n":7}]`,
		`"subagents":[{"value":"general-purpose","n":4}]`,
		`"mcp_servers":[{"value":"notion","n":5}]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("wire missing %s: %s", want, b)
		}
	}
}

// Absent, not an empty list — same rule every inventory before them keeps.
func TestBuildOmitsTheLastFourInventoriesWhenEmpty(t *testing.T) {
	for name, p := range map[string]enrich.Profile{
		"nil": {},
		"empty": {
			FileTypes: []enrich.NameCount{}, ShellVerbs: []enrich.NameCount{},
			Subagents: []enrich.NameCount{}, McpServers: []enrich.NameCount{},
		},
	} {
		e := Build(queue.Job{ID: "j1"}, p, "actor", false, 0, time.Now())
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		for _, unwanted := range []string{`"file_types"`, `"shell_verbs"`, `"subagents"`,
			`"mcp_servers"`} {
			if strings.Contains(string(b), unwanted) {
				t.Errorf("%s: an empty %s was published: %s", name, unwanted, b)
			}
		}
	}
}

// `repo` reaches the wire through `workstreams` with NO new field, because that
// field is a map — which is the point of it being a series level rather than a
// bespoke block. Pinned so a future "repo needs its own key" change has to
// argue with an existing assertion, and because a map is exactly the shape whose
// contents nothing else in this file checks.
func TestBuildCarriesTheRepoWorkstreamThroughTheMap(t *testing.T) {
	p := enrich.Profile{Workstreams: map[string]enrich.Labeled{
		"repo":    {Value: "github.com/ncx-ai/keld-atlas", Confidence: 1, Producer: "w-v19"},
		"project": {Value: "keld-atlas", Confidence: 1, Producer: "w-v19"},
	}}
	e := Build(queue.Job{ID: "j1"}, p, "actor", false, 0, time.Now())
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"repo":{"value":"github.com/ncx-ai/keld-atlas"`) {
		t.Errorf("the repo workstream did not reach the wire: %s", b)
	}
	// BESIDE `project`, never instead of it: measured 1:1 on the corpus but with
	// strictly LOWER cardinality, because a directory that is not a checkout has
	// no repository identity at all. Replacing one with the other loses a
	// distinction the series can make.
	if !strings.Contains(string(b), `"project":{"value":"keld-atlas"`) {
		t.Errorf("publishing `repo` must not displace `project`: %s", b)
	}
}

// THE POINT OF THE WHOLE CHANGE, asserted at the last place it could be broken:
// a dimension BELOW the evidence floor reaches Atlas, carrying its observation
// count and saying that it is thin.
//
// It used to be deleted — twice over, once sidecar-side as a JSON null and once
// Go-side by an empty-Value skip. Measured over 1,502 blocks x 8 dimensions,
// that discarded 924 of 12,016 dimension-slots (7.7%) that held real evidence,
// 198 of them four observations against a floor of five, and on `toolchain` it
// discarded more slots (172) than it published (138).
//
// ⚠️ Nothing is promoted. `attributed` still means the sidecar's MIN_EVIDENCE (5)
// observations plus the share floor — pinned on the producing side by
// sidecar/app/test_analysis_window.py's
// test_the_payload_never_calls_a_sub_floor_dimension_attributed. What this test
// pins is that the label travels WITH the value, because a thin value published
// without its status renders as a confident one.
func TestBuildPublishesASubFloorWorkstreamWithItsEvidenceAndStatus(t *testing.T) {
	p := enrich.Profile{Workstreams: map[string]enrich.Labeled{
		"toolchain": {Value: "pytest", Confidence: 1, Evidence: 4, Status: "thin",
			Producer: "workstreams-v21"},
		"project": {Value: "keld-signal", Confidence: 0.9, Evidence: 30, Status: "attributed",
			Producer: "workstreams-v21"},
		// A level that never fired at all. It publishes too: 4,650 of those
		// 12,016 slots are genuinely absent, and a reader who cannot tell
		// absence from thinness reads a gap as a weak answer.
		"skill": {Status: "absent", Producer: "workstreams-v21"},
	}}
	e := Build(queue.Job{ID: "j1"}, p, "actor", false, 0, time.Now())
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		`"toolchain":{"value":"pytest","confidence":1,"evidence":4,"status":"thin"`,
		`"project":{"value":"keld-signal","confidence":0.9,"evidence":30,"status":"attributed"`,
		`"skill":{"value":"","confidence":0,"status":"absent"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("wire missing %s:\n%s", want, got)
		}
	}
}

// The other half of the same contract: widening `Labeled` must not have cost the
// ML facets a single byte. They share the type and have neither an observation
// count nor an attribution outcome, so publishing `"evidence":0` or `"status":""`
// beside one would state a measurement nobody took — which is why both fields
// are `omitempty`.
//
// Asserted on the EXACT serialisation of a facet rather than on the absence of a
// substring elsewhere in the payload, so a field added to Labeled in future
// fails here rather than silently appearing on seven facets at once.
func TestAnMLFacetsLabeledPayloadIsUnchangedByTheWorkstreamFields(t *testing.T) {
	l := enrich.Labeled{Value: "code_generation", Confidence: 0.83, Producer: "task_type-v21"}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"value":"code_generation","confidence":0.83,"producer":"task_type-v21"}`
	if string(b) != want {
		t.Errorf("an ML facet's payload changed:\n got %s\nwant %s", b, want)
	}
	// And the whole enrichment, so a facet reached through Build cannot pick the
	// keys up by some other route.
	e := Build(queue.Job{ID: "j1"}, enrich.Profile{TaskType: l}, "actor", false, 0, time.Now())
	eb, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(eb), `"task_type":`+want) {
		t.Errorf("task_type did not reach the wire unchanged: %s", eb)
	}
}
