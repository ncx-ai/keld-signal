# Dependency-pull retry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A reusable `internal/retry` package (configurable exp-backoff + jitter, one canonical transient-vs-permanent classifier) and wire it into the HF model download so transient faults retry and permanent ones fail fast.

**Architecture:** Pure Go package with `Do`/`DoClassify`, a `Policy` (env-tunable defaults), and an `IsTransient` classifier keyed off net errors + an HTTP `StatusError` carrier. `hf.go` wraps its two GET paths in `retry.Do`.

**Tech Stack:** Go stdlib (`net`, `errors`, `syscall`, `math/rand`, `time`), `httptest` for tests.

## Global Constraints

- One canonical classifier (`IsTransient`) — adopters don't write their own.
- Unrecognized errors are **permanent** (no retry) — never hammer on unknowns.
- `context.Canceled` (parent shutdown) stops immediately, returns `ctx.Err()`.
- No new deps. Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Tests must not sleep for real seconds — use tiny `BaseDelay`/`MaxDelay` and `Jitter:false` for determinism; test the pure `backoff()` math directly.

---

## File Structure
- `internal/retry/retry.go` (new) — `Policy`, `DefaultPolicy`, `Do`, `DoClassify`, `IsTransient`, `StatusError`/`HTTPStatus`, `backoff`.
- `internal/retry/retry_test.go` (new).
- `internal/agent/enrich/sidecar/hf.go` (modify) — `policy` field + wrap the two GET paths.
- `internal/agent/enrich/sidecar/hf_test.go` (modify) — add retry/fail-fast tests using the existing `hfStub`.
- `AGENTS.md` (modify) — one line: `internal/retry` is the standard for dependency pulls.

---

## Task 1: `internal/retry` package

**Files:** Create `internal/retry/retry.go`, `internal/retry/retry_test.go`.

**Interfaces produced:**
- `type Policy struct { MaxAttempts int; BaseDelay, MaxDelay time.Duration; Multiplier float64; Jitter bool }`
- `func DefaultPolicy() Policy`
- `func Do(ctx context.Context, p Policy, op func() error) error`
- `func DoClassify(ctx context.Context, p Policy, classify func(error) bool, op func() error) error`
- `func IsTransient(err error) bool`
- `type StatusError struct { Code int; RetryAfter time.Duration }` + `func HTTPStatus(code int) error`

- [ ] **Step 1: Write the failing tests**

