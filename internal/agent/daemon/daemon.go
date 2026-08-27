// Package daemon wires the enrichment components and runs the keld-agent server.
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/clientevents/resource"
	"github.com/ncx-ai/keld-signal/internal/agent/creds"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/lenstat"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/promptlog"
	"github.com/ncx-ai/keld-signal/internal/agent/provision"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/agent/resolve"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/agent/watch"
	"github.com/ncx-ai/keld-signal/internal/auth"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/hook"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/retry"
	"github.com/ncx-ai/keld-signal/internal/spool"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// errQueueFull signals a spool drain to keep the file for the next sweep.
var errQueueFull = errors.New("queue full")

// Sender publishes an enrichment (real publisher or a test fake).
type Sender interface {
	Send(publish.Enrichment) error
}

// Worker consumes jobs, resolves text, enriches, and publishes. It is
// panic-isolated per job so one bad prompt never kills the daemon.
// ready is a readiness gate: Worker blocks before processing each job until
// ready() returns true. When ready() is false, warmup (if non-nil) is invoked
// to actively trigger the model load (e.g. the sidecar's on-demand load on
// first inference) instead of passively waiting for something else to warm
// it — the model never warms itself if nothing ever calls it. warmup is bound
// by warmWait and runs OUTSIDE the job's inference deadline; a nil warmup
// falls back to the legacy bounded passive wait (waitWarm), which the block
// exits promptly if the queue is closed while waiting.
//
// ctx is the daemon-lifetime context; each job runs under a child context that
// is cancelled on timeout so a hung/slow enrichment's in-flight sidecar calls
// are reclaimed (not left retrying forever — the death-spiral root cause).
// Worker drains the enrichment queue. The trailing variadic customHolder is an
// optional, backward-compatible hook: the daemon passes the live custom-pass
// holder (hot-swapped by the settings poll); existing callers/tests omit it.
func Worker(ctx context.Context, q *queue.Queue, m enrich.Model, svc serviceFacets, pub Sender, actor string, includeEntityText func() bool, ready func() bool, warmup func(context.Context) error, emitter *clientevents.Emitter, ra *reauther, custom ...*customHolder) {
	ledger := newRetryLedger()
	var holder *customHolder
	if len(custom) > 0 {
		holder = custom[0]
	}
	// One tracker for the daemon's lifetime: it accumulates the prompt-length
	// distribution across jobs (and restarts, via its persisted state) so
	// truncation converges on this machine's actual prompt sizes.
	lens := lenstat.FromEnv(paths.PromptLengthsPath())
	for {
		j, ok := q.Next()
		if !ok {
			return
		}
		if !ready() {
			ww := warmWait()
			warmedInTime := false
			if warmup != nil {
				// Trigger the sidecar's on-demand model load and wait for it,
				// bounded by ww and OUTSIDE the job's inference deadline.
				wctx, wcancel := context.WithTimeout(ctx, ww)
				err := warmup(wctx)
				wcancel()
				warmedInTime = err == nil
			} else {
				// No warmer wired (e.g. tests): bounded passive wait.
				warm, closed := waitWarm(ready, ww, q.Done())
				if closed {
					return // queue closed during the wait; discard in-hand job and exit.
				}
				warmedInTime = warm
			}
			if !warmedInTime {
				// Model not resident in time — DEFER (re-spool) WITHOUT consuming
				// the retry budget; "not ready yet" is never "un-enrichable".
				if err := spool.Write(pointerFromJob(j)); err != nil {
					log.Printf("keld-agent: job %s deferred (model not ready) and re-spool failed: %v", j.Key(), err)
				} else {
					log.Printf("keld-agent: job %s deferred — model not ready after %s, re-spooled", j.Key(), ww)
				}
				continue
			}
		}
		to := jobTimeout()
		// Per-job context: cancelling it aborts the job's in-flight sidecar calls
		// (client.WithContext) so a timed-out attempt stops consuming the
		// single-flight sidecar instead of leaking a retry loop.
		jobCtx, cancel := context.WithCancel(ctx)
		jobModel := withJobCtx(m, jobCtx)
		var published bool
		// Snapshot the live custom passes once per job (consistent even if the
		// settings poll swaps mid-job).
		var copts []enrich.Option
		if w1, w2 := holder.load(); len(w1) > 0 || len(w2) > 0 {
			copts = []enrich.Option{enrich.WithCustomExtractors(w1, w2)}
		}
		finished := runWithTimeout(to, func() {
			published = process(jobCtx, j, jobModel, svc, pub, actor, includeEntityText, emitter, ra, lens, copts...)
		})
		cancel() // always: on timeout this reclaims the abandoned attempt; on success it just releases resources.

		if finished {
			ledger.reset(j.Key())
			// Mark the key done so a later duplicate (same prompt via the hook AND
			// the transcript watcher) is deduped — but only on a real publish, so a
			// job that couldn't resolve its text stays re-offerable for the watcher.
			if published {
				q.Complete(j)
			}
			continue
		}
		// The job exceeded its deadline (sidecar reloading/overloaded). Re-spool
		// so it retries on GLiNER2 later (there is no other backend to fall
		// through to; never lose it) and move on so one stuck job can't wedge
		// the single worker —
		// but bound the retries: after maxAttempts, quarantine it so a genuinely
		// un-enrichable job can't loop forever (the amplification that saturated
		// the sidecar). Atlas dedups on dedup_key, so a late double-publish from
		// a recovering attempt is harmless.
		je := newJobEmit(emitter, j)
		if ledger.exhausted(j.Key(), maxAttempts()) {
			je.Emit("job.retry_exhausted", clientevents.SevWarn, map[string]any{
				"attempts":  maxAttempts(),
				"timeout_s": to.Seconds(),
			})
			if err := spool.Quarantine(pointerFromJob(j)); err != nil {
				log.Printf("keld-agent: job %s exhausted retries and quarantine failed: %v", j.Key(), err)
				je.Emit("job.quarantined", clientevents.SevError, map[string]any{"error": clientevents.RedactError(err)})
			} else {
				log.Printf("keld-agent: job %s exceeded %s on %d attempts — quarantined", j.Key(), to, maxAttempts())
				je.Emit("job.quarantined", clientevents.SevWarn, map[string]any{"attempts": maxAttempts()})
			}
			continue
		}
		if err := spool.Write(pointerFromJob(j)); err != nil {
			log.Printf("keld-agent: job %s exceeded %s and re-spool failed: %v", j.Key(), to, err)
			je.Emit("job.respool_failed", clientevents.SevError, map[string]any{
				"error":     clientevents.RedactError(err),
				"timeout_s": to.Seconds(),
			})
		} else {
			log.Printf("keld-agent: job %s exceeded %s, re-spooled for retry", j.Key(), to)
		}
	}
}

// jobEmit wraps a clientevents.JobEmitter so job-scoped emit sites can call
// Emit unconditionally even when the daemon's Emitter is nil — several
// existing tests exercise Worker/process without wiring client events, and a
// nil *clientevents.JobEmitter (from calling WithJob on a nil Emitter) would
// panic if Emit were invoked on it directly.
type jobEmit struct{ je *clientevents.JobEmitter }

// newJobEmit builds a jobEmit for job j, tolerating a nil emitter.
func newJobEmit(emitter *clientevents.Emitter, j queue.Job) jobEmit {
	if emitter == nil {
		return jobEmit{}
	}
	return jobEmit{je: emitter.WithJob(j.SessionID, j.PromptID)}
}

// Emit is a no-op when the wrapped emitter is nil; otherwise it stamps j's
// session/prompt ids and forwards to the parent Emitter's gate.
func (j jobEmit) Emit(code string, sev clientevents.Severity, fields map[string]any) {
	if j.je == nil {
		return
	}
	j.je.Emit(code, sev, fields)
}

// retryLedger counts per-job re-spool attempts so the worker can cap them. It is
// owned by the single Worker goroutine, so it needs no locking.
type retryLedger struct{ n map[string]int }

func newRetryLedger() *retryLedger { return &retryLedger{n: map[string]int{}} }

// exhausted records one failed attempt for key and reports whether the job has
// reached max attempts. On exhaustion the counter is cleared so a job that is
// later re-delivered (e.g. after a daemon restart) gets a fresh budget.
func (r *retryLedger) exhausted(key string, max int) bool {
	r.n[key]++
	if r.n[key] >= max {
		delete(r.n, key)
		return true
	}
	return false
}

// reset clears a job's attempt count (called when it finally succeeds).
func (r *retryLedger) reset(key string) { delete(r.n, key) }

