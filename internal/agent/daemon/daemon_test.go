package daemon

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ncx-ai/keld-cli/internal/agent/enrich"
	"github.com/ncx-ai/keld-cli/internal/agent/publish"
	"github.com/ncx-ai/keld-cli/internal/agent/queue"
)

type fakeSender struct {
	mu   sync.Mutex
	sent []publish.Enrichment
}

func (f *fakeSender) Send(e publish.Enrichment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, e)
	return nil
}

func (f *fakeSender) all() []publish.Enrichment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]publish.Enrichment(nil), f.sent...)
}

func TestWorkerEnrichesInlineAndNeverLeaksRaw(t *testing.T) {
	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(q, enrich.NewDeterministic(), fs, "dg@keld.co", false)

	q.Offer(queue.Job{
		Source: "claude_desktop", Scheme: "trace", ID: "T1",
		Inline: "write a function; my key is sk-live-ABCDEF0123456789",
	})

	deadline := time.After(2 * time.Second)
	for {
		if len(fs.all()) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("worker did not publish in time")
		case <-time.After(10 * time.Millisecond):
		}
	}
	q.Close()

	e := fs.all()[0]
	if e.Correlation.ID != "T1" || e.TaskType.Value != "codegen" {
		t.Fatalf("unexpected enrichment: %+v", e)
	}
	if e.Sensitivity.Value != "secrets" {
		t.Fatalf("expected secrets, got %+v", e.Sensitivity)
	}
	for _, s := range e.SensitivitySpans {
		if strings.Contains(s.Masked, "ABCDEF0123456789") || s.Text != "" {
			t.Fatalf("raw secret leaked in span: %+v", s)
		}
	}
}
