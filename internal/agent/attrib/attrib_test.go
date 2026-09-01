package attrib

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
)

// fakeClient is the AttributeClient test double. res is returned verbatim on
// every call when ok is true; calls counts invocations so a test can assert
// pending does/doesn't get re-tried.
type fakeClient struct {
	res   sidecar.AttributeResult
	ok    bool
	calls int
}

func (f *fakeClient) Attribute(path, sessionID string, start, end float64, dims map[string]string) (sidecar.AttributeResult, bool) {
	f.calls++
	return f.res, f.ok
}

func successClient(projectID string) *fakeClient {
	return &fakeClient{ok: true, res: sidecar.AttributeResult{
		Status:   enrich.ProjectsAttributed,
		Projects: []enrich.ProjectAttribution{{ID: projectID, Confidence: 0.9, Source: "embedding"}},
	}}
}

func pendingClient() *fakeClient {
	return &fakeClient{ok: true, res: sidecar.AttributeResult{Status: enrich.ProjectsPending}}
}

func errorClient() *fakeClient {
	return &fakeClient{ok: false} // transport failure — a genuine error
}

// blockingClient hangs forever on Attribute — used to prove Schedule never
// calls the sidecar.
type blockingClient struct{ block chan struct{} }

func (f *blockingClient) Attribute(path, sessionID string, start, end float64, dims map[string]string) (sidecar.AttributeResult, bool) {
	<-f.block
	return sidecar.AttributeResult{}, false
}

// fakeSender records every batch handed to SendBlocks. failN, when > 0, fails
// the first failN calls (returning an error) before succeeding — used to
// exercise the publish-failure-holds-the-job path (I2).
type fakeSender struct {
	sent  [][]publish.BlockEnrichment
	failN int
	calls int
}

func (f *fakeSender) SendBlocks(rows []publish.BlockEnrichment) error {
	f.calls++
	if f.calls <= f.failN {
		return errors.New("atlas unreachable")
	}
	f.sent = append(f.sent, rows)
	return nil
}

// orderCheckSender wraps a Store and records, at the moment SendBlocks is
// called, whether the job it is about to publish is STILL present in the
// store — proving the attributor publishes BEFORE deleting rather than the
// other way around. Deleting first and crashing before the publish landed
// would silently lose the block forever; publishing first and crashing
// before the delete only costs a harmless re-publish (Atlas upserts on
// (session, start)). Which order actually runs is exactly what
// TestAttributionJobSurvivesRestart must pin, not merely that both happen.
type orderCheckSender struct {
	st                *Store
	job               Job
	sawJobDuringSend  bool
	checkedAtLeastOne bool
	sent              [][]publish.BlockEnrichment
}

func (o *orderCheckSender) SendBlocks(rows []publish.BlockEnrichment) error {
	o.checkedAtLeastOne = true
	if jobs, _ := o.st.List(); len(jobs) > 0 {
		for _, j := range jobs {
			if j.SessionID == o.job.SessionID && closeEnough(j.Start, o.job.Start) {
				o.sawJobDuringSend = true
			}
		}
	}
	o.sent = append(o.sent, rows)
	return nil
}

// fakeDigester stands in for the sidecar's POST /blocks re-fetch: it always
// returns the one block characterisation it was built with, regardless of
// since/maxBlocks, which is enough to exercise the attributor's re-fetch +
// republish path without a live sidecar.
type fakeDigester struct {
	block enrich.BlockCharacterisation
	ok    bool
}

func (f *fakeDigester) BlocksCharacterised(path, source, sessionID string,
	since *float64, now time.Time, maxBlocks int,
	resolved enrich.ResolvedFacts) ([]enrich.BlockCharacterisation, *float64, bool) {
	if !f.ok {
		return nil, nil, false
	}
	return []enrich.BlockCharacterisation{f.block}, nil, true
}

// digesterFor builds a fake Digester whose one block matches the coordinates
// a Job for (sessionID, start, end) would carry.
func digesterFor(sessionID string, start, end float64) *fakeDigester {
	return &fakeDigester{ok: true, block: enrich.BlockCharacterisation{
		SessionID: sessionID,
		Source:    "claude_code",
		Ref: enrich.BlockRef{
			Start:       time.Unix(int64(start), 0).UTC().Format(time.RFC3339Nano),
			End:         time.Unix(int64(end), 0).UTC().Format(time.RFC3339Nano),
			SpanMinutes: (end - start) / 60.0,
			Evidence:    5,
			StartReason: "session_start",
			EndReason:   "idle",
		},
		StartTS: start,
		EndTS:   end,
	}}
}

