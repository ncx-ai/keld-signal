package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// analyzingModel is a Model that also answers window analysis, like the real
// sidecar client (one process serves both /classify and /analyze).
type analyzingModel struct {
	enrich.Model
	path, prompt string
	span         int
}

func (m *analyzingModel) AnalyzeLabeled(path, promptID string, spanMinutes int) (enrich.WindowAnalysis, bool) {
	m.path, m.prompt, m.span = path, promptID, spanMinutes
	changed := true
	turnover := 0.35
	return enrich.WindowAnalysis{
		Workstreams: map[string]enrich.Labeled{"project": {Value: "keld-signal", Confidence: 0.8}},
		Dynamics: map[string]enrich.Dynamic{
			"branch": {Status: "compared", Reading: "switched", Changed: &changed, Turnover: &turnover},
		},
	}, true
}

func TestProcessPublishesWorkstreamsFromTheAnalyzer(t *testing.T) {
	m := &analyzingModel{Model: enrichtest.NewFake()}
	sender := &fakeSender{}
	j := queue.Job{Source: "claude_code", Scheme: "prompt_id", ID: "WS-1",
		TranscriptPath: "/tmp/t.jsonl", PromptID: "p1", Inline: "hello world"}

	if ok := process(context.Background(), j, m, facetsFor(m, nil), sender, "actor@keld.co",
		func() bool { return true }, nil, nil, nil); !ok {
		t.Fatal("process did not publish")
	}
	sent := sender.all()
	if len(sent) != 1 {
		t.Fatalf("want 1 publish, got %d", len(sent))
	}
	if sent[0].Workstreams["project"].Value != "keld-signal" {
		t.Fatalf("workstreams not threaded into the published enrichment: %+v", sent[0].Workstreams)
	}
	if m.path != "/tmp/t.jsonl" || m.prompt != "p1" || m.span != enrich.WorkstreamSpanMinutes {
		t.Errorf("job coordinates not threaded: path=%q prompt=%q span=%d", m.path, m.prompt, m.span)
	}
	// The dynamics half of the same /analyze call reaches the wire too — this is
	// the assertion that the block leaves the machine at all.
	if sent[0].Dynamics["branch"].Reading != "switched" {
		t.Fatalf("dynamics not threaded into the published enrichment: %+v", sent[0].Dynamics)
	}
	if to := sent[0].Dynamics["branch"].Turnover; to == nil || *to != 0.35 {
		t.Errorf("the number the reading was computed from was dropped: %+v", sent[0].Dynamics["branch"])
	}
}

// ml_backend "deterministic": NO Model at all, the analysis service wired on its
// own. Dynamics ride the same model-free /analyze call the workstreams facet
// does, so the mode that has no GLiNER2 must still publish them — asserted
// through `process`, not inferred from the wiring.
func TestProcessPublishesDynamicsWithNoModel(t *testing.T) {
	shift := -0.31
	svc := serviceFacets{Analyze: func(path, promptID string, span int) (enrich.WindowAnalysis, bool) {
		return enrich.WindowAnalysis{
			Workstreams: map[string]enrich.Labeled{"branch": {Value: "feat/ledger", Confidence: 1}},
			Dynamics: map[string]enrich.Dynamic{
				"branch": {Status: "compared", Reading: "broadening", ConcentrationShift: &shift},
			},
		}, true
	}}
	sender := &fakeSender{}
	j := queue.Job{Source: "claude_code", Scheme: "prompt_id", ID: "WS-3",
		TranscriptPath: "/tmp/t.jsonl", PromptID: "p1", Inline: "hello world"}

	if ok := process(context.Background(), j, nil, svc, sender, "actor@keld.co",
		func() bool { return true }, nil, nil, nil); !ok {
		t.Fatal("process did not publish")
	}
	sent := sender.all()[0]
	if sent.Dynamics["branch"].Reading != "broadening" {
		t.Fatalf("deterministic mode dropped the dynamics: %+v", sent.Dynamics)
	}
	if sh := sent.Dynamics["branch"].ConcentrationShift; sh == nil || *sh != -0.31 {
		t.Errorf("concentration_shift dropped: %+v", sent.Dynamics["branch"])
	}
}