Create `internal/retry/retry_test.go`:
```go
package retry

import (
	"context"
	"errors"
	"io"
	"syscall"
	"testing"
	"time"
)

func fast() Policy { return Policy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond, Multiplier: 2, Jitter: false} }

func TestDoRetriesTransientThenSucceeds(t *testing.T) {
	n := 0
	err := Do(context.Background(), fast(), func() error {
		n++
		if n < 3 {
			return HTTPStatus(503)
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Fatalf("err=%v attempts=%d, want nil/3", err, n)
	}
}

func TestDoPermanentFailsFast(t *testing.T) {
	n := 0
	err := Do(context.Background(), fast(), func() error { n++; return HTTPStatus(404) })
	if err == nil || n != 1 {
		t.Fatalf("err=%v attempts=%d, want error/1", err, n)
	}
}

func TestDoUnknownErrorNotRetried(t *testing.T) {
	n := 0
	_ = Do(context.Background(), fast(), func() error { n++; return errors.New("mystery") })
	if n != 1 {
		t.Fatalf("attempts=%d, want 1 (unknown=permanent)", n)
	}
}

func TestDoExhaustsAttempts(t *testing.T) {
	n := 0
	err := Do(context.Background(), fast(), func() error { n++; return HTTPStatus(500) })
	if err == nil || n != 4 {
		t.Fatalf("err=%v attempts=%d, want error/4", err, n)
	}
}

func TestDoStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := Policy{MaxAttempts: 5, BaseDelay: time.Hour, MaxDelay: time.Hour, Multiplier: 2}
	go func() { time.Sleep(5 * time.Millisecond); cancel() }()
	start := time.Now()
	err := Do(ctx, p, func() error { return HTTPStatus(503) })
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("err=%v elapsed=%s, want prompt context.Canceled", err, time.Since(start))
	}
}

func TestIsTransient(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{HTTPStatus(503), true}, {HTTPStatus(429), true}, {HTTPStatus(404), false},
		{HTTPStatus(401), false}, {context.Canceled, false}, {context.DeadlineExceeded, true},
		{syscall.ECONNREFUSED, true}, {io.ErrUnexpectedEOF, true}, {errors.New("x"), false},
	}
	for _, c := range cases {
		if got := IsTransient(c.err); got != c.want {
			t.Errorf("IsTransient(%v)=%v want %v", c.err, got, c.want)
		}
	}
}

func TestBackoffSequenceAndCap(t *testing.T) {
	p := Policy{BaseDelay: time.Second, MaxDelay: 5 * time.Second, Multiplier: 2, Jitter: false}
	got := []time.Duration{backoff(p, 1, 0), backoff(p, 2, 0), backoff(p, 3, 0), backoff(p, 4, 0)}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second} // capped
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("backoff attempt %d = %s, want %s", i+1, got[i], want[i])
		}
	}
}

func TestBackoffHonorsRetryAfter(t *testing.T) {
	p := Policy{BaseDelay: time.Second, MaxDelay: 30 * time.Second, Multiplier: 2, Jitter: false}
	if got := backoff(p, 1, 10*time.Second); got != 10*time.Second {
		t.Fatalf("backoff w/ Retry-After=%s, want 10s", got)
	}
}

func TestBackoffJitterBounded(t *testing.T) {
	p := Policy{BaseDelay: time.Second, MaxDelay: time.Second, Multiplier: 2, Jitter: true}
	for i := 0; i < 100; i++ {
		if d := backoff(p, 1, 0); d < 0 || d > time.Second {
			t.Fatalf("jittered backoff %s out of [0,1s]", d)
		}
	}
}
```

- [ ] **Step 2: Run — verify it fails**

Run: `go test ./internal/retry/ 2>&1 | head`
Expected: build failure — package `internal/retry` doesn't exist.

- [ ] **Step 3: Implement `internal/retry/retry.go`**

```go
// Package retry runs an operation with configurable exponential backoff + jitter,
// retrying only faults classified TRANSIENT (network blips/timeouts, HTTP
// 408/429/5xx) and giving up immediately on PERMANENT ones (HTTP 4xx, unknown
// errors, or a cancelled context). One canonical classifier (IsTransient) keeps
// every adopter consistent — use it for pulling required dependencies over the
// network (e.g. the HF model download).
package retry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"strconv"
	"syscall"
	"time"
)

type Policy struct {
	MaxAttempts int           // total tries incl. the first
	BaseDelay   time.Duration // first backoff
	MaxDelay    time.Duration // per-sleep cap
	Multiplier  float64       // growth factor
	Jitter      bool          // full jitter in [0, computed]
}

func DefaultPolicy() Policy {
	p := Policy{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: 30 * time.Second, Multiplier: 2.0, Jitter: true}
	if v, err := strconv.Atoi(os.Getenv("KELD_RETRY_MAX_ATTEMPTS")); err == nil && v > 0 {
		p.MaxAttempts = v
	}
	if v, err := strconv.Atoi(os.Getenv("KELD_RETRY_BASE_MS")); err == nil && v > 0 {
		p.BaseDelay = time.Duration(v) * time.Millisecond
	}
	if v, err := strconv.Atoi(os.Getenv("KELD_RETRY_MAX_MS")); err == nil && v > 0 {
		p.MaxDelay = time.Duration(v) * time.Millisecond
	}
	return p
}

// StatusError carries an HTTP status (+ optional Retry-After) so IsTransient can
// judge a non-2xx response. Return retry.HTTPStatus(code) from op on non-2xx.
type StatusError struct {
	Code       int
	RetryAfter time.Duration
}

func (e *StatusError) Error() string { return fmt.Sprintf("http status %d", e.Code) }

func HTTPStatus(code int) error { return &StatusError{Code: code} }

// IsTransient is the canonical transient-vs-permanent classifier.
func IsTransient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true // per-attempt timeout
	}
	var se *StatusError
	if errors.As(err, &se) {
		switch se.Code {
		case 408, 429, 500, 502, 503, 504:
			return true
		default:
			return false
		}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var de *net.DNSError
	if errors.As(err, &de) && de.IsTemporary {
		return true
	}
	return false // unrecognized -> permanent (don't hammer)
}

func Do(ctx context.Context, p Policy, op func() error) error {
	return DoClassify(ctx, p, IsTransient, op)
}

func DoClassify(ctx context.Context, p Policy, classify func(error) bool, op func() error) error {
	if p.MaxAttempts < 1 {
		p.MaxAttempts = 1
	}
	for attempt := 1; ; attempt++ {
		err := op()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt >= p.MaxAttempts || !classify(err) {
			return fmt.Errorf("retry: gave up after %d attempt(s): %w", attempt, err)
		}
		var ra time.Duration
		var se *StatusError
		if errors.As(err, &se) {
			ra = se.RetryAfter
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff(p, attempt, ra)):
		}
	}
}

// backoff is the sleep before the (attempt+1)-th try. Pure + deterministic when
// Jitter is false, so it is unit-tested directly.
func backoff(p Policy, attempt int, retryAfter time.Duration) time.Duration {
	d := float64(p.BaseDelay)
	for i := 1; i < attempt; i++ {
		d *= p.Multiplier
	}
	dd := time.Duration(d)
	if p.MaxDelay > 0 && dd > p.MaxDelay {
		dd = p.MaxDelay
	}
	if retryAfter > dd {
		dd = retryAfter
		if p.MaxDelay > 0 && dd > p.MaxDelay {
			dd = p.MaxDelay
		}
	}
	if p.Jitter && dd > 0 {
		dd = time.Duration(rand.Int63n(int64(dd) + 1))
	}
	return dd
}
```

