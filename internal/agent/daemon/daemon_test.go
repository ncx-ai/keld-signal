package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/spool"
)

// sha256Hex returns the hex-encoded SHA256 of b.
func sha256Hex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

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

// TestWorkerEnrichesInlineAndNeverLeaksRaw verifies Worker against a fake
// Model with an always-ready gate (unchanged behaviour from before Task 7).
func TestWorkerEnrichesInlineAndNeverLeaksRaw(t *testing.T) {
	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(context.Background(), q, enrichtest.NewFake(), fs, "dg@keld.co", func() bool { return false }, func() bool { return true }, nil)

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
	if e.Actor != "dg@keld.co" {
		t.Fatalf("actor not propagated: %q", e.Actor)
	}
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

// TestWorkerAlwaysReadyGatePublishesImmediately confirms Worker publishes
// immediately against an always-ready gate (unchanged Worker behaviour;
// ml_backend=off wiring itself is covered by TestWireEnrichmentDisabledWhenMLOff).
func TestWorkerAlwaysReadyGatePublishesImmediately(t *testing.T) {
	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(context.Background(), q, enrichtest.NewFake(), fs, "test@keld.co", func() bool { return false }, func() bool { return true }, nil)

	q.Offer(queue.Job{
		Source: "claude_code", Scheme: "trace", ID: "ML-OFF-1",
		Inline: "refactor this function",
	})

	deadline := time.After(2 * time.Second)
	for {
		if len(fs.all()) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("deterministic worker did not publish in time")
		case <-time.After(10 * time.Millisecond):
		}
	}
	q.Close()

	e := fs.all()[0]
	if e.Correlation.ID != "ML-OFF-1" {
		t.Fatalf("unexpected correlation: %+v", e.Correlation)
	}
}

// TestWorkerGateExitsOnQueueClose confirms that when the queue is closed while
// the worker is blocked on a never-ready gate, the worker returns promptly
// (no goroutine leak).
func TestWorkerGateExitsOnQueueClose(t *testing.T) {
	q := queue.New(10)
	fs := &fakeSender{}

	// Gate that never becomes ready.
	neverReady := func() bool { return false }

	done := make(chan struct{})
	go func() {
		Worker(context.Background(), q, enrichtest.NewFake(), fs, "test@keld.co", func() bool { return false }, neverReady, nil)
		close(done)
	}()

	// Offer a job so the worker pulls it and blocks on the gate.
	q.Offer(queue.Job{
		Source: "claude_code", Scheme: "trace", ID: "GATE-1",
		Inline: "test prompt",
	})

	// Give worker time to pull the job and block.
	time.Sleep(60 * time.Millisecond)

	// Close the queue — the worker must unblock and return.
	q.Close()

	select {
	case <-done:
		// Worker exited as expected.
	case <-time.After(1 * time.Second):
		t.Fatal("worker did not exit after queue closed")
	}

	// Nothing should have been published (gate was never ready).
	if got := len(fs.all()); got != 0 {
		t.Fatalf("expected 0 published, got %d", got)
	}
}

