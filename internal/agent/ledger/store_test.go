package ledger

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// setHome points the ledger at a fresh temp directory, exactly as
// internal/spool's tests do — each test gets its own KELD_HOME and hence its
// own ledger.db, so no test can ever touch the developer's real ~/.keld
// (AGENTS.md is explicit that a test which does that is a worse defect than
// the one it's checking for).
func setHome(t *testing.T) {
	t.Helper()
	t.Setenv("KELD_HOME", t.TempDir())
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tm
}

// --- stage absence / presence -------------------------------------------

func TestUnmarkedStagesAreAbsentFromCells(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s1", Start: 1000}
	s.Cut(k, 1060, "idle", "budget", "claude_code", mustTime(t, "2026-09-04T17:00:00Z"))

	snap, err := s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(snap.Blocks))
	}
	cells := snap.Blocks[0].Cells
	if _, ok := cells["cut"]; !ok {
		t.Fatal("cut cell should be present")
	}
	for _, stage := range []string{"measured", "attributed", "sent", "received"} {
		if _, ok := cells[stage]; ok {
			t.Fatalf("stage %q was never marked and must be ABSENT from cells, got %#v", stage, cells[stage])
		}
	}

	// The absence must also hold at the raw JSON level: marshal and check
	// the literal bytes never contain the key at all.
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	block := generic["blocks"].([]any)[0].(map[string]any)
	rawCells := block["cells"].(map[string]any)
	if len(rawCells) != 1 {
		t.Fatalf("want exactly 1 cell key in JSON, got %v", rawCells)
	}
}

// --- closed reason vocabulary --------------------------------------------

func TestReasonOnlyFromClosedSet(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s2", Start: 2000}
	s.Cut(k, 2060, "idle", "budget", "claude_code", time.Now())

	// A caller that doesn't respect the closed set (Reason is just a string
	// type, so nothing stops this at compile time) must not get its free
	// text stored verbatim.
	freeText := Reason("atlas said: temporary failure in name resolution, retry in 30s")
	s.Failed(k, StageReceived, freeText, 503, time.Now())

	snap, err := s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	cell := snap.Blocks[0].Cells["received"]
	if r, _ := cell["reason"].(string); r == string(freeText) {
		t.Fatalf("free-text reason was stored verbatim: %q", r)
	}
	if _, ok := cell["reason"]; ok {
		t.Fatalf("an unrecognised reason must clamp to ReasonNone (absent), got %v", cell["reason"])
	}

	// A real closed-set reason passes through unchanged.
	s.Failed(k, StageReceived, ReasonAtlasRejected, 401, time.Now())
	snap, _ = s.Read(time.Time{}, 10)
	cell = snap.Blocks[0].Cells["received"]
	if cell["reason"] != string(ReasonAtlasRejected) {
		t.Fatalf("want reason %q, got %v", ReasonAtlasRejected, cell["reason"])
	}
}

func TestBlockBoundaryReasonClamped(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s3", Start: 3000}
	s.Cut(k, 3060, "not a real boundary reason", "budget", "claude_code", time.Now())

	snap, _ := s.Read(time.Time{}, 10)
	be := snap.Blocks[0]
	if be.StartReason != "" {
		t.Fatalf("unrecognised start_reason must clamp to empty, got %q", be.StartReason)
	}
	if be.EndReason != "budget" {
		t.Fatalf("recognised end_reason must pass through, got %q", be.EndReason)
	}
}

// --- idempotence / ordering ------------------------------------------------

func TestFailedThenOKClearsReason(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s4", Start: 4000}
	s.Cut(k, 4060, "idle", "budget", "claude_code", time.Now())

	s.Failed(k, StageReceived, ReasonAtlasUnavailable, 0, mustTime(t, "2026-09-04T10:00:00Z"))
	snap, _ := s.Read(time.Time{}, 10)
	cell := snap.Blocks[0].Cells["received"]
	if cell["status"] != string(StatusFailed) || cell["reason"] != string(ReasonAtlasUnavailable) {
		t.Fatalf("expected failed/atlas_unavailable, got %#v", cell)
	}

	s.Received(k, 200, mustTime(t, "2026-09-04T10:05:00Z"))
	snap, _ = s.Read(time.Time{}, 10)
	cell = snap.Blocks[0].Cells["received"]
	if cell["status"] != string(StatusOK) {
		t.Fatalf("want ok after retry succeeded, got %v", cell["status"])
	}
	if _, ok := cell["reason"]; ok {
		t.Fatalf("a cleared reason must be absent from the cell, got %v", cell["reason"])
	}
	if hs, ok := cell["http_status"]; !ok || hs.(int64) != 200 {
		t.Fatalf("want http_status 200 on the successful receive, got %v", cell["http_status"])
	}
}

