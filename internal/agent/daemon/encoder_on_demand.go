package daemon

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/agent/features"
	"github.com/ncx-ai/keld-signal/internal/agent/provision"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// ON-DEMAND PROVISIONING FOR THE TEXT ENCODER — modelProvisioner's sibling.
//
// The sidecar's textembed child encodes messages with Qwen3-Embedding-0.6B, and
// it never downloads anything: weights_dir() returns None when the directory is
// absent and the whole text half reports degraded:weights_unavailable. Until
// this file existed nothing on the Go side ever put weights there, so on every
// real machine the text half was permanently degraded and SILENTLY — the only
// weights in the field were hand-placed during development.
//
// It is deliberately modelProvisioner's shape rather than a new one, and the
// comments there are the specification. The three rules carried over:
//
//   - ON DEMAND, NEVER AT STARTUP. A machine that will never encode a message
//     must not pay 1,191,586,416 bytes for the possibility.
//   - THE DOWNLOAD IS NOT AWAITED INSIDE A CALLER'S BUDGET. EnsureModel stages
//     into a temp dir it REMOVES on failure, so a cancelled fetch throws away
//     everything it had; awaiting one inside a short deadline means every
//     attempt restarts from zero and none ever converges. The fetch runs on the
//     DAEMON's context and a caller is told "not ready yet".
//   - SUCCESS LATCHES, FAILURE DOES NOT. Verification streams a SHA-256 over
//     ~1.2 GB, so re-asking per demand would re-hash the weights forever.
//
// ⚠️ AND ONE RULE THAT IS STRONGER HERE THAN THERE, stated because it is the
// requirement this whole file is subordinate to: NOTHING WAITS ON THIS FETCH.
// modelProvisioner hangs off Worker's warmup, which is a caller that is
// entitled to defer its job. This has no such caller and must not acquire one:
// the sidecar starts, /health answers, and /features, /analyze, /ingest,
// /blocks and /pii all answer in full while the download runs, because the
// text half's absence is a STATED status (degraded:weights_unavailable) rather
// than a wait. So demand() is fire-and-forget from a hook on the watcher's
// poll loop, and ensure() — the one function here that can block — is reachable
// from no request path at all.

// errEncoderProvisioning is "not ready yet, still fetching". Unlike
// errProvisioning it never reaches a job: nothing defers on the encoder.
var errEncoderProvisioning = errors.New("text encoder not provisioned yet (fetching)")

// encoderRetryCooldown bounds re-attempts after a failure.
//
// modelProvisioner needs no such bound because its demand signal is
// one-per-job. Ours is the watcher's poll loop — KELD_WATCH_POLL, default 5s,
// per advancing transcript — so an uncooled retry would re-attempt a 1.2 GB
// download every five seconds against whatever is failing. Five minutes, the
// same number and the same reasoning as textembed.py's KELD_TEXTEMBED_RETRY_S
// (default 300): a cooldown rather than a latch, because the failure is
// usually the network and the weights genuinely may arrive on a later try.
const encoderRetryCooldown = 5 * time.Minute

// encoderState is what an operator needs to tell apart, and the reason this
// type exists at all: "not fetched yet", "fetch in flight" and "fetch failing"
// look identical from outside — an absent directory and a
// degraded:weights_unavailable in every /features response — while being three
// completely different situations to be in.
type encoderState string

const (
	encoderOff      encoderState = "off"          // KELD_TEXTEMBED not set: no fetch will ever happen
	encoderIdle     encoderState = "not_demanded" // on, but no transcript has wanted a vector yet
	encoderFetching encoderState = "fetching"
	encoderReady    encoderState = "ready"
	encoderFailed   encoderState = "failed" // last attempt failed; retried after the cooldown
)

// encoderProvisioner fetches the text-encoder weights on demand.
type encoderProvisioner struct {
	dir     string
	sha     string
	fetcher provision.Fetcher
	emitter *clientevents.Emitter
	// gate is the LIVE `features` toggle (env > agent-config.json > off, with
	// the org override on top). Consulted at demand time rather than captured,
	// because the org's value arrives on the first /v1/enrichment-settings poll
	// — after this is built. An org that never switches the path on never pays
	// the download, and one that switches it on mid-run pays it without a
	// restart.
	gate func() bool
	// bg is the daemon-lifetime context the fetch runs under, never a caller's.
	bg context.Context
	// cooldown is encoderRetryCooldown, a field only so a test can compress it
	// — the same reason HFFetcher.Policy is exported.
	cooldown time.Duration

	// busy is demand()'s fast path and its single-flight in one: set while an
	// attempt is in flight and left set forever on success. An atomic because
	// demand() runs on the watcher's poll loop and must not take a mutex there
	// on the common path.
	busy atomic.Bool

	mu       sync.Mutex
	state    encoderState
	done     bool
	wait     chan struct{}
	err      error
	attempts int
}

