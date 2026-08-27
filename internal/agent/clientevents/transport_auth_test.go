package clientevents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/retry"
)

// onePolicy is a single-attempt policy: these tests are about what happens to a
// batch AFTER the retries are exhausted, so retrying just slows them down.
func onePolicy() retry.Policy { return retry.Policy{MaxAttempts: 1, BaseDelay: 0, Multiplier: 1} }

// A rejection must KEEP the batch and STOP the drain.
//
// ⚠️ Deleting it is today's behaviour — retry.IsTransient classifies 401 as
// permanent, and DrainSpool implements permanent as "delete the file and carry
// on". Both halves are wrong for a rejection: the batch is fine and will deliver
// after a re-onboard, and carrying on means learning the same rejection once per
// spooled file. On a laptop back from a week offline that is hundreds of
// pointless requests at the moment an org least wants them.
func TestDrainStopsAndKeepsOnAuthRejection(t *testing.T) {
	dir := t.TempDir()
	tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" }, dir)
	tr.policy = onePolicy()
	posts := 0
	tr.post = func(ctx context.Context, body []byte) (int, error) { posts++; return 401, nil }
	authHits := 0
	tr.OnAuthRejection(func() { authHits++ })

	for i := 0; i < 5; i++ {
		if err := tr.spool([]byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	_ = tr.DrainSpool(context.Background())

	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 5 {
		t.Fatalf("spool has %d files after an auth rejection, want all 5 kept", len(files))
	}
	if posts != 1 {
		t.Fatalf("posted %d times, want 1 — the drain must stop on the first rejection", posts)
	}
	if authHits != 1 {
		t.Fatalf("auth hook fired %d times, want exactly 1", authHits)
	}
}

// 5xx is NOT a rejection and must never trigger re-onboarding: nothing is wrong
// with the credential, the laptop is on a train.
//
// ⚠️ This is the assertion most likely to regress, because 401 and 503 arrive on
// the same code path.
func TestUnavailableNeverTriggersReonboard(t *testing.T) {
	dir := t.TempDir()
	tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" }, dir)
	tr.policy = onePolicy()
	tr.post = func(ctx context.Context, body []byte) (int, error) { return 503, nil }
	authHits := 0
	tr.OnAuthRejection(func() { authHits++ })

	if err := tr.spool([]byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	_ = tr.DrainSpool(context.Background())

	if authHits != 0 {
		t.Fatalf("5xx triggered %d re-onboards, want 0", authHits)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("5xx left %d files, want the batch KEPT", len(files))
	}
}

// A refused payload belongs to that batch, not the connection, so the drain
// CONTINUES. Stopping would let one bad batch block every good one behind it —
// head-of-line blocking that presents as "telemetry silently stopped".
func TestRefusedPayloadDoesNotBlockTheQueueBehindIt(t *testing.T) {
	dir := t.TempDir()
	tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" }, dir)
	tr.policy = onePolicy()
	delivered := 0
	tr.post = func(ctx context.Context, body []byte) (int, error) {
		if bytes.Contains(body, []byte("bad")) {
			return 422, nil
		}
		delivered++
		return 200, nil
	}
	if err := tr.spool([]byte(`{"v":"bad"}`)); err != nil {
		t.Fatal(err)
	}
	if err := tr.spool([]byte(`{"v":"good"}`)); err != nil {
		t.Fatal(err)
	}
	_ = tr.DrainSpool(context.Background())

	if delivered != 1 {
		t.Fatalf("delivered %d good batches, want 1 — a refused batch must not stall the queue", delivered)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(files) != 0 {
		t.Fatalf("%d files left, want 0 (bad dropped, good delivered)", len(files))
	}
}

// Days offline: the spool fills and drop-oldest evicts. The NEWEST events must
// survive — the failure to catch is an eviction policy that keeps the oldest.
func TestSpoolCapKeepsTheNewest(t *testing.T) {
	dir := t.TempDir()
	tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" }, dir)
	tr.maxSpool = 3
	for i := 0; i < 10; i++ {
		if err := tr.spool([]byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 3 {
		t.Fatalf("%d files, want the cap of 3", len(files))
	}
	newest := false
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if bytes.Contains(b, []byte(`{"n":9}`)) {
			newest = true
		}
	}
	if !newest {
		t.Fatal("eviction discarded the NEWEST batch — it must drop the oldest")
	}
}

// Kill -9 mid-write leaves a torn file. The drain must not choke on it: one
// corrupt file must not stall every good batch behind it.
func TestATornSpoolFileDoesNotStallTheDrain(t *testing.T) {
	dir := t.TempDir()
	tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" }, dir)
	tr.policy = onePolicy()
	delivered := 0
	tr.post = func(ctx context.Context, body []byte) (int, error) { delivered++; return 200, nil }

	// Sorts before the spooled file, so it is drained first.
	if err := os.WriteFile(filepath.Join(dir, "0000-torn.json"), []byte(`{"resourceL`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tr.spool([]byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := tr.DrainSpool(context.Background()); err != nil {
		t.Fatalf("drain errored on a torn file: %v", err)
	}
	if delivered < 1 {
		t.Fatal("the good batch behind the torn file never delivered")
	}
}

// Disk full: spooling fails. The caller must learn about it (an error), and the
// process must not panic — the daemon stays up and keeps serving the listener.
func TestSpoolFailureIsReportedNotFatal(t *testing.T) {
	base := t.TempDir()
	blocker := filepath.Join(base, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// spoolDir is a path UNDER a regular file: MkdirAll must fail.
	tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" },
		filepath.Join(blocker, "spool"))
	tr.policy = onePolicy()
	tr.post = func(ctx context.Context, body []byte) (int, error) { return 0, errors.New("network down") }

	if err := tr.Deliver(context.Background(), []byte(`{"n":1}`)); err == nil {
		t.Fatal("an unspoolable failure was reported as success")
	}
}
