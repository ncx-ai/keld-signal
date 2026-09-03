package daemon

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/attrib"
	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/agent/provision"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// ON-DEMAND PROVISIONING FOR THE ATTRIBUTION VERIFIER — modelProvisioner's and
// encoderProvisioner's sibling, and deliberately their shape: the comments on
// those two files are the specification. The verifier is Gemma 4 E2B,
// Q4_K_M GGUF (a few GB), a small local LLM sidecar/app/verifier.py loads to
// give YES/NO on borderline (block, project) pairs inside /attribute.
//
// Same three rules, restated because this file is subordinate to them:
//   - ON DEMAND, NEVER AT STARTUP.
//   - THE DOWNLOAD IS NOT AWAITED INSIDE A CALLER'S BUDGET — EnsureFile
//     stages into a temp dir it removes on failure, so the fetch runs on the
//     daemon's own context and nothing waits on it (see /attribute's own
//     `pending` status, and attrib.Job's "pending never consumes an attempt"
//     rule one package over).
//   - SUCCESS LATCHES, FAILURE DOES NOT.
//
// ⚠️ UNLIKE THE ENCODER, THERE IS NO LIVE GATE HERE. Both conditions that
// decide whether this provisioner exists at all are resolved once, for the
// whole run, with no remote override: attribution (attrib.Enabled reads only
// env + local config) and the verifier's own opt-out (attrib.VerifierEnabled,
// mirroring verifier.py's enabled(), is env-only too). So there is nothing to
// re-check between demand() calls the way encoderProvisioner's `gate`
// re-checks the org's live `features` toggle.

// errVerifierProvisioning is "not ready yet, still fetching". Nothing waits
// on it directly — /attribute's own pending status is what a caller actually
// sees — but ensure() exists so that contract is testable, mirroring
// errEncoderProvisioning one file over.
var errVerifierProvisioning = errors.New("attribution verifier GGUF not provisioned yet (fetching)")

// verifierRetryCooldown bounds re-attempts after a failure. Demand fires once
// per attribution sweep (attrib.DefaultInterval, 60s) rather than per block,
// so this is more generous headroom than encoderRetryCooldown needs to be,
// but the same number keeps the two files easy to compare.
const verifierRetryCooldown = 5 * time.Minute

// verifierProvisioner fetches the attribution verifier's GGUF on demand.
type verifierProvisioner struct {
	dir     string
	sha     string
	fetcher provision.Fetcher
	emitter *clientevents.Emitter
	// bg is the daemon-lifetime context the fetch runs under, never a
	// caller's.
	bg context.Context
	// cooldown is verifierRetryCooldown, a field only so a test can compress
	// it — the same reason HFFetcher.Policy and encoderProvisioner.cooldown
	// are exported/fields.
	cooldown time.Duration

	// busy is demand()'s fast path and its single-flight in one: set while an
	// attempt is in flight and left set forever on success.
	busy atomic.Bool

	mu       sync.Mutex
	done     bool
	wait     chan struct{}
	err      error
	attempts int
}

// verifierModelDir is where the GGUF lives. It MUST agree with
// verifier.weights_path()'s default (both resolve through KELD_HOME) — the
// same agreement encoderModelDir documents one model over — so the sidecar
// finds it with no extra wiring: no KELD_VERIFIER_GGUF is ever set by this
// daemon.
func verifierModelDir() string { return paths.ModelsDir(provision.VerifierDirName) }

// newVerifierProvisioner returns a provisioner, or nil when attribution is
// off or the verifier is opted out — either makes the GGUF something this
// machine will never load, so there is structurally nothing to fetch for.
func newVerifierProvisioner(ctx context.Context, attribOn bool, emitter *clientevents.Emitter) *verifierProvisioner {
	if !attribOn || !attrib.VerifierEnabled() {
		return nil
	}
	return &verifierProvisioner{
		dir:      verifierModelDir(),
		sha:      provision.VerifierSHA256,
		fetcher:  newVerifierFetcher(),
		emitter:  emitter,
		bg:       ctx,
		cooldown: verifierRetryCooldown,
	}
}

// newVerifierFetcher builds the Hugging Face fetcher for the pinned verifier
// file. A var so a test can substitute one that never reaches the network —
// mirroring newEncoderFetcher exactly.
//
// ⚠️ WithFileAs, NOT WithFiles, AND THAT ONE WORD IS THE WHOLE OF C1. The repo
// ships the GGUF as VerifierFile ("gemma-4-E2B-it-Q4_K_M.gguf"); EnsureFile
// below SHA-checks VerifierSentinel ("model.gguf"), which is the name
// verifier.weights_path() looks for. With WithFiles the fetched file landed
// under the remote name, the sentinel check found nothing, EnsureFile's
// `defer os.RemoveAll(tmp)` discarded a complete ~3 GB download, and the
// 5-minute cooldown re-armed it on the next published block — forever, with
// the GGUF never once provisioned. The two names are now bridged at the one
// place that knows both.
var newVerifierFetcher = func() provision.Fetcher {
	return sidecar.NewHFFetcher(provision.VerifierRepo, provision.VerifierRevision).
		WithFileAs(provision.VerifierFile, provision.VerifierSentinel)
}

