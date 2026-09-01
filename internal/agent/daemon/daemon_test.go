package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/creds"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/auth"
	"github.com/ncx-ai/keld-signal/internal/retry"
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

// count returns the number of publishes so far (mutexed, safe for polling
// from a test goroutine while Worker runs concurrently).
func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// sampleInlineJob builds a minimal inline queue.Job keyed by id, for tests
// that only care about the worker's wait/defer/publish behavior, not the
// enrichment content itself.
func sampleInlineJob(id string) queue.Job {
	return queue.Job{Source: "claude_code", Scheme: "trace", ID: id, Inline: "write a function"}
}

// TestWorkerEnrichesInlineAndNeverLeaksRaw verifies Worker against a fake
// Model with an always-ready gate (unchanged behaviour from before Task 7).
func TestWorkerEnrichesInlineAndNeverLeaksRaw(t *testing.T) {
	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(context.Background(), q, enrichtest.NewFake(), serviceFacets{}, fs, "dg@keld.co", func() bool { return false }, func() bool { return true }, nil, nil, nil)

	// A real gitleaks-shaped GitHub PAT. The previous `sk-live-...` fixture was
	// detected only by the fake Model's NER, and sensitivity no longer consults
	// a model at all — so it would now publish "none" and the leak assertion
	// below would run over an empty span list. This token is found by the
	// pure-Go credential layer, which is what actually ships.
	q.Offer(queue.Job{
		Source: "claude_desktop", Scheme: "trace", ID: "T1",
		Inline: "write a function; my key is ghp_16C7e42F292c6912E7710c838347Ae178B4a",
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
	if e.Correlation.ID != "T1" || e.TaskType.Value != "code_generation" {
		t.Fatalf("unexpected enrichment: %+v", e)
	}
	if e.Sensitivity.Value != "secrets" {
		t.Fatalf("expected secrets, got %+v", e.Sensitivity)
	}
	if len(e.SensitivitySpans) == 0 {
		t.Fatal("premise: expected a masked credential span, or the leak check below is vacuous")
	}
	for _, s := range e.SensitivitySpans {
		if strings.Contains(s.Masked, "16C7e42F292c6912E7710c838347Ae178B4a") || s.Text != "" {
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
	go Worker(context.Background(), q, enrichtest.NewFake(), serviceFacets{}, fs, "test@keld.co", func() bool { return false }, func() bool { return true }, nil, nil, nil)

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
		Worker(context.Background(), q, enrichtest.NewFake(), serviceFacets{}, fs, "test@keld.co", func() bool { return false }, neverReady, nil, nil, nil)
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
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"worker":{"state":"ready"}}`))
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

	// The per-job gate is model-resident warmth (sidecar /metrics
	// worker.state=="ready"), mirroring mlBackendWithOpts's real wiring —
	// not the supervisor's latched liveness.
	wg := newWarmGate()
	go wg.run(ctx, client.WorkerReady, warmPollInterval)
	gate := wg.Warm

	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(context.Background(), q, client, serviceFacets{}, fs, "sidecar-test@keld.co", func() bool { return false }, gate, nil, nil, nil)

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
	router, _, gate, _ := mlBackendWithOpts(ctx, mlBackendOpts{
		sup:      sup,
		client:   client,
		modelDir: modelDir,
		modelSHA: sentinelSHA,
		fetcher:  fakeFetcherOK{content: modelContent},
		healthFn: healthFn,
	})

	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(context.Background(), q, router, serviceFacets{}, fs, "provision-test@keld.co", func() bool { return false }, gate, nil, nil, nil)

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

	model, _, gate, _ := mlBackendWithOpts(ctx, mlBackendOpts{
		sup:      sup,
		client:   unhealthyClient,
		modelDir: modelDir,
		modelSHA: "some-sha",
		fetcher:  fakeFetcherErr{},
		healthFn: healthFn,
	})

	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(context.Background(), q, model, serviceFacets{}, fs, "fail-test@keld.co", func() bool { return false }, gate, nil, nil, nil)

	q.Offer(queue.Job{
		Source: "claude_code", Scheme: "trace", ID: "FAIL-1",
		Inline: "write a function",
	})

	// gate must never open when provisioning fails (no sidecar → not warm).
	time.Sleep(100 * time.Millisecond)
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
	go Worker(context.Background(), q, bm, serviceFacets{}, fs, "t@keld.co", func() bool { return true }, func() bool { return true }, nil, nil, nil)

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
	go Worker(context.Background(), q, bm, serviceFacets{}, fs, "t@keld.co", func() bool { return true }, func() bool { return true }, nil, nil, nil)

	// Deliver once, then mirror the daemon's sweep: drain each re-spooled pointer
	// and re-deliver it. With max=2, attempt 1 re-spools and attempt 2 exhausts
	// the budget → quarantine to spool/bad/ (never re-spooled again).
	job := queue.Job{Source: "claude_code", Scheme: "trace", ID: "STUCK-1", Inline: "write code"}
	q.Offer(job)

	// Quarantine identity is now (source, scheme, id), not the bare id — glob rather
	// than pinning the exact prefix.
	badGlob := filepath.Join(os.Getenv("KELD_HOME"), "spool", "bad", "*STUCK-1.json")
	deadline := time.After(6 * time.Second)
	for {
		if matches, _ := filepath.Glob(badGlob); len(matches) > 0 {
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
	handler, model, _, gate, warmup, enabled := wireEnrichment(context.Background(), set, "s3cret", q, nil, nil, false)

	if warmup != nil {
		t.Fatal("disabled enrichment must not hand back a warmup: nothing to load, nothing to provision")
	}

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
//
// The sidecar lookup is pointed at nothing on purpose: this test is about the
// handler, and a host that happens to have a sidecar installed would otherwise
// make it spawn the real service (and provision weights) as a side effect.
func TestWireEnrichmentEnabledStartsRealHandler(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KELD_SIDECAR_BIN", filepath.Join(t.TempDir(), "absent"))
	if p, found := sidecarBinPath(); found {
		t.Skipf("host has a sidecar installed at %s; this test is not hermetic here", p)
	}
	q := queue.New(10)
	emitter := clientevents.NewEmitter(clientevents.Corr{}, 16)
	emitter.SetGate(clientevents.Gate{Enabled: true, MinSeverity: clientevents.SevInfo, SampleRate: 1})
	set := settings.Settings{MLBackend: "auto"}
	handler, _, _, gate, _, enabled := wireEnrichment(context.Background(), set, "s3cret", q, emitter, nil, false)

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

// TestWireEnrichmentDeterministicModeRunsWithoutAModel pins the third
// ml_backend mode: enrichment stays ON (real ingress.Handler, a Worker gets
// started) but no Model is wired, unlike "auto" (real model) and "off"
// (enrichment disabled entirely). What the mode does with the analysis
// service, and how its gate behaves, is covered by
// TestDeterministicModeStartsTheServiceAndWiresTheAnalyzer and
// TestDeterministicGateIsClosedUntilTheServiceIsUp; here the sidecar lookup is
// pointed at nothing so the test stays about the handler and the Model.
func TestWireEnrichmentDeterministicModeRunsWithoutAModel(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KELD_SIDECAR_BIN", filepath.Join(t.TempDir(), "absent"))
	if p, found := sidecarBinPath(); found {
		t.Skipf("host has a sidecar installed at %s; this test is not hermetic here", p)
	}
	q := queue.New(10)
	set := settings.Settings{MLBackend: "deterministic"}
	handler, model, _, gate, warmup, enabled := wireEnrichment(context.Background(), set, "s3cret", q, nil, nil, false)

	// No model to load means no warmup — and since provisioning now hangs off
	// warmup, deterministic mode cannot download the 1.9 GB it never uses.
	if warmup != nil {
		t.Fatal("deterministic mode must not hand back a warmup: it would provision a model this mode never asks for")
	}

	if !enabled {
		t.Fatal("enrichment must stay enabled in deterministic mode")
	}
	if model != nil {
		t.Fatalf("deterministic mode must not wire a model, got %v", model)
	}
	// gate must NOT be nil: Worker calls ready() unconditionally on every job
	// (see Worker's loop and waitWarm), so a nil func would panic the Worker
	// goroutine on the first job ever pulled off the queue. Which gate it is
	// depends on whether a service exists — see
	// TestDeterministicModeWithNoSidecarBinaryDoesNotWedge (none installed:
	// trivially true) and TestDeterministicGateIsClosedUntilTheServiceIsUp
	// (present but not yet serving: closed).
	if gate == nil {
		t.Fatal("deterministic mode must wire a non-nil gate (Worker calls it unconditionally)")
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
		t.Fatal("deterministic mode must enqueue the job via the real handler, not discard it")
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

	gate := sidecarUnavailable(emitter, map[string]any{"reason": "no_sidecar_binary"})
	// There is no Model on this path — the gate never opens, so whatever the
	// caller pairs it with (mlBackend a nil Model, deterministic mode a nil
	// analyzer) is never invoked. Worker below is driven with a nil Model to
	// prove exactly that.
	var model enrich.Model
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
	go Worker(context.Background(), q, model, serviceFacets{}, fs, "unavailable-test@keld.co", func() bool { return false }, gate, nil, nil, nil)

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

// authFailSender is a Sender whose Send always returns a 401 *retry.StatusError,
// exercising process()'s publish-401 → reauther.refresh trigger.
type authFailSender struct {
	mu    sync.Mutex
	sends int
}

func (a *authFailSender) Send(publish.Enrichment) error {
	a.mu.Lock()
	a.sends++
	a.mu.Unlock()
	return &retry.StatusError{Code: http.StatusUnauthorized}
}

// newTestReauther builds a reauther wired with injected seams (no real
// network/filesystem auth calls) that counts Onboarding calls and reports
// what token it handed back — the shape the wiring tests below need to
// observe refresh's single-flight/cooldown behavior and the token swap.
func newTestReauther(t *testing.T, tok *creds.Token, newIngestToken string) (ra *reauther, onboardCalls func() int) {
	t.Helper()
	t.Setenv("KELD_HOME", t.TempDir())
	ra = newReauther(tok, nil)
	fixedNow := time.Unix(9000, 0)
	ra.now = func() time.Time { return fixedNow }
	ra.cooldown = time.Minute
	var calls int
	var mu sync.Mutex
	ra.loadAuth = func() (*auth.AuthData, error) {
		return &auth.AuthData{AccessToken: "cli-token", APIURL: "https://atlas.example"}, nil
	}
	ra.onboard = func(apiURL, cliToken string) (*api.Onboarding, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return &api.Onboarding{Endpoint: "https://atlas.example/v1/ingest", IngestToken: newIngestToken}, nil
	}
	ra.save = func(endpoint, token string) error { return nil }
	onboardCalls = func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
	return ra, onboardCalls
}

// TestProcessPublish401TriggersReauthRefreshExactlyOnce is the RED→GREEN test
// for Task 3's wiring: a publish 401 must trigger the reauther's refresh, and
// a successful refresh must live-swap the shared token so the next Send would
// use it. Cooldown/single-flight means two 401s in a row within the window
// still cost exactly one Onboarding call — that guard lives in reauther.refresh
// itself (see reauth_test.go); this test proves process() actually calls it.
func TestProcessPublish401TriggersReauthRefreshExactlyOnce(t *testing.T) {
	tok := creds.NewToken("old-ingest-token")
	ra, onboardCalls := newTestReauther(t, tok, "new-ingest-token")

	sender := &authFailSender{}
	j := queue.Job{Source: "claude_code", Scheme: "trace", ID: "AUTH-1", Inline: "write code"}

	process(context.Background(), j, enrichtest.NewFake(), serviceFacets{}, sender, "actor@keld.co", func() bool { return false }, nil, ra, nil)
	process(context.Background(), j, enrichtest.NewFake(), serviceFacets{}, sender, "actor@keld.co", func() bool { return false }, nil, ra, nil)

	if got := onboardCalls(); got != 1 {
		t.Fatalf("onboard called %d times, want exactly 1 (cooldown-guarded single-flight)", got)
	}
	if sender.sends != 2 {
		t.Fatalf("sender.sends = %d, want 2 (both publish attempts still went out)", sender.sends)
	}
	if got := tok.Get(); got != "new-ingest-token" {
		t.Fatalf("tok.Get() = %q, want new-ingest-token after refresh — a subsequent Send would still use the stale token otherwise", got)
	}
}

// TestProcessPublish401WithNilReautherIsSafe proves process() never panics on
// a publish 401 when no reauther is wired (ra == nil) — the nil-safety choice
// that keeps every pre-existing Worker/process test (which pass nil for ra)
// unaffected by this change.
func TestProcessPublish401WithNilReautherIsSafe(t *testing.T) {
	j := queue.Job{Source: "claude_code", Scheme: "trace", ID: "AUTH-NIL-1", Inline: "write code"}
	process(context.Background(), j, enrichtest.NewFake(), serviceFacets{}, &authFailSender{}, "actor@keld.co", func() bool { return false }, nil, nil, nil)
}

// TestProcessNonAuthPublishErrorDoesNotTriggerRefresh proves a non-401/403
// publish failure (e.g. a 500 or a network error) must NOT call refresh — only
// an auth error is the self-heal trigger; other failures already have their
// own handling (log + publish.failed event) and calling Onboarding for them
// would just churn the CLI token endpoint for no reason.
func TestProcessNonAuthPublishErrorDoesNotTriggerRefresh(t *testing.T) {
	tok := creds.NewToken("old-ingest-token")
	ra, onboardCalls := newTestReauther(t, tok, "new-ingest-token")

	j := queue.Job{Source: "claude_code", Scheme: "trace", ID: "AUTH-500", Inline: "write code"}
	process(context.Background(), j, enrichtest.NewFake(), serviceFacets{}, failingSender{}, "actor@keld.co", func() bool { return false }, nil, ra, nil)

	if got := onboardCalls(); got != 0 {
		t.Fatalf("onboard called %d times, want 0 for a non-auth publish error", got)
	}
	if got := tok.Get(); got != "old-ingest-token" {
		t.Fatalf("tok.Get() = %q, want unchanged old-ingest-token", got)
	}
}

// waitFor polls cond every 2ms until it is true, failing the test if it does
// not become true within d.
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.After(d)
	for !cond() {
		select {
		case <-deadline:
			t.Fatal("condition not met in time")
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// spoolCount reports how many pointers are live in home's spool.db, proving a
// job was preserved (re-spooled) rather than lost. The live spool is a SQLite
// database now, not one file per job, so this drains-and-counts via the public
// API rather than walking spool/*.json — safe here since both call sites check
// this last, with nothing after that depends on the rows still being present.
func spoolCount(t *testing.T, home string) int {
	t.Helper()
	n, _ := spool.Drain(func(spool.Pointer) error { return nil })
	return n
}

// quarantineCount counts .json files under home/spool/bad — the exact
// subtree spool.Quarantine writes to (see internal/spool.Quarantine). Used to
// prove a model-not-ready defer never drives quarantine, no matter how many
// times it re-spools.
func quarantineCount(t *testing.T, home string) int {
	t.Helper()
	n := 0
	filepath.WalkDir(filepath.Join(home, "spool", "bad"), func(_ string, d fs.DirEntry, _ error) error {
		if d != nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".json") {
			n++
		}
		return nil
	})
	return n
}

// A job must WAIT (not burn an attempt) until warm, then publish. With a tiny
// warm-wait and a gate that flips to true, the job should publish exactly once.
func TestWorkerWaitsForWarmThenPublishes(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_ENRICH_WARM_WAIT", "5s")

	var warm atomic.Bool // false until we flip it
	q := queue.New(4)
	fs := &fakeSender{}
	q.Offer(sampleInlineJob("warm-wait-1")) // helper used by existing tests
	go Worker(context.Background(), q, enrichtest.NewFake(), serviceFacets{}, fs, "t@keld.co",
		func() bool { return false }, warm.Load, nil, nil, nil)

	time.Sleep(50 * time.Millisecond) // job pulled, waiting for warm
	if fs.count() != 0 {
		t.Fatal("published before warm")
	}
	warm.Store(true)
	waitFor(t, time.Second, func() bool { return fs.count() == 1 })
	q.Close()
}

// If warmth never arrives within KELD_ENRICH_WARM_WAIT, the job is re-spooled
// (deferred) WITHOUT consuming the retry budget — so it is NEVER quarantined,
// no matter how many times it defers.
func TestWorkerDefersWhenNeverWarmNeverQuarantines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	t.Setenv("KELD_ENRICH_WARM_WAIT", "20ms")
	t.Setenv("KELD_ENRICH_MAX_ATTEMPTS", "2") // low cap: prove defers don't count

	q := queue.New(4)
	fs := &fakeSender{}
	q.Offer(sampleInlineJob("never-warm-1"))
	go Worker(context.Background(), q, enrichtest.NewFake(), serviceFacets{}, fs, "t@keld.co",
		func() bool { return false }, func() bool { return false }, nil, nil, nil)

	// Give it time to defer: the job is deferred exactly once (re-spooled to
	// disk), then the loop pulls from the now-empty queue and blocks until
	// Close — there's no spool sweeper here to re-offer it and defer again.
	time.Sleep(200 * time.Millisecond)
	q.Close()

	if fs.count() != 0 {
		t.Fatalf("nothing should publish while never warm; got %d", fs.count())
	}
	if n := quarantineCount(t, home); n != 0 {
		t.Fatalf("model-not-ready must never quarantine; found %d quarantined", n)
	}
	// A spooled (deferred) pointer should exist — the job was preserved.
	if n := spoolCount(t, home); n == 0 {
		t.Fatal("expected the deferred job to be re-spooled, not lost")
	}
}

// Cold model: Worker must call warmup (which loads the model → ready flips
// true), then process and publish, with the retry ledger untouched.
func TestWorkerWarmupLoadsThenPublishes(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_ENRICH_WARM_WAIT", "5s")
	var warm atomic.Bool // starts false (cold)
	var warmupCalls atomic.Int32
	warmup := func(context.Context) error { warmupCalls.Add(1); warm.Store(true); return nil }

	q := queue.New(4)
	fs := &fakeSender{}
	q.Offer(sampleInlineJob("warmup-1"))
	go Worker(context.Background(), q, enrichtest.NewFake(), serviceFacets{}, fs, "t@keld.co",
		func() bool { return false }, warm.Load, warmup, nil, nil)

	waitFor(t, time.Second, func() bool { return fs.count() == 1 })
	if warmupCalls.Load() != 1 {
		t.Fatalf("warmup calls = %d, want 1", warmupCalls.Load())
	}
	q.Close()
}

// Warmup never makes it ready (returns error): job defers (re-spool) WITHOUT
// consuming the retry budget — never quarantined, even at a low max-attempts.
func TestWorkerWarmupTimesOutDefersNeverQuarantines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	t.Setenv("KELD_ENRICH_WARM_WAIT", "20ms")
	t.Setenv("KELD_ENRICH_MAX_ATTEMPTS", "2")
	warmup := func(context.Context) error { return context.DeadlineExceeded }

	q := queue.New(4)
	fs := &fakeSender{}
	q.Offer(sampleInlineJob("warmup-fail-1"))
	go Worker(context.Background(), q, enrichtest.NewFake(), serviceFacets{}, fs, "t@keld.co",
		func() bool { return false }, func() bool { return false }, warmup, nil, nil)

	time.Sleep(150 * time.Millisecond)
	q.Close()
	if fs.count() != 0 {
		t.Fatalf("nothing should publish; got %d", fs.count())
	}
	if n := quarantineCount(t, home); n != 0 {
		t.Fatalf("model-not-ready must never quarantine; found %d", n)
	}
	if n := spoolCount(t, home); n == 0 {
		t.Fatal("expected the deferred job to be re-spooled")
	}
}

// Already warm: warmup must NOT be called.
func TestWorkerSkipsWarmupWhenReady(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	var warmupCalls atomic.Int32
	warmup := func(context.Context) error { warmupCalls.Add(1); return nil }

	q := queue.New(4)
	fs := &fakeSender{}
	q.Offer(sampleInlineJob("already-warm-1"))
	go Worker(context.Background(), q, enrichtest.NewFake(), serviceFacets{}, fs, "t@keld.co",
		func() bool { return false }, func() bool { return true }, warmup, nil, nil)

	waitFor(t, time.Second, func() bool { return fs.count() == 1 })
	if warmupCalls.Load() != 0 {
		t.Fatalf("warmup must not be called when already ready; calls=%d", warmupCalls.Load())
	}
	q.Close()
}

func TestQueueCapFromEnv(t *testing.T) {
	if got := queueCap(); got != 1024 {
		t.Fatalf("default queue cap = %d, want 1024", got)
	}
	t.Setenv("KELD_QUEUE_CAP", "4096")
	if got := queueCap(); got != 4096 {
		t.Fatalf("env queue cap = %d, want 4096", got)
	}
	t.Setenv("KELD_QUEUE_CAP", "garbage")
	if got := queueCap(); got != 1024 {
		t.Fatalf("garbage should fall back to the default, got %d", got)
	}
}

// TestSidecarServiceBuildsTheServiceWithoutProvisioning pins the split between
// "spawn the analysis service" and "provision the GLiNER2 weights". The
// sidecar is the client-side analysis service in general (/analyze, /match,
// /vocabulary, ...); GLiNER2 is one capability it loads lazily, so building
// the service plumbing must not depend on — or trigger — a model download.
//
// It never starts anything: the supervisor is returned unstarted and the spawn
// closure is only invoked here to inspect the command that WOULD be run.
func TestSidecarServiceBuildsTheServiceWithoutProvisioning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	binPath := filepath.Join(t.TempDir(), "fake-svc-bin")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KELD_SIDECAR_BIN", binPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	emitter := clientevents.NewEmitter(clientevents.Corr{}, 16)
	emitter.SetGate(clientevents.Gate{Enabled: true, MinSeverity: clientevents.SevInfo, SampleRate: 1})

	client, sup, healthFn, ok, err := sidecarService(ctx, emitter, false)
	if !ok || err != nil {
		t.Fatalf("sidecarService: ok=%v err=%v, want true/nil", ok, err)
	}
	if client == nil || sup == nil || healthFn == nil {
		t.Fatalf("sidecarService must return client+supervisor+healthFn, got %v/%v/healthFn nil=%v", client, sup, healthFn == nil)
	}
	if sup.emitter != emitter {
		t.Fatal("the supervisor must carry the emitter (mlBackend's sup.SetEmitter today)")
	}
	if sup.port <= 0 {
		t.Fatalf("supervisor port = %d, want an allocated ephemeral port", sup.port)
	}
	// The client and the health probe must both address the port the
	// supervisor will hand the spawned service. Stand a stub /health on that
	// (just-released) port and prove the probe reaches it.
	ln, lerr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", sup.port))
	if lerr != nil {
		t.Fatalf("re-binding the allocated sidecar port: %v", lerr)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	if !healthFn() {
		t.Fatal("healthFn must probe /health on the supervisor's port")
	}
	if !client.Healthy(ctx) {
		t.Fatal("the returned client must address the supervisor's port")
	}

	// Nothing provisioned: the weights directory must not even exist.
	modelDir := filepath.Join(home, "models", "gliner2-large-v1")
	if _, serr := os.Stat(modelDir); !errors.Is(serr, fs.ErrNotExist) {
		t.Fatalf("sidecarService must not provision the model, but %s exists (stat err %v)", modelDir, serr)
	}

	// Nothing spawned either: the supervisor is returned unstarted.
	if pid := sup.Pid(); pid != 0 {
		t.Fatalf("sidecarService must not start the supervisor, got pid %d", pid)
	}

	// The spawn closure still resolves the binary, the port flag and the
	// sidecar env (incl. the weights location the service uses when it does
	// load GLiNER2) exactly as mlBackend wires it today.
	cmd, cerr := sup.spawn(sup.port)
	if cerr != nil {
		t.Fatalf("spawn closure: %v", cerr)
	}
	if cmd.Path != binPath {
		t.Fatalf("spawn path = %q, want %q", cmd.Path, binPath)
	}
	wantArg := fmt.Sprintf("--port=%d", sup.port)
	if len(cmd.Args) != 2 || cmd.Args[1] != wantArg {
		t.Fatalf("spawn args = %v, want [%s %s]", cmd.Args, binPath, wantArg)
	}
	if cmd.Process != nil {
		t.Fatal("building the spawn command must not start the process")
	}
	var gotModelDir string
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "KELD_GLINER2_DIR=") {
			gotModelDir = strings.TrimPrefix(kv, "KELD_GLINER2_DIR=")
		}
	}
	if gotModelDir != modelDir {
		t.Fatalf("KELD_GLINER2_DIR = %q, want %q", gotModelDir, modelDir)
	}

	// Policy (the sidecar.unavailable event) belongs to the caller, so a
	// successful build emits nothing of its own.
	if evs := emitter.Drain(); len(evs) != 0 {
		t.Fatalf("sidecarService must not emit client events, got %+v", evs)
	}
}

// TestSidecarServiceNoBinaryLeavesThePolicyToTheCaller covers the "no service
// this run" report. sidecarService must NOT emit sidecar.unavailable itself:
// each caller (ML enrichment, and later deterministic mode) responds
// differently to a missing binary.
func TestSidecarServiceNoBinaryLeavesThePolicyToTheCaller(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KELD_SIDECAR_BIN", filepath.Join(t.TempDir(), "absent"))
	if p, found := sidecarBinPath(); found {
		t.Skipf("host has a sidecar installed at %s; the no-binary path is not hermetic here", p)
	}

	emitter := clientevents.NewEmitter(clientevents.Corr{}, 16)
	emitter.SetGate(clientevents.Gate{Enabled: true, MinSeverity: clientevents.SevInfo, SampleRate: 1})

	client, sup, healthFn, ok, err := sidecarService(context.Background(), emitter, false)
	if ok {
		t.Fatal("no sidecar binary must report ok=false")
	}
	if err != nil {
		t.Fatalf("a missing binary is not a failure with a cause; err must stay nil so the caller can tell it from a port-alloc failure, got %v", err)
	}
	if client != nil || sup != nil || healthFn != nil {
		t.Fatalf("no service means no client/supervisor/healthFn, got %v/%v/healthFn nil=%v", client, sup, healthFn == nil)
	}
	if evs := emitter.Drain(); len(evs) != 0 {
		t.Fatalf("sidecarService must leave sidecar.unavailable to its caller, got %+v", evs)
	}
}

// blockingFetcher is a provision.Fetcher that never completes on its own: it
// signals that a fetch started and then parks until released (or the context
// ends). It stands in for the field's ~1.9 GB first-run download.
type blockingFetcher struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (f blockingFetcher) Fetch(ctx context.Context, _ string) error {
	select {
	case f.started <- struct{}{}:
	default:
	}
	select {
	case <-f.release:
	case <-ctx.Done():
	}
	return errors.New("provisioning did not complete")
}

// notWarmService is a sidecar stub that IS up (answers /health) but whose
// GLiNER2 capability is not loaded (/metrics reports worker.state "down") —
// exactly the shape of a freshly-spawned service on an unprovisioned machine.
func notWarmService(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"worker":{"state":"down"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestMLBackendStartsTheServiceWithoutFetchingTheModel pins two orderings at
// once.
//
// The first is the one the analysis service depends on: the sidecar is the
// client-side analysis-and-enrichment service in general (/analyze, /match,
// /vocabulary, /classify, /extract) and GLiNER2 is one capability it loads
// lazily — so the service must be spawned without waiting on the weights.
// Gating the spawn on them meant a machine that had never provisioned had no
// service at all, and therefore no /analyze.
//
// The second is the fix this test was rewritten for: starting the service no
// longer starts a DOWNLOAD either. Provisioning belongs to an attempted
// inference, so nothing is fetched until a warmup asks for it — and then it is.
//
// The safety half is asserted throughout: while the weights are absent the
// warm gate stays SHUT, so no job ever starts its inference deadline against a
// cold model.
func TestMLBackendStartsTheServiceWithoutFetchingTheModel(t *testing.T) {
	svc := notWarmService(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := sidecar.New(svc.URL, 2*time.Second)
	healthFn := func() bool { return client.Healthy(ctx) }

	spawned := make(chan struct{}, 1)
	sup := NewSupervisor(func(int) (*exec.Cmd, error) {
		select {
		case spawned <- struct{}{}:
		default:
		}
		return exec.CommandContext(ctx, "sleep", "30"), nil
	}, 0, healthFn, 5*time.Second)

	fetching := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)

	_, _, gate, warmup := mlBackendWithOpts(ctx, mlBackendOpts{
		sup:      sup,
		client:   client,
		modelDir: filepath.Join(t.TempDir(), "gliner2"),
		modelSHA: "sha-that-never-arrives",
		fetcher:  blockingFetcher{started: fetching, release: release},
		healthFn: healthFn,
	})

	select {
	case <-spawned:
	case <-time.After(2 * time.Second):
		t.Fatal("the analysis service must start without the model, not after it")
	}
	// Give a (wrongly) eager provisioning goroutine every chance to run.
	time.Sleep(200 * time.Millisecond)
	select {
	case <-fetching:
		t.Fatal("starting the service must not start the ~1.9 GB download; nothing had attempted an inference")
	default:
	}

	// An attempted inference IS the demand signal, and it does start it.
	go func() {
		wctx, wcancel := context.WithTimeout(ctx, time.Second)
		defer wcancel()
		_ = warmup(wctx)
	}()
	select {
	case <-fetching:
	case <-time.After(2 * time.Second):
		t.Fatal("an attempted inference must provision the model")
	}

	// ...and the gate stays shut for as long as the weights are absent:
	// WorkerReady polls /metrics (it never spawns a worker), and only
	// worker.state "ready" opens the gate.
	for i := 0; i < 4; i++ {
		if gate() {
			t.Fatal("the warm gate must stay shut until the GLiNER2 worker reports ready")
		}
		time.Sleep(warmPollInterval / 2)
	}
}

// TestMLBackendProvisionFailureStillLeavesTheServiceUp covers the failure path
// of the same ordering: a provision that fails — now triggered by a warmup,
// not by startup — must not take the analysis service with it, and the gate
// must still never open. (The report-exactly-once contract lives with the
// on-demand provisioner: see TestWarmupReportsAProvisionFailureOnceAndStillFails.)
func TestMLBackendProvisionFailureStillLeavesTheServiceUp(t *testing.T) {
	svc := notWarmService(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := sidecar.New(svc.URL, 2*time.Second)
	healthFn := func() bool { return client.Healthy(ctx) }

	spawned := make(chan struct{}, 1)
	sup := NewSupervisor(func(int) (*exec.Cmd, error) {
		select {
		case spawned <- struct{}{}:
		default:
		}
		return exec.CommandContext(ctx, "sleep", "30"), nil
	}, 0, healthFn, 5*time.Second)

	_, _, gate, warmup := mlBackendWithOpts(ctx, mlBackendOpts{
		sup:      sup,
		client:   client,
		modelDir: filepath.Join(t.TempDir(), "gliner2"),
		modelSHA: "some-sha",
		fetcher:  fakeFetcherErr{},
		healthFn: healthFn,
	})

	select {
	case <-spawned:
	case <-time.After(2 * time.Second):
		t.Fatal("the analysis service must be up before any inference is attempted")
	}

	wctx, wcancel := context.WithTimeout(ctx, 2*time.Second)
	defer wcancel()
	if err := warmup(wctx); err == nil {
		t.Fatal("a failed provision must fail the warmup, so the job defers")
	}
	if !healthFn() {
		t.Fatal("a failed provision must not take the analysis service down with it")
	}
	if gate() {
		t.Fatal("the warm gate must stay shut when the model could not be provisioned")
	}
}

// TestWorkerWarmupIsBoundedPerJobNotAHotLoop is the retry-behaviour assertion
// that makes starting the service against an unprovisioned model dir safe.
// With the service up but the weights absent, every job's warmup fails — so
// the question is whether that becomes a spin. It does not: warmup is invoked
// exactly ONCE per job, under a context bounded by KELD_ENRICH_WARM_WAIT, and
// the job is then deferred (re-spooled) rather than retried in place.
func TestWorkerWarmupIsBoundedPerJobNotAHotLoop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	t.Setenv("KELD_ENRICH_WARM_WAIT", "50ms")

	var calls atomic.Int32
	var bounded atomic.Int32
	warmup := func(wctx context.Context) error {
		calls.Add(1)
		if dl, ok := wctx.Deadline(); ok && time.Until(dl) <= warmWait() {
			bounded.Add(1)
		}
		<-wctx.Done() // never warms: hold until the bound expires, like a failing load
		return wctx.Err()
	}

	const jobs = 3
	q := queue.New(8)
	fs := &fakeSender{}
	for i := 0; i < jobs; i++ {
		q.Offer(sampleInlineJob(fmt.Sprintf("unprovisioned-%d", i)))
	}
	go Worker(context.Background(), q, enrichtest.NewFake(), serviceFacets{}, fs, "t@keld.co",
		func() bool { return false }, func() bool { return false }, warmup, nil, nil)

	waitFor(t, 5*time.Second, func() bool { return calls.Load() == jobs })
	// Nothing re-drives the queue here, so the count must now be stable: a
	// spin (or an in-place retry) would push it past one call per job.
	time.Sleep(300 * time.Millisecond)
	q.Close()

	if n := calls.Load(); n != jobs {
		t.Fatalf("warmup called %d times for %d jobs — warmup must be one bounded attempt per job, not a retry loop", n, jobs)
	}
	if n := bounded.Load(); n != jobs {
		t.Fatalf("%d of %d warmup contexts carried a deadline within warmWait; every one must be bounded", n, jobs)
	}
	if fs.count() != 0 {
		t.Fatalf("nothing may publish against a model that never warmed; got %d", fs.count())
	}
	if n := quarantineCount(t, home); n != 0 {
		t.Fatalf("an unprovisioned model must never quarantine a job; found %d", n)
	}
	if n := spoolCount(t, home); n != jobs {
		t.Fatalf("spooled %d deferred jobs, want %d — every deferred job must be preserved", n, jobs)
	}
}

// fakeAnalysisService writes a fake sidecar binary that records the argv it was
// spawned with (proof the service was actually started) and then parks until
// killed. It returns the marker path and a helper that blocks until the marker
// appears and yields the --port the supervisor handed the service.
//
// It is a shell script, never the real sidecar: these tests must prove the
// wiring, not load a model.
func fakeAnalysisService(t *testing.T) (markerPath string, awaitPort func() int) {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "spawned.argv")
	bin := filepath.Join(dir, "fake-analysis-service")
	script := "#!/bin/sh\nprintf '%s' \"$*\" > " + marker + "\nexec sleep 120\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KELD_SIDECAR_BIN", bin)

	return marker, func() int {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			b, err := os.ReadFile(marker)
			if err == nil && len(b) > 0 {
				var port int
				if _, serr := fmt.Sscanf(strings.TrimSpace(string(b)), "--port=%d", &port); serr == nil && port > 0 {
					return port
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the analysis service was never spawned: deterministic mode did not start it")
		return 0
	}
}

// TestDeterministicModeStartsTheServiceAndWiresTheAnalyzer pins the whole point
// of ml_backend="deterministic": the sidecar is the client-side ANALYSIS service
// (/analyze, /match, /vocabulary), and GLiNER2 is one capability it loads lazily.
// So deterministic mode starts the service and wires its window analyzer — it
// just never asks for the model. Before this, wireEnrichment returned early
// without a service, facetsFor(nil) was empty, the workstreams pass never
// registered, and the mode published a single credential-derived facet.
func TestDeterministicModeStartsTheServiceAndWiresTheAnalyzer(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	_, awaitPort := fakeAnalysisService(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q := queue.New(10)
	defer q.Close()
	emitter := clientevents.NewEmitter(clientevents.Corr{}, 16)
	emitter.SetGate(clientevents.Gate{Enabled: true, MinSeverity: clientevents.SevInfo, SampleRate: 1})
	set := settings.Settings{MLBackend: "deterministic"}

	handler, model, svc, gate, _, enabled := wireEnrichment(ctx, set, "s3cret", q, emitter, nil, false)

	if !enabled {
		t.Fatal("enrichment must stay enabled in deterministic mode")
	}
	if model != nil {
		t.Fatalf("deterministic mode must not wire a model, got %v", model)
	}
	if svc.Analyze == nil {
		t.Fatal("deterministic mode must wire the window analyzer — /analyze needs no model")
	}
	if svc.ScanPII == nil {
		t.Fatal("deterministic mode must wire the PII scan — /pii needs no model either, and it is the only source the sensitivity facet has here")
	}
	if gate == nil {
		t.Fatal("deterministic mode must wire a non-nil gate (Worker calls it unconditionally)")
	}
	if handler == nil {
		t.Fatal("deterministic mode must serve the real ingress handler")
	}

	// The service really is spawned, on the port the supervisor allocated.
	if port := awaitPort(); port <= 0 {
		t.Fatalf("spawned service port = %d, want an allocated ephemeral port", port)
	}
}

// TestDeterministicGateIsClosedUntilTheServiceIsUp pins the readiness gate to
// service health rather than a trivially-true stub. A gate that always reported
// ready would publish workstream-less profiles for every job that landed before
// the service finished starting — silently missing their dimensions. Worker
// warmth is equally wrong here: the model never loads in this mode, so a warm
// gate would hold every job forever.
func TestDeterministicGateIsClosedUntilTheServiceIsUp(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	_, awaitPort := fakeAnalysisService(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q := queue.New(10)
	defer q.Close()
	set := settings.Settings{MLBackend: "deterministic"}
	emitter := clientevents.NewEmitter(clientevents.Corr{}, 16)
	emitter.SetGate(clientevents.Gate{Enabled: true, MinSeverity: clientevents.SevInfo, SampleRate: 1})

	_, _, _, gate, _, _ := wireEnrichment(ctx, set, "s3cret", q, emitter, nil, false)
	if gate == nil {
		t.Fatal("deterministic mode must wire a non-nil gate")
	}

	// The fake service answers nothing, so /health is unreachable: the gate is
	// shut. A trivially-true gate fails right here.
	if gate() {
		t.Fatal("the gate must be closed while the analysis service is not serving")
	}

	// Stand a stub /health on the port the supervisor handed the service and
	// the same gate opens — it polls health, it is not latched or faked.
	port := awaitPort()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("binding the service port %d: %v", port, err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	deadline := time.Now().Add(3 * time.Second)
	for !gate() {
		if time.Now().After(deadline) {
			t.Fatal("the gate never opened once the analysis service was serving /health")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestDeterministicModeWithNoSidecarBinaryDoesNotWedge pins the OTHER half of
// the health gate: it exists for a service that is present but not yet ready
// (starting, restarting, mid-recycle), where waiting is right because the work
// becomes doable shortly. It must not be applied to a machine with NO SIDECAR
// BINARY AT ALL — there is no daemon-lifetime path to a service there, so a
// closed gate wedges deterministic mode forever: every job queues and spools
// and nothing is ever published, on what is the state of every machine before
// the sidecar tarball is fetched.
//
// With no binary, enrichment must instead run its other model-free facets
// (credential detection) with the workstreams pass simply unregistered — the
// ordinary pipeline_status "partial" path, not a lower-fidelity substitute for
// a facet. So: a trivially-true gate and a nil analyzer.
func TestDeterministicModeWithNoSidecarBinaryDoesNotWedge(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KELD_SIDECAR_BIN", filepath.Join(t.TempDir(), "absent"))
	if p, found := sidecarBinPath(); found {
		t.Skipf("host has a sidecar installed at %s; the no-binary path is not hermetic here", p)
	}

	q := queue.New(10)
	defer q.Close()
	emitter := clientevents.NewEmitter(clientevents.Corr{}, 16)
	emitter.SetGate(clientevents.Gate{Enabled: true, MinSeverity: clientevents.SevInfo, SampleRate: 1})
	set := settings.Settings{MLBackend: "deterministic"}

	_, model, svc, gate, _, enabled := wireEnrichment(context.Background(), set, "s3cret", q, emitter, nil, false)

	if !enabled {
		t.Fatal("enrichment must stay enabled in deterministic mode")
	}
	if model != nil {
		t.Fatalf("deterministic mode must not wire a model, got %v", model)
	}
	if svc.Analyze != nil {
		t.Fatal("with no service there is nothing to answer /analyze; the analyzer must be nil so the workstreams pass never registers")
	}
	if svc.ScanPII != nil {
		t.Fatal("with no service there is nothing to answer /pii; the scanner must be nil so sensitivity reports itself degraded rather than clean")
	}
	if gate == nil {
		t.Fatal("deterministic mode must wire a non-nil gate (Worker calls it unconditionally)")
	}
	for i := 0; i < 3; i++ {
		if !gate() {
			t.Fatal("with NO sidecar binary the gate must be trivially true: nothing will change without an install, so waiting only wedges the mode")
		}
	}

	// The event still fires, and still says which of the two "no service this
	// run" causes it was, so the absence is visible in Atlas exactly as it is
	// for ml_backend=auto.
	events := emitter.Drain()
	if len(events) != 1 || events[0].Code != "sidecar.unavailable" || events[0].Severity != clientevents.SevWarn {
		t.Fatalf("expected one sidecar.unavailable/warn event, got %+v", events)
	}
	if got := events[0].Fields["reason"]; got != "no_sidecar_binary" {
		t.Fatalf("reason field = %v, want \"no_sidecar_binary\" (it must stay distinguishable from a port-alloc failure)", got)
	}
}

// TestDeterministicPortAllocFailureDoesNotWedge covers the second "no service
// this run" cause. A port-alloc failure is no more resolvable without a daemon
// restart than a missing binary is, so it takes the same no-wedge path — but
// it must stay distinguishable in what it reports. net.Listen on an ephemeral
// loopback port effectively never fails, so the shared helper is exercised
// directly rather than through sidecarService.
func TestDeterministicPortAllocFailureDoesNotWedge(t *testing.T) {
	emitter := clientevents.NewEmitter(clientevents.Corr{}, 16)
	emitter.SetGate(clientevents.Gate{Enabled: true, MinSeverity: clientevents.SevInfo, SampleRate: 1})

	gate := noAnalysisService(emitter, map[string]any{"error": clientevents.RedactError(errors.New("listen tcp: boom"))})
	if gate == nil || !gate() {
		t.Fatal("a port-alloc failure has no daemon-lifetime remedy either; the gate must not hold jobs forever")
	}

	events := emitter.Drain()
	if len(events) != 1 || events[0].Code != "sidecar.unavailable" || events[0].Severity != clientevents.SevWarn {
		t.Fatalf("expected one sidecar.unavailable/warn event, got %+v", events)
	}
	if _, ok := events[0].Fields["error"]; !ok {
		t.Fatalf("the port-alloc cause must stay distinguishable via its error field, got %+v", events[0].Fields)
	}
}

// TestDeterministicGateIsCachedNotOneHTTPCallPerRead pins the COST of the
// deterministic readiness gate, not just its boolean value. Worker calls
// ready() at the top of every job and waitWarm re-calls it roughly every 20ms
// for up to warmWait, so a gate that performs a live loopback GET per call is
// a real defect: a crashed-but-installed service costs thousands of connects
// per deferred job, and a service that ACCEPTS TCP but never answers costs the
// client's full timeout on EVERY call — adding that timeout to every healthy
// job once the service degrades.
//
// "auto" has never had this problem: its gate is a cached atomic refreshed by
// warmGate. Deterministic mode must use the same mechanism. A test that only
// asserts gate() == true passes with the defect still in place, so this one
// counts requests against a stub /health across many reads.
func TestDeterministicGateIsCachedNotOneHTTPCallPerRead(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	_, awaitPort := fakeAnalysisService(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q := queue.New(10)
	defer q.Close()
	emitter := clientevents.NewEmitter(clientevents.Corr{}, 16)
	emitter.SetGate(clientevents.Gate{Enabled: true, MinSeverity: clientevents.SevInfo, SampleRate: 1})
	set := settings.Settings{MLBackend: "deterministic"}

	_, _, _, gate, _, _ := wireEnrichment(ctx, set, "s3cret", q, emitter, nil, false)
	if gate == nil {
		t.Fatal("deterministic mode must wire a non-nil gate")
	}

	var health atomic.Int64
	port := awaitPort()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("binding the service port %d: %v", port, err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		health.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	deadline := time.Now().Add(3 * time.Second)
	for !gate() {
		if time.Now().After(deadline) {
			t.Fatal("the gate never opened once the analysis service was serving /health")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The tight loop below runs in well under one poll interval, so a cached
	// gate can only be overtaken by at most a poll or two from the background
	// refresher (and the supervisor's own liveness poll). A gate that probes
	// per call would record ~1000.
	const reads = 1000
	before := health.Load()
	for i := 0; i < reads; i++ {
		gate()
	}
	if got := health.Load() - before; got > 4 {
		t.Fatalf("%d gate reads issued %d /health requests — the gate must read a cached atomic, not probe the service per call", reads, got)
	}
}

// TestMLBackendWiresTheServiceFacets pins that "auto" — the mode nearly every
// user runs — still produces the non-inference service facets. Nothing else
// did: the only auto-mode wiring test discards them and runs with the sidecar
// deliberately absent (where empty is correct), so a refactor could return
// nothing for auto, every claude_code user silently loses their workstream
// dimensions and their PII detection, and the suite stays green.
//
// wireEnrichment's auto branch is a straight pass-through of what mlBackend
// returns, so pinning it here keeps the test hermetic: no real sidecar is
// spawned and no weights are fetched.
func TestMLBackendWiresTheServiceFacets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	healthFn := func() bool { return false }
	sup := NewSupervisor(
		func(int) (*exec.Cmd, error) { return exec.Command("sleep", "1"), nil },
		0,
		healthFn,
		100*time.Millisecond,
	)

	model, svc, gate, _ := mlBackendWithOpts(ctx, mlBackendOpts{
		sup:      sup,
		client:   sidecar.New("http://127.0.0.1:1", 50*time.Millisecond),
		modelDir: filepath.Join(t.TempDir(), "gliner2"),
		modelSHA: "some-sha",
		fetcher:  fakeFetcherErr{},
		healthFn: healthFn,
	})
	if model == nil {
		t.Fatal("auto mode must wire the sidecar client as the Model")
	}
	if gate == nil {
		t.Fatal("auto mode must wire a readiness gate")
	}
	if svc.Analyze == nil {
		t.Fatal("auto mode must wire the window analyzer: the sidecar serves /analyze, so every claude_code job should get its workstream dimensions")
	}
	if svc.ScanPII == nil {
		t.Fatal("auto mode must wire the PII scan: the sidecar serves /pii, and without it the sensitivity facet loses every entity type but credentials")
	}
}