// AC-8: a scheduled job survives a "restart" — a fresh Attributor over the
// same store dir drains it and re-publishes with projects.
//
// This also pins the at-least-once ORDERING the whole design rests on:
// orderCheckSender proves the job is still IN THE STORE at the moment
// SendBlocks runs (publish before delete), not merely that both eventually
// happen. Delete-then-publish would silently lose the block forever on a
// crash between the two; publish-then-delete only ever costs a harmless
// re-publish, since Atlas upserts on (session, start).
func TestAttributionJobSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	job := Job{Source: "claude_code", SessionID: "s1", Path: "/tmp/x.jsonl", Start: 100, End: 700}
	if err := st.Put(job); err != nil {
		t.Fatalf("Put: %v", err)
	}

	freshStore := NewStore(dir)
	sender := &orderCheckSender{st: freshStore, job: job}
	cl := successClient("proj_pay")
	dig := digesterFor("s1", 100, 700)
	a := New(freshStore, cl, sender, nil, "actor@x", dig)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	a.drainOnce(ctx)

	if len(sender.sent) != 1 {
		t.Fatalf("expected one re-publish, got %d", len(sender.sent))
	}
	if !sender.checkedAtLeastOne {
		t.Fatal("SendBlocks was never observed — test setup is broken")
	}
	if !sender.sawJobDuringSend {
		t.Fatal("job was already deleted from the store when SendBlocks ran — " +
			"this must publish BEFORE deleting, not the other way around")
	}
	row := sender.sent[0][0]
	if row.ProjectsStatus != enrich.ProjectsAttributed || len(row.Projects) != 1 || row.Projects[0].ID != "proj_pay" {
		t.Fatalf("row = %+v", row)
	}
	if row.SessionID != "s1" {
		t.Fatalf("republished row lost its session: %+v", row)
	}
	if left, _ := NewStore(dir).List(); len(left) != 0 {
		t.Fatalf("job not deleted after success: %d left", len(left))
	}
}

// ⚠️ AMENDED 2026-09-01: pending MUST NOT consume a retry attempt. MaxAttempts
// bounds genuine ERRORS only. Driving drainOnce well past MaxAttempts against
// a client that only ever answers "pending" must never quarantine the job —
// counting "still waiting" as failing would permanently abandon every block
// produced while the model/weights are still provisioning.
func TestPendingNeverConsumesAnAttemptOrQuarantines(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Put(Job{SessionID: "s1", Path: "/tmp/x.jsonl", Start: 1, End: 2}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sender := &fakeSender{}
	dig := digesterFor("s1", 1, 2)
	a := New(st, pendingClient(), sender, nil, "actor@x", dig)
	ctx := context.Background()

	for i := 0; i < MaxAttempts*3; i++ {
		a.drainOnce(ctx)
	}

	jobs, err := st.List()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("job should still be live after %d pending drains, got %d jobs (err=%v)", MaxAttempts*3, len(jobs), err)
	}
	if jobs[0].Attempts != 0 {
		t.Fatalf("pending must not consume an attempt, got Attempts=%d", jobs[0].Attempts)
	}
	if bad, _ := NewStore(dir + "/bad").List(); len(bad) != 0 {
		t.Fatalf("pending must never quarantine, got %d in bad/", len(bad))
	}
	if len(sender.sent) != 0 {
		t.Fatalf("pending must never publish, got %d batches sent", len(sender.sent))
	}
}

