package clientevents

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/retry"
)

func fastPolicy() retry.Policy {
	return retry.Policy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond, Multiplier: 2}
}

func testEvents() []Event {
	return []Event{
		{Code: "job.start", Severity: SevInfo, Corr: Corr{InstallID: "install-1"}, TS: time.Now()},
	}
}

func spoolFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("glob spool dir: %v", err)
	}
	sort.Strings(matches)
	return matches
}

func TestFlushPostsEnvelopeWithAuthHeader(t *testing.T) {
	var gotAuth string
	var gotEnv envelope
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("x-keld-ingest-token")
		if err := json.NewDecoder(r.Body).Decode(&gotEnv); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	events := testEvents()
	drained := false
	drain := func() []Event {
		if drained {
			return nil
		}
		drained = true
		return events
	}

	dir := t.TempDir()
	r := NewReporter(srv.URL, func() string { return "tok-123" }, "install-1", drain, dir)
	r.policy = fastPolicy()

	if err := r.flush(t.Context()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if gotAuth != "tok-123" {
		t.Fatalf("expected x-keld-ingest-token header, got %q", gotAuth)
	}
	if gotEnv.SchemaVersion != SchemaVersion {
		t.Fatalf("expected schema_version %d, got %d", SchemaVersion, gotEnv.SchemaVersion)
	}
	if gotEnv.InstallID != "install-1" {
		t.Fatalf("expected install_id install-1, got %q", gotEnv.InstallID)
	}
	if len(gotEnv.Events) != 1 || gotEnv.Events[0].Code != "job.start" {
		t.Fatalf("expected 1 event job.start, got %+v", gotEnv.Events)
	}

	if files := spoolFiles(t, dir); len(files) != 0 {
		t.Fatalf("expected no spool files after success, got %v", files)
	}
}

func TestFlushRetriesTransientThenSucceeds(t *testing.T) {
	var reqCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&reqCount, 1)
		if n < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	r := NewReporter(srv.URL, func() string { return "tok" }, "install-1", func() []Event { return testEvents() }, dir)
	r.policy = fastPolicy()

	if err := r.flush(t.Context()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := atomic.LoadInt32(&reqCount); got < 2 {
		t.Fatalf("expected >= 2 requests (503 then 200), got %d", got)
	}
	if files := spoolFiles(t, dir); len(files) != 0 {
		t.Fatalf("expected no spool files after eventual success, got %v", files)
	}
}

func TestFlushExhaustedTransientSpools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	dir := t.TempDir()
	r := NewReporter(srv.URL, func() string { return "tok" }, "install-1", func() []Event { return testEvents() }, dir)
	r.policy = fastPolicy()

	err := r.flush(t.Context())
	if err == nil {
		t.Fatalf("expected flush to return the exhausted retry error")
	}

	files := spoolFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 spool file after exhausted transient failure, got %v", files)
	}

	data, readErr := os.ReadFile(files[0])
	if readErr != nil {
		t.Fatalf("read spool file: %v", readErr)
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal spooled envelope: %v", err)
	}
	if env.InstallID != "install-1" || len(env.Events) != 1 {
		t.Fatalf("unexpected spooled envelope: %+v", env)
	}

	fi, statErr := os.Stat(files[0])
	if statErr != nil {
		t.Fatalf("stat spool file: %v", statErr)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected spool file mode 0600, got %v", fi.Mode().Perm())
	}
}

func TestFlushPermanentFailureDropsNoSpool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	dir := t.TempDir()
	r := NewReporter(srv.URL, func() string { return "tok" }, "install-1", func() []Event { return testEvents() }, dir)
	r.policy = fastPolicy()

	err := r.flush(t.Context())
	if err == nil {
		t.Fatalf("expected flush to return the permanent error")
	}
	if files := spoolFiles(t, dir); len(files) != 0 {
		t.Fatalf("expected permanent 400 to be dropped (no spool file), got %v", files)
	}
}

func TestFlushSpoolsOnContextCancellation(t *testing.T) {
	// Server always fails; we cancel the ctx as soon as the first request
	// lands, simulating daemon shutdown while Atlas is slow/down mid-retry.
	// retry.Do then returns context.Canceled, which IsTransient classifies as
	// PERMANENT — but the already-drained batch must be spooled, not dropped.
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	dir := t.TempDir()
	r := NewReporter(srv.URL, func() string { return "tok" }, "install-1", func() []Event { return testEvents() }, dir)
	r.policy = fastPolicy()

	err := r.flush(ctx)
	if err == nil {
		t.Fatalf("expected flush to return an error after ctx cancellation")
	}

	files := spoolFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("expected drained batch to be spooled (not dropped) on ctx cancellation, got %v", files)
	}
	data, readErr := os.ReadFile(files[0])
	if readErr != nil {
		t.Fatalf("read spool file: %v", readErr)
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal spooled envelope: %v", err)
	}
	if env.InstallID != "install-1" || len(env.Events) != 1 {
		t.Fatalf("unexpected spooled envelope: %+v", env)
	}
}

