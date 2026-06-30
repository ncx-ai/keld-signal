// Package daemon wires the enrichment components and runs the keld-agent server.
package daemon

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ncx-ai/keld-cli/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-cli/internal/agent/enrich"
	"github.com/ncx-ai/keld-cli/internal/agent/ingress"
	"github.com/ncx-ai/keld-cli/internal/agent/publish"
	"github.com/ncx-ai/keld-cli/internal/agent/queue"
	"github.com/ncx-ai/keld-cli/internal/agent/resolve"
	"github.com/ncx-ai/keld-cli/internal/agent/settings"
	"github.com/ncx-ai/keld-cli/internal/hook"
)

// Sender publishes an enrichment (real publisher or a test fake).
type Sender interface {
	Send(publish.Enrichment) error
}

// Worker consumes jobs, resolves text, enriches, and publishes. It is
// panic-isolated per job so one bad prompt never kills the daemon.
func Worker(q *queue.Queue, m enrich.Model, pub Sender, actor string, includeEntityText bool) {
	for {
		j, ok := q.Next()
		if !ok {
			return
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

// Run starts the daemon: ingress on loopback, worker, agent.json discovery file.
func Run(ctx context.Context) error {
	cfg, _ := hook.LoadConfig()
	if cfg.Endpoint == "" || cfg.IngestToken == "" {
		return fmt.Errorf("keld-agent: not configured (run `keld login` / setup first)")
	}

	secret, err := agentcfg.NewSecret()
	if err != nil {
		return err
	}
	set := settings.Load()
	q := queue.New(256)
	pub := publish.New(enrichEndpoint(cfg.Endpoint), cfg.IngestToken, "")
	go Worker(q, enrich.NewDeterministic(), pub, "", set.IncludeEntityText)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := agentcfg.Write(agentcfg.Info{Port: port, Secret: secret}); err != nil {
		return err
	}
	log.Printf("keld-agent: listening on 127.0.0.1:%d", port)

	srv := &http.Server{Handler: ingress.Handler(q, secret)}
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

// enrichEndpoint derives the enrichments URL from the configured ingest endpoint
// by swapping the trailing path segment for /v1/enrichments.
func enrichEndpoint(ingest string) string {
	if i := strings.Index(ingest, "/v1/"); i >= 0 {
		return ingest[:i] + "/v1/enrichments"
	}
	return strings.TrimRight(ingest, "/") + "/v1/enrichments"
}
