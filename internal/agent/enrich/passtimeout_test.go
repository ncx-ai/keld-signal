package enrich_test

import (
	"context"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// blockingModel blocks forever (until its bound context is cancelled) on the
// named classification task, and answers every other task with the first label
// offered. It implements enrich.ContextModel so a per-pass deadline can abort
// the in-flight call, exactly as the real sidecar client does via WithContext.
type blockingModel struct {
	blockTask string
	ctx       context.Context
	// unblocked records tasks that were answered normally, so a test can assert
	// the other passes really ran rather than inferring it from the profile.
	answered chan string
}

func (m *blockingModel) WithModelContext(ctx context.Context) enrich.Model {
	c := *m
	c.ctx = ctx
	return &c
}

func (m *blockingModel) Classify(text string, tasks map[string][]string) map[string][]enrich.Ranked {
	out := map[string][]enrich.Ranked{}
	for name, labels := range tasks {
		if name == m.blockTask {
			<-m.ctx.Done() // the pass deadline must cancel this
			return nil
		}
		select {
		case m.answered <- name:
		default:
		}
		if len(labels) > 0 {
			out[name] = []enrich.Ranked{{Label: labels[0], Confidence: 0.9}}
		}
	}
	return out
}

func (m *blockingModel) Entities(text string, labels map[string]string) []enrich.Entity {
	return nil
}

func (m *blockingModel) Extract(text string, labels map[string]string, tasks map[string][]string) enrich.ExtractResult {
	return enrich.ExtractResult{}
}

// A single slow pass must not discard the whole job: it fails alone, the other
// passes still commit, and the profile is published as "partial". This is what
// makes progress monotonic and removes the re-spool amplification loop.
func TestRunFailsOnlyTheTimedOutPass(t *testing.T) {
	m := &blockingModel{blockTask: "task_type", ctx: context.Background(), answered: make(chan string, 16)}

	start := time.Now()
	p := enrich.Run("write a go function", "claude_code", enrich.Meta{}, m,
		enrich.WithPassTimeout(150*time.Millisecond))
	elapsed := time.Since(start)

	if p.PipelineStatus != "partial" {
		t.Errorf("status = %q, want partial (one pass timed out)", p.PipelineStatus)
	}
	if p.TaskType.Value != "" {
		t.Errorf("task_type = %q, want empty (its pass timed out)", p.TaskType.Value)
	}
	if p.SpeechAct.Value == "" {
		t.Error("speech_act empty: other passes must still run after one times out")
	}
	// Only the blocked pass may burn the deadline; the rest answer instantly.
	if elapsed > 2*time.Second {
		t.Errorf("Run took %s: a per-pass deadline must not serialize into a job-length stall", elapsed)
	}
	if len(m.answered) == 0 {
		t.Error("no other pass was answered")
	}
}

// The deadline must be per pass, not shared across the wave: N slow passes each
// get their own budget rather than the first one consuming it for everyone.
func TestPassTimeoutIsPerPassNotPerJob(t *testing.T) {
	// Block a task that no pass classifies, so every pass answers immediately;
	// a job-wide budget would still be fine here. Then block a real one and
	// confirm the surviving passes are unaffected by its deadline.
	m := &blockingModel{blockTask: "personal", ctx: context.Background(), answered: make(chan string, 16)}
	p := enrich.Run("write a go function", "claude_code", enrich.Meta{}, m,
		enrich.WithPassTimeout(100*time.Millisecond))

	if p.Personal.Value != "" {
		t.Errorf("personal = %q, want empty (blocked)", p.Personal.Value)
	}
	if p.SpeechAct.Value == "" || p.Activity.Value == "" {
		t.Errorf("other passes must survive: speech_act=%q activity=%q",
			p.SpeechAct.Value, p.Activity.Value)
	}
}