func TestFailureAfterSuccessRetainsBoth(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s5", Start: 5000}
	s.Cut(k, 5060, "idle", "budget", "claude_code", time.Now())

	okAt := mustTime(t, "2026-09-04T11:00:00Z")
	s.Sent(k, okAt)
	failAt := mustTime(t, "2026-09-04T11:10:00Z")
	// A stage marked failed after having been marked ok — e.g. a resend
	// attempt that broke. Sent has no natural "Failed" caller in the wire
	// pipeline described in AGENTS.md, but the ledger must handle it for any
	// stage regardless, since Failed(k, stage, ...) is generic.
	s.Failed(k, StageSent, ReasonAtlasUnavailable, 0, failAt)

	snap, err := s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	cell := snap.Blocks[0].Cells["sent"]
	if cell["status"] != string(StatusFailed) {
		t.Fatalf("want failed (latest transition), got %v", cell["status"])
	}
	if cell["reason"] != string(ReasonAtlasUnavailable) {
		t.Fatalf("want the new failure's reason, got %v", cell["reason"])
	}
	if cell["at"] != failAt.UTC().Format(time.RFC3339) {
		t.Fatalf("want at=%s (the failure's own time), got %v", failAt.UTC().Format(time.RFC3339), cell["at"])
	}
	gotOkAt, ok := cell["ok_at"]
	if !ok {
		t.Fatal("a failure after a success must retain the success's time as ok_at — it is currently absent")
	}
	if gotOkAt != okAt.UTC().Format(time.RFC3339) {
		t.Fatalf("want ok_at=%s (the earlier success), got %v", okAt.UTC().Format(time.RFC3339), gotOkAt)
	}
}

func TestDoubleMarkingOKAdvancesTimestamp(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s6", Start: 6000}
	s.Cut(k, 6060, "idle", "budget", "claude_code", time.Now())

	t1 := mustTime(t, "2026-09-04T12:00:00Z")
	t2 := mustTime(t, "2026-09-04T12:05:00Z")
	s.Sent(k, t1)
	s.Sent(k, t2)

	snap, _ := s.Read(time.Time{}, 10)
	cell := snap.Blocks[0].Cells["sent"]
	if cell["at"] != t2.UTC().Format(time.RFC3339) {
		t.Fatalf("want the latest ok time, got %v", cell["at"])
	}
}

// --- CutPending -------------------------------------------------------------

func TestCutPendingClearedByLaterCut(t *testing.T) {
	setHome(t)
	s := New()
	s.CutPending("stuck-session", ReasonSidecarOutdated, time.Now())

	snap, err := s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Pending) != 1 || snap.Pending[0].Session != "stuck-session" {
		t.Fatalf("want 1 pending entry, got %#v", snap.Pending)
	}
	if snap.Pending[0].Reason != string(ReasonSidecarOutdated) {
		t.Fatalf("want reason %q, got %q", ReasonSidecarOutdated, snap.Pending[0].Reason)
	}

	// The sidecar caught up and a real block was cut for that session.
	s.Cut(BlockKey{Session: "stuck-session", Start: 9000}, 9060, "idle", "budget", "claude_code", time.Now())

	snap, err = s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Pending) != 0 {
		t.Fatalf("pending entry should be cleared once blocks can be asked for again, got %#v", snap.Pending)
	}
}

func TestCutPendingRejectsFreeTextReason(t *testing.T) {
	setHome(t)
	s := New()
	s.CutPending("s7", Reason("some free-form explanation"), time.Now())
	snap, _ := s.Read(time.Time{}, 10)
	if snap.Pending[0].Reason != "" {
		t.Fatalf("want clamped-to-empty reason, got %q", snap.Pending[0].Reason)
	}
}

// --- Attribute --------------------------------------------------------------

func TestAttributeOKShowsProjectAndMethod(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s8", Start: 8000}
	s.Cut(k, 8060, "idle", "budget", "claude_code", time.Now())
	s.Attribute(k, Attributed{ProjectID: "p_keld_signal", Method: MethodRepo}, ReasonNone, time.Now())

	snap, _ := s.Read(time.Time{}, 10)
	cell := snap.Blocks[0].Cells["attributed"]
	if cell["status"] != string(StatusOK) {
		t.Fatalf("want ok, got %v", cell["status"])
	}
	if cell["project_id"] != "p_keld_signal" || cell["method"] != string(MethodRepo) {
		t.Fatalf("want project_id/method, got %#v", cell)
	}
}

