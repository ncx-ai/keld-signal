package daemon

import (
	"context"
	"errors"
	"log"
	"sync"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/provision"
)

// errProvisioning is what a warmup returns while the weights are still being
// fetched. It is a "not ready yet" — Worker's warmup contract turns that into
// a DEFER (re-spool, no retry attempt consumed), never into a job that runs
// against a cold model or a lower-fidelity substitute.
var errProvisioning = errors.New("model not provisioned yet (fetching)")

// modelProvisioner fetches the GLiNER2 weights ON DEMAND — at the first
// ATTEMPTED INFERENCE — rather than at daemon start.
//
// Why not at startup. Provisioning downloads ~1.9 GB, and it used to run in a
// goroutine kicked off by mlBackendWithOpts, i.e. on every daemon start in
// ml_backend "auto" (the default) whether or not that machine ever enriched a
// prompt. The weights are needed by exactly one thing — an inference — and the
// daemon already has a precise signal for that: Worker calls warmup when its
// readiness gate is shut, which is the one call that triggers a model load.
// Hanging provisioning off that signal makes the download owed to a specific
// attempted inference. A machine that never enriches never pays it, and if the
// model-backed facets are ever removed (nothing warms ⇒ nothing provisions)
// the fetch stops happening at all, with no further code change.
//
// Why the download is not simply awaited. warmup is bounded by warmWait
// (default 120s) — a bound sized for a model LOAD, not a multi-gigabyte
// download over a home link. Awaiting it inside that bound would cancel the
// fetch on expiry, and since EnsureModel stages into a temp dir that is
// removed on failure, every job would restart the download from zero and throw
// away what it had: unbounded work, never converging. So `ensure` starts the
// fetch on the DAEMON's context (it outlives the job), waits for it only as
// long as the caller's ctx allows, and reports "not ready yet" if that runs
// out. Jobs defer and re-spool meanwhile; the next one finds the download
// further along, or finished.
type modelProvisioner struct {
	dir     string
	sha     string
	fetcher provision.Fetcher
	emitter *clientevents.Emitter
	// bg is the daemon-lifetime context the fetch runs under, deliberately NOT
	// the per-job warm context (see the type comment).
	bg context.Context

	mu sync.Mutex
	// done latches success: EnsureModel verifies the sentinel by streaming a
	// SHA-256 over ~1.9 GB, so re-asking it per job would re-hash the weights
	// on every prompt.
	done bool
	// wait is non-nil exactly while a fetch is in flight; it is closed when
	// that attempt finishes, so concurrent demands join the one attempt
	// instead of starting competing downloads.
	wait chan struct{}
	err  error
}

// ensure returns nil once the model is on disk. If it is not, it starts (or
// joins) the single in-flight fetch and waits for it until ctx expires,
// returning a non-nil error in that case so the caller defers the job.
//
// A failed attempt does NOT latch: the next demand may try again. That is
// bounded, not a spin — Worker invokes warmup at most once per job, so one
// deferred job costs at most one attempt, and EnsureModel's own retry policy
// governs transient faults within an attempt.
func (p *modelProvisioner) ensure(ctx context.Context) error {
	p.mu.Lock()
	if p.done {
		p.mu.Unlock()
		return nil
	}
	if p.wait == nil {
		p.wait = make(chan struct{})
		go p.fetch(p.wait)
	}
	wait := p.wait
	p.mu.Unlock()

	select {
	case <-wait:
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.done {
			return nil
		}
		if p.err != nil {
			return p.err
		}
		return errProvisioning
	case <-ctx.Done():
		// The fetch keeps running under p.bg; this job just stops waiting.
		return errProvisioning
	}
}

// fetch runs one provisioning attempt and publishes its outcome to waiters.
func (p *modelProvisioner) fetch(wait chan struct{}) {
	err := provision.EnsureModel(p.bg, p.dir, p.sha, p.fetcher)
	if err != nil {
		log.Printf("keld-agent: model provisioning failed: %v", err)
		if p.emitter != nil {
			p.emitter.Emit("model.load_failed", clientevents.SevError, map[string]any{"error": clientevents.RedactError(err)})
		}
	}
	p.mu.Lock()
	p.err = err
	if err == nil {
		p.done = true
	}
	p.wait = nil // a failed attempt is retryable by a later demand
	p.mu.Unlock()
	close(wait)
}

// provisioningWarmup composes on-demand provisioning with the model load:
// fetch the weights if they are absent, then trigger and await the load. It is
// the function the daemon hands Worker as its warmup seam.
//
// warm nil means there is nothing to warm — ml_backend "deterministic" (no
// Model at all) or a test/eval fake — and the answer is a nil warmup, so
// Worker keeps its passive-wait path. Returning a non-nil warmup there would
// make a model-free mode download 1.9 GB it never uses, which is the whole
// defect this file exists to remove.
func provisioningWarmup(p *modelProvisioner, warm func(context.Context) error) func(context.Context) error {
	if warm == nil {
		return nil
	}
	return func(ctx context.Context) error {
		if err := p.ensure(ctx); err != nil {
			return err
		}
		return warm(ctx)
	}
}
