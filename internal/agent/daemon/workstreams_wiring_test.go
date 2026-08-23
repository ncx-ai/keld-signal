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

	if ok := process(context.Background(), j, m, sender, "actor@keld.co",
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

func TestAnalyzerForRequiresTheCapability(t *testing.T) {
	if analyzerFor(nil) != nil {
		t.Error("deterministic mode has no Model and no sidecar: no analyzer")
	}
	if analyzerFor(enrichtest.NewFake()) != nil {
		t.Error("a Model without the analysis capability must yield no analyzer")
	}
	// The real client is the production wiring; assert it qualifies (no call made).
	if analyzerFor(sidecar.New("http://127.0.0.1:0", time.Second)) == nil {
		t.Error("the sidecar client must satisfy the window-analysis capability")
	}
}