// AC-8: a genuine ERROR (transport failure) retries up to MaxAttempts and
// then quarantines — the death-spiral lesson MaxAttempts exists for.
//
// ⚠️ Tightened: this now asserts the job is STILL LIVE, with Attempts ==
// MaxAttempts-1, after exactly MaxAttempts-1 drains — not only that it is
// gone after MaxAttempts. Asserting only the end state lets an off-by-one
// that quarantines one drain early (or late) pass unnoticed.
func TestGenuineErrorRetriesThenQuarantines(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Put(Job{SessionID: "s1", Path: "/tmp/x.jsonl", Start: 1, End: 2}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	dig := digesterFor("s1", 1, 2)
	a := New(st, errorClient(), &fakeSender{}, nil, "actor@x", dig)
	ctx := context.Background()

	for i := 0; i < MaxAttempts-1; i++ {
		a.drainOnce(ctx)
	}
	jobs, err := st.List()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("job should still be live after %d of %d attempts, got %d jobs (err=%v)",
			MaxAttempts-1, MaxAttempts, len(jobs), err)
	}
	if jobs[0].Attempts != MaxAttempts-1 {
		t.Fatalf("Attempts = %d, want %d after %d failed drains", jobs[0].Attempts, MaxAttempts-1, MaxAttempts-1)
	}
	if bad, _ := NewStore(dir + "/bad").List(); len(bad) != 0 {
		t.Fatalf("quarantined %d attempts early", len(bad))
	}

	a.drainOnce(ctx) // the MaxAttempts-th failure
	if jobs, _ := st.List(); len(jobs) != 0 {
		t.Fatalf("job should be quarantined after %d attempts, %d left", MaxAttempts, len(jobs))
	}
	if bad, _ := NewStore(dir + "/bad").List(); len(bad) != 1 {
		t.Fatalf("expected 1 quarantined job, got %d", len(bad))
	}
}

// I2: a transient Atlas outage (SendBlocks failing) must HOLD the job — no
// attempt consumed, never quarantined — the same argument the block emitter
// already makes for its own publish path: a block's identity is
// deterministic and Atlas upserts, so re-publishing after Atlas comes back is
// free, and spending an attempt on Atlas's downtime would let four sweeps of
// an outage quarantine every in-flight job.
func TestPublishFailureHoldsTheJobWithoutConsumingAnAttempt(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Put(Job{SessionID: "s1", Path: "/tmp/x.jsonl", Start: 1, End: 2}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sender := &fakeSender{failN: MaxAttempts * 3} // always fails, well past MaxAttempts
	cl := successClient("proj_pay")
	dig := digesterFor("s1", 1, 2)
	a := New(st, cl, sender, nil, "actor@x", dig)
	ctx := context.Background()

	for i := 0; i < MaxAttempts*3; i++ {
		a.drainOnce(ctx)
	}

	jobs, err := st.List()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("job should still be live after %d publish failures, got %d jobs (err=%v)", MaxAttempts*3, len(jobs), err)
	}
	if jobs[0].Attempts != 0 {
		t.Fatalf("a publish failure must not consume an attempt, got Attempts=%d", jobs[0].Attempts)
	}
	if bad, _ := NewStore(dir + "/bad").List(); len(bad) != 0 {
		t.Fatalf("a publish failure must never quarantine, got %d in bad/", len(bad))
	}
}