// withJobCtx binds m to a per-job context when it is the sidecar client, so
// the job's timeout can cancel its in-flight calls. Any other Model (nil when
// the readiness gate never opens, or a test fake) has no network calls to
// cancel, so it passes through unchanged.
func withJobCtx(m enrich.Model, ctx context.Context) enrich.Model {
	if c, ok := m.(*sidecar.Client); ok {
		return c.WithContext(ctx)
	}
	return m
}

// bindMaxLen binds an input token cap to the sidecar client for this job's
// inferences, bounding each one's transient activation memory (the cap is
// derived adaptively — see enrich/lenstat). n <= 0 means "no cap" and leaves the
// model untouched; a non-sidecar Model (test fake, eval harness) has nothing to
// cap and passes through. Returns a copy, so the job context bound by
// withJobCtx is preserved.
func bindMaxLen(m enrich.Model, n int) enrich.Model {
	if n <= 0 {
		return m
	}
	if c, ok := m.(*sidecar.Client); ok {
		return c.WithMaxLen(n)
	}
	return m
}

// warmupFunc returns a warmup trigger bound to the sidecar client, or nil when
// m is not the sidecar client (nothing to warm — e.g. a test fake or the eval
// model). The daemon passes this to Worker as its warmup seam.
func warmupFunc(m enrich.Model) func(context.Context) error {
	c, ok := m.(*sidecar.Client)
	if !ok {
		return nil
	}
	return c.Warmup
}

// jobTimeout is a WEDGE BACKSTOP, not the operating bound: enrichment is
// bounded per pass (enrich.DefaultPassTimeout), which is the correct unit
// because a job issues 8-9 inferences. It exists only to catch a job wedged
// somewhere outside a pass (resolve, publish) and must stay above the worst
// case of every pass burning its full deadline — otherwise it pre-empts the
// per-pass deadlines and resurrects the old failure mode, where a job-wide
// expiry discarded every pass that had already succeeded and re-spooled the
// job, redoing and re-discarding the same work until the attempt budget ran
// out. That amplification kept the sidecar in permanent burst and drove the RAM
// oscillation. Default 5m (> 8 passes x 30s); override with
// KELD_ENRICH_JOB_TIMEOUT (Go duration).
func jobTimeout() time.Duration {
	if v := os.Getenv("KELD_ENRICH_JOB_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 5 * time.Minute
}

// warmWait bounds how long the worker waits for the sidecar model to become
// resident before deferring a job (re-spool, no retry attempt consumed).
// Default 120s (a cold model load plus headroom); override with
// KELD_ENRICH_WARM_WAIT (Go duration).
func warmWait() time.Duration {
	if v := os.Getenv("KELD_ENRICH_WARM_WAIT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 120 * time.Second
}

// waitWarm blocks until ready() is true (warm=true), bound elapses
// (warm=false, closed=false), or done is closed (warm=false, closed=true). It
// polls ready on a short interval; ready is expected to be a cheap gate read.
func waitWarm(ready func() bool, bound time.Duration, done <-chan struct{}) (warm, closed bool) {
	if ready() {
		return true, false
	}
	deadline := time.NewTimer(bound)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return false, true
		case <-deadline.C:
			return false, false
		case <-tick.C:
			if ready() {
				return true, false
			}
		}
	}
}

// bindAddr is the daemon's listen address. Default is loopback on an ephemeral port
// (the historical behavior, with the port published via agent.json). Service
// deployments set KELD_AGENT_BIND to a fixed address so the plugin can reach it from
// another host.
func bindAddr() string {
	if v := os.Getenv("KELD_AGENT_BIND"); v != "" {
		return v
	}
	return "127.0.0.1:0"
}

// isLoopbackBind reports whether addr's host is "localhost" or an IP that
// parses as loopback. An EMPTY host is deliberately NOT loopback: Go's
// net.Listen treats "" as the wildcard address (all interfaces, IPv4 and
// IPv6) — the same as "0.0.0.0" but wider, since it also covers IPv6 — so
// KELD_AGENT_BIND=":7788" is the idiomatic and widest-possible off-loopback
// bind, not a local one. Getting this backwards would let a
// network-reachable listener start with no operator-supplied secret.
func isLoopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// minServiceSecretLen is the floor for KELD_AGENT_SECRET off loopback, where
// it is the sole access control on an endpoint that accepts prompt text.
const minServiceSecretLen = 32

// serviceSecret resolves the /enrich secret. On loopback the daemon keeps generating
// one into agent.json for the logged-in user. Off loopback there is no logged-in
// human and the secret becomes the sole access control, so it must be supplied
// explicitly and be long enough to be worth having.
func serviceSecret() (string, error) {
	addr := bindAddr()
	if isLoopbackBind(addr) {
		return "", nil // caller falls back to the generated agent.json secret
	}
	s := os.Getenv("KELD_AGENT_SECRET")
	if s == "" {
		return "", fmt.Errorf(
			"keld-agent: KELD_AGENT_BIND=%s is not loopback; KELD_AGENT_SECRET must be set", addr)
	}
	if len(s) < minServiceSecretLen {
		return "", fmt.Errorf(
			"keld-agent: KELD_AGENT_SECRET must be at least %d characters when binding off-loopback", minServiceSecretLen)
	}
	return s, nil
}

