package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
)

// analyzingModel is a Model that also answers window analysis, like the real
// sidecar client (one process serves both /classify and /analyze).
type analyzingModel struct {
	enrich.Model
	path, prompt string
	span         int
}

func (m *analyzingModel) AnalyzeLabeled(path, promptID string, spanMinutes int) (map[string]enrich.Labeled, bool) {
	m.path, m.prompt, m.span = path, promptID, spanMinutes
	return map[string]enrich.Labeled{"project": {Value: "keld-signal", Confidence: 0.8}}, true
}

func TestProcessPublishesWorkstreamsFromTheAnalyzer(t *testing.T) {
	m := &analyzingModel{Model: enrichtest.NewFake()}
	sender := &fakeSender{}
	j := queue.Job{Source: "claude_code", Scheme: "prompt_id", ID: "WS-1",
		TranscriptPath: "/tmp/t.jsonl", PromptID: "p1", Inline: "hello world"}

	if ok := process(context.Background(), j, m, facetsFor(m), sender, "actor@keld.co",
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
}

func TestFacetsForRequiresTheCapability(t *testing.T) {
	if f := facetsFor(nil); f.Analyze != nil || f.ScanPII != nil {
		t.Error("deterministic mode with no sidecar has no Model and no service: no facets")
	}
	if f := facetsFor(enrichtest.NewFake()); f.Analyze != nil || f.ScanPII != nil {
		t.Error("a Model without the service capabilities must yield none of them")
	}
	// The real client is the production wiring; assert it qualifies (no call made).
	f := facetsFor(sidecar.New("http://127.0.0.1:0", time.Second))
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

	if ok := process(context.Background(), j, m, facetsFor(m), sender, "actor@keld.co",
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
