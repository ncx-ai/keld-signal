package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
)

// countingFetcher records how many times a fetch was ATTEMPTED and installs a
// valid sentinel so EnsureModel accepts the result. The count is the whole
// point of these tests: the ~1.9 GB download must be attributable to a
// specific attempted inference, never to daemon startup.
type countingFetcher struct {
	n       atomic.Int32
	content []byte
}

func (f *countingFetcher) Fetch(_ context.Context, dest string) error {
	f.n.Add(1)
	return os.WriteFile(filepath.Join(dest, "model.safetensors"), f.content, 0o644)
}

// sleepSup builds a Supervisor whose spawn is a harmless sleep, recording that
// it was asked to spawn. Nothing here touches the host's real sidecar.
func sleepSup(t *testing.T, ctx context.Context, healthFn func() bool, spawned chan<- struct{}) *Supervisor {
	t.Helper()
	return NewSupervisor(func(int) (*exec.Cmd, error) {
		select {
		case spawned <- struct{}{}:
		default:
		}
		return exec.CommandContext(ctx, "sleep", "30"), nil
	}, 0, healthFn, 5*time.Second)
}

// TestMLBackendDoesNotFetchTheModelUntilAnInferenceIsAttempted is the whole
// point of on-demand provisioning: starting the daemon must not pull ~1.9 GB
// of weights. The download is owed to a specific attempted inference, and
// nothing else. The analysis service still starts (it serves /analyze and /pii
// with no model at all), so "no fetch" must not be achieved by not starting.
func TestMLBackendDoesNotFetchTheModelUntilAnInferenceIsAttempted(t *testing.T) {
	stub := sidecarStub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := sidecar.New(stub.URL, 2*time.Second)
	healthFn := func() bool { return client.Healthy(ctx) }
	spawned := make(chan struct{}, 1)

	content := []byte("test-model-weights")
	fetcher := &countingFetcher{content: content}

	_, _, _, warmup := mlBackendWithOpts(ctx, mlBackendOpts{
		sup:      sleepSup(t, ctx, healthFn, spawned),
		client:   client,
		modelDir: filepath.Join(t.TempDir(), "gliner2"),
		modelSHA: sha256Hex(content),
		fetcher:  fetcher,
		healthFn: healthFn,
	})

	select {
	case <-spawned:
	case <-time.After(2 * time.Second):
		t.Fatal("the analysis service must still start at daemon start — it needs no model")
	}
	// Give a (wrongly) eager provisioning goroutine every chance to run.
	time.Sleep(200 * time.Millisecond)
	if n := fetcher.n.Load(); n != 0 {
		t.Fatalf("startup fetched the model %d times; nothing had attempted an inference", n)
	}

	if warmup == nil {
		t.Fatal("auto mode must hand Worker a warmup — it is the demand signal that provisions")
	}
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	if err := warmup(wctx); err != nil {
		t.Fatalf("warmup on an attempted inference must provision then load: %v", err)
	}
	if n := fetcher.n.Load(); n != 1 {
		t.Fatalf("the attempted inference must have provisioned exactly once, got %d fetches", n)
	}
}