// queueCap is the in-memory job queue depth. The old hard-coded 256 overran on a
// single agent burst (20 calls/run x 10 concurrent runs); service deployments raise
// this via KELD_QUEUE_CAP.
func queueCap() int {
	if v := os.Getenv("KELD_QUEUE_CAP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 1024
}

// maxAttempts bounds how many times a timed-out job is re-spooled before it is
// quarantined. Default 4 (a couple of reload/transient windows); override with
// KELD_ENRICH_MAX_ATTEMPTS.
func maxAttempts() int {
	if v := os.Getenv("KELD_ENRICH_MAX_ATTEMPTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 4
}

// runWithTimeout runs fn in a goroutine and reports whether it finished within d.
// The goroutine keeps the worker unblocked on timeout; the caller cancels the
// job context so fn's sidecar calls abort and the goroutine unwinds promptly
// (rather than leaking a live retry loop).
func runWithTimeout(d time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// pointerFromJob rebuilds a spool.Pointer from a queue.Job (inverse of
// ingress.JobFrom) so a timed-out job can be re-spooled for retry.
func pointerFromJob(j queue.Job) spool.Pointer {
	p := spool.Pointer{
		Source:      spool.Source{ID: j.Source, Origin: j.Origin, Version: j.Version},
		Correlation: spool.Correlation{Scheme: j.Scheme, ID: j.ID, SessionID: j.SessionID},
		Pointer:     &spool.Ptr{TranscriptPath: j.TranscriptPath, PromptID: j.PromptID, Cwd: j.Cwd},
	}
	if j.Inline != "" {
		p.Inline = &spool.Inline{Text: j.Inline}
	}
	return p
}

// isAuthError reports whether err is a *retry.StatusError carrying 401 or
// 403 — the daemon's self-heal trigger: the ingest token was rejected (rotated
// or revoked), so the caller should kick a reauther.refresh (cooldown/
// single-flight-guarded, so this is cheap to call on every such error).
func isAuthError(err error) bool {
	var se *retry.StatusError
	return errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden)
}

// process resolves, enriches, and publishes one job. It returns true only when
// the enrichment was actually published — the caller uses that to decide whether
// to mark the job's key deduped. A skip (unresolved text), a deadline-cancelled
// attempt, a publish failure, or a panic all return false so the job stays
// re-offerable (retry / watcher fallback).
func process(ctx context.Context, j queue.Job, m enrich.Model, svc serviceFacets, pub Sender, actor string, includeEntityText func() bool, emitter *clientevents.Emitter, ra *reauther, lens *lenstat.Tracker, customOpts ...enrich.Option) bool {
	je := newJobEmit(emitter, j)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("keld-agent: worker recovered: %v", r)
			panicErr, ok := r.(error)
			if !ok {
				panicErr = fmt.Errorf("%v", r)
			}
			je.Emit("worker.panic", clientevents.SevError, map[string]any{"error": clientevents.RedactError(panicErr)})
		}
	}()
	text, ok := resolve.Resolve(j.Source, j.TranscriptPath, j.PromptID, j.Inline)
	if !ok {
		return false // could not resolve prompt text; skip silently
	}
	// This prompt's own hour is characterised by THIS job, so the tick must not
	// characterise it again — spend inside an overlap would be counted twice.
	// Recorded after the resolve so an unresolvable prompt leaves its hour to the
	// tick; a no-op unless window characterisation is switched on (see tick.go).
	noteTickPrompt(j)
	// Size this job's input truncation from the machine's own prompt-length
	// distribution: record this prompt's length (a count, never its text) and
	// bind the resulting cap for the job's inferences. Without a cap, gliner2
	// applies none, and a long prompt's activation memory is what drove the
	// sidecar's RSS oscillation.
	if lens != nil {
		lens.Observe(lenstat.Words(text))
		if err := lens.Save(); err != nil {
			log.Printf("keld-agent: prompt-length stats not persisted: %v", err)
		}
		m = bindMaxLen(m, lens.Cap())
	}
	meta := enrich.Meta{Repo: j.Cwd, Tool: j.Source}
	if enrich.ContextEligible(j.Source) {
		meta = contextMeta(j)
	}
	// Bound each pass individually and derive those deadlines from the job
	// context, so cancelling the job still aborts whatever pass is in flight.
	// A pass that exceeds its deadline costs only its own facet: the profile
	// comes back "partial" and is still published, so a slow pass never
	// discards the work the other passes already completed.
	opts := append([]enrich.Option{
		enrich.WithJobContext(ctx),
		// Coordinates (never text) for the model-free passes that characterise
		// the window around this prompt rather than its text.
		enrich.WithCoordinates(j.TranscriptPath, j.PromptID),
		// The facts about this job's CHECKOUT that only the daemon can resolve,
		// travelling INTO the analysis rather than into a prompt preamble. This
		// path resolves FRESH per job rather than through the ticker's cache,
		// because it has the real cwd (`j.Cwd`) and because `git_branch` is the
		// field that goes stale — a branch changes often, and a prompt's window
		// is exactly where that matters. Every field is best-effort and an empty
		// result is normal: a project directory is not necessarily a repository.
		// See enrich.ResolvedFacts.
		enrich.WithResolvedFacts(resolvedFacts(j.Cwd)),
	}, customOpts...)
	// Wire the deterministic workstream pass only when this run actually has a
	// window-analysis backend; without one the pass stays unregistered rather
	// than running and failing every job (see facetsFor). The service facets are
	// threaded in rather than derived from m, because ml_backend
	// "deterministic" has an analysis service and no Model at all.
	if svc.Analyze != nil {
		opts = append(opts, enrich.WithWorkstreams(svc.Analyze))
	}
	// The personal-data scan is threaded the same way and for the same reason.
	// Unlike the analyzer its absence does not unregister a pass: sensitivity
	// still runs on its credential layer, and reports itself in facets_degraded
	// so a reader never mistakes "we could not look" for "we looked and it is
	// clean" (see enrich.WithPIIScanner).
	if svc.ScanPII != nil {
		opts = append(opts, enrich.WithPIIScanner(svc.ScanPII))
	}
	profile := enrich.Run(text, j.Source, meta, m, opts...)
	// The job-level backstop only fires for a wedge outside the passes; if it
	// did, the profile is untrustworthy — don't publish it. The worker re-spools
	// (bounded) so it retries on a healthy sidecar.
	if ctx.Err() != nil {
		return false
	}
	// Derived integer only — the resolved prompt's length in code points, never its text.
	promptChars := utf8.RuneCountInString(text)
	e := publish.Build(j, profile, actor, includeEntityText(), promptChars, time.Now())
	if err := pub.Send(e); err != nil {
		log.Printf("keld-agent: publish failed for %s: %v", j.Key(), err)
		je.Emit("publish.failed", clientevents.SevError, map[string]any{"error": clientevents.RedactError(err)})
		// A 401/403 means the ingest token was rotated/revoked out from under
		// us — kick the reauther so the retried job (existing re-spool path
		// above, in Worker) has a shot at a fresh token. refresh is cooldown +
		// single-flight guarded, so calling it on every auth failure is cheap;
		// ra is nil in tests that don't care about self-heal, so guard it.
		if ra != nil && isAuthError(err) {
			ra.refresh(ctx)
		}
		return false
	}
	// Successful publish is positive proof auth works — clear any stale marker.
	if ra != nil {
		ra.noteAuthOK()
	}
	// Log successes too (not just failures) so "are enrichments reaching Atlas"
	// is answerable from the daemon log — silent success made this hard to tell
	// apart from a broken pipeline.
	log.Printf("keld-agent: published enrichment for %s", j.Key())
	return true
}

// isRegularFile returns true if p exists and is a regular file (not a directory
// or other special file).
func isRegularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// resolveSidecar probes dir for the sidecar binary in both the flat layout
// (Windows Inno flattens it) and the one-dir subdir layout (macOS .pkg /
// Linux install.sh keep the subdirectory):
//
//	flat:   dir/keld-agent-sidecar[.exe]
//	nested: dir/keld-agent-sidecar/keld-agent-sidecar[.exe]
//
// Returns the path and true if a regular file is found; ("", false) otherwise.
func resolveSidecar(dir string) (string, bool) {
	name := "keld-agent-sidecar"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	for _, c := range []string{
		filepath.Join(dir, name),
		filepath.Join(dir, "keld-agent-sidecar", name),
	} {
		if isRegularFile(c) {
			return c, true
		}
	}
	return "", false
}

// sidecarBinPath returns the path to the sidecar binary and whether it exists.
// Resolution order:
//
//	(1) KELD_SIDECAR_BIN env — returned only if it is a regular file (a
//	    directory is rejected so a one-dir PyInstaller bundle is never mistaken
//	    for the binary).
//	(2) resolveSidecar beside os.Executable() — handles both flat (Windows)
//	    and one-dir subdir layouts (macOS / Linux).
//	(3) resolveSidecar on each per-OS well-known base directory.
func sidecarBinPath() (string, bool) {
	if p, ok := sidecarBinFromEnv(); ok {
		return p, true
	}
	// (2) beside the running keld-agent executable (how the installers lay it out).
	if exe, err := os.Executable(); err == nil {
		if p, ok := resolveSidecar(filepath.Dir(exe)); ok {
			return p, true
		}
	}
	// (3) per-OS well-known fallback.
	for _, base := range wellKnownSidecarDirs() {
		if p, ok := resolveSidecar(base); ok {
			return p, true
		}
	}
	return "", false
}

// sidecarBinFromEnv resolves the KELD_SIDECAR_BIN override (resolution step 1).
// It returns the path and true only when the env var is set AND points at a
// regular file. An unset var, a nonexistent path, or a directory (e.g. the
// one-dir PyInstaller bundle root) all yield ("", false) so the caller falls
// through to the beside-executable and well-known-dir probes.
func sidecarBinFromEnv() (string, bool) {
	if p := os.Getenv("KELD_SIDECAR_BIN"); p != "" {
		if isRegularFile(p) {
			return p, true
		}
	}
	return "", false
}

// wellKnownSidecarDirs returns per-OS base directories that are fed to
// resolveSidecar. Each directory is a parent that may contain the sidecar in
// either the flat or one-dir subdir layout.
func wellKnownSidecarDirs() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return []string{"/usr/local/keld", "/usr/local/bin", filepath.Join(home, ".local/bin")}
	case "windows":
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			return []string{filepath.Join(la, "Programs", "keld")}
		}
		return nil
	default: // linux
		return []string{filepath.Join(home, ".local/bin"), "/usr/local/bin", "/usr/local/keld"}
	}
}