// newEncoderProvisioner returns a provisioner, or nil when KELD_TEXTEMBED is
// off — the toggle-off case is an absent object rather than an object that
// declines, so there is structurally nothing to fetch from.
func newEncoderProvisioner(ctx context.Context, gate func() bool, emitter *clientevents.Emitter) *encoderProvisioner {
	if !features.TextEmbedEnabled() {
		return nil
	}
	return &encoderProvisioner{
		dir:      encoderModelDir(),
		sha:      provision.EncoderSHA256,
		fetcher:  newEncoderFetcher(),
		emitter:  emitter,
		gate:     gate,
		bg:       ctx,
		cooldown: encoderRetryCooldown,
		state:    encoderIdle,
	}
}

// newEncoderFetcher builds the Hugging Face fetcher for the pinned encoder
// revision. A var so a test can substitute one that never reaches the network —
// a test that exercised the real constructor would download 1.2 GB from
// huggingface.co. Nothing in production reassigns it.
var newEncoderFetcher = func() provision.Fetcher {
	return sidecar.NewHFFetcher(provision.EncoderRepo, provision.EncoderRevision)
}

// encoderModelDir is where the weights live. It MUST agree with
// textembed.weights_dir()'s default (both resolve through KELD_HOME), because
// that agreement is what lets a sidecar spawned BEFORE the fetch finished pick
// the weights up on its next textembed call with no respawn and no
// KELD_TEXTEMBED_DIR pointing at them.
func encoderModelDir() string { return paths.ModelsDir(provision.EncoderDirName) }

// encoderWeightsPresent reports whether dir looks like installed weights.
//
// A STAT, never a hash. EnsureModel verifies by streaming SHA-256 over
// ~1.2 GB; this runs at every sidecar spawn, and spawns recur (idle recycle,
// crash, supervisor restart), so hashing here would put a multi-second read of
// 1.2 GB in front of a respawn. Cheap presence is the right question anyway:
// EnsureModel owns correctness, this only answers "is there something for
// KELD_TEXTEMBED_DIR to point at".
func encoderWeightsPresent(dir string) bool {
	if dir == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(dir, "model.safetensors"))
	return err == nil && st.Mode().IsRegular() && st.Size() > 0
}

// encoderDirForSpawn is the value sidecarEnv puts in KELD_TEXTEMBED_DIR, or ""
// to leave the variable unset.
//
// ⚠️ SET ONLY WHEN THE WEIGHTS ARE ACTUALLY THERE, and the reason is not
// caution. The sidecar reads an explicit KELD_TEXTEMBED_DIR as authoritative
// and, finding it absent on disk, answers None — the same answer it gives with
// the variable unset, so setting it eagerly buys nothing — while telling every
// operator who reads the daemon's child environment that the weights are
// installed. Unset means "the sidecar's own default path", which is the same
// directory (see encoderModelDir), so the fetch is adopted either way.
func encoderDirForSpawn() string {
	if !features.TextEmbedEnabled() {
		return ""
	}
	dir := encoderModelDir()
	if !encoderWeightsPresent(dir) {
		return ""
	}
	return dir
}

// demand is the trigger: a transcript grew, so there are messages that want
// vectors. Fire-and-forget.
//
// ⚠️ IT RUNS ON THE WATCHER'S POLL LOOP — the path every hook-free prompt on
// the machine travels — so it must never do I/O and never block. It does not:
// the common path is one atomic load, and the uncommon path is two closure
// calls and a `go`.
func (p *encoderProvisioner) demand() {
	if p == nil || p.busy.Load() {
		return
	}
	// The toggle check comes before the CAS so a switched-off org leaves busy
	// clear and can still be switched on later in the same daemon run.
	if p.gate != nil && !p.gate() {
		return
	}
	p.mu.Lock()
	p.startLocked()
	p.mu.Unlock()
}