// sidecarStub returns an httptest server that mimics a healthy GLiNER2 sidecar.
// /health -> {"ok":true}
// /extract -> minimal valid ExtractResult JSON
func sidecarStub(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/extract", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := map[string]any{
			"entities": []map[string]any{},
			"results":  map[string]any{},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/entities", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"entities":[]}`))
	})
	mux.HandleFunc("/classify", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":{}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestWorkerWithSidecarStubPublishes sets up a real httptest sidecar stub + a
// Supervisor whose spawn is a harmless "sleep" command and whose health
// function reports the stub as healthy. It asserts that a job Offered to the
// queue is published once the worker gate becomes ready. The sidecar client
// is used directly as the Model — there is no router/deterministic backend to
// fall through to; the gate alone holds the worker until the sidecar is up.
func TestWorkerWithSidecarStubPublishes(t *testing.T) {
	stub := sidecarStub(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Build a sidecar client pointing at the httptest stub.
	client := sidecar.New(stub.URL, 2*time.Second)

	// Supervisor whose spawn is a harmless "sleep 10" and health checks the stub.
	healthFn := func() bool { return client.Healthy(ctx) }
	sup := NewSupervisor(
		func(int) (*exec.Cmd, error) { return exec.Command("sleep", "10"), nil },
		0,
		healthFn,
		5*time.Second,
	)

	go sup.Start(ctx)

	gate := func() bool { return sup.Ready() }

	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(context.Background(), q, client, fs, "sidecar-test@keld.co", func() bool { return false }, gate, nil)

	q.Offer(queue.Job{
		Source: "claude_code", Scheme: "trace", ID: "SC-1",
		Inline: "implement binary search",
	})

	deadline := time.After(5 * time.Second)
	for {
		if len(fs.all()) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("worker with sidecar did not publish in time")
		case <-time.After(20 * time.Millisecond):
		}
	}
	q.Close()

	e := fs.all()[0]
	if e.Correlation.ID != "SC-1" {
		t.Fatalf("unexpected correlation: %+v", e.Correlation)
	}
	if e.Actor != "sidecar-test@keld.co" {
		t.Fatalf("actor not propagated: %q", e.Actor)
	}
}

// fakeFetcherOK is a provision.Fetcher that writes a sentinel file whose SHA
// matches testSentinelSHA into destDir.
type fakeFetcherOK struct{ content []byte }

func (f fakeFetcherOK) Fetch(_ context.Context, dest string) error {
	return os.WriteFile(filepath.Join(dest, "model.safetensors"), f.content, 0o644)
}

// fakeFetcherErr always returns an error, simulating a download failure.
type fakeFetcherErr struct{}

func (fakeFetcherErr) Fetch(context.Context, string) error {
	return errors.New("simulated fetch failure")
}

// preloadModelDir creates a model dir that looks like a valid pre-provisioned
// model to EnsureModel so EnsureModel short-circuits (no fetch required).
func preloadModelDir(t *testing.T, sentinelSHA string) (string, []byte) {
	t.Helper()
	// We can't compute the SHA in advance without writing the file first —
	// instead, use a small known payload and compute its SHA.
	content := []byte("test-model-weights")
	dir := t.TempDir()
	modelDir := filepath.Join(dir, "gliner2-large-v1")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "model.safetensors"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	return modelDir, content
}

// TestMLBackendProvisionSuccessPublishesViaSidecar exercises the mlBackend path
// where provisioning succeeds instantly (model already present) and the sidecar
// stub is healthy. The worker gate should open (via provisionFailed or sup) and
// publish the job via the router.
func TestMLBackendProvisionSuccessPublishesViaSidecar(t *testing.T) {
	stub := sidecarStub(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := sidecar.New(stub.URL, 2*time.Second)
	healthFn := func() bool { return client.Healthy(ctx) }

	sup := NewSupervisor(
		func(int) (*exec.Cmd, error) { return exec.Command("sleep", "10"), nil },
		0,
		healthFn,
		5*time.Second,
	)

	// Build a model dir that EnsureModel will accept as already-provisioned.
	modelDir, modelContent := preloadModelDir(t, "")
	sentinelSHA := sha256Hex(modelContent)

	// Use the mlBackend test seam.
	router, gate := mlBackendWithOpts(ctx, mlBackendOpts{
		sup:      sup,
		client:   client,
		modelDir: modelDir,
		modelSHA: sentinelSHA,
		fetcher:  fakeFetcherOK{content: modelContent},
		healthFn: healthFn,
	})

	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(context.Background(), q, router, fs, "provision-test@keld.co", func() bool { return false }, gate, nil)

	q.Offer(queue.Job{
		Source: "claude_code", Scheme: "trace", ID: "PROV-1",
		Inline: "implement binary search",
	})

	deadline := time.After(5 * time.Second)
	for {
		if len(fs.all()) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("worker with provisioned sidecar did not publish in time")
		case <-time.After(20 * time.Millisecond):
		}
	}
	q.Close()

	e := fs.all()[0]
	if e.Correlation.ID != "PROV-1" {
		t.Fatalf("unexpected correlation: %+v", e.Correlation)
	}
}

// TestMLBackendProvisionFailureDoesNotDegradeToDeterministic asserts the current
// contract: enrichment NEVER silently degrades to the deterministic backend. When
// provisioning fails, the gate stays closed so jobs wait (queue/spool) until the
// sidecar recovers, rather than publishing lower-fidelity deterministic results.
func TestMLBackendProvisionFailureDoesNotDegradeToDeterministic(t *testing.T) {
	unhealthyClient := sidecar.New("http://127.0.0.1:1", 50*time.Millisecond)
	healthFn := func() bool { return false }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sup := NewSupervisor(
		func(int) (*exec.Cmd, error) { return exec.Command("sleep", "10"), nil },
		0,
		healthFn,
		100*time.Millisecond,
	)

	modelDir := filepath.Join(t.TempDir(), "gliner2")

	model, gate := mlBackendWithOpts(ctx, mlBackendOpts{
		sup:      sup,
		client:   unhealthyClient,
		modelDir: modelDir,
		modelSHA: "some-sha",
		fetcher:  fakeFetcherErr{},
		healthFn: healthFn,
	})

	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(context.Background(), q, model, fs, "fail-test@keld.co", func() bool { return false }, gate, nil)

	q.Offer(queue.Job{
		Source: "claude_code", Scheme: "trace", ID: "FAIL-1",
		Inline: "write a function",
	})

	// Let provisioning fail; the gate must stay closed and nothing may publish.
	time.Sleep(1500 * time.Millisecond)
	if gate() {
		t.Fatal("gate must stay closed on provision failure — no deterministic fallback")
	}
	if n := len(fs.all()); n != 0 {
		t.Fatalf("enrichment must wait, not degrade: expected 0 publishes, got %d", n)
	}
	q.Close()
}

// TestRetryLedgerBoundsAttempts pins the re-spool cap policy: a job re-spools
// until it has exhausted maxAttempts, then it must be quarantined (not retried
// forever) — the safety cap that prevents one un-enrichable job from looping.
func TestRetryLedgerBoundsAttempts(t *testing.T) {
	r := newRetryLedger()
	// max=3: attempts 1 and 2 re-spool (false), attempt 3 exhausts (true).
	if r.exhausted("k", 3) {
		t.Fatal("attempt 1 should re-spool, not quarantine")
	}
	if r.exhausted("k", 3) {
		t.Fatal("attempt 2 should re-spool, not quarantine")
	}
	if !r.exhausted("k", 3) {
		t.Fatal("attempt 3 should exhaust the budget → quarantine")
	}
	// Exhaustion clears the counter so a freshly delivered job starts over.
	if r.exhausted("k", 3) {
		t.Fatal("after exhaustion the count resets; next delivery re-spools again")
	}
	// A success (reset) also clears the counter.
	r.exhausted("k2", 3)
	r.reset("k2")
	if r.exhausted("k2", 3) {
		t.Fatal("after reset, next attempt is attempt 1 → re-spool")
	}
}

// blockModel simulates a sidecar that never answers (client waiting through a
// reload/outage): every call blocks until release is closed.
type blockModel struct{ release chan struct{} }

func (b blockModel) Classify(string, map[string][]string) map[string][]enrich.Ranked {
	<-b.release
	return nil
}
func (b blockModel) Entities(string, map[string]string) []enrich.Entity { <-b.release; return nil }
func (b blockModel) Extract(string, map[string]string, map[string][]string) enrich.ExtractResult {
	<-b.release
	return enrich.ExtractResult{}
}

// TestWorkerTimesOutAndRespools: a job whose model call hangs must not wedge the
// worker — it times out, re-spools the pointer for retry, and the worker moves on.
func TestWorkerTimesOutAndRespools(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_ENRICH_JOB_TIMEOUT", "150ms")

	bm := blockModel{release: make(chan struct{})}
	defer close(bm.release) // unblock the abandoned goroutine at teardown

	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(context.Background(), q, bm, fs, "t@keld.co", func() bool { return true }, func() bool { return true }, nil)

	q.Offer(queue.Job{Source: "claude_code", Scheme: "trace", ID: "SLOW-1", Inline: "write code"})

	// Within a few timeouts the job must be re-spooled (not wedged, not published).
	deadline := time.After(3 * time.Second)
	for {
		n, _ := spool.Drain(func(p spool.Pointer) error { return nil })
		if n >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed-out job was not re-spooled — worker likely wedged")
		case <-time.After(50 * time.Millisecond):
		}
	}
	if len(fs.all()) != 0 {
		t.Fatalf("a hung job must not publish; got %d", len(fs.all()))
	}
	q.Close()
}