- [ ] **Step 4: Run — verify it passes**

Run: `go test ./internal/retry/ -v 2>&1 | tail -20`
Expected: all `TestDo*`, `TestIsTransient`, `TestBackoff*` PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/retry/
git commit -m "feat(retry): reusable exp-backoff retry with canonical transient classifier

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: HF download adoption

**Files:** Modify `internal/agent/enrich/sidecar/hf.go`, `internal/agent/enrich/sidecar/hf_test.go`.

**Interfaces:**
- Consumes: `retry.Do`, `retry.HTTPStatus`, `retry.Policy`, `retry.DefaultPolicy`.
- `HFFetcher` gains an exported-for-tests field `Policy retry.Policy` (so tests can inject a fast policy).

- [ ] **Step 1: Write the failing tests**

Add to `internal/agent/enrich/sidecar/hf_test.go` (reuse the existing `hfStub` helper; import `retry` + `sync/atomic`):
```go
func fastPolicy() retry.Policy {
	return retry.Policy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond, Multiplier: 2}
}

func TestHFFetcherRetriesTransient(t *testing.T) {
	var hits int32
	// Fail the revision endpoint twice with 503, then serve normally.
	real := hfStub(t, "owner/repo", "rev1", map[string][]byte{"config.json": []byte("{}")})
	defer real.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) <= 2 && strings.Contains(r.URL.Path, "/revision/") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		http.Redirect(w, r, real.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	f := NewHFFetcher("owner/repo", "rev1")
	f.baseURL = srv.URL
	f.Policy = fastPolicy()
	if err := f.Fetch(context.Background(), t.TempDir()); err != nil {
		t.Fatalf("Fetch after transient 503s: %v", err)
	}
	if hits < 3 {
		t.Fatalf("expected retries, hits=%d", hits)
	}
}

func TestHFFetcherPermanentFailsFast(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	f := NewHFFetcher("owner/repo", "rev1")
	f.baseURL = srv.URL
	f.Policy = fastPolicy()
	if err := f.Fetch(context.Background(), t.TempDir()); err == nil {
		t.Fatal("want error on 404")
	}
	if hits != 1 {
		t.Fatalf("404 must not retry, hits=%d", hits)
	}
}
```
> Implementer: adjust the redirect/stub wiring to whatever cleanly makes the revision endpoint fail N times then succeed against the existing `hfStub` shape — the assertions (Fetch succeeds after transient 503s; 404 → 1 hit) are what matter. Add imports `context`, `strings`, `sync/atomic`, `time`, and the `retry` package.