// Run starts the daemon: ingress on loopback, worker, agent.json discovery file.
//
// An unconfigured agent IDLES rather than failing: the service is routinely
// registered before onboarding runs (the documented macOS pkg order), so this
// waits for hook.json and starts the moment onboarding writes it. See
// awaitConfig for why exiting here was wrong.
func Run(ctx context.Context) error {
	cfg, err := awaitConfig(ctx, hook.LoadConfig, configPollInterval(), func() {
		log.Printf("keld-agent: not configured yet — idling until `keld login` + `keld signal setup` "+
			"write %s; no restart needed once they do", paths.HookConfigPath())
	})
	if err != nil {
		return nil // context cancelled while idling: a clean shutdown, not a failure
	}

	secret, err := agentcfg.NewSecret()
	if err != nil {
		return err
	}
	set := settings.Load()
	actor := ""
	if m, err := config.LoadManifest(); err == nil && m != nil && m.Actor != nil {
		actor = *m.Actor
	}
	// Org comes from the login-time auth store; a manifest-less actor falls
	// back to the auth principal. Non-fatal: an unreadable/absent auth.json
	// just leaves Org "" (and Actor unresolved) — client events still flow,
	// with reduced correlation.
	var org string
	if a, aerr := auth.Load(); aerr == nil && a != nil {
		org = a.Org
		if actor == "" {
			actor = a.Principal
		}
	}
	installID, _ := clientevents.InstallID() // non-fatal: "" just weakens cross-run correlation
	base := clientevents.Corr{
		Org:       org,
		Actor:     actor,
		InstallID: installID,
		RunID:     newRunID(),
		Version:   version.CLI,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	emitter := clientevents.NewEmitter(base, 1024)

	// Client-events gate/thresholds default ON immediately — BEFORE any emit
	// (daemon.start below, the pre-poll sidecar.unavailable inside mlBackend, ...)
	// so early startup events aren't dropped by the emitter's zero-value
	// (disabled) gate. This also means telemetry works even if the settings
	// fetch never completes (Atlas unreachable, or an Atlas predating the
	// /v1/enrichment-settings client_telemetry block). The settings poll below
	// narrows/widens this on each successful fetch; a fetch error leaves it
	// exactly as-is (never closes the gate).
	eff := (*settings.ClientTelemetry)(nil).WithDefaults()
	emitter.SetGate(gateFrom(eff))
	var gaugesEnabled atomic.Bool
	gaugesEnabled.Store(eff.GaugesEnabled)

	// tok is the shared, live-swappable ingest token: publish/settings/reporter
	// all read it through tok.Get rather than capturing a static string, so a
	// later self-heal re-auth (a future task) can rotate it in one place via
	// tok.Set and have every consumer observe the new value immediately.
	tok := creds.NewToken(cfg.IngestToken)

	// ra is the self-heal reauther: publish (process) and settings poll
	// trigger ra.refresh on a 401/403 so a rotated/revoked ingest token is
	// re-fetched (via the still-valid CLI token) with no daemon restart. Its
	// startupEndpoint is cfg.Endpoint so a successful refresh can warn if
	// Onboarding now reports a *different* endpoint — refresh only swaps the
	// token, not the endpoint, so that case still needs a restart to adopt.
	ra := newReauther(tok, emitter)
	ra.startupEndpoint = cfg.Endpoint

	q := queue.New(queueCap())
	pub := publish.New(enrichEndpoint(cfg.Endpoint), tok.Get, actor)

	addr := bindAddr()
	if svcSecret, err := serviceSecret(); err != nil {
		return err
	} else if svcSecret != "" {
		secret = svcSecret // overrides the generated agent.json secret in service mode
		if os.Getenv("KELD_AGENT_TLS_TERMINATED") == "" {
			log.Printf("keld-agent: WARNING binding %s off-loopback with no TLS termination declared; "+
				"the /enrich secret is the only control on this listener", addr)
		}
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := agentcfg.Write(agentcfg.Info{Port: port, Secret: secret}); err != nil {
		return err
	}
	log.Printf("keld-agent: listening on %s", ln.Addr().String())
	// EmitExempt: daemon.start is SevInfo but must surface even under the
	// default warn floor (lifecycle narrative), and it fires once here before
	// any poll could lower the floor — a plain Emit would always drop it.
	emitter.EmitExempt("daemon.start", clientevents.SevInfo, map[string]any{"port": port})

	// Decide once, at startup, whether enrichment runs at all (ml_backend is a
	// local, startup-only setting — never re-read at runtime, see
	// settings.Settings.MLEnabled). When disabled, handler is the
	// accept-and-discard /enrich endpoint and there is no model/gate/Worker to
	// start at all. When enabled, mlBackend provisions+supervises the sidecar
	// (never a deterministic fallback — see mlBackend's doc comment) and
	// handler is the normal ingress.Handler bound to q.
	// live is built BEFORE wireEnrichment so the PII region tier the enrichment
	// facets resolve is the LIVE one — local base now, org override from the
	// first successful settings poll onwards. Binding it after would freeze the
	// facets on the startup value.
	live := settings.NewLive(set)

	handler, model, svc, gate, warmup, enrichmentEnabled := wireEnrichment(ctx, set, secret, q, emitter, live.PIIRegions)
	pollInterval := 5 * time.Minute
	if v := os.Getenv("KELD_SETTINGS_POLL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			pollInterval = d
		}
	}

	flushInterval := 30 * time.Second
	if v := os.Getenv("KELD_CLIENTEVENTS_FLUSH"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			flushInterval = d
		}
	}
	reporter := clientevents.NewReporter(signalClientEventsEndpoint(cfg.Endpoint), tok.Get, installID, emitter.Drain, paths.ClientEventsSpoolDir())
	go reporter.Run(ctx, flushInterval)

	sampleInterval := 10 * time.Second
	if v := os.Getenv("KELD_CLIENTEVENTS_SAMPLE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			sampleInterval = d
		}
	}
	gaugeEmit := func(f map[string]any) {
		if gaugesEnabled.Load() {
			emitter.EmitGauge("resource.gauge", f)
		}
	}
	watcher := resource.NewWatcher(emitter.Emit, gaugeEmit, thresholdsFrom(eff), resource.NewProcessTreeSampler(os.Getpid()), time.Now)
	go watcher.Run(ctx, sampleInterval)

	// custom holds the org's live custom enrichment passes, rebuilt on each
	// successful settings poll and read per-job by the Worker.
	custom := newCustomHolder()
	// onRemote runs on EVERY successful poll, so the reporter is what keeps a static reject
	// from being restated on each one. Owned out here (not per-call) so it remembers across
	// polls; pollSettings drives onRemote from a single goroutine, so no lock is needed.
	rejects := &rejectReporter{}
	onRemote := func(r *settings.Remote) {
		re := r.ClientTelemetry.WithDefaults()
		emitter.SetGate(gateFrom(re))
		watcher.SetThresholds(thresholdsFrom(re))
		gaugesEnabled.Store(re.GaugesEnabled)

		// Rebuild + hot-swap the org's custom passes. Built-in/unsupported
		// passes are rejected here (never as extractors); surface each as a
		// client-telemetry warning without leaking prompt content — once per
		// distinct reject set, not once per poll (see rejectReporter).
		syncCustomPasses(r.EnrichmentSchema, custom, rejects, emitter.Emit)
	}
	go pollSettings(ctx, settings.NewClient(settingsEndpoint(cfg.Endpoint), tok.Get, 10*time.Second), live, pollInterval, emitter, onRemote, ra)
	if enrichmentEnabled {
		// warmup comes from wireEnrichment, not from warmupFunc(model): it is
		// the composition of on-demand provisioning with the model load, and
		// only the backend that owns the weights can build it.
		// The tick characterises the work no prompt's look-back reaches — measured,
		// 43-45 points of it. OFF by default because such a row joins to nothing at
		// Atlas yet; see tick.go's envTick for the whole of that reasoning. Started
		// BEFORE the worker so the observer is in place for the first job.
		setTickObserver(startTicker(ctx, svc.Tick, pub, actor, emitter))
		// v2's block emitter, and the reason it sits beside the tick rather than
		// inside it: a block reaches nowhere, so it needs none of the tick's
		// frontier reasoning about which future prompts might sweep over a
		// moment. OFF by default (KELD_BLOCKS) for the same reason the tick is —
		// Atlas stores blocks now but nothing reads them yet. Returns nil when
		// off, which setBlockAdvance takes as "no observer".
		setBlockAdvance(startBlockEmitter(ctx, svc.Blocks, cfg.Endpoint, tok.Get, actor, emitter, set.Blocks))
		// THE SIGNAL-EMBEDDINGS PATH: the client-side training corpus for
		// future-work prediction. svc.Features is non-nil ONLY under
		// ml_backend "deterministic" (see deterministicBackend), so this is
		// also the registration condition that keeps the subsystem absent
		// under "auto". Both toggles default OFF and both are read live, so
		// the goroutines start and take nothing until something switches them
		// on — which is what lets an org enable it without a restart.
		setFeatureAdvance(startFeatureEmitter(ctx, svc.Features, cfg.Endpoint, tok.Get,
			actor, installID, live.FeaturesEnabled, live.FeaturesPublishEnabled, emitter))
		go Worker(ctx, q, model, svc, pub, actor, live.IncludeEntityText, gate, warmup, emitter, ra, custom)
	}

	// Drain enrich pointers the hook spooled while the daemon was down, then keep
	// sweeping for ones spooled during brief unavailability. Idempotent:
	// delete-after-enqueue + Atlas dedups on dedup_key. Only when enrichment is
	// enabled: with it disabled there's no Worker to consume the queue, so
	// draining/sweeping would just re-enqueue pointers nobody processes.
	if enrichmentEnabled {
		if n, err := spool.ImportLegacy(); err != nil {
			log.Printf("keld-agent: legacy spool import failed: %v", err)
		} else if n > 0 {
			log.Printf("keld-agent: imported %d legacy spool records", n)
		}
		drainEnrichSpool(q, emitter)

		sweepIv := 30 * time.Second
		if v := os.Getenv("KELD_SPOOL_SWEEP"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				sweepIv = d
			}
		}

		// spool.depth gets its own, deliberately slower ticker rather than
		// riding the sweep ticker above. The Emitter's ring coalesces
		// same-code+same-severity events (emitter.go's insert), keeping
		// only the FIRST snapshot's fields and bumping a count — so any
		// gauge tick landing between two reporter flushes (flushInterval,
		// KELD_CLIENTEVENTS_FLUSH, default 30s) reports a stale
		// rows/bytes/oldest_age_s for that interval. resource.gauge avoids
		// this the same way: GaugeIntervalS defaults to 300s against a 30s
		// flush default, a 10x margin. Mirror that ratio here rather than
		// tying the gauge to KELD_SPOOL_SWEEP — which exists to control
		// resync/drain/eviction-check freshness and, if ever tuned faster
		// for finer backlog visibility, would shrink this margin to zero
		// again. Do NOT re-couple these two cadences.
		gaugeIv := 300 * time.Second
		if v := os.Getenv("KELD_SPOOL_GAUGE_INTERVAL"); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d > 0 {
				gaugeIv = d
			}
		}

		go runSweep(ctx, q, emitter, sweepIv, gaugeIv)
	}

	// Transcript watcher: the hook-free capture trigger. Tails Claude Code and
	// Cowork transcripts and offers pointers into the SAME queue the hook feeds,
	// so surfaces that don't fire the command hook (Cowork; Claude Code in the
	// Desktop app) still enrich. Only when enrichment is enabled (no Worker to
	// consume the queue otherwise) and not explicitly disabled.
	if enrichmentEnabled && watch.EnabledFromEnv() {
		// Host-side telemetry: for captured sources whose own OTEL can't reach Keld
		// (Cowork's sandbox blocks egress to atlas.keld.co), the daemon mirrors the
		// transcript's events into OTLP logs+metrics itself — from the host,
		// unrestricted egress — so watched sources reach Atlas with the same
		// footprint the CLI's native OTEL provides. Claude Code is excluded by
		// default (it emits its own OTEL host-side). The watcher's observe hook
		// feeds every new transcript line to the telemetry; offer handles enrichment.
		tel := promptlog.New(logsEndpoint(cfg.Endpoint), metricsEndpoint(cfg.Endpoint), tok.Get, promptlog.SourcesFromEnv())
		offer := func(p spool.Pointer) { q.Offer(ingress.JobFrom(p)) }
		observe := func(source, path string, line []byte) { tel.Observe(source, path, line) }
		txw := watch.New(offer, observe, version.CLI, watch.PollFromEnv(), watch.BackfillFromEnv())
		// Third use of the same detection: the watcher already knows when a
		// transcript grew, so it tells the sidecar, which brings its
		// reference-series store up to date from its own byte offset. That is
		// what keeps /analyze a query instead of a parse — and, more to the
		// point, keeps the parse off an enrichment job's per-pass deadline: a
		// first whole-file ingest measured 5.1s inside an /analyze request.
		// Fire-and-forget and never on this loop's critical path; the sidecar
		// deliberately does not poll for growth. See ingestSignalHook for the
		// scoping and the drop policy, and note /analyze keeps its own on-demand
		// ingest as the backstop, so a dropped signal costs latency only.
		if svc.SignalIngest != nil {
			txw = txw.WithIngestSignal(ingestSignalHook(ctx, svc.SignalIngest))
		}
		go txw.Run(ctx)
	}

	err = serve(ctx, ln, handler, q, emitter)

	// ⚠️ SHUTDOWN IS NOT DONE WHEN serve() RETURNS, and pretending otherwise is
	// what made the supervisor's kill path unreachable. serve returns as soon
	// as ctx is cancelled and the listener closes — microseconds — while the
	// supervisor goroutine is still SIGTERMing the sidecar and waiting for its
	// ~2.9 GB GLiNER2 worker and ~1.7-2.3 GB encoder child to exit. Returning
	// here exits the process mid-reap and leaves precisely the orphans this
	// whole path exists to prevent. So the daemon holds itself open for the
	// stop, and only for the stop: the wait is bounded by the supervisor's own
	// grace plus a second of slack, and expiring it just means we hand the
	// remainder to the service manager rather than hanging.
	if svc.AwaitSidecarStop != nil {
		svc.AwaitSidecarStop()
	}
	return err
}

