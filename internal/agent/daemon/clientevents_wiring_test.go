package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
)

// enabledEmitter builds a real clientevents.Emitter with an always-on,
// sample-everything gate, so a call site's Emit is guaranteed to land in the
// ring for Drain() to inspect. No network is involved anywhere in this file.
func enabledEmitter() *clientevents.Emitter {
	e := clientevents.NewEmitter(clientevents.Corr{InstallID: "test-install"}, 16)
	e.SetGate(clientevents.Gate{Enabled: true, MinSeverity: clientevents.SevInfo, SampleRate: 1})
	return e
}

// findEvent returns the first event with the given code, or nil.
func findEvent(events []clientevents.Event, code string) *clientevents.Event {
	for i := range events {
		if events[i].Code == code {
			return &events[i]
		}
	}
	return nil
}

// TestProcessEmitsWorkerPanicWithRedactedError proves the daemon.go process()
// recover-site emits a client event alongside its existing log.Printf.
//
// Note: enrich.Run's own extractors are already panic-isolated per-stage
// (runStage recovers internally — see enrich/pipeline.go), so a panicking
// Model never reaches process()'s recover; that isolation is deliberate
// (one bad extractor shouldn't kill the job). Instead this test panics via
// includeEntityText(), a real call process() makes directly in its own body
// (`publish.Build(j, profile, actor, includeEntityText(), ...)`), which is a
// genuine, non-hacky way to exercise the same recover/emit site any other
// bug in process() itself would hit.
func TestProcessEmitsWorkerPanicWithRedactedError(t *testing.T) {
	emitter := enabledEmitter()
	j := queue.Job{
		Source: "claude_code", Scheme: "trace", ID: "PANIC-1",
		SessionID: "sess-abc", PromptID: "prompt-xyz",
		Inline: "trigger a panic",
	}
	panicGate := func() bool { panic("boom: the panic payload itself") }

	// process recovers internally, so this must not itself panic the test.
	process(context.Background(), j, enrichtest.NewFake(), &fakeSender{}, "actor@keld.co", panicGate, emitter, nil)

	events := emitter.Drain()
	ev := findEvent(events, "worker.panic")
	if ev == nil {
		t.Fatalf("expected a worker.panic event, got %+v", events)
	}
	if ev.Severity != clientevents.SevError {
		t.Fatalf("expected error severity, got %v", ev.Severity)
	}
	if ev.Corr.SessionID != "sess-abc" || ev.Corr.PromptID != "prompt-xyz" {
		t.Fatalf("expected WithJob to stamp session/prompt ids, got %+v", ev.Corr)
	}
	errField, ok := ev.Fields["error"].(string)
	if !ok || errField == "" {
		t.Fatalf("expected a non-empty redacted error field, got %+v", ev.Fields)
	}
	if errField == "boom: the panic payload itself" {
		t.Fatalf("error field must be redacted (RedactError), not the raw panic value: %q", errField)
	}
}

// TestProcessEmitsPublishFailedWithRedactedError proves the publish-failure
// site (daemon.go process(), pub.Send err branch) emits publish.failed at
// error severity with a redacted error field.
func TestProcessEmitsPublishFailedWithRedactedError(t *testing.T) {
	emitter := enabledEmitter()
	j := queue.Job{
		Source: "claude_code", Scheme: "trace", ID: "PUBFAIL-1",
		SessionID: "sess-pub", PromptID: "prompt-pub",
		Inline: "write a function",
	}

	process(context.Background(), j, enrichtest.NewFake(), failingSender{}, "actor@keld.co", func() bool { return false }, emitter, nil)

	events := emitter.Drain()
	ev := findEvent(events, "publish.failed")
	if ev == nil {
		t.Fatalf("expected a publish.failed event, got %+v", events)
	}
	if ev.Severity != clientevents.SevError {
		t.Fatalf("expected error severity, got %v", ev.Severity)
	}
	if ev.Corr.SessionID != "sess-pub" || ev.Corr.PromptID != "prompt-pub" {
		t.Fatalf("expected WithJob to stamp session/prompt ids, got %+v", ev.Corr)
	}
	if _, ok := ev.Fields["error"].(string); !ok {
		t.Fatalf("expected a string error field, got %+v", ev.Fields)
	}
}

// TestWorkerEmitsJobQuarantinedOnExhaustion proves the Worker exhaustion
// branch (daemon.go Worker, ledger.exhausted true) emits job.retry_exhausted
// then job.quarantined (warn, on a successful quarantine write) alongside its
// existing log.Printf, stamped with the job's session/prompt ids.
func TestWorkerEmitsJobQuarantinedOnExhaustion(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_ENRICH_JOB_TIMEOUT", "60ms")
	t.Setenv("KELD_ENRICH_MAX_ATTEMPTS", "1") // exhausted on the very first attempt

	emitter := enabledEmitter()
	bm := blockModel{release: make(chan struct{})}
	defer close(bm.release)

	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(context.Background(), q, bm, fs, "t@keld.co", func() bool { return true }, func() bool { return true }, nil, emitter, nil)

	job := queue.Job{
		Source: "claude_code", Scheme: "trace", ID: "QUAR-1",
		SessionID: "sess-quar", PromptID: "prompt-quar",
		Inline: "write code",
	}
	q.Offer(job)

	// Wait for the job.quarantined client event itself rather than for the
	// quarantine file's existence: the Worker writes the badFile via
	// spool.Quarantine and only emits job.quarantined on the following
	// statement (see daemon.go's quarantine block), so keying the done
	// signal off the file let this test observe the file before the event
	// landed and drain too early, catching only job.retry_exhausted.
	//
	// Drain() empties the ring on every call, so accumulate each batch as we
	// poll -- otherwise an earlier job.retry_exhausted (or job.quarantined
	// itself) drained on one poll iteration would be lost by the next.
	var events []clientevents.Event
	deadline := time.Now().Add(2 * time.Second)
	for {
		events = append(events, emitter.Drain()...)
		if findEvent(events, "job.quarantined") != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job.quarantined event not emitted within deadline, got %+v", events)
		}
		time.Sleep(20 * time.Millisecond)
	}
	q.Close()

	// The event is only emitted after spool.Quarantine's write succeeds, so
	// the badFile must exist by now too.
	badFile := filepath.Join(os.Getenv("KELD_HOME"), "spool", "bad", "QUAR-1.json")
	if _, err := os.Stat(badFile); err != nil {
		t.Fatalf("expected the job to have been quarantined to disk, got: %v", err)
	}

	if findEvent(events, "job.retry_exhausted") == nil {
		t.Fatalf("expected a job.retry_exhausted event, got %+v", events)
	}
	ev := findEvent(events, "job.quarantined")
	if ev == nil {
		t.Fatalf("expected a job.quarantined event, got %+v", events)
	}
	if ev.Severity != clientevents.SevWarn {
		t.Fatalf("expected warn severity on a successful quarantine, got %v", ev.Severity)
	}
	if ev.Corr.SessionID != "sess-quar" || ev.Corr.PromptID != "prompt-quar" {
		t.Fatalf("expected WithJob to stamp session/prompt ids, got %+v", ev.Corr)
	}
}

// failingSender is a Sender whose Send always errors, exercising process()'s
// publish.failed emit site.
type failingSender struct{}

func (failingSender) Send(publish.Enrichment) error { return errSimulatedPublishFailure }

type simulatedPublishError struct{}

func (simulatedPublishError) Error() string { return "simulated publish failure" }

var errSimulatedPublishFailure = simulatedPublishError{}