func TestFlushEmptyDrainNoPost(t *testing.T) {
	var reqCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reqCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	r := NewReporter(srv.URL, func() string { return "tok" }, "install-1", func() []Event { return nil }, dir)
	r.policy = fastPolicy()

	if err := r.flush(t.Context()); err != nil {
		t.Fatalf("flush on empty drain: %v", err)
	}
	if got := atomic.LoadInt32(&reqCount); got != 0 {
		t.Fatalf("expected 0 requests on empty drain, got %d", got)
	}
}

func TestDrainSpoolRepostsAndDeletes(t *testing.T) {
	var gotEnv envelope
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotEnv); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	env := envelope{SchemaVersion: SchemaVersion, InstallID: "install-1", Events: testEvents()}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	spoolPath := filepath.Join(dir, "111-1.json")
	if err := os.WriteFile(spoolPath, data, 0o600); err != nil {
		t.Fatalf("write spool file: %v", err)
	}

	r := NewReporter(srv.URL, func() string { return "tok" }, "install-1", func() []Event { return nil }, dir)
	r.policy = fastPolicy()

	if err := r.drainSpool(t.Context()); err != nil {
		t.Fatalf("drainSpool: %v", err)
	}

	if _, statErr := os.Stat(spoolPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected spool file to be removed, stat err: %v", statErr)
	}
	if gotEnv.InstallID != "install-1" || len(gotEnv.Events) != 1 {
		t.Fatalf("expected server to receive spooled envelope, got %+v", gotEnv)
	}
}

func TestDrainSpoolStopsOnTransientFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	dir := t.TempDir()
	env := envelope{SchemaVersion: SchemaVersion, InstallID: "install-1", Events: testEvents()}
	data, _ := json.Marshal(env)
	spoolPath := filepath.Join(dir, "111-1.json")
	if err := os.WriteFile(spoolPath, data, 0o600); err != nil {
		t.Fatalf("write spool file: %v", err)
	}

	r := NewReporter(srv.URL, func() string { return "tok" }, "install-1", func() []Event { return nil }, dir)
	r.policy = fastPolicy()

	_ = r.drainSpool(t.Context())

	if _, statErr := os.Stat(spoolPath); statErr != nil {
		t.Fatalf("expected spool file to remain after transient failure, stat err: %v", statErr)
	}
}

func TestDrainSpoolDropsPoisonFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	dir := t.TempDir()
	env := envelope{SchemaVersion: SchemaVersion, InstallID: "install-1", Events: testEvents()}
	data, _ := json.Marshal(env)
	spoolPath := filepath.Join(dir, "111-1.json")
	if err := os.WriteFile(spoolPath, data, 0o600); err != nil {
		t.Fatalf("write spool file: %v", err)
	}

	r := NewReporter(srv.URL, func() string { return "tok" }, "install-1", func() []Event { return nil }, dir)
	r.policy = fastPolicy()

	_ = r.drainSpool(t.Context())

	if _, statErr := os.Stat(spoolPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected poison spool file to be removed, stat err: %v", statErr)
	}
}

func TestDrainSpoolMissingDirIsNoop(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	r := NewReporter("http://example.invalid", func() string { return "tok" }, "install-1", func() []Event { return nil }, dir)
	r.policy = fastPolicy()

	if err := r.drainSpool(t.Context()); err != nil {
		t.Fatalf("expected nil error for missing spool dir, got %v", err)
	}
}

func TestSpoolBoundedDropsOldest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	dir := t.TempDir()
	r := NewReporter(srv.URL, func() string { return "tok" }, "install-1", func() []Event { return testEvents() }, dir)
	r.policy = fastPolicy()
	r.maxSpool = 2

	base := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	tick := 0
	r.clock = func() time.Time {
		t := base.Add(time.Duration(tick) * time.Second)
		tick++
		return t
	}

	for i := 0; i < 3; i++ {
		if err := r.flush(t.Context()); err == nil {
			t.Fatalf("expected flush to fail against always-503 server")
		}
	}

	files := spoolFiles(t, dir)
	if len(files) != 2 {
		t.Fatalf("expected spool bounded to 2 files, got %d: %v", len(files), files)
	}
}

func TestRunFlushesOnShutdown(t *testing.T) {
	var reqCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reqCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	var drainCalls int32
	events := testEvents()
	drain := func() []Event {
		if atomic.AddInt32(&drainCalls, 1) == 1 {
			return events
		}
		return nil
	}

	r := NewReporter(srv.URL, func() string { return "tok" }, "install-1", drain, dir)
	r.policy = fastPolicy()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx, time.Hour) // long interval: only the shutdown flush should fire
		close(done)
	}()

	// Give the goroutine a moment to start and reach the ticker select.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return after context cancellation")
	}

	if got := atomic.LoadInt32(&reqCount); got < 1 {
		t.Fatalf("expected at least 1 request from shutdown flush, got %d", got)
	}
}