// TestWorkerQuarantinesAfterMaxAttempts: a job that keeps exceeding its deadline
// must NOT re-spool forever (the amplification that saturated the sidecar) — after
// maxAttempts it is quarantined to spool/bad/ and never retried again.
func TestWorkerQuarantinesAfterMaxAttempts(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_ENRICH_JOB_TIMEOUT", "80ms")
	t.Setenv("KELD_ENRICH_MAX_ATTEMPTS", "2")

	bm := blockModel{release: make(chan struct{})}
	defer close(bm.release)

	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(context.Background(), q, bm, fs, "t@keld.co", func() bool { return true }, func() bool { return true }, nil)

	// Deliver once, then mirror the daemon's sweep: drain each re-spooled pointer
	// and re-deliver it. With max=2, attempt 1 re-spools and attempt 2 exhausts
	// the budget → quarantine to spool/bad/ (never re-spooled again).
	job := queue.Job{Source: "claude_code", Scheme: "trace", ID: "STUCK-1", Inline: "write code"}
	q.Offer(job)

	badFile := filepath.Join(os.Getenv("KELD_HOME"), "spool", "bad", "STUCK-1.json")
	deadline := time.After(6 * time.Second)
	for {
		if _, err := os.Stat(badFile); err == nil {
			break
		}
		// Sweep: a re-spooled pointer is drained (removing the live file) and
		// re-delivered — exactly what the daemon does periodically.
		spool.Drain(func(spool.Pointer) error { q.Offer(job); return nil })
		select {
		case <-deadline:
			t.Fatal("hung job was never quarantined — re-spool is unbounded")
		case <-time.After(30 * time.Millisecond):
		}
	}
	if len(fs.all()) != 0 {
		t.Fatalf("a hung job must not publish; got %d", len(fs.all()))
	}
	q.Close()
}

