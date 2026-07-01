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
	"time"

	"github.com/ncx-ai/keld-cli/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-cli/internal/agent/enrich"
	"github.com/ncx-ai/keld-cli/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-cli/internal/agent/govern"
	"github.com/ncx-ai/keld-cli/internal/agent/ingress"
	"github.com/ncx-ai/keld-cli/internal/agent/publish"
	"github.com/ncx-ai/keld-cli/internal/agent/queue"
	"github.com/ncx-ai/keld-cli/internal/agent/resolve"
	"github.com/ncx-ai/keld-cli/internal/agent/settings"
	"github.com/ncx-ai/keld-cli/internal/config"
	"github.com/ncx-ai/keld-cli/internal/hook"
)

// Sender publishes an enrichment (real publisher or a test fake).
type Sender interface {
	Send(publish.Enrichment) error
}

// Worker consumes jobs, resolves text, enriches, and publishes. It is
// panic-isolated per job so one bad prompt never kills the daemon.
// ready is a readiness gate: Worker blocks before processing each job until
// ready() returns true. The block exits promptly when the queue is closed.
func Worker(q *queue.Queue, m enrich.Model, pub Sender, actor string, includeEntityText bool, ready func() bool) {
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
	model, gate := mlBackend(ctx, set)

	go Worker(q, model, pub, actor, set.IncludeEntityText, gate)

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

// mlBackend returns the enrichment model and the worker readiness gate.
//
// When ML is enabled and a sidecar binary is present it spawns and supervises
// the sidecar, starts the governor sampling loop and provisioning, and returns
// a router (sidecar when healthy, else deterministic) with a gate that holds the
// worker until the sidecar is ready or has fallen back. In every other case
// (ML off, no binary, or a setup failure) it returns the deterministic model
// with an always-ready gate — behaviourally identical to the pre-ML daemon.
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
	scClient := sidecar.New(scBaseURL, 5*time.Second)
	healthFn := func() bool { return scClient.Healthy(ctx) }

	sup := NewSupervisor(
		func(p int) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, binPath, fmt.Sprintf("--port=%d", p)), nil
		},
		scPort,
		healthFn,
		30*time.Second,
	)
	go sup.Start(ctx)

	// Kick provisioning in a background goroutine (stub — real HF fetcher is
	// Task 9). This must not block Run or crash when no model is present.
	go func() {
		// No-op stub: real provisioning wired when Fetcher is available.
		_ = binPath
	}()

	// Governor: observe host CPU every 5 s. The sampler is nil (no real CPU
	// sampler yet); Sample() is a no-op in that case.
	go func() {
		g := govern.New(nil, 4)
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

	// Router: sidecar when healthy, else deterministic. The gate holds the
	// worker until the sidecar is ready OR has fallen back (i.e. we know which
	// path to take).
	router := enrich.NewRouter(scClient, enrich.NewDeterministic(), healthFn)
	gate := func() bool { return sup.Ready() || sup.FellBack() }
	return router, gate
}

// enrichEndpoint derives the enrichments URL from the configured ingest endpoint
// by swapping the trailing path segment for /v1/enrichments.
func enrichEndpoint(ingest string) string {
	if i := strings.Index(ingest, "/v1/"); i >= 0 {
		return ingest[:i] + "/v1/enrichments"
	}
	return strings.TrimRight(ingest, "/") + "/v1/enrichments"
}