// demand is the trigger: an attribution job was scheduled, so a block wants
// attributing and may need the verifier. Fire-and-forget, and cheap on the
// common path (one atomic load) — the caller is the attributor's
// OnPublished hook, not a hot loop, but nil-safety and the busy fast path are
// kept identical to encoderProvisioner's demand() for the same reason: so a
// future caller on a hotter path has the right shape to copy.
func (p *verifierProvisioner) demand() {
	if p == nil || p.busy.Load() {
		return
	}
	p.mu.Lock()
	p.startLocked()
	p.mu.Unlock()
}

// startLocked begins one attempt if none is in flight and the cooldown has
// elapsed, returning the channel that attempt will close. nil means "not
// started". p.mu must be held.
func (p *verifierProvisioner) startLocked() chan struct{} {
	if p.wait != nil {
		return p.wait
	}
	if !p.busy.CompareAndSwap(false, true) {
		return nil
	}
	p.wait = make(chan struct{})
	go p.attempt(p.wait)
	return p.wait
}

// attempt runs one provisioning attempt on the daemon's context and reports
// it, then arms the cooldown if it failed.
func (p *verifierProvisioner) attempt(wait chan struct{}) {
	started := time.Now()
	p.mu.Lock()
	p.attempts++
	n := p.attempts
	p.mu.Unlock()

	log.Printf("keld-agent: fetching attribution-verifier weights (%s) for the project-attribution path; "+
		"nothing waits on this — /attribute answers pending until it lands", provision.VerifierRepo)
	p.emit("attribution.verifier_provisioning", clientevents.SevWarn, map[string]any{
		"repo":    provision.VerifierRepo,
		"attempt": n,
	})

	err := provision.EnsureFile(p.bg, p.dir, provision.VerifierSentinel, p.sha, p.fetcher)
	elapsed := int(time.Since(started).Seconds())

	p.mu.Lock()
	p.err = err
	if err == nil {
		p.done = true
	}
	p.wait = nil
	p.mu.Unlock()
	close(wait)

	if err == nil {
		log.Printf("keld-agent: attribution-verifier weights ready after %ds", elapsed)
		p.emit("attribution.verifier_provisioned", clientevents.SevInfo, map[string]any{
			"duration_s": elapsed,
			"attempt":    n,
		})
		// busy stays set: success latches, so no later demand re-enters
		// EnsureFile and re-hashes the GGUF.
		return
	}

	cooldown := p.cooldown
	if cooldown <= 0 {
		cooldown = verifierRetryCooldown
	}
	log.Printf("keld-agent: attribution-verifier provisioning failed after %ds: %v; retrying in %s", elapsed, err, cooldown)
	p.emit("attribution.verifier_provision_failed", clientevents.SevError, map[string]any{
		"error":      clientevents.RedactError(err),
		"attempt":    n,
		"retry_in_s": int(cooldown.Seconds()),
		"duration_s": elapsed,
	})

	go func() {
		t := time.NewTimer(cooldown)
		defer t.Stop()
		select {
		case <-t.C:
			p.busy.Store(false)
		case <-p.bg.Done():
		}
	}()
}

// emit sends a provisioning transition, exempt from the severity floor —
// same reasoning as encoderProvisioner.emit.
func (p *verifierProvisioner) emit(code string, sev clientevents.Severity, fields map[string]any) {
	if p.emitter == nil {
		return
	}
	p.emitter.EmitExempt(code, sev, fields)
}

// ensure returns nil once the GGUF is on disk, joining the single in-flight
// attempt and waiting for it only as long as ctx allows. No production
// request path reaches this today (the attributor's drainJob treats a
// verifier-unavailable answer from the sidecar as its own "unavailable"
// meta, never as a Go-side wait) — it exists, like encoderProvisioner.ensure,
// so the "told not ready rather than made to wait" contract has a primitive
// a future caller can reach for.
func (p *verifierProvisioner) ensure(ctx context.Context) error {
	p.mu.Lock()
	if p.done {
		p.mu.Unlock()
		return nil
	}
	wait := p.startLocked()
	if wait == nil {
		err := p.err
		p.mu.Unlock()
		if err != nil {
			return err
		}
		return errVerifierProvisioning
	}
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
		return errVerifierProvisioning
	case <-ctx.Done():
		return errVerifierProvisioning
	}
}