// drainEnrichSpool drains queued spool pointers into q, offering each as an
// ingress job. Idempotent and safe to call repeatedly (at startup and on
// every sweep tick): a row is deleted only once its offer to q succeeds, and
// a full queue leaves the row in place for the next call to retry.
func drainEnrichSpool(q *queue.Queue, emitter *clientevents.Emitter) {
	if _, err := spool.Drain(func(p spool.Pointer) error {
		// TakenOn, not "accepted": a DUPLICATE row must be deleted too. The
		// prompt is already queued or already published, so keeping the file
		// would re-offer it on every sweep forever — it can never become
		// acceptable — and the spool would grow without bound. Only real
		// backpressure (Full/Closed) keeps the row.
		if q.Offer(ingress.JobFrom(p)).TakenOn() {
			return nil
		}
		return errQueueFull // queue full: keep the file, retry next sweep
	}); err != nil {
		// Pre-branch, spool.Drain always returned nil; it now surfaces real
		// backend errors (open() failing, a wedged db.Query) and under the
		// completeness SLO a silently-discarded one here means every future
		// sweep becomes a no-op forever — no drains, and (since Stats()
		// shares the same failing backend) no spool.depth gauge either, i.e.
		// no signal at all that anything is wrong. Log it like Resync/
		// ImportLegacy's errors nearby, and also surface it as a client
		// event: a local log alone doesn't reach the platform dashboard.
		log.Printf("keld-agent: spool drain failed: %v", err)
		emitter.Emit("spool.drain_failed", clientevents.SevError,
			map[string]any{"error": clientevents.RedactError(err)})
	}
}

// runSweep is the daemon's periodic spool-maintenance loop, extracted
// verbatim from Run's former inline sweep goroutine so it can be driven
// directly by a test with no credentials and millisecond intervals. Every
// dependency arrives as a parameter rather than via an enclosing closure.
//
// On sweepIv it resyncs the in-memory byte total from the table, drains the
// spool into q, and checks Evicted() for a delta since the last check. On
// the independent, slower gaugeIv it reports a spool.depth backlog
// snapshot (rows/bytes/oldest_age_s). The two cadences are deliberately
// decoupled — see gaugeIv's computation in Run — and this loop must not
// re-couple them. Returns (stopping both tickers via their deferred Stop)
// when ctx is done.
func runSweep(ctx context.Context, q *queue.Queue, emitter *clientevents.Emitter, sweepIv, gaugeIv time.Duration) {
	lastEvicted := int64(0)

	t := time.NewTicker(sweepIv)
	defer t.Stop()
	gt := time.NewTicker(gaugeIv)
	defer gt.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Resync the in-memory byte total from the table before
			// draining: the hook (cmd/keld) is a separate, short-lived
			// process that writes to this same spool.db, so this
			// long-lived daemon's total can't otherwise observe rows
			// the hook inserted since the daemon started (or since the
			// last sweep) — left unresynced that drift is
			// one-directional (the daemon's total understates the
			// table), so evictFor never trips and the spool grows past
			// its configured budget. This bounds the drift to one
			// sweep interval; the aggregate it runs is the same one
			// open() already runs once at startup (~9.5ms/50k rows),
			// negligible at this cadence.
			if err := spool.Resync(); err != nil {
				log.Printf("keld-agent: spool byte-total resync failed: %v", err)
			}
			drainEnrichSpool(q, emitter)

			// Evictions are the opposite of a gauge: dropped rows are
			// enrichment data that is gone, not merely late, so this
			// stays a real warn-level anomaly, checked every sweep
			// tick (not the slower gauge tick) and reported as a delta
			// since the last check.
			if n := spool.Evicted(); n > lastEvicted {
				emitter.Emit("spool.evicted", clientevents.SevWarn, map[string]any{"dropped": n - lastEvicted})
				lastEvicted = n
			}
		case <-gt.C:
			// Backlog visibility: depth/bytes/oldest-age is a gauge —
			// a deep backlog is a designed steady state under agent
			// load, not a problem, so it rides EmitGauge (info,
			// floor-exempt) the same way resource.gauge does, rather
			// than a plain Emit that a warn-and-above gate would
			// silently drop.
			if st, err := spool.Stats(); err == nil {
				oldestAgeS := 0.0
				if st.OldestUnixNano != 0 {
					oldestAgeS = time.Since(time.Unix(0, st.OldestUnixNano)).Seconds()
				}
				emitter.EmitGauge("spool.depth", map[string]any{
					"rows":         st.Rows,
					"bytes":        st.Bytes,
					"oldest_age_s": oldestAgeS,
				})
			} else {
				// Same backend as Drain above: a wedged spool fails Stats the
				// same way it fails Drain, and this gauge is otherwise the
				// only remaining signal once a sweep goes silent. Log rather
				// than swallow — spool.drain_failed above already covers the
				// client-event side for the shared underlying failure.
				log.Printf("keld-agent: spool stats failed: %v", err)
			}
		}
	}
}