// TestWireEnrichmentDisabledWhenMLOff pins the ml_backend="off" contract: no
// enrichment worker is started (enabled=false, model/gate nil) and the
// /enrich handler accepts-and-discards — POSTing a valid pointer returns 202
// but the request never reaches a queue at all (DiscardHandler takes no
// *queue.Queue), so nothing can ever be enqueued or published.
func TestWireEnrichmentDisabledWhenMLOff(t *testing.T) {
	q := queue.New(10)
	set := settings.Settings{MLBackend: "off"}
	handler, model, gate, enabled := wireEnrichment(context.Background(), set, "s3cret", q, nil)

	if enabled {
		t.Fatal("enrichment must be disabled when ml_backend=off")
	}
	if model != nil {
		t.Fatalf("disabled wiring must not produce a model, got %v", model)
	}
	if gate != nil {
		t.Fatal("disabled wiring must not produce a gate")
	}

	body := `{"source":{"id":"claude_code","origin":"hook"},"correlation":{"scheme":"prompt_id","id":"X"},"pointer":{"transcript_path":"/t","prompt_id":"X"}}`
	req := httptest.NewRequest(http.MethodPost, "/enrich", strings.NewReader(body))
	req.Header.Set("x-keld-agent-secret", "s3cret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202", rr.Code)
	}

	// The queue passed in must stay untouched: draining it after Close should
	// report no jobs (ok=false immediately).
	q.Close()
	if _, ok := q.Next(); ok {
		t.Fatal("ml_backend=off must never enqueue a job")
	}
}

