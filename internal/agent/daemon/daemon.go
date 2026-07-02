// Package daemon wires the enrichment components and runs the keld-agent server.
package daemon

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-cli/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-cli/internal/agent/enrich"
	"github.com/ncx-ai/keld-cli/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-cli/internal/agent/govern"
	"github.com/ncx-ai/keld-cli/internal/agent/ingress"
	"github.com/ncx-ai/keld-cli/internal/agent/provision"
	"github.com/ncx-ai/keld-cli/internal/agent/publish"
	"github.com/ncx-ai/keld-cli/internal/agent/queue"
	"github.com/ncx-ai/keld-cli/internal/agent/resolve"
	"github.com/ncx-ai/keld-cli/internal/agent/settings"
	"github.com/ncx-ai/keld-cli/internal/config"
	"github.com/ncx-ai/keld-cli/internal/hook"
	"github.com/ncx-ai/keld-cli/internal/paths"
)

// Sender publishes an enrichment (real publisher or a test fake).
type Sender interface {
	Send(publish.Enrichment) error
}

// Worker consumes jobs, resolves text, enriches, and publishes. It is
// panic-isolated per job so one bad prompt never kills the daemon.
// ready is a readiness gate: Worker blocks before processing each job until
// ready() returns true. The block exits promptly when the queue is closed.
// admit is an optional load-shedding gate for the ML path: when non-nil and
// returning false the job is skipped (CPU busy). Pass nil on the deterministic
// path so it is never shed.
func Worker(q *queue.Queue, m enrich.Model, pub Sender, actor string, includeEntityText func() bool, ready func() bool, admit func() bool) {
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
		if admit != nil && !admit() {
			// Governor overload shedding: intentionally DROP this ML job (no enrichment) —
			// deterministic is the failure/timeout fallback, not the overload fallback.
			continue
		}
		process(j, m, pub, actor, includeEntityText)
	}
}

func process(j queue.Job, m enrich.Model, pub Sender, actor string, includeEntityText func() bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("keld-agent: worker recovered: %v", r)
		}
	}()
	text, ok := resolve.Resolve(j.Source, j.TranscriptPath, j.PromptID, j.Inline)
	if !ok {
		return // could not resolve prompt text; skip silently
	}
	profile := enrich.Run(text, j.Source, m)
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
	model, gate, admit := mlBackend(ctx, set)

	live := settings.NewLive(set)
	pollInterval := 5 * time.Minute
	if v := os.Getenv("KELD_SETTINGS_POLL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			pollInterval = d
		}
	}
	go pollSettings(ctx, settings.NewClient(settingsEndpoint(cfg.Endpoint), cfg.IngestToken, 10*time.Second), live, pollInterval)
	go Worker(q, model, pub, actor, live.IncludeEntityText, gate, admit)

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

// mlBackend returns the enrichment model, the worker readiness gate, and the
// admit function for ML-path load shedding.
//
// When ML is enabled and a sidecar binary is present it provisions the model,
// spawns and supervises the sidecar, starts the governor sampling loop, and
// returns a router (sidecar when healthy, else deterministic) with a gate that
// holds the worker until the sidecar is ready, has fallen back, OR provisioning
// has failed. In every other case (ML off, no binary, or a setup failure) it
// returns the deterministic model with an always-ready gate and nil admit.
func mlBackend(ctx context.Context, set settings.Settings) (enrich.Model, func() bool, func() bool) {
	deterministic := func() (enrich.Model, func() bool, func() bool) {
		return enrich.NewDeterministic(), func() bool { return true }, nil
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
	scClient := sidecar.New(scBaseURL, 5*time.Second)
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
func mlBackendWithOpts(ctx context.Context, opts mlBackendOpts) (enrich.Model, func() bool, func() bool) {
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

	// Governor: observe host CPU every 5 s using a real CPUSampler.
	// Dynamic-concurrency pooling is deferred (YAGNI for single-user daemon);
	// the governor is used solely for Admit()-based ML-path shedding.
	g := govern.New(govern.CPUSampler{}, 1)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				g.Sample()
			}
		}
	}()

	// Gate: open when sidecar is ready, has fallen back, OR provisioning failed.
	gate := func() bool {
		return opts.sup.Ready() || opts.sup.FellBack() || provisionFailed.Load()
	}

	// admit gates ML-path jobs: when the sidecar is healthy the governor decides;
	// when it has fallen back to deterministic we always admit (no ML cost).
	admit := func() bool {
		if opts.healthFn() {
			return g.Admit()
		}
		return true
	}

	router := enrich.NewRouter(opts.client, enrich.NewDeterministic(), opts.healthFn)
	return router, gate, admit
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
