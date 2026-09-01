// Package attrib is the daemon's GO INTEGRATION half of on-device project
// attribution: a durable job store (coordinates only, never text) plus a
// loop that drives the sidecar's /attribute for each scheduled block and
// re-publishes the block with its Projects/ProjectsStatus/Attribution filled
// in once the sidecar has a terminal answer.
//
// # WHY A SEPARATE DURABLE STORE, NOT A SPOOL RE-USE
//
// The block emitter (internal/agent/blocks) already publishes a block the
// moment its own analysis is done — attribution is a SECOND pass over the
// same block, run asynchronously, because the embedding match (and,
// sometimes, a verifier call) needs the on-device encoder warm and the
// weights provisioned, and that provisioning is a multi-gigabyte download
// that can outlive the process that scheduled the job many times over. A job
// therefore has to survive a daemon restart, which is why Schedule persists
// to disk rather than an in-memory queue.
//
// # WHAT A JOB CARRIES, AND WHY NOT MORE
//
// A Job holds coordinates only: source, session, transcript path, span,
// attempts. It does NOT hold the published BlockEnrichment row. Re-publishing
// re-fetches the block through the SAME Digester the block emitter uses —
// blocks are deterministic from the sidecar's reference-series store, so a
// re-fetch reproduces the row byte-for-byte apart from attribution — which
// keeps the durable record small and keeps this package from serialising a
// second copy of a block's full analysis to disk.
//
// # PENDING NEVER CONSUMES AN ATTEMPT — THE AMENDED, LOAD-BEARING RULE
//
// MaxAttempts bounds genuine ERRORS only (a transport failure, or a status
// this side cannot recognise). A "pending" answer — the sidecar's encoder is
// cold, or its weights are not provisioned yet — leaves Job.Attempts
// untouched and re-spools the job unchanged. The models are a multi-gigabyte
// download that outlives any small attempt count; counting "still waiting" as
// "failing" would permanently abandon every block produced during
// provisioning — exactly what this durable job exists to prevent. This was a
// real bug caught in review (see TestPendingNeverConsumesAnAttemptOrQuarantines)
// and must not be reintroduced.
package attrib

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/blocks"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/debuglog"
)

// Job is a durable attribution job's coordinates ONLY — no message text, no
// span of characters, and deliberately no copy of the published row (see the
// package comment). Source/SessionID/Path/Start/End are what Schedule reads
// off the block it was just handed; Attempts is this package's own counter of
// GENUINE ERRORS, never of pending answers.
type Job struct {
	Source    string  `json:"source"`
	SessionID string  `json:"session_id"`
	Path      string  `json:"path"`
	Start     float64 `json:"start"`
	End       float64 `json:"end"`
	Attempts  int     `json:"attempts"`
}

// Store is one JSON file per job under dir, keyed by a hash of (session,
// start) so Put is idempotent — re-scheduling the same block (e.g. after a
// verifier supersedes an embedding-only match) overwrites the same file
// rather than piling up duplicates.
type Store struct{ dir string }

// NewStore builds a Store rooted at dir. dir is created lazily by Put/
// Quarantine, not here, so a Store for a directory nothing has been
// scheduled into yet costs nothing.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// filenameFor is sha256(session|start)[:16] + ".json" — deterministic and
// independent of Attempts, so Put/Delete/Quarantine of the same (session,
// start) always address the same file regardless of how many times the job
// has been retried.
func filenameFor(j Job) string {
	h := sha256.Sum256([]byte(j.SessionID + "|" + strconv.FormatFloat(j.Start, 'f', -1, 64)))
	return hex.EncodeToString(h[:8]) + ".json"
}

// Put writes j, creating dir if needed. Atomic (write-then-rename) so a crash
// mid-write can never leave a half-written job file for List to choke on.
func (s *Store) Put(j Job) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	final := filepath.Join(s.dir, filenameFor(j))
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// List returns every job currently in the store. A missing directory (no job
// has ever been scheduled here) is not an error — it is an empty store.
// Unreadable/undecodable entries are skipped rather than failing the whole
// list: one poisoned file must not hide every healthy job from the sweep.
func (s *Store) List() ([]Job, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Job
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var j Job
		if err := json.Unmarshal(b, &j); err != nil {
			continue
		}
		out = append(out, j)
	}
	return out, nil
}

