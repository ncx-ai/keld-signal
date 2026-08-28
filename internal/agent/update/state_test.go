package update

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLoadStateMissingFileIsZeroAndNoError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	s, err := LoadState(p)
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if s.PendingConfirm || s.To != "" {
		t.Fatalf("want zero state, got %+v", s)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	want := State{
		From: "v0.4.1", To: "v0.4.2",
		InstallDir:     "/home/u/.local/bin",
		Prev:           []string{"/home/u/.local/bin/keld.prev"},
		PendingConfirm: true,
		AttemptedAt:    time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC),
		FailedVersions: []string{"v0.4.0"},
		LastOutcome:    "staged",
	}
	if err := SaveState(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.From != want.From || got.To != want.To || !got.PendingConfirm ||
		got.InstallDir != want.InstallDir || len(got.Prev) != 1 ||
		!got.AttemptedAt.Equal(want.AttemptedAt) || len(got.FailedVersions) != 1 {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

// A corrupt marker must never wedge the daemon. It is moved aside and read as
// a zero state — "no update in flight" — which is the only safe reading: the
// alternative is a daemon that refuses to start because of a file it wrote.
func TestCorruptStateIsMovedAsideAndReadsZero(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadState(p)
	if err != nil {
		t.Fatalf("corrupt state must not error: %v", err)
	}
	if s.PendingConfirm {
		t.Fatal("corrupt state must read as no-update-in-flight")
	}
	if _, err := os.Stat(p + ".bad"); err != nil {
		t.Fatalf("corrupt file was not preserved for diagnosis: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("corrupt file should have been moved aside, not left in place")
	}
}

func TestMarkFailedDedups(t *testing.T) {
	var s State
	s.MarkFailed("v1")
	s.MarkFailed("v1")
	s.MarkFailed("v2")
	if len(s.FailedVersions) != 2 {
		t.Fatalf("want 2 distinct, got %v", s.FailedVersions)
	}
	if !s.HasFailed("v1") || !s.HasFailed("v2") || s.HasFailed("v3") {
		t.Fatalf("HasFailed wrong on %v", s.FailedVersions)
	}
}

// HasFailed compares normalized versions so a pin written "0.4.2" cannot
// sneak past a failure recorded as "v0.4.2".
func TestHasFailedNormalizes(t *testing.T) {
	var s State
	s.MarkFailed("v0.4.2")
	if !s.HasFailed("0.4.2") {
		t.Fatal("normalized comparison expected")
	}
}

// The failed set is bounded: a machine that has been fighting a bad pin for a
// year must not accumulate an unbounded list in a file read at every start.
func TestFailedVersionsAreBounded(t *testing.T) {
	var s State
	for i := 0; i < maxFailedVersions+10; i++ {
		s.MarkFailed(string(rune('a'+i%26)) + string(rune('0'+i/26)))
	}
	if len(s.FailedVersions) > maxFailedVersions {
		t.Fatalf("unbounded: %d", len(s.FailedVersions))
	}
	// The most recent failure must survive the trim — it is the one the next
	// decision is about.
	if !s.HasFailed(s.FailedVersions[len(s.FailedVersions)-1]) {
		t.Fatal("newest failure evicted")
	}
}

func TestSaveStateIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	p := filepath.Join(t.TempDir(), "state.json")
	if err := SaveState(p, State{To: "v1"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
}

// SaveState creates the directory it is pointed at: the very first update on a
// machine writes into ~/.keld/update, which nothing else creates.
func TestSaveStateCreatesDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "update", "state.json")
	if err := SaveState(p, State{To: "v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

// A save must be atomic: a crash mid-write must not leave a half-written
// marker, because the file it half-writes is the one that decides whether to
// roll back.
func TestSaveStateLeavesNoTempBehind(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	if err := SaveState(p, State{To: "v1"}); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Name() != "state.json" {
		t.Fatalf("temp file left behind: %v", ents)
	}
}
