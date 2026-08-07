package llmstudy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// chatReply wraps content in the OpenAI chat-completions envelope llama-server uses.
func chatReply(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
	})
	return string(b)
}

const waveOneOK = `{"task_type":"code_generation","domain":"software","activity_type":"generate","personal":"work","function_guess":"eng"}`

func TestClassifyParsesBothWaves(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) == 1 {
			io.WriteString(w, chatReply(waveOneOK))
			return
		}
		io.WriteString(w, chatReply(`{"subcategory":"eng.dev"}`))
	}))
	defer srv.Close()

	got := NewLlama(srv.URL).Classify(mineFixture(t, 8)[1])
	if !got.Valid {
		t.Fatalf("Valid=false, Err=%q", got.Err)
	}
	want := map[Facet]string{
		FacetTaskType: "code_generation", FacetDomain: "software",
		FacetActivity: "generate", FacetPersonal: "work",
		FacetFunction: "eng", FacetSubcategory: "eng.dev",
	}
	for f, v := range want {
		if got.Labels[f] != v {
			t.Errorf("%s = %q, want %q", f, got.Labels[f], v)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("want 2 requests (wave 1 + subcategory), got %d", len(bodies))
	}
	if !strings.Contains(bodies[0], `"json_schema"`) {
		t.Error("wave-1 request did not request constrained decoding")
	}
	if !strings.Contains(bodies[0], `"temperature":0`) {
		t.Error("wave-1 request must be deterministic (temperature 0)")
	}
}

// Constrained decoding should make this impossible, but a silent off-vocabulary
// label would corrupt the study, so it is checked rather than trusted.
func TestClassifyRejectsOffVocabularyLabel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(`{"task_type":"telepathy","domain":"software","activity_type":"generate","personal":"work","function_guess":"eng"}`))
	}))
	defer srv.Close()

	got := NewLlama(srv.URL).Classify(mineFixture(t, 8)[1])
	if got.Valid {
		t.Fatal("an off-vocabulary label must invalidate the answer")
	}
	if !strings.Contains(got.Err, "telepathy") {
		t.Errorf("Err should name the offending label, got %q", got.Err)
	}
}

func TestClassifySurvivesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	got := NewLlama(srv.URL).Classify(mineFixture(t, 8)[1])
	if got.Valid {
		t.Fatal("HTTP 500 must not yield a valid answer")
	}
	if got.Err == "" {
		t.Error("Err must be populated on failure")
	}
	if got.LatencyMS < 0 {
		t.Error("LatencyMS must be recorded even on failure")
	}
}

func TestClassifySurvivesNonJSONContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply("I'm sorry, I can't do that."))
	}))
	defer srv.Close()

	got := NewLlama(srv.URL).Classify(mineFixture(t, 8)[1])
	if got.Valid {
		t.Fatal("prose instead of JSON must not yield a valid answer")
	}
}

// Every one of the 12 functions has subcategories, so Wave 2 ALWAYS runs and the
// LLM arm costs two inferences per window — the figure the latency budget must be
// built on. Pinned here because Classify's schema==nil branch is unreachable while
// this holds, and would become live if a function ever shipped without subcats.
func TestEveryFunctionHasSubcategoriesSoWaveTwoAlwaysRuns(t *testing.T) {
	for _, f := range enrich.Functions {
		if SubcategorySchema(f.ID) == nil {
			t.Errorf("function %q has no subcategories; Classify would skip Wave 2 for it", f.ID)
		}
	}
}

func TestClassifyIssuesTwoCallsPerWindow(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			io.WriteString(w, chatReply(waveOneOK))
			return
		}
		io.WriteString(w, chatReply(`{"subcategory":"eng.dev"}`))
	}))
	defer srv.Close()

	if got := NewLlama(srv.URL).Classify(mineFixture(t, 8)[1]); !got.Valid {
		t.Fatalf("Valid=false, Err=%q", got.Err)
	}
	if calls != 2 {
		t.Fatalf("want exactly 2 inferences per window, got %d", calls)
	}
}

// A Wave-2 failure must not discard the Wave-1 facets that already succeeded —
// the same "one slow pass must not throw away the rest" principle the pipeline's
// per-pass deadlines exist to enforce.
func TestWaveTwoFailureKeepsWaveOneLabels(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			io.WriteString(w, chatReply(waveOneOK))
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	got := NewLlama(srv.URL).Classify(mineFixture(t, 8)[1])
	if got.Labels[FacetDomain] != "software" {
		t.Errorf("wave-1 domain lost on wave-2 failure: %q", got.Labels[FacetDomain])
	}
	// Wave 1 committed, so the answer stays usable — otherwise one subcategory
	// failure would discard five good facets and the differ would skip the row.
	if !got.Valid {
		t.Error("Valid must stay true: wave 1 succeeded and its facets are usable")
	}
	if !got.Partial {
		t.Error("Partial must be set so the missing subcategory is visible")
	}
	if got.Labels[FacetSubcategory] != "" {
		t.Errorf("subcategory must be absent, got %q", got.Labels[FacetSubcategory])
	}
	if got.Err == "" || !strings.Contains(got.Err, "subcategory") {
		t.Errorf("Err should attribute the failure to subcategory, got %q", got.Err)
	}
}