// Delete removes j's file. Deleting an already-absent job is not an error —
// the caller's intent ("this job should not be live any more") is already
// satisfied.
func (s *Store) Delete(j Job) error {
	err := os.Remove(filepath.Join(s.dir, filenameFor(j)))
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// Quarantine moves j to dir/bad/ (its own file there, atomically written)
// and removes it from the live store — mirroring spool.Quarantine's shape
// one package over. Only reached after MaxAttempts GENUINE ERRORS; a pending
// answer never calls this.
func (s *Store) Quarantine(j Job) error {
	bad := filepath.Join(s.dir, "bad")
	if err := os.MkdirAll(bad, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	final := filepath.Join(bad, filenameFor(j))
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	return s.Delete(j)
}

// AttributeClient is the capability the Attributor needs from the sidecar
// client: match one block against the declared project list. Declared as an
// interface, mirroring blocks.Digester/Sender, so the loop is testable
// without a live sidecar. *sidecar.Client satisfies it directly.
type AttributeClient interface {
	Attribute(path, sessionID string, start, end float64, dims map[string]string) (sidecar.AttributeResult, bool)
}

// MaxAttempts bounds GENUINE ERRORS only — a transport failure, an
// unpublishable result, or a status this side cannot recognise. It does NOT
// bound pending answers; see the package comment.
const MaxAttempts = 4

// Attributor is the daemon-side driver: Schedule persists a job (never
// touching the sidecar); Run drains the store on start and on each interval
// tick or Schedule nudge.
type Attributor struct {
	st    *Store
	cl    AttributeClient
	pub   blocks.Sender
	facts blocks.Facts
	actor string
	// dig is the SAME Digester the block emitter holds — re-publishing
	// re-fetches the one block matching a job's coordinates through it rather
	// than carrying a second copy of the row in the job file. May be nil in
	// tests that only exercise Schedule.
	dig blocks.Digester
	// nudge lets a fresh Schedule wake a sleeping Run loop between ticks,
	// without Schedule itself doing any sidecar work. Buffered 1 and
	// non-blocking: a nudge that arrives while one is already pending is
	// redundant, not lost — the pending one will drain everything Schedule
	// just wrote.
	nudge chan struct{}
}

// New builds an Attributor. facts may be nil (empty resolved facts are sent
// on re-fetch); dig may be nil in tests that never reach the re-fetch path.
func New(st *Store, cl AttributeClient, pub blocks.Sender, facts blocks.Facts, actor string, dig blocks.Digester) *Attributor {
	return &Attributor{st: st, cl: cl, pub: pub, facts: facts, actor: actor, dig: dig, nudge: make(chan struct{}, 1)}
}

// Schedule persists a job for b's block and returns. It NEVER calls the
// sidecar — the whole point of a durable store is that the FIRST block
// publish (blocks.Emitter.publish, which calls this via OnPublished) must
// never be delayed by attribution, however slow or unavailable the sidecar
// currently is. See TestScheduleIsNonBlocking.
func (a *Attributor) Schedule(b publish.BlockEnrichment, path string) {
	start, ok1 := epochSeconds(b.Window.Start)
	end, ok2 := epochSeconds(b.Window.End)
	if !ok1 || !ok2 {
		log.Printf("keld-agent: attribution schedule skipped for session %s: unparseable block window %q/%q",
			b.SessionID, b.Window.Start, b.Window.End)
		return
	}
	j := Job{Source: b.Source.ID, SessionID: b.SessionID, Path: path, Start: start, End: end}
	if err := a.st.Put(j); err != nil {
		log.Printf("keld-agent: attribution job not persisted for session=%s start=%v: %v", j.SessionID, j.Start, err)
		return
	}
	select {
	case a.nudge <- struct{}{}:
	default:
	}
}

// Run drains the store on start (so a job scheduled before a restart is not
// stranded until the first tick), then keeps draining on interval or on a
// Schedule nudge, until ctx ends.
func (a *Attributor) Run(ctx context.Context, interval time.Duration) {
	a.drainOnce(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.drainOnce(ctx)
		case <-a.nudge:
			a.drainOnce(ctx)
		}
	}
}

// drainOnce is one pass over every job currently in the store. Split out from
// Run so a test can drive it directly without a ticker.
func (a *Attributor) drainOnce(ctx context.Context) {
	jobs, err := a.st.List()
	if err != nil {
		log.Printf("keld-agent: attribution store list failed: %v", err)
		return
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		a.drainJob(j)
	}
}

// drainJob resolves one job's block, asks the sidecar to attribute it, and
// either re-spools (pending, no attempt consumed; or a genuine error, one
// attempt consumed) or re-publishes and deletes (any terminal status).
func (a *Attributor) drainJob(j Job) {
	var resolved enrich.ResolvedFacts
	if a.facts != nil {
		resolved = a.facts(j.Path)
	}
	b, found := a.blockFor(j, resolved)
	if !found {
		// The block is not (yet, or any longer) visible through the Digester
		// — e.g. the sidecar has not re-ingested this transcript since a
		// restart. This is a GENUINE condition to retry and eventually give
		// up on, never "pending": there is no sidecar answer to distinguish
		// it from an error, so it is treated as one.
		a.retryOrQuarantine(j, "block not found for re-fetch")
		return
	}
	res, ok := a.cl.Attribute(j.Path, j.SessionID, j.Start, j.End, dimsFrom(b.Analysis))
	if !ok {
		a.retryOrQuarantine(j, "sidecar /attribute call failed")
		return
	}
	if res.Status == enrich.ProjectsPending {
		// ⚠️ AMENDED RULE: pending does NOT consume an attempt. Re-spool j
		// UNCHANGED (Attempts untouched) rather than the incremented copy
		// retryOrQuarantine would write.
		if err := a.st.Put(j); err != nil {
			log.Printf("keld-agent: attribution job re-spool (pending) failed for session=%s start=%v: %v",
				j.SessionID, j.Start, err)
		}
		return
	}
	row := publish.WithProjects(publish.BuildBlock(b, a.actor, time.Now()), res.Projects, res.Status, res.Attribution)
	if err := a.pub.SendBlocks([]publish.BlockEnrichment{row}); err != nil {
		a.retryOrQuarantine(j, "publish failed")
		return
	}
	if err := a.st.Delete(j); err != nil {
		log.Printf("keld-agent: attribution job not deleted after publish for session=%s start=%v: %v",
			j.SessionID, j.Start, err)
	}
}

// retryOrQuarantine records one GENUINE ERROR against j (never called for a
// pending answer) and either re-spools it or, at MaxAttempts, quarantines it
// — the death-spiral lesson MaxAttempts exists for: a job that can never
// succeed must eventually stop being retried instead of consuming a sweep
// forever.
func (a *Attributor) retryOrQuarantine(j Job, reason string) {
	j.Attempts++
	if j.Attempts >= MaxAttempts {
		if err := a.st.Quarantine(j); err != nil {
			log.Printf("keld-agent: attribution job quarantine failed for session=%s start=%v: %v",
				j.SessionID, j.Start, err)
			return
		}
		debuglog.Append("attrib: quarantined job session=%s start=%v after %d attempts (%s)",
			j.SessionID, j.Start, j.Attempts, reason)
		return
	}
	if err := a.st.Put(j); err != nil {
		log.Printf("keld-agent: attribution job re-spool failed for session=%s start=%v: %v",
			j.SessionID, j.Start, err)
	}
}

// refetchMaxBlocks bounds the Digester call blockFor makes. 1 is enough: the
// job names an exact (session, start), and blocks within a session are
// chronological and disjoint, so the first block whose start is >= j.Start
// is either the block itself or nothing this job can use.
const refetchMaxBlocks = 1

// blockFor re-fetches the one block a job refers to, through the SAME
// Digester the block emitter uses (see the package comment on why the job
// does not carry the row itself).
func (a *Attributor) blockFor(j Job, resolved enrich.ResolvedFacts) (enrich.BlockCharacterisation, bool) {
	if a.dig == nil {
		return enrich.BlockCharacterisation{}, false
	}
	since := j.Start
	got, _, ok := a.dig.BlocksCharacterised(j.Path, j.Source, j.SessionID, &since, time.Now(), refetchMaxBlocks, resolved)
	if !ok {
		return enrich.BlockCharacterisation{}, false
	}
	for _, b := range got {
		if b.SessionID == j.SessionID && closeEnough(b.StartTS, j.Start) {
			return b, true
		}
	}
	return enrich.BlockCharacterisation{}, false
}

// closeEnough compares two epoch-second instants with sub-second tolerance —
// the same precision the block path's own cursor comparisons use.
func closeEnough(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 0.001
}

// dimsFrom builds the /attribute call's dims argument from a block's own
// already-computed workstream dimensions (repo, branch, ...) — the caller's
// own facts, passed through rather than re-derived, per the sidecar contract.
func dimsFrom(an enrich.WindowAnalysis) map[string]string {
	if len(an.Workstreams) == 0 {
		return nil
	}
	out := make(map[string]string, len(an.Workstreams))
	for dim, l := range an.Workstreams {
		if l.Value == "" {
			continue
		}
		out[dim] = l.Value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// epochSeconds parses a BlockRef's RFC3339 instant into the epoch-second form
// Job/AttributeClient speak.
func epochSeconds(rfc3339 string) (float64, bool) {
	t, err := time.Parse(time.RFC3339Nano, rfc3339)
	if err != nil {
		return 0, false
	}
	return float64(t.UnixNano()) / 1e9, true
}

// EnvEnabled is the switch that turns the attribution subsystem on. Copies
// blocks.Enabled's exact shape (emitter.go:489): the env var wins in both
// directions over the config value, and the compiled-in default is off.
const EnvEnabled = "KELD_ATTRIBUTION"

// EnvInterval overrides the sweep interval.
const EnvInterval = "KELD_ATTRIBUTION_INTERVAL"

// DefaultInterval is the sweep cadence when nothing overrides it.
const DefaultInterval = 60 * time.Second

// Enabled reports the LOCAL value of the `attribution` toggle: KELD_ATTRIBUTION,
// else the agent-config value the caller resolved, else off. Mirrors
// blocks.Enabled exactly.
func Enabled(fromConfig bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvEnabled))) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	return fromConfig
}

// IntervalFromEnv is the sweep interval, DefaultInterval unless overridden.
func IntervalFromEnv() time.Duration {
	if v := os.Getenv(EnvInterval); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return DefaultInterval
}