// TestWireEnrichmentEnabledStartsRealHandler confirms the ml_backend="auto"
// (default) path still wires the normal ingress.Handler bound to the real
// queue, unchanged from before this purge.
func TestWireEnrichmentEnabledStartsRealHandler(t *testing.T) {
	q := queue.New(10)
	emitter := clientevents.NewEmitter(clientevents.Corr{}, 16)
	emitter.SetGate(clientevents.Gate{Enabled: true, MinSeverity: clientevents.SevInfo, SampleRate: 1})
	set := settings.Settings{MLBackend: "auto"}
	handler, _, gate, enabled := wireEnrichment(context.Background(), set, "s3cret", q, emitter)

	if !enabled {
		t.Fatal("enrichment must be enabled by default (ml_backend=auto)")
	}
	if gate == nil {
		t.Fatal("enabled wiring must produce a gate")
	}

	body := `{"source":{"id":"claude_code","origin":"hook"},"correlation":{"scheme":"prompt_id","id":"X"},"pointer":{"transcript_path":"/t","prompt_id":"X"}}`
	req := httptest.NewRequest(http.MethodPost, "/enrich", strings.NewReader(body))
	req.Header.Set("x-keld-agent-secret", "s3cret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202", rr.Code)
	}
	q.Close()
	if _, ok := q.Next(); !ok {
		t.Fatal("ml_backend=auto must enqueue the job via the real handler")
	}
}

// TestSidecarUnavailableClosedGateNeverPublishes covers mlBackend's shared
// "no sidecar this run" path (missing binary, or port-alloc failure): it must
// return a permanently-closed gate and a model that is never invoked — never
// a deterministic (or any other) fallback publish. Mirrors
// TestMLBackendProvisionFailureDoesNotDegradeToDeterministic, but exercises
// the sidecarUnavailable helper directly (bypassing sidecarBinPath/net.Listen,
// which depend on the host's real filesystem/network state).
func TestSidecarUnavailableClosedGateNeverPublishes(t *testing.T) {
	emitter := clientevents.NewEmitter(clientevents.Corr{}, 16)
	emitter.SetGate(clientevents.Gate{Enabled: true, MinSeverity: clientevents.SevInfo, SampleRate: 1})

	model, gate := sidecarUnavailable(emitter, map[string]any{"reason": "no_sidecar_binary"})

	if model != nil {
		t.Fatalf("sidecarUnavailable must return a nil model (never invoked), got %v", model)
	}
	for i := 0; i < 3; i++ {
		if gate() {
			t.Fatal("gate must stay permanently closed")
		}
	}

	events := emitter.Drain()
	if len(events) != 1 || events[0].Code != "sidecar.unavailable" || events[0].Severity != clientevents.SevWarn {
		t.Fatalf("expected one sidecar.unavailable/warn event, got %+v", events)
	}

	// Drive it through the real Worker like the provisioning-failure test
	// does: nothing may ever be enqueued-and-processed since the gate never
	// opens.
	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(context.Background(), q, model, fs, "unavailable-test@keld.co", func() bool { return false }, gate, nil)

	q.Offer(queue.Job{
		Source: "claude_code", Scheme: "trace", ID: "UNAVAIL-1",
		Inline: "write a function",
	})

	time.Sleep(200 * time.Millisecond)
	if n := len(fs.all()); n != 0 {
		t.Fatalf("closed gate must never publish: got %d", n)
	}
	q.Close()
}
