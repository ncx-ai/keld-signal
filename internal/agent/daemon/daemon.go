// Package daemon wires the enrichment components and runs the keld-agent server.
package daemon

import (
	"context"
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

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/provision"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/agent/resolve"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/hook"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/spool"
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
func Worker(ctx context.Context, q *queue.Queue, m enrich.Model, pub Sender, actor string, includeEntityText func() bool, ready func() bool) {
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
		finished := runWithTimeout(to, func() { process(jobCtx, j, jobModel, pub, actor, includeEntityText) })
		cancel() // always: on timeout this reclaims the abandoned attempt; on success it just releases resources.

		if finished {
			ledger.reset(j.Key())
			continue
		}
		// The job exceeded its deadline (sidecar reloading/overloaded). Re-spool
		// so it retries on GLiNER2 later (NEVER degrade to deterministic, never
		// lose it) and move on so one stuck job can't wedge the single worker —
		// but bound the retries: after maxAttempts, quarantine it so a genuinely
		// un-enrichable job can't loop forever (the amplification that saturated
		// the sidecar). Atlas dedups on dedup_key, so a late double-publish from
		// a recovering attempt is harmless.
		if ledger.exhausted(j.Key(), maxAttempts()) {
			if err := spool.Quarantine(pointerFromJob(j)); err != nil {
				log.Printf("keld-agent: job %s exhausted retries and quarantine failed: %v", j.Key(), err)
			} else {
				log.Printf("keld-agent: job %s exceeded %s on %d attempts — quarantined", j.Key(), to, maxAttempts())
			}
			continue
		}
		if err := spool.Write(pointerFromJob(j)); err != nil {
			log.Printf("keld-agent: job %s exceeded %s and re-spool failed: %v", j.Key(), to, err)
		} else {
			log.Printf("keld-agent: job %s exceeded %s, re-spooled for retry", j.Key(), to)
		}
	}
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

// withJobCtx binds m to a per-job context when it is the sidecar client, so the
// job's timeout can cancel its in-flight calls. The deterministic backend has no
// network calls to cancel, so it passes through unchanged.
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

func process(ctx context.Context, j queue.Job, m enrich.Model, pub Sender, actor string, includeEntityText func() bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("keld-agent: worker recovered: %v", r)
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
	e := publish.Build(j, profile, actor, includeEntityText(), time.Now())
	if err := pub.Send(e); err != nil {
		log.Printf("keld-agent: publish failed for %s: %v", j.Key(), err)
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
	if p := os.Getenv("KELD_SIDECAR_BIN"); p != "" {
		if isRegularFile(p) {
			return p, true
		}
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
	q := queue.New(256)
	pub := publish.New(enrichEndpoint(cfg.Endpoint), cfg.IngestToken, actor)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := agentcfg.Write(agentcfg.Info{Port: port, Secret: secret}); err != nil {
		return err
	}
	log.Printf("keld-agent: listening on 127.0.0.1:%d", port)

	// Select the enrichment model and readiness gate. When ML is enabled and a
	// sidecar binary is present we route through the sidecar (falling back to
	// deterministic when it is unhealthy); otherwise we use the deterministic
	// model with an always-ready gate. mlBackend returns the deterministic pair
	// when it cannot bring the sidecar up, so the caller has a single code path.
	model, gate := mlBackend(ctx, set)

	live := settings.NewLive(set)
	pollInterval := 5 * time.Minute
	if v := os.Getenv("KELD_SETTINGS_POLL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			pollInterval = d
		}
	}
	go pollSettings(ctx, settings.NewClient(settingsEndpoint(cfg.Endpoint), cfg.IngestToken, 10*time.Second), live, pollInterval)
	go Worker(ctx, q, model, pub, actor, live.IncludeEntityText, gate)

	// Drain enrich pointers the hook spooled while the daemon was down, then keep
	// sweeping for ones spooled during brief unavailability. Idempotent:
	// delete-after-enqueue + Atlas dedups on dedup_key.
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

	return serve(ctx, ln, ingress.Handler(q, secret), q)
}

// serve runs the ingress HTTP server until ctx is cancelled, then gracefully
// shuts it down and closes the queue. It blocks until the server stops.
func serve(ctx context.Context, ln net.Listener, handler http.Handler, q *queue.Queue) error {
	srv := &http.Server{Handler: handler}
	go func() {
		<-ctx.Done()
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
}

// mlBackend returns the enrichment model and the worker readiness gate.
//
// When ML is enabled and a sidecar binary is present it provisions the model,
// spawns and supervises the sidecar, and returns a router (sidecar when
// healthy, else deterministic) with a gate that holds the worker until the
// sidecar is ready, has fallen back, OR provisioning has failed. In every
// other case (ML off, no binary, or a setup failure) it returns the
// deterministic model with an always-ready gate.
func mlBackend(ctx context.Context, set settings.Settings) (enrich.Model, func() bool) {
	deterministic := func() (enrich.Model, func() bool) {
		return enrich.NewDeterministic(), func() bool { return true }
	}

	binPath, hasBin := sidecarBinPath()
	if !set.MLEnabled() || !hasBin {
		return deterministic()
	}

	// Pick an ephemeral port for the sidecar.
	scLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Printf("keld-agent: sidecar port alloc failed, using deterministic: %v", err)
		return deterministic()
	}
	scPort := scLn.Addr().(*net.TCPAddr).Port
	scLn.Close() // Release; sidecar will bind it.

	scBaseURL := fmt.Sprintf("http://127.0.0.1:%d", scPort)
	scClient := sidecar.NewCtx(ctx, scBaseURL, 5*time.Second)
	healthFn := func() bool { return scClient.Healthy(ctx) }

	modelDir := paths.ModelsDir("gliner2-large-v1")

	sup := NewSupervisor(
		func(p int) (*exec.Cmd, error) {
			cmd := exec.CommandContext(ctx, binPath, fmt.Sprintf("--port=%d", p))
			cmd.Env = append(os.Environ(), "KELD_GLINER2_DIR="+modelDir)
			return cmd, nil
		},
		scPort,
		healthFn,
		30*time.Second,
	)

	return mlBackendWithOpts(ctx, mlBackendOpts{
		sup:      sup,
		client:   scClient,
		modelDir: modelDir,
		modelSHA: provision.ModelSHA256,
		fetcher:  sidecar.NewHFFetcher(provision.ModelRepo, provision.ModelRevision),
		healthFn: healthFn,
	})
}

// mlBackendWithOpts is the testable core of mlBackend. It accepts all
// dependencies explicitly so tests can inject stubs without touching the
// real filesystem or spawning real processes.
func mlBackendWithOpts(ctx context.Context, opts mlBackendOpts) (enrich.Model, func() bool) {
	var provisionFailed atomic.Bool

	// Provision the model BEFORE spawning the sidecar; on success start the
	// supervisor; on failure open the gate so the worker can fall through to
	// the deterministic path.
	go func() {
		if err := provision.EnsureModel(ctx, opts.modelDir, opts.modelSHA, opts.fetcher); err != nil {
			log.Printf("keld-agent: model provisioning failed: %v", err)
			provisionFailed.Store(true)
			return
		}
		go opts.sup.Start(ctx)
	}()

	// Enrichment never degrades to deterministic: the worker waits until the
	// sidecar has loaded at least once, then the client itself waits+retries
	// through idle-eviction (503) and transient restarts. If the model can't be
	// provisioned/started, the gate stays closed and jobs stay queued/spooled
	// (durable) until the sidecar recovers — rather than producing degraded
	// deterministic enrichment. (Deterministic is used only when ML is disabled
	// outright — see mlBackend's early return.) provisionFailed is still set by
	// the provisioning goroutine for logging; it no longer opens a fallback gate.
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

// pollSettings fetches org settings on startup then on each tick of interval.
// On any Fetch error it logs and keeps the last-known effective settings (non-fatal).
// It returns when ctx is cancelled.
func pollSettings(ctx context.Context, c *settings.Client, live *settings.Live, interval time.Duration) {
	apply := func() {
		if r, err := c.Fetch(ctx); err == nil {
			live.Apply(r)
		} else {
			log.Printf("keld-agent: settings poll failed (keeping current): %v", err)
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
