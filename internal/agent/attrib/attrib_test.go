package attrib

import (
	"context"
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

// fakeSender records every batch handed to SendBlocks.
type fakeSender struct{ sent [][]publish.BlockEnrichment }

func (f *fakeSender) SendBlocks(rows []publish.BlockEnrichment) error {
	f.sent = append(f.sent, rows)
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
func TestAttributionJobSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Put(Job{Source: "claude_code", SessionID: "s1", Path: "/tmp/x.jsonl", Start: 100, End: 700}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	sender := &fakeSender{}
	cl := successClient("proj_pay")
	dig := digesterFor("s1", 100, 700)
	a := New(NewStore(dir), cl, sender, nil, "actor@x", dig)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	a.drainOnce(ctx)

	if len(sender.sent) != 1 {
		t.Fatalf("expected one re-publish, got %d", len(sender.sent))
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
func TestGenuineErrorRetriesThenQuarantines(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	if err := st.Put(Job{SessionID: "s1", Path: "/tmp/x.jsonl", Start: 1, End: 2}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	dig := digesterFor("s1", 1, 2)
	a := New(st, errorClient(), &fakeSender{}, nil, "actor@x", dig)
	ctx := context.Background()
	for i := 0; i < MaxAttempts; i++ {
		a.drainOnce(ctx)
	}
	if jobs, _ := st.List(); len(jobs) != 0 {
		t.Fatalf("job should be quarantined after %d attempts, %d left", MaxAttempts, len(jobs))
	}
	if bad, _ := NewStore(dir + "/bad").List(); len(bad) != 1 {
		t.Fatalf("expected 1 quarantined job, got %d", len(bad))
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