// wireEnrichment decides, once at startup, whether enrichment runs at all
// (ml_backend is a local, startup-only setting — see settings.Settings) and
// returns everything Run needs to wire it up:
//
//   - handler: the /enrich HTTP handler to serve — the real ingress.Handler
//     bound to q when enabled, or ingress.DiscardHandler (202, never
//     enqueues) when disabled, so the hook stops spooling pointers that would
//     never be processed.
//   - model: the enrichment Model, nil when ml_backend="deterministic" (that
//     mode asks the service for no inference) or when enrichment is disabled
//     entirely.
//   - svc: the service facets (the non-inference sidecar routes — /analyze
//     for workstreams, /pii for sensitivity). Wired in BOTH "auto" and
//     "deterministic", because neither needs a model — they are derived from
//     the service client, not from the Model, which is why they are returned
//     separately rather than left to facetsFor(model).
//   - gate: the Worker readiness gate. nil ONLY when enrichment is disabled
//     (Run must not start Worker in that case). "auto" gates on model warmth;
//     "deterministic" gates on service health when a service exists, and is
//     trivially true when none does (see deterministicBackend).
//   - enabled: whether Run should start the enrich Worker — true for both
//     "auto" (or "") and "deterministic"; only "off" disables it.
func wireEnrichment(ctx context.Context, set settings.Settings, secret string, q *queue.Queue, emitter *clientevents.Emitter, regions func() []string) (handler http.Handler, model enrich.Model, svc serviceFacets, gate func() bool, warmup func(context.Context) error, enabled bool) {
	if !set.EnrichmentEnabled() {
		log.Printf("keld-agent: enrichment disabled (ml_backend=off)")
		return ingress.DiscardHandler(secret), nil, serviceFacets{}, nil, nil, false
	}
	if !set.MLEnabled() {
		// deterministic: the Worker still runs, and so does the analysis
		// service — it is only the GLiNER2 model that is never asked for. No
		// model to load means no warmup and, since provisioning now hangs off
		// warmup, no download either.
		log.Printf("keld-agent: enrichment running in deterministic mode (ml_backend=%s); the analysis service runs, the model is never loaded", set.MLBackend)
		svc, gate := deterministicBackend(ctx, emitter, regions)
		return ingress.Handler(q, secret), nil, svc, gate, nil, true
	}
	model, svc, gate, warmup = mlBackend(ctx, emitter, regions)
	return ingress.Handler(q, secret), model, svc, gate, warmup, true
}

// newRunID generates a per-run correlation id (16 random bytes, hex-encoded),
// mirroring clientevents.InstallID's random-id style. A read failure from the
// system CSPRNG is vanishingly rare and non-fatal here — an empty run_id just
// weakens correlation within a single process lifetime, it never blocks startup.
func newRunID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}

// gateFrom maps resolved client_telemetry settings to the clientevents Gate.
func gateFrom(eff settings.EffectiveClientTelemetry) clientevents.Gate {
	return clientevents.Gate{
		Enabled:     eff.Enabled,
		MinSeverity: clientevents.Severity(eff.MinSeverity),
		SampleRate:  eff.SampleRate,
	}
}

// thresholdsFrom maps resolved client_telemetry settings to the resource
// watcher's Thresholds.
func thresholdsFrom(eff settings.EffectiveClientTelemetry) resource.Thresholds {
	return resource.Thresholds{
		RSSMB:           eff.RSSThresholdMB,
		CPUPct:          eff.CPUThresholdPct,
		SustainedWindow: time.Duration(eff.SustainedWindowS) * time.Second,
		GaugeInterval:   time.Duration(eff.GaugeIntervalS) * time.Second,
	}
}