- [ ] **Step 2: Run — verify it fails**

Run: `go test ./internal/agent/enrich/sidecar/ -run TestHFFetcher 2>&1 | head`
Expected: FAIL — `f.Policy` undefined (and no retry behavior yet).

- [ ] **Step 3: Implement in `hf.go`**

Add the import `"github.com/ncx-ai/keld-signal/internal/retry"`. Add the field + default:
```go
type HFFetcher struct {
	// ...existing fields...
	Policy retry.Policy
}
```
In `NewHFFetcher`, set `Policy: retry.DefaultPolicy()` in the returned struct.

Add a small helper for the status carrier:
```go
// hfStatus turns a non-2xx response into a retry.StatusError carrying Retry-After.
func hfStatus(resp *http.Response) error {
	ra := time.Duration(0)
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
			ra = time.Duration(secs) * time.Second
		}
	}
	return &retry.StatusError{Code: resp.StatusCode, RetryAfter: ra}
}
```
Wrap the revision-manifest fetch in `Fetch` with `retry.Do`:
```go
func (f *HFFetcher) Fetch(ctx context.Context, destDir string) error {
	apiURL := fmt.Sprintf("%s/api/models/%s/revision/%s", f.baseURL, f.repo, f.rev)
	var rev revisionResp
	err := retry.Do(ctx, f.Policy, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			return err
		}
		resp, err := f.hc.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return hfStatus(resp)
		}
		return json.NewDecoder(resp.Body).Decode(&rev)
	})
	if err != nil {
		return fmt.Errorf("hf: revision %s: %w", f.rev, err)
	}
	for _, s := range rev.Siblings {
		if err := f.fetchFile(ctx, destDir, s.Rfilename); err != nil {
			return err
		}
	}
	return nil
}
```
Wrap the per-file GET+write in `fetchFile` the same way — the entire download-to-temp-then-rename body goes inside `retry.Do(ctx, f.Policy, func() error { ... })`, returning `hfStatus(resp)` on non-200 and propagating `hc.Do`/copy errors (so a mid-file truncation → `io.ErrUnexpectedEOF` → transient; the temp file is discarded on each failed attempt before retry). Keep the `filepath.IsLocal` path-traversal guard OUTSIDE the retry closure.

- [ ] **Step 4: Run — verify it passes**

Run: `go test ./internal/agent/enrich/sidecar/ 2>&1 | tail -5`
Expected: all sidecar tests pass, including the new `TestHFFetcher*`.

- [ ] **Step 5: Commit**
```bash
git add internal/agent/enrich/sidecar/hf.go internal/agent/enrich/sidecar/hf_test.go
git commit -m "feat(hf): retry the model download with backoff on transient faults

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Docs

**Files:** Modify `AGENTS.md`.

- [ ] **Step 1:** In the conventions/gotchas, add one line: outbound dependency pulls should use `internal/retry` (`retry.Do` + the canonical `IsTransient` classifier; env-tunable `KELD_RETRY_*`); the HF model download uses it; settings-poll/publish/api adopt it when next touched. Note unknown errors are treated permanent by design.
- [ ] **Step 2: Commit**
```bash
git add AGENTS.md
git commit -m "docs: internal/retry is the standard for dependency-pull retries

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review
- Spec coverage: reusable package w/ Policy+env+classifier+backoff+Retry-After (T1) ✓; HF adoption retry-transient/fail-fast-permanent (T2) ✓; adopt-as-touched documented (T3) ✓.
- Placeholders: none — full code given; the T2 stub-wiring note is guidance atop concrete assertions, not a vague TODO.
- Type consistency: `Policy`, `StatusError`/`HTTPStatus`, `Do`/`DoClassify`, `IsTransient`, `backoff` names match across T1 def + T1 tests + T2 usage. `HFFetcher.Policy` set in `NewHFFetcher` and injected in tests.
- Tests don't sleep real seconds (ms delays + Jitter:false; pure `backoff` tested directly; ctx-cancel test uses a goroutine cancel with an elapsed-time bound).
