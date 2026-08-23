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

func TestBuildCarriesSpeechActFields(t *testing.T) {
	// Verify that SpeechAct and SpeechActAlt are properly mapped through to
	// the wire payload and serialized with the correct JSON keys.
	p := enrich.Profile{
		SpeechAct: enrich.Labeled{Value: "question", Confidence: 0.9},
		SpeechActAlt: []enrich.Labeled{
			{Value: "command", Confidence: 0.5},
			{Value: "statement", Confidence: 0.3},
		},
	}
	e := Build(queue.Job{Source: "claude_code"}, p, "a@b.test", false, 0, time.Now())

	if e.SpeechAct != p.SpeechAct {
		t.Errorf("SpeechAct = %+v, want %+v", e.SpeechAct, p.SpeechAct)
	}
	if len(e.SpeechActAlt) != 2 || e.SpeechActAlt[0] != p.SpeechActAlt[0] {
		t.Errorf("SpeechActAlt = %+v, want %+v", e.SpeechActAlt, p.SpeechActAlt)
	}

	// Verify JSON serialization includes speech_act field with correct value.
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	jsonStr := string(b)
	if !strings.Contains(jsonStr, `"speech_act"`) {
		t.Fatalf("JSON missing speech_act key: %s", b)
	}
	if !strings.Contains(jsonStr, `"question"`) {
		t.Fatalf("JSON missing speech_act value 'question': %s", b)
	}
	if !strings.Contains(jsonStr, `"speech_act_alt"`) {
		t.Fatalf("JSON missing speech_act_alt key: %s", b)
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