// TestWarmupProvisionsOnceNotOncePerJob pins the cost: provisioning is
// per-daemon-run work, not per-job work. Re-running EnsureModel per job would
// also re-hash the 1.9 GB sentinel on every prompt.
func TestWarmupProvisionsOnceNotOncePerJob(t *testing.T) {
	stub := sidecarStub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := sidecar.New(stub.URL, 2*time.Second)
	healthFn := func() bool { return client.Healthy(ctx) }
	content := []byte("test-model-weights")
	fetcher := &countingFetcher{content: content}

	modelDir := filepath.Join(t.TempDir(), "gliner2")
	_, _, _, warmup := mlBackendWithOpts(ctx, mlBackendOpts{
		sup:      sleepSup(t, ctx, healthFn, make(chan struct{}, 1)),
		client:   client,
		modelDir: modelDir,
		modelSHA: sha256Hex(content),
		fetcher:  fetcher,
		healthFn: healthFn,
	})

	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	if err := warmup(wctx); err != nil {
		t.Fatalf("first warmup: %v", err)
	}
	wcancel()

	// Corrupt the sentinel. Nothing legitimate does this — it is a probe: if a
	// later warmup re-enters EnsureModel, it now sees a SHA mismatch and
	// re-fetches, which is exactly the per-job re-verification being ruled
	// out. Success latches, so warmups 2 and 3 must not look at the disk at
	// all (re-verifying means streaming a SHA-256 over ~1.9 GB per prompt).
	if err := os.WriteFile(filepath.Join(modelDir, "model.safetensors"), []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 3; i++ {
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
		if err := warmup(wctx); err != nil {
			wcancel()
			t.Fatalf("warmup %d: %v", i, err)
		}
		wcancel()
	}
	if n := fetcher.n.Load(); n != 1 {
		t.Fatalf("provisioning must happen once per daemon run, got %d fetches", n)
	}
}

// TestWarmupDefersWhileTheDownloadIsStillRunning covers the field's first run:
// the download outlives any single job's warm budget. The job must be told
// "not ready" — so Worker defers and re-spools it, never handing it to
// something lower-fidelity — while the download itself keeps running on the
// daemon's own lifetime, so the next job finds it further along rather than
// restarting it.
func TestWarmupDefersWhileTheDownloadIsStillRunning(t *testing.T) {
	stub := sidecarStub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := sidecar.New(stub.URL, 2*time.Second)
	healthFn := func() bool { return client.Healthy(ctx) }

	fetching := make(chan struct{}, 4)
	release := make(chan struct{})
	defer close(release)

	_, _, _, warmup := mlBackendWithOpts(ctx, mlBackendOpts{
		sup:      sleepSup(t, ctx, healthFn, make(chan struct{}, 1)),
		client:   client,
		modelDir: filepath.Join(t.TempDir(), "gliner2"),
		modelSHA: "sha-that-never-arrives",
		fetcher:  slowFetcher{started: fetching, release: release},
		healthFn: healthFn,
	})

	for i := 0; i < 2; i++ {
		wctx, wcancel := context.WithTimeout(ctx, 100*time.Millisecond)
		err := warmup(wctx)
		wcancel()
		if err == nil {
			t.Fatalf("warmup %d returned nil while the weights are still downloading — the job would run against a cold model", i)
		}
	}
	// Exactly one download in flight: a second job must join the one already
	// running, not start a competing 1.9 GB fetch.
	if n := len(fetching); n != 1 {
		t.Fatalf("started %d downloads for 2 deferred jobs, want 1", n)
	}
}

// slowFetcher signals that a fetch started, then parks until released or the
// context ends. It stands in for the field's ~1.9 GB first-run download.
type slowFetcher struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (f slowFetcher) Fetch(ctx context.Context, _ string) error {
	f.started <- struct{}{}
	select {
	case <-f.release:
	case <-ctx.Done():
	}
	return errors.New("provisioning did not complete")
}

// TestWarmupReportsAProvisionFailureOnceAndStillFails keeps the reporting
// contract the eager path had: a failed provision emits exactly one
// model.load_failed per attempt, and the warmup fails so the gate stays shut
// and the job defers.
func TestWarmupReportsAProvisionFailureOnceAndStillFails(t *testing.T) {
	stub := sidecarStub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := sidecar.New(stub.URL, 2*time.Second)
	healthFn := func() bool { return client.Healthy(ctx) }

	emitter := clientevents.NewEmitter(clientevents.Corr{}, 16)
	emitter.SetGate(clientevents.Gate{Enabled: true, MinSeverity: clientevents.SevInfo, SampleRate: 1})

	_, _, _, warmup := mlBackendWithOpts(ctx, mlBackendOpts{
		sup:      sleepSup(t, ctx, healthFn, make(chan struct{}, 1)),
		client:   client,
		modelDir: filepath.Join(t.TempDir(), "gliner2"),
		modelSHA: "some-sha",
		fetcher:  fakeFetcherErr{},
		healthFn: healthFn,
		emitter:  emitter,
	})

	wctx, wcancel := context.WithTimeout(ctx, 2*time.Second)
	defer wcancel()
	if err := warmup(wctx); err == nil {
		t.Fatal("warmup must fail when the model cannot be provisioned — never proceed without it")
	}
	var loadFailed []clientevents.Event
	for _, e := range emitter.Drain() {
		if e.Code == "model.load_failed" {
			loadFailed = append(loadFailed, e)
		}
	}
	if len(loadFailed) != 1 {
		t.Fatalf("model.load_failed emitted %d times, want exactly 1", len(loadFailed))
	}
	if loadFailed[0].Severity != clientevents.SevError {
		t.Fatalf("severity = %v, want %v", loadFailed[0].Severity, clientevents.SevError)
	}
	if _, ok := loadFailed[0].Fields["error"]; !ok {
		t.Fatalf("model.load_failed must carry a redacted error field, got %+v", loadFailed[0].Fields)
	}
}

// TestProvisioningWarmupIsNilWithoutAModelToWarm keeps deterministic mode (and
// the eval/test fakes) on their existing passive-wait path: there is no model
// to load, so there is nothing to provision and no warmup to hand Worker.
// A non-nil warmup here would make deterministic mode fetch 1.9 GB it never uses.
func TestProvisioningWarmupIsNilWithoutAModelToWarm(t *testing.T) {
	p := &modelProvisioner{}
	if got := provisioningWarmup(p, nil); got != nil {
		t.Fatal("no model to warm must mean no warmup — otherwise a model-free mode provisions")
	}
}
