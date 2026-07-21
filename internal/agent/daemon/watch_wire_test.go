package daemon

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/spool"
)

func TestMetricsEndpoint(t *testing.T) {
	if got := metricsEndpoint("https://atlas.keld.co"); got != "https://atlas.keld.co/v1/metrics" {
		t.Fatalf("bare base: got %s", got)
	}
	if got := metricsEndpoint("https://x/v1/enrichments"); got != "https://x/v1/metrics" {
		t.Fatalf("swap path: got %s", got)
	}
}

func TestWatchOfferEnqueues(t *testing.T) {
	q := queue.New(4)
	offer := func(p spool.Pointer) { q.Offer(ingress.JobFrom(p)) }
	offer(spool.Pointer{
		Source:      spool.Source{ID: "cowork", Origin: "watch"},
		Correlation: spool.Correlation{Scheme: "prompt_id", ID: "P1"},
		Pointer:     &spool.Ptr{TranscriptPath: "/t.jsonl", PromptID: "P1", Cwd: "/c"},
	})
	j, ok := q.Next()
	if !ok || j.Source != "cowork" || j.Origin != "watch" || j.PromptID != "P1" || j.Cwd != "/c" {
		t.Fatalf("unexpected job: %+v ok=%v", j, ok)
	}
}