// serve runs the ingress HTTP server until ctx is cancelled, then gracefully
// shuts it down and closes the queue. It blocks until the server stops.
func serve(ctx context.Context, ln net.Listener, handler http.Handler, q *queue.Queue, emitter *clientevents.Emitter) error {
	srv := &http.Server{
		Handler: handler,
		// KELD_AGENT_BIND=0.0.0.0:… (service mode) makes this reachable from
		// anywhere, and connection acceptance happens before ingress.go's
		// constant-time secret check — so an unauthenticated caller can hold a
		// connection open (slowloris, or just an idle connection) before ever
		// presenting a secret. A zero-value http.Server has no timeouts at all,
		// which was fine on the loopback-only listener this predates but isn't
		// once the bind can be public. ReadHeaderTimeout/ReadTimeout bound how
		// long an unauthenticated connection can occupy a goroutine; IdleTimeout
		// bounds a keep-alive connection sitting idle between requests. All three
		// are generous relative to ingress.go's 1 MiB body cap — a legitimate
		// large inline-prompt POST over a slow link still completes well inside
		// ReadTimeout.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		emitter.EmitExempt("daemon.stop", clientevents.SevInfo, nil)
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		q.Close()
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// mlBackendOpts holds overridable dependencies for mlBackend. Zero values
// cause mlBackendWithOpts to use production defaults. Only used in tests.
type mlBackendOpts struct {
	sup      *Supervisor
	client   *sidecar.Client
	modelDir string
	modelSHA string
	fetcher  provision.Fetcher
	healthFn func() bool
	emitter  *clientevents.Emitter
	// regions resolves the org's PII country tiers at CALL time (see
	// facetsFor). nil means "no opinion" — the sidecar's own default.
	regions func() []string
}

// gliner2ModelDir is the on-disk home of the GLiNER2 weights. It is named in
// exactly two places — the spawned service's KELD_GLINER2_DIR (where the
// service looks when it lazily loads that capability) and provisioning (where
// the weights are fetched to) — so it lives here rather than being recomputed
// by either caller.
func gliner2ModelDir() string { return paths.ModelsDir("gliner2-large-v1") }

// sidecarService builds the plumbing for the local analysis service: it
// resolves the sidecar binary, reaps any orphan left by a prior daemon,
// allocates the ephemeral loopback port (recording it in agent.json for
// `keld-agent metrics`), and constructs the HTTP client, the health probe and
// the (unstarted) Supervisor that will spawn it.
//
// It deliberately stops short of both provisioning a model and starting the
// supervisor. The sidecar is the analysis-and-enrichment service in general —
// /analyze, /match, /vocabulary, /classify, /extract — and GLiNER2 is one
// capability it loads lazily, not a precondition for serving. Callers that
// need the model provisioned do that themselves (see mlBackend).
//
// ok is false when there is no service this run, and the reason is carried by
// err so the caller can tell the two apart:
//
//	err == nil — no sidecar binary is installed.
//	err != nil — the ephemeral port could not be allocated. Returned raw and
//	             unwrapped, so a caller's clientevents.RedactError summary
//	             (which includes the error's concrete type) is unchanged.
//
// It logs only mechanical detail and never emits a client event: what a
// missing service *means* is policy, and each caller answers that differently.
func sidecarService(ctx context.Context, emitter *clientevents.Emitter) (*sidecar.Client, *Supervisor, func() bool, bool, error) {
	binPath, hasBin := sidecarBinPath()
	if !hasBin {
		return nil, nil, nil, false, nil
	}

	// Reap any orphaned sidecar from a prior daemon before spawning ours, so a
	// SIGKILL'd/crashed/reinstalled predecessor's child can't accumulate.
	reapStaleSidecars(binPath)

	// Pick an ephemeral port for the sidecar.
	scLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, nil, false, err
	}
	scPort := scLn.Addr().(*net.TCPAddr).Port
	scLn.Close() // Release; sidecar will bind it.

	// Record the sidecar port in agent.json so `keld-agent metrics` can reach it.
	// Best-effort: a failure here only affects that diagnostic command.
	if err := agentcfg.SetSidecarPort(scPort); err != nil {
		log.Printf("keld-agent: could not record sidecar port: %v", err)
	}

	scBaseURL := fmt.Sprintf("http://127.0.0.1:%d", scPort)
	scClient := sidecar.NewCtx(ctx, scBaseURL, 5*time.Second)
	healthFn := func() bool { return scClient.Healthy(ctx) }

	modelDir := gliner2ModelDir()

	sup := NewSupervisor(
		func(p int) (*exec.Cmd, error) {
			// ⚠️ exec.Command, NOT exec.CommandContext, and that is the point.
			// CommandContext installs a cancel hook that SIGKILLs the child's
			// PID the instant ctx is done — pid-only, uncatchable, and racing
			// the supervisor's own stop. It would pre-empt the SIGTERM that
			// lets the sidecar's lifespan teardown run, which is exactly the
			// bug being fixed. The supervisor owns termination on every path it
			// can return from (see stopChild), so there is nothing left for the
			// context hook to do except get in the way.
			cmd := exec.Command(binPath, fmt.Sprintf("--port=%d", p))
			cmd.Env = sidecarEnv(os.Environ(), modelDir, encoderDirForSpawn(), watch.AnalyzeRoots())
			return cmd, nil
		},
		scPort,
		healthFn,
		30*time.Second,
	)
	sup.SetEmitter(emitter)

	return scClient, sup, healthFn, true, nil
}

// deterministicBackend wires ml_backend "deterministic": the analysis service
// runs, the GLiNER2 model is never loaded, and the Worker gates on the
// service being up.
//
// The sidecar is the client-side analysis-and-enrichment service in general
// (/analyze, /match, /vocabulary, /classify, /extract); GLiNER2 is one
// capability it loads lazily on its first inference. Deterministic mode issues
// no inference, so nothing ever triggers that load — but /analyze still needs
// a process to answer it. Not starting the service is what made this mode a
// trap: it produced no workstreams at all and published a single
// credential-derived facet.
//
// When a service EXISTS, the gate is SERVICE HEALTH, deliberately, and
// neither alternative is acceptable: model warmth never arrives here (the
// model never loads), so it would hold every job forever; and a trivially-true
// gate would publish workstream-less profiles for every job that landed before
// the service finished starting, silently dropping their dimensions. Waiting
// is right there because the work becomes doable shortly — the supervisor is
// bringing the service up.
//
// When there is NO service this run, that reasoning does not apply and the
// gate must not be used: sidecarService reports !ok when no sidecar binary is
// installed (err == nil) or when the loopback port could not be allocated
// (err != nil), and neither resolves without a daemon restart. A closed gate
// would then wedge the mode forever — every job queueing and spooling, nothing
// ever published — on what is the state of every machine before the sidecar
// tarball is fetched. So that case takes noAnalysisService: a trivially-true
// gate and zero service facets, leaving enrichment to run its other model-free
// facets (credential detection) with the workstreams pass unregistered.
//
// That is not the degradation AGENTS.md forbids. Nothing lower-fidelity stands
// in for window analysis; the facet is dropped entirely and reported dropped
// via the pipeline's ordinary pipeline_status "partial" path.
func deterministicBackend(ctx context.Context, emitter *clientevents.Emitter, regions func() []string) (serviceFacets, func() bool) {
	scClient, sup, _, ok, err := sidecarService(ctx, emitter)
	if !ok {
		// No service this run and no path to one before a restart, so waiting
		// is pointless: run without window analysis rather than wedge.
		if err != nil {
			log.Printf("keld-agent: sidecar port alloc failed: %v; enrichment will run WITHOUT window analysis (no service this run) until the daemon is restarted", err)
			return serviceFacets{}, noAnalysisService(emitter, map[string]any{"error": clientevents.RedactError(err)})
		}
		log.Printf("keld-agent: no sidecar binary installed; enrichment will run WITHOUT window analysis (credential detection only) until one is installed and the daemon restarts")
		return serviceFacets{}, noAnalysisService(emitter, map[string]any{"reason": "no_sidecar_binary"})
	}
	go sup.Start(ctx)
	// facetsFor is the same capability probe "auto" uses — /analyze and /pii are
	// properties of the service client, not of the Model (there is none here).
	// The gate is health, but polled and CACHED (serviceHealthGate), the same
	// mechanism "auto" uses for warmth: Worker reads it per job and waitWarm
	// re-reads it every ~20ms, so sidecarService's raw healthFn — a live
	// loopback GET per call — is the wrong shape for a gate.
	svc := facetsFor(scClient, regions)
	// THE SIGNAL-EMBEDDINGS PATH is attached HERE and only here. facetsFor runs
	// in "auto" too, and the design scopes this subsystem to "deterministic"
	// ONLY: under "auto" it must be ABSENT — never registered, so it appears in
	// neither facets_skipped nor extractor_versions, the existing distinction
	// between a pass that was skipped and one that was never wired. Lifting
	// that later is this one line moving into facetsFor. See features.go.
	svc.Features = featureSourceFor(scClient)
	svc.AwaitSidecarStop = awaitSidecarStop(sup)
	return svc, serviceHealthGate(ctx, scClient)
}

// noAnalysisService is deterministic mode's "no service this run, and no path
// to one without a restart" path: no sidecar binary is installed, or its
// loopback port could not be allocated. It reports the absence exactly as
// mlBackend does — one sidecar.unavailable (SevWarn) carrying the caller's
// diagnostic fields, which keep the two causes apart ("reason" vs "error") —
// but returns a trivially-true gate rather than mlBackend's permanently-closed
// one.
//
// The difference is what waiting would buy. A closed gate is right for a
// service that is present but not yet ready, and for "auto", where every facet
// the mode produces needs the model. Here it buys nothing: no service will
// appear this daemon lifetime, so jobs would queue and spool forever. The
// caller pairs this gate with zero service facets, so the workstreams pass
// never registers, sensitivity names itself in facets_degraded, and enrichment
// runs its remaining model-free work, publishing pipeline_status "partial".
// Those are dropped facets, reported dropped — not lower-fidelity substitutes
// for them, which is what never-degrade forbids.
func noAnalysisService(emitter *clientevents.Emitter, fields map[string]any) func() bool {
	emitSidecarUnavailable(emitter, fields)
	return func() bool { return true }
}

// mlBackend returns the enrichment model and the worker readiness gate. It is
// only called when ML enrichment is enabled (see wireEnrichment) — ml_backend
// off is handled entirely by the caller and never reaches here.
//
// It spawns and supervises the sidecar and returns the sidecar client, a gate
// that opens once the model is resident, and the warmup that PROVISIONS the
// weights on demand and then loads them. Provisioning is no longer started
// here: the download is owed to an attempted inference (see
// modelProvisioner), and Worker's warmup call is that signal. There is no lower-fidelity fallback: when the sidecar binary
// is missing, or its port cannot be allocated, this returns
// sidecarUnavailable's permanently-closed gate (jobs queue/spool until the
// daemon is restarted) rather than a synthetic/degraded model.
//
// The service facets are returned alongside the Model — symmetric with
// deterministicBackend — because they belong to the SERVICE, not the model:
// both modes derive them the same way, and returning them here means
// wireEnrichment only passes them through instead of rederiving them.
func mlBackend(ctx context.Context, emitter *clientevents.Emitter, regions func() []string) (enrich.Model, serviceFacets, func() bool, func(context.Context) error) {
	scClient, sup, healthFn, ok, err := sidecarService(ctx, emitter)
	if !ok {
		// The consequence of having no service is this caller's to state: an
		// enrichment job queues/spools, which is not what a future non-ML
		// caller of sidecarService would say.
		//
		// No service also means no warmup: there is nothing to load and
		// nothing to provision FOR. The permanently-closed gate holds every
		// job, so a warmup would never be consulted anyway — returning nil
		// keeps "we never fetch weights we cannot use" true by construction.
		if err != nil {
			log.Printf("keld-agent: sidecar port alloc failed: %v", err)
			return nil, serviceFacets{}, sidecarUnavailable(emitter, map[string]any{"error": clientevents.RedactError(err)}), nil
		}
		log.Printf("keld-agent: no sidecar binary found; enrichment jobs will queue/spool until one is installed")
		return nil, serviceFacets{}, sidecarUnavailable(emitter, map[string]any{"reason": "no_sidecar_binary"}), nil
	}

	return mlBackendWithOpts(ctx, mlBackendOpts{
		sup:      sup,
		client:   scClient,
		modelDir: gliner2ModelDir(),
		modelSHA: provision.ModelSHA256,
		fetcher:  sidecar.NewHFFetcher(provision.ModelRepo, provision.ModelRevision),
		healthFn: healthFn,
		emitter:  emitter,
		regions:  regions,
	})
}

// sidecarUnavailable is the shared "no analysis service this run" path for
// both backends: the sidecar binary is missing, or its ephemeral port could
// not be allocated. Enrichment never degrades to a lower-fidelity backend in
// this case — it emits sidecar.unavailable (SevWarn) with the given diagnostic
// fields and returns a permanently-closed readiness gate, so jobs simply
// queue/spool until the daemon is restarted (matching the supervisor-give-up
// path in mlBackendWithOpts). The gate never opens, so whatever Model or
// service facets the caller pairs it with are never invoked.
func sidecarUnavailable(emitter *clientevents.Emitter, fields map[string]any) func() bool {
	emitSidecarUnavailable(emitter, fields)
	return func() bool { return false }
}

// emitSidecarUnavailable reports "no analysis service this run" to Atlas. It
// is shared by both no-service paths (sidecarUnavailable and
// noAnalysisService) because the observation is identical — only the readiness
// policy the caller pairs with it differs.
func emitSidecarUnavailable(emitter *clientevents.Emitter, fields map[string]any) {
	if emitter != nil {
		emitter.Emit("sidecar.unavailable", clientevents.SevWarn, fields)
	}
}

// mlBackendWithOpts is the testable core of mlBackend. It accepts all
// dependencies explicitly so tests can inject stubs without touching the
// real filesystem or spawning real processes.
func mlBackendWithOpts(ctx context.Context, opts mlBackendOpts) (enrich.Model, serviceFacets, func() bool, func(context.Context) error) {
	// Start the service NOW, alongside provisioning rather than behind it. The
	// sidecar is the client-side analysis-and-enrichment service in general —
	// /analyze, /match, /vocabulary, /classify, /extract — and GLiNER2 is one
	// capability it loads lazily on its first inference, not a precondition
	// for serving. Gating the spawn on a ~1.9 GB download meant a machine that
	// had not yet provisioned had no service at all, so nothing could answer
	// /analyze either.
	//
	// Spawning against an unprovisioned model dir is safe because nothing
	// attempts inference until the model is resident: the gate below polls
	// /metrics (WorkerReady never spawns a worker) and opens only on
	// worker.state=="ready", and Worker's warmup — the one call that would
	// trigger a load — is bounded by warmWait per job, after which the job is
	// deferred and re-spooled. With the weights absent that warmup fails, the
	// gate stays shut, and jobs queue/spool: the same outcome as not spawning
	// at all, minus the loss of every non-model route.
	go opts.sup.Start(ctx)

	// Provisioning is NOT started here. The weights are needed by exactly one
	// thing — an inference — so they are fetched by the warmup below, which
	// Worker calls when a job finds the readiness gate shut. Kicking the
	// ~1.9 GB download off at startup instead charged it to every machine
	// running the default mode, including ones that never enriched a prompt.
	// See modelProvisioner.
	prov := &modelProvisioner{
		dir:     opts.modelDir,
		sha:     opts.modelSHA,
		fetcher: opts.fetcher,
		emitter: opts.emitter,
		bg:      ctx,
	}

	// Enrichment never degrades to a lower-fidelity backend: the worker waits
	// until the sidecar has loaded at least once, then the client itself
	// waits+retries through idle-eviction (503) and transient restarts. If the
	// model can't be provisioned/started, the gate stays closed and jobs stay
	// queued/spooled (durable) until the sidecar recovers on a later daemon
	// run — there is no fallback to fall through to. provisionFailed is still
	// set by the provisioning goroutine for logging; it does not affect the
	// gate (which is now keyed off model warmth, not the supervisor).
	//
	// The per-job gate is model-resident WARMTH (sidecar /metrics
	// worker.state=="ready"), not latched liveness: after an idle-kill the
	// /health server stays up while the worker reloads, and counting that
	// reload against a job's deadline is the death-spiral this fixes. A dead or
	// never-started sidecar simply never reports warm (metrics unreachable), so
	// the gate stays closed — same durable queue/spool behaviour as before.
	wg := newWarmGate()
	go wg.run(ctx, opts.client.WorkerReady, warmPollInterval)
	// facetsFor(opts.client), not facetsFor-of-the-Model at the call site: the
	// non-inference routes are properties of the service client and must
	// survive any later change to what "the Model" is.
	svc := facetsFor(opts.client, opts.regions)
	svc.AwaitSidecarStop = awaitSidecarStop(opts.sup)
	return opts.client, svc, wg.Warm,
		provisioningWarmup(prov, warmupFunc(opts.client))
}

// enrichEndpoint derives the enrichments URL from the configured ingest endpoint
// by swapping the trailing path segment for /v1/enrichments.
func enrichEndpoint(ingest string) string {
	if i := strings.Index(ingest, "/v1/"); i >= 0 {
		return ingest[:i] + "/v1/enrichments"
	}
	return strings.TrimRight(ingest, "/") + "/v1/enrichments"
}

// settingsEndpoint derives the org enrichment-settings URL from the configured
// ingest endpoint by swapping the trailing path segment for
// /v1/enrichment-settings.
func settingsEndpoint(ingest string) string {
	if i := strings.Index(ingest, "/v1/"); i >= 0 {
		return ingest[:i] + "/v1/enrichment-settings"
	}
	return strings.TrimRight(ingest, "/") + "/v1/enrichment-settings"
}

// signalClientEventsEndpoint derives the client-events ingest URL from the
// configured ingest endpoint by swapping the trailing path segment for
// /v1/signal/client-events.
func signalClientEventsEndpoint(ingest string) string {
	if i := strings.Index(ingest, "/v1/"); i >= 0 {
		return ingest[:i] + "/v1/signal/client-events"
	}
	return strings.TrimRight(ingest, "/") + "/v1/signal/client-events"
}

// logsEndpoint derives the OTLP logs ingest URL from the configured ingest
// endpoint by swapping the trailing path segment for /v1/logs — the same OTLP
// logs receiver the tools' native OTEL exports to.
func logsEndpoint(ingest string) string {
	if i := strings.Index(ingest, "/v1/"); i >= 0 {
		return ingest[:i] + "/v1/logs"
	}
	return strings.TrimRight(ingest, "/") + "/v1/logs"
}

// metricsEndpoint derives the OTLP metrics ingest URL from the configured ingest
// endpoint by swapping the trailing path segment for /v1/metrics.
func metricsEndpoint(ingest string) string {
	if i := strings.Index(ingest, "/v1/"); i >= 0 {
		return ingest[:i] + "/v1/metrics"
	}
	return strings.TrimRight(ingest, "/") + "/v1/metrics"
}

// pollSettings fetches org settings on startup then on each tick of interval.
// On a successful fetch it applies the remote doc to live and — if onRemote is
// non-nil — invokes onRemote(r) so the caller can additionally react to
// fields Live doesn't itself model (e.g. client_telemetry gate/thresholds).
// On any Fetch error it logs + emits settings.poll_failed and keeps the
// last-known effective settings (non-fatal) — onRemote is NOT called, so a
// caller-held gate/thresholds simply persist unchanged rather than closing.
// It returns when ctx is cancelled.
func pollSettings(ctx context.Context, c *settings.Client, live *settings.Live, interval time.Duration, emitter *clientevents.Emitter, onRemote func(*settings.Remote), ra *reauther) {
	apply := func() {
		if r, err := c.Fetch(ctx); err == nil {
			// A successful poll is positive proof auth works — clear any stale
			// re-auth marker (incl. one left by a previous daemon run).
			if ra != nil {
				ra.noteAuthOK()
			}
			live.Apply(r)
			if onRemote != nil {
				onRemote(r)
			}
		} else {
			log.Printf("keld-agent: settings poll failed (keeping current): %v", err)
			if emitter != nil {
				emitter.Emit("settings.poll_failed", clientevents.SevWarn, map[string]any{"error": clientevents.RedactError(err)})
			}
			// A 401/403 means the ingest token was rotated/revoked — kick the
			// reauther (cooldown/single-flight guarded); last-known settings
			// are kept as-is either way (unchanged behavior).
			if ra != nil && isAuthError(err) {
				ra.refresh(ctx)
			}
		}
	}
	apply() // startup fetch
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			apply()
		}
	}
}
