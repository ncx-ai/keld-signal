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
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/provision"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/agent/resolve"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
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
// ready() returns true. The block exits promptly when the queue is closed.
//
// ctx is the daemon-lifetime context; each job runs under a child context that
// is cancelled on timeout so a hung/slow enrichment's in-flight sidecar calls
// are reclaimed (not left retrying forever — the death-spiral root cause).
func Worker(ctx context.Context, q *queue.Queue, m enrich.Model, pub Sender, actor string, includeEntityText func() bool, ready func() bool, emitter *clientevents.Emitter, ra *reauther) {
	ledger := newRetryLedger()
	for {
		j, ok := q.Next()
		if !ok {
			return
		}
		// Wait until the backend is ready. Poll with a short sleep; break out
		// immediately if the queue is closed so shutdown is never blocked.
		for !ready() {
			select {
			case <-q.Done():
				// Queue closed; discard the in-hand job and exit.
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
		to := jobTimeout()
		// Per-job context: cancelling it aborts the job's in-flight sidecar calls
		// (client.WithContext) so a timed-out attempt stops consuming the
		// single-flight sidecar instead of leaking a retry loop.
		jobCtx, cancel := context.WithCancel(ctx)
		jobModel := withJobCtx(m, jobCtx)
		finished := runWithTimeout(to, func() { process(jobCtx, j, jobModel, pub, actor, includeEntityText, emitter, ra) })
		cancel() // always: on timeout this reclaims the abandoned attempt; on success it just releases resources.

		if finished {
			ledger.reset(j.Key())
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

// jobTimeout bounds how long the worker spends on one enrichment before it
// re-spools and moves on. Default 30s (covers a model reload ~15s + the ~7-pass
// enrichment); override with KELD_ENRICH_JOB_TIMEOUT (Go duration).
func jobTimeout() time.Duration {
	if v := os.Getenv("KELD_ENRICH_JOB_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 30 * time.Second
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

func process(ctx context.Context, j queue.Job, m enrich.Model, pub Sender, actor string, includeEntityText func() bool, emitter *clientevents.Emitter, ra *reauther) {
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
		return // could not resolve prompt text; skip silently
	}
	meta := enrich.Meta{Repo: j.Cwd, Tool: j.Source}
	if enrich.ContextEligible(j.Source) {
		meta = contextMeta(j)
	}
	profile := enrich.Run(text, j.Source, meta, m)
	// If the job's deadline fired mid-enrichment its sidecar calls were cancelled,
	// leaving a partial/empty profile — don't publish that. The worker re-spools
	// (bounded) so it retries on a healthy sidecar.
	if ctx.Err() != nil {
		return
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
	}
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
func Run(ctx context.Context) error {
	cfg, err := hook.LoadConfig()
	if err != nil {
		log.Printf("keld-agent: hook config read error: %v", err)
	}
	if cfg.Endpoint == "" || cfg.IngestToken == "" {
		return fmt.Errorf("keld-agent: not configured (run `keld login` / setup first)")
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

	q := queue.New(256)
	pub := publish.New(enrichEndpoint(cfg.Endpoint), tok.Get, actor)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := agentcfg.Write(agentcfg.Info{Port: port, Secret: secret}); err != nil {
		return err
	}
	log.Printf("keld-agent: listening on 127.0.0.1:%d", port)
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
	handler, model, gate, enrichmentEnabled := wireEnrichment(ctx, set, secret, q, emitter)

	live := settings.NewLive(set)
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

	onRemote := func(r *settings.Remote) {
		re := r.ClientTelemetry.WithDefaults()
		emitter.SetGate(gateFrom(re))
		watcher.SetThresholds(thresholdsFrom(re))
		gaugesEnabled.Store(re.GaugesEnabled)
	}
	go pollSettings(ctx, settings.NewClient(settingsEndpoint(cfg.Endpoint), tok.Get, 10*time.Second), live, pollInterval, emitter, onRemote, ra)
	if enrichmentEnabled {
		go Worker(ctx, q, model, pub, actor, live.IncludeEntityText, gate, emitter, ra)
	}

	// Drain enrich pointers the hook spooled while the daemon was down, then keep
	// sweeping for ones spooled during brief unavailability. Idempotent:
	// delete-after-enqueue + Atlas dedups on dedup_key. Only when enrichment is
	// enabled: with it disabled there's no Worker to consume the queue, so
	// draining/sweeping would just re-enqueue pointers nobody processes.
	if enrichmentEnabled {
		drainSpool := func() {
			spool.Drain(func(p spool.Pointer) error {
				if q.Offer(ingress.JobFrom(p)) {
					return nil
				}
				return errQueueFull // queue full: keep the file, retry next sweep
			})
		}
		drainSpool()
		go func() {
			iv := 30 * time.Second
			if v := os.Getenv("KELD_SPOOL_SWEEP"); v != "" {
				if d, err := time.ParseDuration(v); err == nil {
					iv = d
				}
			}
			t := time.NewTicker(iv)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					drainSpool()
				}
			}
		}()
	}

	return serve(ctx, ln, handler, q, emitter)
}

// wireEnrichment decides, once at startup, whether enrichment runs at all
// (ml_backend is a local, startup-only setting — see settings.Settings) and
// returns everything Run needs to wire it up:
//
//   - handler: the /enrich HTTP handler to serve — the real ingress.Handler
//     bound to q when enabled, or ingress.DiscardHandler (202, never
//     enqueues) when disabled, so the hook stops spooling pointers that would
//     never be processed.
//   - model, gate: the enrichment Model + Worker readiness gate when enabled
//     (nil, nil otherwise — Run must not start Worker in that case).
//   - enabled: whether Run should start the enrich Worker.
func wireEnrichment(ctx context.Context, set settings.Settings, secret string, q *queue.Queue, emitter *clientevents.Emitter) (handler http.Handler, model enrich.Model, gate func() bool, enabled bool) {
	if !set.MLEnabled() {
		log.Printf("keld-agent: enrichment disabled (ml_backend=off)")
		return ingress.DiscardHandler(secret), nil, nil, false
	}
	model, gate = mlBackend(ctx, emitter)
	return ingress.Handler(q, secret), model, gate, true
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
	srv := &http.Server{Handler: handler}
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
}

// mlBackend returns the enrichment model and the worker readiness gate. It is
// only called when ML enrichment is enabled (see wireEnrichment) — ml_backend
// off is handled entirely by the caller and never reaches here.
//
// It provisions the model, spawns and supervises the sidecar, and returns the
// sidecar client with a gate that opens once the sidecar has reported healthy
// at least once. There is no lower-fidelity fallback: when the sidecar binary
// is missing, or its port cannot be allocated, this returns
// sidecarUnavailable's permanently-closed gate (jobs queue/spool until the
// daemon is restarted) rather than a synthetic/degraded model.
func mlBackend(ctx context.Context, emitter *clientevents.Emitter) (enrich.Model, func() bool) {
	binPath, hasBin := sidecarBinPath()
	if !hasBin {
		log.Printf("keld-agent: no sidecar binary found; enrichment jobs will queue/spool until one is installed")
		return sidecarUnavailable(emitter, map[string]any{"reason": "no_sidecar_binary"})
	}

	// Pick an ephemeral port for the sidecar.
	scLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Printf("keld-agent: sidecar port alloc failed: %v", err)
		return sidecarUnavailable(emitter, map[string]any{"error": clientevents.RedactError(err)})
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

	modelDir := paths.ModelsDir("gliner2-large-v1")

	sup := NewSupervisor(
		func(p int) (*exec.Cmd, error) {
			cmd := exec.CommandContext(ctx, binPath, fmt.Sprintf("--port=%d", p))
			cmd.Env = sidecarEnv(os.Environ(), modelDir)
			return cmd, nil
		},
		scPort,
		healthFn,
		30*time.Second,
	)
	sup.SetEmitter(emitter)

	return mlBackendWithOpts(ctx, mlBackendOpts{
		sup:      sup,
		client:   scClient,
		modelDir: modelDir,
		modelSHA: provision.ModelSHA256,
		fetcher:  sidecar.NewHFFetcher(provision.ModelRepo, provision.ModelRevision),
		healthFn: healthFn,
		emitter:  emitter,
	})
}

// sidecarUnavailable is mlBackend's shared "no sidecar this run" path: the
// sidecar binary is missing, or its ephemeral port could not be allocated.
// Enrichment never degrades to a lower-fidelity backend in this case either —
// it emits sidecar.unavailable (SevWarn) with the given diagnostic fields and
// returns a permanently-closed gate, so jobs simply queue/spool until the
// daemon is restarted (matching the supervisor-give-up path in
// mlBackendWithOpts). The returned Model is nil: the gate never opens, so
// Worker never invokes it.
func sidecarUnavailable(emitter *clientevents.Emitter, fields map[string]any) (enrich.Model, func() bool) {
	emitter.Emit("sidecar.unavailable", clientevents.SevWarn, fields)
	return nil, func() bool { return false }
}

// mlBackendWithOpts is the testable core of mlBackend. It accepts all
// dependencies explicitly so tests can inject stubs without touching the
// real filesystem or spawning real processes.
func mlBackendWithOpts(ctx context.Context, opts mlBackendOpts) (enrich.Model, func() bool) {
	var provisionFailed atomic.Bool

	// Provision the model BEFORE spawning the sidecar; on success start the
	// supervisor; on failure leave the gate closed (see below) rather than
	// starting a sidecar against an unprovisioned model dir.
	go func() {
		if err := provision.EnsureModel(ctx, opts.modelDir, opts.modelSHA, opts.fetcher); err != nil {
			log.Printf("keld-agent: model provisioning failed: %v", err)
			if opts.emitter != nil {
				opts.emitter.Emit("model.load_failed", clientevents.SevError, map[string]any{"error": clientevents.RedactError(err)})
			}
			provisionFailed.Store(true)
			return
		}
		go opts.sup.Start(ctx)
	}()

	// Enrichment never degrades to a lower-fidelity backend: the worker waits
	// until the sidecar has loaded at least once, then the client itself
	// waits+retries through idle-eviction (503) and transient restarts. If the
	// model can't be provisioned/started, the gate stays closed and jobs stay
	// queued/spooled (durable) until the sidecar recovers on a later daemon
	// run — there is no fallback to fall through to. provisionFailed is still
	// set by the provisioning goroutine for logging; it does not affect the
	// gate (which is keyed off the supervisor's own Ready state).
	gate := func() bool { return opts.sup.Ready() }
	return opts.client, gate
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
