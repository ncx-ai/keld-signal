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
func Worker(q *queue.Queue, m enrich.Model, pub Sender, actor string, includeEntityText bool, ready func() bool, admit func() bool) {
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
			// shedding: host CPU busy, skip ML enrichment
			continue
		}
		process(j, m, pub, actor, includeEntityText)
	}
}

func process(j queue.Job, m enrich.Model, pub Sender, actor string, includeEntityText bool) {
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
	e := publish.Build(j, profile, actor, includeEntityText, time.Now())
	if err := pub.Send(e); err != nil {
		log.Printf("keld-agent: publish failed for %s: %v", j.Key(), err)
	}
}

// sidecarBinPath returns the path to the sidecar binary and whether it exists.
// It checks KELD_SIDECAR_BIN env override first, then a well-known path.
func sidecarBinPath() (string, bool) {
	if p := os.Getenv("KELD_SIDECAR_BIN"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	// Well-known path alongside the keld binary (not required to exist).
	const wellKnown = "/usr/local/bin/keld-sidecar"
	if _, err := os.Stat(wellKnown); err == nil {
		return wellKnown, true
	}
	return "", false
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

	go Worker(q, model, pub, actor, set.IncludeEntityText, gate, admit)

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
