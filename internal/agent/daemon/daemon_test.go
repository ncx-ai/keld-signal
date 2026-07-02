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

	"github.com/ncx-ai/keld-cli/internal/agent/enrich"
	"github.com/ncx-ai/keld-cli/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-cli/internal/agent/publish"
	"github.com/ncx-ai/keld-cli/internal/agent/queue"
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

// TestWorkerEnrichesInlineAndNeverLeaksRaw verifies the deterministic (ML-off)
// path with an always-ready gate (unchanged behaviour from before Task 7).
func TestWorkerEnrichesInlineAndNeverLeaksRaw(t *testing.T) {
	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(q, enrich.NewDeterministic(), fs, "dg@keld.co", func() bool { return false }, func() bool { return true })

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

// TestWorkerDeterministicMLOff confirms the ML-off / no-sidecar path publishes
// immediately (always-ready gate) and does not regress existing behaviour.
func TestWorkerDeterministicMLOff(t *testing.T) {
	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(q, enrich.NewDeterministic(), fs, "test@keld.co", func() bool { return false }, func() bool { return true })

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
		Worker(q, enrich.NewDeterministic(), fs, "test@keld.co", func() bool { return false }, neverReady)
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

// TestWorkerWithSidecarStubPublishesViaRouter sets up a real httptest sidecar
// stub + a Supervisor whose spawn is a harmless "sleep" command and whose
// health function reports the stub as healthy. It asserts that a job Offered
// to the queue is published once the worker gate becomes ready.
func TestWorkerWithSidecarStubPublishesViaRouter(t *testing.T) {
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

	router := enrich.NewRouter(client, enrich.NewDeterministic(), healthFn)
	gate := func() bool { return sup.Ready() || sup.FellBack() }

	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(q, router, fs, "sidecar-test@keld.co", func() bool { return false }, gate)

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
	go Worker(q, router, fs, "provision-test@keld.co", func() bool { return false }, gate)

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

// TestMLBackendProvisionFailurePublishesViaDeterministic exercises the path
// where provisioning fails — the gate must open via provisionFailed so the
// worker publishes via the deterministic model.
func TestMLBackendProvisionFailurePublishesViaDeterministic(t *testing.T) {
	// A sidecar that is never healthy (we never spawn it).
	unhealthyClient := sidecar.New("http://127.0.0.1:1", 50*time.Millisecond)
	healthFn := func() bool { return false }

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sup := NewSupervisor(
		func(int) (*exec.Cmd, error) { return exec.Command("sleep", "10"), nil },
		0,
		healthFn,
		100*time.Millisecond,
	)

	modelDir := filepath.Join(t.TempDir(), "gliner2")

	router, gate := mlBackendWithOpts(ctx, mlBackendOpts{
		sup:      sup,
		client:   unhealthyClient,
		modelDir: modelDir,
		modelSHA: "some-sha",
		fetcher:  fakeFetcherErr{},
		healthFn: healthFn,
	})

	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(q, router, fs, "fail-test@keld.co", func() bool { return false }, gate)

	q.Offer(queue.Job{
		Source: "claude_code", Scheme: "trace", ID: "FAIL-1",
		Inline: "write a function",
	})

	deadline := time.After(5 * time.Second)
	for {
		if len(fs.all()) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("worker with failed provisioning did not publish via deterministic in time")
		case <-time.After(20 * time.Millisecond):
		}
	}
	q.Close()

	e := fs.all()[0]
	if e.Correlation.ID != "FAIL-1" {
		t.Fatalf("unexpected correlation: %+v", e.Correlation)
	}
}