func TestAttributeConflictShowsCompetingProjects(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s9", Start: 9500}
	s.Cut(k, 9560, "idle", "budget", "claude_code", time.Now())
	s.Attribute(k, Attributed{Conflict: []string{"p_a", "p_b"}}, ReasonConflict, time.Now())

	snap, _ := s.Read(time.Time{}, 10)
	cell := snap.Blocks[0].Cells["attributed"]
	if cell["status"] != string(StatusFailed) || cell["reason"] != string(ReasonConflict) {
		t.Fatalf("want failed/conflict, got %#v", cell)
	}
	conflict, ok := cell["conflict"].([]string)
	if !ok || len(conflict) != 2 {
		t.Fatalf("want 2 competing project ids, got %#v", cell["conflict"])
	}
	if _, ok := cell["project_id"]; ok {
		t.Fatal("a conflicted attribution must not also publish a winning project_id")
	}
}

func TestAttributeNoRuleMatchedIsFailedWithNoProject(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s10", Start: 10000}
	s.Cut(k, 10060, "idle", "budget", "claude_code", time.Now())
	s.Attribute(k, Attributed{}, ReasonNoRuleMatched, time.Now())

	snap, _ := s.Read(time.Time{}, 10)
	cell := snap.Blocks[0].Cells["attributed"]
	if cell["status"] != string(StatusFailed) || cell["reason"] != string(ReasonNoRuleMatched) {
		t.Fatalf("want failed/no_rule_matched, got %#v", cell)
	}
	if _, ok := cell["project_id"]; ok {
		t.Fatal("no project_id should be published when nothing matched")
	}
}

// --- Health / SetHealth ------------------------------------------------------

func TestSetHealthRoundTrips(t *testing.T) {
	setHome(t)
	s := New()
	at := mustTime(t, "2026-09-04T17:34:00Z")
	s.SetHealth(Health{Key: HealthDaemon, Status: StatusOK, Detail: "2.5.0", At: at})
	s.SetHealth(Health{Key: HealthAtlas, Status: StatusFailed, Detail: string(ReasonAtlasRejected), At: at})

	snap, err := s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Health) != 2 {
		t.Fatalf("want 2 health entries, got %d: %#v", len(snap.Health), snap.Health)
	}
	byKey := map[string]HealthEntry{}
	for _, h := range snap.Health {
		byKey[h.Key] = h
	}
	if byKey["daemon"].Status != "ok" || byKey["daemon"].Detail != "2.5.0" {
		t.Fatalf("bad daemon health: %#v", byKey["daemon"])
	}
	if byKey["atlas"].Status != "failed" || byKey["atlas"].Detail != "atlas_rejected" {
		t.Fatalf("bad atlas health: %#v", byKey["atlas"])
	}
}

func TestSetHealthRejectsUnknownKeyAndStatus(t *testing.T) {
	setHome(t)
	s := New()
	s.SetHealth(Health{Key: HealthKey("totally_made_up"), Status: StatusOK, At: time.Now()})
	s.SetHealth(Health{Key: HealthDaemon, Status: Status("kinda ok i guess"), At: time.Now()})

	snap, err := s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Health) != 0 {
		t.Fatalf("neither write should have landed, got %#v", snap.Health)
	}
}

// --- Read: since / limit -----------------------------------------------------

func TestReadSinceFiltersByBlockStart(t *testing.T) {
	setHome(t)
	s := New()
	s.Cut(BlockKey{Session: "old", Start: 1000}, 1060, "idle", "budget", "claude_code", time.Now())
	s.Cut(BlockKey{Session: "new", Start: 5000}, 5060, "idle", "budget", "claude_code", time.Now())

	snap, err := s.Read(time.Unix(3000, 0), 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Blocks) != 1 || snap.Blocks[0].Key.Session != "new" {
		t.Fatalf("want only the block at-or-after since, got %#v", snap.Blocks)
	}
}

func TestReadLimitBoundsResults(t *testing.T) {
	setHome(t)
	s := New()
	for i := int64(0); i < 5; i++ {
		s.Cut(BlockKey{Session: "s", Start: i * 100}, i*100+60, "idle", "budget", "claude_code", time.Now())
	}
	snap, err := s.Read(time.Time{}, 2)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Blocks) != 2 {
		t.Fatalf("want 2 blocks (limit), got %d", len(snap.Blocks))
	}
}

// --- deleted-file recovery ---------------------------------------------------

func TestDeletedDBFileIsRecreatedOnNextWrite(t *testing.T) {
	setHome(t)
	s := New()
	s.Cut(BlockKey{Session: "before", Start: 1}, 61, "idle", "budget", "claude_code", time.Now())

	removeDBFiles(t)

	s.Sent(BlockKey{Session: "after", Start: 2}, time.Now())

	snap, err := s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read after recreation: %v", err)
	}
	if len(snap.Blocks) != 1 || snap.Blocks[0].Key.Session != "after" {
		t.Fatalf("expected only the post-deletion write to survive (old data is gone, which is expected — the file was deleted), got %#v", snap.Blocks)
	}
}

func removeDBFiles(t *testing.T) {
	t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(dbPath() + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s: %v", dbPath()+suffix, err)
		}
	}
}