// A terminal status that named nothing (skipped:no_projects) is still
// terminal: it must publish (carrying the status) and delete the job, never
// retry — this is what stops a machine with no declared projects from
// spinning on every block forever.
func TestSkippedNoProjectsIsTerminalNotRetried(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Put(Job{SessionID: "s1", Path: "/tmp/x.jsonl", Start: 1, End: 2}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sender := &fakeSender{}
	cl := &fakeClient{ok: true, res: sidecar.AttributeResult{Status: enrich.ProjectsSkippedNoProjects}}
	dig := digesterFor("s1", 1, 2)
	a := New(st, cl, sender, nil, "actor@x", dig)
	a.drainOnce(context.Background())

	if len(sender.sent) != 1 || sender.sent[0][0].ProjectsStatus != enrich.ProjectsSkippedNoProjects {
		t.Fatalf("expected one terminal publish, got %+v", sender.sent)
	}
	if left, _ := st.List(); len(left) != 0 {
		t.Fatalf("terminal status must delete the job, %d left", len(left))
	}
}

// C1: degraded:weights_unavailable is the SAME provisioning-window condition
// pending exists to protect, reached through a different status — the
// sidecar answers this, not pending, while its embedding weights are still
// downloading. It must publish the degraded row (stating its own
// degradation) AND re-spool WITHOUT consuming an attempt, driven well past
// MaxAttempts, exactly like TestPendingNeverConsumesAnAttemptOrQuarantines.
// Getting this wrong reintroduces the amended bug through a status pending
// doesn't cover.
func TestDegradedWeightsUnavailablePublishesAndHoldsWithoutConsumingAnAttempt(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Put(Job{SessionID: "s1", Path: "/tmp/x.jsonl", Start: 1, End: 2}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sender := &fakeSender{}
	cl := &fakeClient{ok: true, res: sidecar.AttributeResult{Status: enrich.ProjectsDegradedWeights}}
	dig := digesterFor("s1", 1, 2)
	a := New(st, cl, sender, nil, "actor@x", dig)
	ctx := context.Background()

	for i := 0; i < MaxAttempts*3; i++ {
		a.drainOnce(ctx)
	}

	jobs, err := st.List()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("job should still be live after %d degraded drains, got %d jobs (err=%v)", MaxAttempts*3, len(jobs), err)
	}
	if jobs[0].Attempts != 0 {
		t.Fatalf("degraded:weights_unavailable must not consume an attempt, got Attempts=%d", jobs[0].Attempts)
	}
	if !jobs[0].DegradedPublished {
		t.Fatal("DegradedPublished must be set once the degraded row has been sent")
	}
	if bad, _ := NewStore(dir + "/bad").List(); len(bad) != 0 {
		t.Fatalf("degraded:weights_unavailable must never quarantine, got %d in bad/", len(bad))
	}
	// NB2 (round 2): publish exactly ONCE, then hold silently — NOT once per
	// sweep for the whole provisioning window (Atlas upserts, so re-sending
	// loses nothing, but MaxAttempts*3 = 12 drains publishing 12 identical
	// rows is needless write amplification for the entire download).
	if len(sender.sent) != 1 {
		t.Fatalf("published %d times across %d drains, want exactly 1 (publish once, then hold silently)",
			len(sender.sent), MaxAttempts*3)
	}
	if sender.sent[0][0].ProjectsStatus != enrich.ProjectsDegradedWeights {
		t.Fatalf("published status = %q, want %q", sender.sent[0][0].ProjectsStatus, enrich.ProjectsDegradedWeights)
	}
}

// NB2 regression: once weights finish provisioning and the sidecar starts
// answering a TERMINAL status, the job must still publish and delete
// normally — DegradedPublished must not suppress the terminal publish.
func TestDegradedThenAttributedStillPublishesTheTerminalRow(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Put(Job{SessionID: "s1", Path: "/tmp/x.jsonl", Start: 1, End: 2}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sender := &fakeSender{}
	cl := &fakeClient{ok: true, res: sidecar.AttributeResult{Status: enrich.ProjectsDegradedWeights}}
	dig := digesterFor("s1", 1, 2)
	a := New(st, cl, sender, nil, "actor@x", dig)
	ctx := context.Background()

	a.drainOnce(ctx) // degraded: publishes once, holds
	if len(sender.sent) != 1 {
		t.Fatalf("expected the one degraded publish, got %d", len(sender.sent))
	}

	// Weights finish provisioning: the sidecar now answers attributed.
	cl.res = sidecar.AttributeResult{Status: enrich.ProjectsAttributed,
		Projects: []enrich.ProjectAttribution{{ID: "proj_pay", Confidence: 0.9, Source: "embedding"}}}
	a.drainOnce(ctx)

	if len(sender.sent) != 2 {
		t.Fatalf("expected a second publish for the terminal answer, got %d total", len(sender.sent))
	}
	if sender.sent[1][0].ProjectsStatus != enrich.ProjectsAttributed {
		t.Fatalf("second publish status = %q, want %q", sender.sent[1][0].ProjectsStatus, enrich.ProjectsAttributed)
	}
	if left, _ := st.List(); len(left) != 0 {
		t.Fatalf("job should be deleted once attributed, %d left", len(left))
	}
}

// C2: a status outside the closed vocabulary (version skew — the sidecar is
// frozen and shipped separately) is a GENUINE ERROR, never a silent success.
// It must consume an attempt and eventually quarantine like any other
// genuine error, and it must never reach a published row.
func TestUnknownStatusIsAGenuineErrorThatConsumesAnAttempt(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Put(Job{SessionID: "s1", Path: "/tmp/x.jsonl", Start: 1, End: 2}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sender := &fakeSender{}
	cl := &fakeClient{ok: true, res: sidecar.AttributeResult{Status: "some_future_status_this_binary_cannot_read"}}
	dig := digesterFor("s1", 1, 2)
	a := New(st, cl, sender, nil, "actor@x", dig)
	ctx := context.Background()

	for i := 0; i < MaxAttempts; i++ {
		a.drainOnce(ctx)
	}

	if jobs, _ := st.List(); len(jobs) != 0 {
		t.Fatalf("job should be quarantined after %d attempts at an unrecognised status, %d left", MaxAttempts, len(jobs))
	}
	if bad, _ := NewStore(dir + "/bad").List(); len(bad) != 1 {
		t.Fatalf("expected 1 quarantined job, got %d", len(bad))
	}
	if len(sender.sent) != 0 {
		t.Fatalf("an unrecognised status must never reach a published row, got %d batches sent", len(sender.sent))
	}
}

// AC-8: first publish is never delayed — Schedule only writes a file and
// never touches the sidecar client.
func TestScheduleIsNonBlocking(t *testing.T) {
	st := NewStore(t.TempDir())
	block := make(chan struct{}) // never closed: Attribute would hang forever if called
	a := New(st, &blockingClient{block: block}, &fakeSender{}, nil, "actor@x", nil)
	done := make(chan struct{})
	go func() {
		a.Schedule(publish.BlockEnrichment{
			Source:    publish.Source{ID: "claude_code"},
			SessionID: "s1",
			Window: enrich.BlockRef{
				Start: time.Unix(1, 0).UTC().Format(time.RFC3339Nano),
				End:   time.Unix(2, 0).UTC().Format(time.RFC3339Nano),
			},
		}, "/tmp/x.jsonl")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Schedule blocked on the sidecar")
	}

	jobs, err := st.List()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("Schedule should have persisted one job, got %d (err=%v)", len(jobs), err)
	}
	if jobs[0].SessionID != "s1" || jobs[0].Start != 1 || jobs[0].End != 2 {
		t.Fatalf("job coordinates wrong: %+v", jobs[0])
	}
}

// I1: the /blocks re-fetch must be bounded, or one job wedged inside an
// unreachable-but-not-disconnecting sidecar parks the WHOLE sweep (every
// other job behind it) until daemon shutdown. Uses a REAL *sidecar.Client
// against a server that accepts the connection and never answers — the shape
// a wedged-but-not-crashed sidecar takes — with blockFetchTimeout shrunk so
// the test does not wait out the real 30s bound.
func TestBlockRefetchIsBounded(t *testing.T) {
	old := blockFetchTimeout
	blockFetchTimeout = 300 * time.Millisecond
	defer func() { blockFetchTimeout = old }()

	block := make(chan struct{}) // closed at the end of the test so srv.Close() can complete
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // accept the connection, never answer within the test's own bound
	}))
	// LIFO: srv.Close() (registered first, so it runs LAST) must not run until
	// the handler goroutine has been released, or Close() itself blocks
	// forever waiting for the in-flight handler to return.
	defer srv.Close()
	defer close(block)

	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Put(Job{SessionID: "s1", Path: "/tmp/x.jsonl", Start: 1, End: 2}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	realDig := sidecar.NewCtx(context.Background(), srv.URL, 5*time.Second) // daemon-lifetime ctx, never expires on its own
	a := New(st, errorClient(), &fakeSender{}, nil, "actor@x", realDig)

	done := make(chan struct{})
	go func() {
		a.drainOnce(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("drainOnce did not return — the /blocks re-fetch has no bound of its own")
	}
}

// I3: a backlog beyond maxPerSweep must be PACED across sweeps, not drained
// all at once inside a single drainOnce call.
func TestDrainOnceCapsWorkAtMaxPerSweep(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	for i := 0; i < maxPerSweep+5; i++ {
		if err := st.Put(Job{SessionID: "s1", Path: "/tmp/x.jsonl", Start: float64(i), End: float64(i) + 1}); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	sender := &fakeSender{}
	cl := successClient("proj_pay")
	// A digester that always answers for whichever job is asked — same shape
	// as digesterFor, but matches by START rather than a single fixed block.
	dig := &multiBlockDigester{sessionID: "s1"}
	a := New(st, cl, sender, nil, "actor@x", dig)

	a.drainOnce(context.Background())

	jobs, err := st.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 5 {
		t.Fatalf("one sweep drained %d jobs, want exactly 5 left (maxPerSweep=%d of %d total)",
			maxPerSweep+5-len(jobs), maxPerSweep, maxPerSweep+5)
	}
	if len(sender.sent) != maxPerSweep {
		t.Fatalf("published %d rows in one sweep, want exactly maxPerSweep=%d", len(sender.sent), maxPerSweep)
	}
}

// multiBlockDigester answers BlocksCharacterised for ANY start within its
// session, unlike fakeDigester's single fixed block — needed so
// TestDrainOnceCapsWorkAtMaxPerSweep's many distinct jobs each resolve.
type multiBlockDigester struct{ sessionID string }

func (d *multiBlockDigester) BlocksCharacterised(path, source, sessionID string,
	since *float64, now time.Time, maxBlocks int,
	resolved enrich.ResolvedFacts) ([]enrich.BlockCharacterisation, *float64, bool) {
	if since == nil {
		return nil, nil, true
	}
	start := *since
	return []enrich.BlockCharacterisation{{
		SessionID: d.sessionID,
		Source:    "claude_code",
		Ref: enrich.BlockRef{
			Start:       time.Unix(int64(start), 0).UTC().Format(time.RFC3339Nano),
			End:         time.Unix(int64(start)+1, 0).UTC().Format(time.RFC3339Nano),
			SpanMinutes: 1.0 / 60.0,
			Evidence:    5,
			StartReason: "session_start",
			EndReason:   "idle",
		},
		StartTS: start,
		EndTS:   start + 1,
	}}, nil, true
}

// recordingPendingClient always answers pending (so every job it sees stays
// held forever, never terminating and never freeing its sweep slot) and
// records which job STARTs it was ever asked to attribute — used to prove
// every job in a backlog eventually gets a turn rather than only the first
// maxPerSweep.
type recordingPendingClient struct {
	mu   sync.Mutex
	seen map[float64]bool
}

func (c *recordingPendingClient) Attribute(path, sessionID string, start, end float64, dims map[string]string) (sidecar.AttributeResult, bool) {
	c.mu.Lock()
	c.seen[start] = true
	c.mu.Unlock()
	return sidecar.AttributeResult{Status: enrich.ProjectsPending}, true
}

// NB3 (round 2 review): a job held indefinitely (pending, or degraded before
// its one publish) must not permanently occupy a sweep slot and starve the
// rest of the backlog. This seeds more than maxPerSweep jobs, all of which
// stay held forever (always pending), and asserts every one of them is
// eventually attributed at least once across a bounded number of sweeps —
// which only holds if the drain window ROTATES rather than always taking
// the same jobs[:maxPerSweep].
func TestDrainRotatesSoAHeldHeadDoesNotStarveTheTail(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	total := maxPerSweep + 3
	for i := 0; i < total; i++ {
		if err := st.Put(Job{SessionID: "s1", Path: "/tmp/x.jsonl", Start: float64(i), End: float64(i) + 1}); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	cl := &recordingPendingClient{seen: map[float64]bool{}}
	dig := &multiBlockDigester{sessionID: "s1"}
	a := New(st, cl, &fakeSender{}, nil, "actor@x", dig)
	ctx := context.Background()

	// ceil(total/maxPerSweep) = 2 sweeps is the minimum needed for full
	// coverage under exact round-robin; a few extra sweeps of margin.
	for i := 0; i < 4; i++ {
		a.drainOnce(ctx)
	}

	cl.mu.Lock()
	defer cl.mu.Unlock()
	if len(cl.seen) != total {
		t.Fatalf("only %d of %d jobs were ever attributed across sweeps — the tail is starved by a held head", len(cl.seen), total)
	}
}

func TestEnabledMirrorsBlocksEnabledShape(t *testing.T) {
	t.Setenv(EnvEnabled, "")
	if Enabled(false) {
		t.Fatal("no env, config false -> should be off")
	}
	if !Enabled(true) {
		t.Fatal("no env, config true -> should be on")
	}
	t.Setenv(EnvEnabled, "1")
	if !Enabled(false) {
		t.Fatal("env=1 must win over a false config")
	}
	t.Setenv(EnvEnabled, "0")
	if Enabled(true) {
		t.Fatal("env=0 must win over a true config")
	}
}

// ---------------------------------------------------------------------------
// C4 / I5 / I6 — the final-review fixes.
// ---------------------------------------------------------------------------

// C4 (second half): `skipped:no_projects` is a statement about the SIDECAR, not
// about the org, and treating it as terminal is irreversible — the row publishes
// attributed to nothing and the job is DELETED, so nothing can ever repair it.
// While the daemon believes projects are declared, the job must be HELD and the
// list re-asserted instead.
func TestSkippedNoProjectsIsHeldWhileTheDaemonBelievesProjectsExist(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Put(Job{SessionID: "s1", Path: "/tmp/x.jsonl", Start: 1, End: 2}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sender := &fakeSender{}
	cl := &fakeClient{ok: true, res: sidecar.AttributeResult{Status: enrich.ProjectsSkippedNoProjects}}
	reposts := 0
	a := New(st, cl, sender, nil, "actor@x", digesterFor("s1", 1, 2)).
		WithProjects(func() bool { return true }, func() { reposts++ })

	// Driven well past MaxAttempts: a held job must never age into a quarantine.
	for i := 0; i < MaxAttempts+2; i++ {
		a.drainOnce(context.Background())
	}
	if len(sender.sent) != 0 {
		t.Fatalf("nothing may be published while the daemon holds a project list, got %+v", sender.sent)
	}
	jobs, _ := st.List()
	if len(jobs) != 1 {
		t.Fatalf("the job must be held, got %d in the store", len(jobs))
	}
	if jobs[0].Attempts != 0 {
		t.Fatalf("a sidecar that lost its project list is not the job's fault; Attempts=%d", jobs[0].Attempts)
	}
	if bad, _ := NewStore(dir + "/bad").List(); len(bad) != 0 {
		t.Fatal("a held job must never quarantine")
	}
	if reposts == 0 {
		t.Fatal("the daemon must be asked to re-tell the sidecar its project list")
	}
}

// The other side of the same switch: with NO project list known, the status is
// the honest terminal answer it always was. Pins that the fix did not turn a
// machine that genuinely declares nothing into one that spins forever.
func TestSkippedNoProjectsStaysTerminalWhenNoProjectsAreKnown(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Put(Job{SessionID: "s1", Path: "/tmp/x.jsonl", Start: 1, End: 2}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sender := &fakeSender{}
	cl := &fakeClient{ok: true, res: sidecar.AttributeResult{Status: enrich.ProjectsSkippedNoProjects}}
	a := New(st, cl, sender, nil, "actor@x", digesterFor("s1", 1, 2)).
		WithProjects(func() bool { return false }, func() { t.Fatal("nothing to re-post") })
	a.drainOnce(context.Background())

	if len(sender.sent) != 1 {
		t.Fatalf("expected one terminal publish, got %+v", sender.sent)
	}
	if left, _ := st.List(); len(left) != 0 {
		t.Fatalf("terminal status must delete the job, %d left", len(left))
	}
}

// I5: an older frozen sidecar 404s /attribute. That is version skew in a
// component that updates on its own cadence — the job is HELD, not spent, and
// never quarantined into a subdirectory Store.List will not re-read.
func TestA404FromAnOlderSidecarHoldsTheJobRatherThanConsumingAttempts(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Put(Job{SessionID: "s1", Path: "/tmp/x.jsonl", Start: 1, End: 2}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sender := &fakeSender{}
	cl := &fakeClient{ok: false, res: sidecar.AttributeResult{RouteUnsupported: true}}
	a := New(st, cl, sender, nil, "actor@x", digesterFor("s1", 1, 2))
	for i := 0; i < MaxAttempts+2; i++ {
		a.drainOnce(context.Background())
	}
	jobs, _ := st.List()
	if len(jobs) != 1 || jobs[0].Attempts != 0 {
		t.Fatalf("a 404 must hold the job unchanged, got %+v", jobs)
	}
	if bad, _ := NewStore(dir + "/bad").List(); len(bad) != 0 {
		t.Fatal("version skew must never quarantine: spool/attrib/bad/ is never re-read")
	}
	if len(sender.sent) != 0 {
		t.Fatalf("nothing may publish off a 404, got %+v", sender.sent)
	}
}

// I6: only an `attributed` dimension may be read as the block's answer. A
// `thin`/`tie`/`no_majority` leader fed to the sidecar as a repo is worth 0.15
// of the 0.49 assignment threshold against the WRONG project.
func TestDimsFromDropsEveryDimensionThatIsNotAttributed(t *testing.T) {
	an := enrich.WindowAnalysis{Workstreams: map[string]enrich.Labeled{
		"project":  {Value: "acme-billing", Status: enrich.WorkstreamAttributed},
		"branch":   {Value: "fix/rounding"}, // pre-16 sidecar: no status means attributed
		"language": {Value: "python", Status: "thin"},
		"skill":    {Value: "review", Status: "tie"},
		"model":    {Value: "sonnet", Status: "no_majority"},
		"tooling":  {Value: "pytest", Status: "absent"},
		"output":   {Value: "docs", Status: "some_future_status"},
		"empty":    {Value: "", Status: enrich.WorkstreamAttributed},
	}}
	got := dimsFrom(an)
	want := map[string]string{"project": "acme-billing", "branch": "fix/rounding"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// And the whole-map case: every dimension sub-floor means NO dims at all, not
// an empty-but-present map — the sidecar's metadata boost must see nothing
// rather than something wrong.
func TestDimsFromIsNilWhenNothingIsAttributed(t *testing.T) {
	an := enrich.WindowAnalysis{Workstreams: map[string]enrich.Labeled{
		"project": {Value: "acme-billing", Status: "thin"},
	}}
	if got := dimsFrom(an); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

// ⚠️ THE OTHER SIDE OF THE 404 FIX, AND THE REASON IT NEEDED ONE. `RouteUnsupported`
// is unbounded-hold semantics, so it must attach to version skew and to nothing else.
// An unreadable transcript — deleted, rotated, moved — is a GENUINE error: no retry
// makes an absent file readable, so it consumes an attempt and eventually quarantines,
// which is what bounds it. Under the first cut of the I5 fix the sidecar answered 404
// for both, the two merged, and each affected block leaked one permanently-resident job
// into a 24-job sweep budget. The route now answers 410 there; this pins the daemon half
// of that split, and the client half is pinned across the language boundary by
// TestTheAttributeRouteNeverAnswers404ForAnythingButAMissingRoute.
func TestAnUnreadableTranscriptConsumesAnAttemptAndEventuallyQuarantines(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Put(Job{SessionID: "s1", Path: "/tmp/gone.jsonl", Start: 1, End: 2}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sender := &fakeSender{}
	// What the client returns for a 410: a failed call with NO RouteUnsupported.
	cl := &fakeClient{ok: false}
	a := New(st, cl, sender, nil, "actor@x", digesterFor("s1", 1, 2))
	for i := 0; i < MaxAttempts; i++ {
		a.drainOnce(context.Background())
	}
	if left, _ := st.List(); len(left) != 0 {
		t.Fatalf("an unreadable transcript must be bounded, not held forever: %d still live", len(left))
	}
	bad, _ := os.ReadDir(filepath.Join(dir, "bad"))
	if len(bad) != 1 {
		t.Fatalf("expected the job to quarantine after %d attempts, got %d in bad/", MaxAttempts, len(bad))
	}
}

// Secondary: every held `skipped:no_projects` job in a sweep has ONE cause — one
// sidecar that lost one list — so the re-post is collapsed to at most one per
// sweep. Ungated, a backlog produced up to maxPerSweep (24) identical POSTs,
// each a synchronous 30-second-budgeted call serialised inside the drain loop.
func TestTheProjectListIsRePostedAtMostOncePerSweep(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	for i := 0; i < 5; i++ {
		if err := st.Put(Job{SessionID: "s1", Path: "/tmp/x.jsonl",
			Start: float64(i + 1), End: float64(i + 2)}); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	cl := &fakeClient{ok: true, res: sidecar.AttributeResult{Status: enrich.ProjectsSkippedNoProjects}}
	reposts := 0
	a := New(st, cl, &fakeSender{}, nil, "actor@x", &multiBlockDigester{sessionID: "s1"}).
		WithProjects(func() bool { return true }, func() { reposts++ })

	a.drainOnce(context.Background())
	if cl.calls != 5 {
		t.Fatalf("expected all 5 jobs drained, got %d /attribute calls", cl.calls)
	}
	if reposts != 1 {
		t.Fatalf("5 jobs with one shared cause must produce ONE re-post, got %d", reposts)
	}
	// The next sweep may ask again — the flag is per sweep, not a latch, or a
	// re-post that failed would never be retried.
	a.drainOnce(context.Background())
	if reposts != 2 {
		t.Fatalf("the dedup must reset each sweep, got %d re-posts over two sweeps", reposts)
	}
}