func TestFacetsForRequiresTheCapability(t *testing.T) {
	if f := facetsFor(nil, nil); f.Analyze != nil || f.ScanPII != nil {
		t.Error("deterministic mode with no sidecar has no Model and no service: no facets")
	}
	if f := facetsFor(enrichtest.NewFake(), nil); f.Analyze != nil || f.ScanPII != nil {
		t.Error("a Model without the service capabilities must yield none of them")
	}
	// The real client is the production wiring; assert it qualifies (no call made).
	f := facetsFor(sidecar.New("http://127.0.0.1:0", time.Second), nil)
	if f.Analyze == nil {
		t.Error("the sidecar client must satisfy the window-analysis capability")
	}
	if f.ScanPII == nil {
		t.Error("the sidecar client must satisfy the PII-scan capability")
	}
}

// A Codex job must not pay for a pass the analysis cannot serve: no sidecar
// round-trip, no workstreams, and — critically, since ml_backend "auto" is what
// nearly every user runs — no downgrade of the published pipeline_status.
func TestProcessSkipsWorkstreamsForCodex(t *testing.T) {
	t.Setenv("KELD_ENRICH_GATE_ENABLED", "false")
	m := &analyzingModel{Model: enrichtest.NewFake()}
	sender := &fakeSender{}
	j := queue.Job{Source: "codex", Scheme: "prompt_id", ID: "WS-2",
		TranscriptPath: "/tmp/t.jsonl", PromptID: "sess-1#3", Inline: "write a function that adds two numbers"}

	if ok := process(context.Background(), j, m, facetsFor(m, nil), sender, "actor@keld.co",
		func() bool { return true }, nil, nil, nil); !ok {
		t.Fatal("process did not publish")
	}
	sent := sender.all()[0]
	if m.path != "" {
		t.Errorf("analysis called for an unreadable source: path=%q", m.path)
	}
	if sent.Workstreams != nil {
		t.Errorf("unexpected workstreams: %+v", sent.Workstreams)
	}
	if sent.PipelineStatus == "partial" {
		t.Errorf("a pass that cannot serve this source must not downgrade it: status=%q versions=%v",
			sent.PipelineStatus, sent.ExtractorVersions)
	}
}

// The org's region tier has to reach the sidecar on EVERY scan, resolved at call
// time rather than captured at wiring time. That is the whole point of the
// local-then-remote shaping: wireEnrichment runs once at startup, the settings
// poll lands minutes later, and a facet bound to the startup value would ignore
// the org until the daemon restarted.
func TestFacetsForResolvesPIIRegionsPerCall(t *testing.T) {
	var got [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Regions []string `json:"regions"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body.Regions)
		w.Write([]byte(`{"spans":[],"truncated":false}`))
	}))
	defer srv.Close()

	live := settings.NewLive(settings.Settings{PIIRegions: []string{"us"}})
	f := facetsFor(sidecar.New(srv.URL, 5*time.Second), live.PIIRegions)
	if f.ScanPII == nil {
		t.Fatal("the sidecar client must satisfy the PII-scan capability")
	}
	f.ScanPII("text")
	live.Apply(&settings.Remote{PIIRegions: &[]string{"uk", "au"}})
	f.ScanPII("text")

	if len(got) != 2 {
		t.Fatalf("got %d scans, want 2", len(got))
	}
	if len(got[0]) != 1 || got[0][0] != "us" {
		t.Fatalf("first scan regions = %v, want [us]", got[0])
	}
	if len(got[1]) != 2 || got[1][0] != "uk" {
		t.Fatalf("second scan regions = %v, want [uk au] — the org's change never reached /pii", got[1])
	}
}

// A nil provider is the test/eval wiring, and it must send no opinion at all so
// the sidecar applies its own default rather than being pinned to the universal
// tier.
func TestFacetsForWithNoRegionProviderSendsNoOpinion(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&raw)
		w.Write([]byte(`{"spans":[],"truncated":false}`))
	}))
	defer srv.Close()

	f := facetsFor(sidecar.New(srv.URL, 5*time.Second), nil)
	f.ScanPII("text")
	if v, ok := raw["regions"]; !ok || v != nil {
		t.Fatalf("regions = %v (present=%v), want null", v, ok)
	}
}