// startLocked begins one attempt if none is in flight and the cooldown has
// elapsed, returning the channel that attempt will close. nil means "not
// started" — either an attempt is already running (join p.wait) or the cooldown
// after a failure has not expired. p.mu must be held.
//
// The single-flight is the atomic, not the mutex: demand() must be able to take
// its fast path without touching a lock at all.
func (p *encoderProvisioner) startLocked() chan struct{} {
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

// attempt runs one provisioning attempt on the daemon's context and reports it,
// then arms the cooldown if it failed. wait is closed when the attempt is over,
// so anything that joined it sees the outcome.
func (p *encoderProvisioner) attempt(wait chan struct{}) {
	started := time.Now()
	p.mu.Lock()
	p.attempts++
	n := p.attempts
	p.state = encoderFetching
	p.mu.Unlock()

	// Announced BEFORE the fetch, not after: the whole point of the state
	// vocabulary is that an operator can tell "fetching" from "never started",
	// and an event emitted only on completion cannot say that. The size is
	// stated because this is the one client-event that means a ~1.2 GB download
	// is happening on someone's home link right now.
	log.Printf("keld-agent: fetching text-encoder weights (%s, ~1.2 GB) for the signal-embeddings path; "+
		"nothing waits on this — the text half of a feature row reports degraded:weights_unavailable until it lands",
		provision.EncoderRepo)
	p.emit("features.encoder_provisioning", clientevents.SevWarn, map[string]any{
		"repo":     provision.EncoderRepo,
		"revision": provision.EncoderRevision,
		"attempt":  n,
	})

	err := provision.EnsureModel(p.bg, p.dir, p.sha, p.fetcher)
	elapsed := int(time.Since(started).Seconds())

	p.mu.Lock()
	p.err = err
	if err == nil {
		p.done = true
		p.state = encoderReady
	} else {
		p.state = encoderFailed
	}
	p.wait = nil
	p.mu.Unlock()
	close(wait)

	if err == nil {
		log.Printf("keld-agent: text-encoder weights ready after %ds; the text half will fill on the sidecar's next textembed retry", elapsed)
		p.emit("features.encoder_provisioned", clientevents.SevInfo, map[string]any{
			"duration_s": elapsed,
			"attempt":    n,
		})
		// busy stays set: success latches, so no later demand re-enters
		// EnsureModel and re-hashes 1.2 GB.
		return
	}

	cooldown := p.cooldown
	if cooldown <= 0 {
		cooldown = encoderRetryCooldown
	}
	log.Printf("keld-agent: text-encoder provisioning failed after %ds: %v; retrying in %s", elapsed, err, cooldown)
	p.emit("features.encoder_provision_failed", clientevents.SevError, map[string]any{
		"error":      clientevents.RedactError(err),
		"attempt":    n,
		"retry_in_s": int(cooldown.Seconds()),
		"duration_s": elapsed,
	})

	// A failure does not latch — but it does not re-arm instantly either. The
	// timer runs on the daemon's context so a shutdown does not leave it
	// pending.
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

// emit sends a provisioning transition, exempt from the severity floor.
//
// Exempt because the default floor is "warn" (settings.ClientTelemetry) and
// the SUCCESS transition is the one an operator most needs: under the floor,
// "ready" would be dropped and only failures would appear, leaving "ready" and
// "never started" indistinguishable — the exact confusion the state vocabulary
// exists to remove. Same call the path's features.emitter_enabled makes, for
// the same reason: these describe what this run is doing, not a sampled gauge.
func (p *encoderProvisioner) emit(code string, sev clientevents.Severity, fields map[string]any) {
	if p.emitter == nil {
		return
	}
	p.emitter.EmitExempt(code, sev, fields)
}

// status is the observability read: which of the five states this is in.
func (p *encoderProvisioner) status() encoderState {
	if p == nil {
		return encoderOff
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// ensure returns nil once the weights are on disk, joining the single in-flight
// attempt and waiting for it only as long as ctx allows. It is
// modelProvisioner.ensure's mechanism, not a second one.
//
// ⚠️ NO REQUEST PATH REACHES THIS, and none may be given one. The production
// trigger is demand(), which never waits; ensure exists so the "told not ready
// rather than made to wait" contract is a property something can be tested
// against, and so a future caller that IS entitled to defer (the way Worker's
// warmup is) has the correct primitive to reach for instead of inventing a
// blocking one.
func (p *encoderProvisioner) ensure(ctx context.Context) error {
	p.mu.Lock()
	if p.done {
		p.mu.Unlock()
		return nil
	}
	wait := p.startLocked()
	if wait == nil {
		// Nothing in flight and the post-failure cooldown has not elapsed, so
		// waiting could only expire. Report the last failure if there is one:
		// "the fetch is failing" and "the fetch has not finished" are the two
		// states this must not conflate.
		err := p.err
		p.mu.Unlock()
		if err != nil {
			return err
		}
		return errEncoderProvisioning
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
		return errEncoderProvisioning
	case <-ctx.Done():
		// The fetch keeps running under p.bg; this caller just stops waiting.
		return errEncoderProvisioning
	}
}
