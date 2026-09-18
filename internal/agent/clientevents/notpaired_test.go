package clientevents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ⚠️ **"NOT PAIRED" MUST SPOOL, WHERE "SEND TO ATLAS IS OFF" DISCARDS.** Both
// present as an empty endpoint and the two are one line apart in this file, so
// the distinction is asserted rather than described: a batch produced before
// somebody finished signing in is owed to them, and the telemetry proxy is the
// path that produces them — it binds port 14318 from the daemon's first second,
// which is earlier than any address is known.
func TestAPendingTransportSpoolsRatherThanDiscardingOrDialling(t *testing.T) {
	dir := t.TempDir()
	tr := NewPendingTransport(func() string { return "" }, func() string { return "tok" }, dir)
	tr.post = func(context.Context, []byte) (int, []byte, error) {
		t.Fatal("a pending transport dialled; it has no address to dial")
		return 0, nil, nil
	}

	if err := tr.Deliver(context.Background(), []byte(`{"batch":1}`)); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("Deliver while unpaired = %v, want ErrNotPaired", err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("spool holds %d files, want the one batch that could not be sent", len(files))
	}

	// ⚠️ AND A DRAIN MUST NOT RUN. Its permanent-failure branch DELETES the
	// file, so draining an unpaired transport would classify "no address" as a
	// bad payload and destroy exactly the backlog the pairing is about to make
	// deliverable.
	if err := tr.DrainSpool(context.Background()); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("DrainSpool while unpaired = %v, want ErrNotPaired", err)
	}
	files, _ = filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("spool holds %d files after an unpaired drain, want 1 — nothing may be deleted", len(files))
	}

	// A plain Transport with no endpoint is the OTHER case and still discards.
	off := NewTransport("", func() string { return "tok" }, filepath.Join(dir, "off"))
	if err := off.Deliver(context.Background(), []byte(`{"batch":2}`)); err != nil {
		t.Fatalf("Send to Atlas off must read as delivered, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "off")); !os.IsNotExist(err) {
		t.Fatal("Send to Atlas off spooled a batch; it must discard, or the queue can never drain")
	}
}

// Once the pairing lands the same transport delivers, and the drain it refused
// to run now empties what it held.
func TestAPendingTransportDeliversWhatItHeldOncePaired(t *testing.T) {
	dir := t.TempDir()
	var endpoint string
	var posted int
	tr := NewPendingTransport(func() string { return endpoint }, func() string { return "tok" }, dir)
	tr.post = func(context.Context, []byte) (int, []byte, error) {
		posted++
		return 200, []byte(`{}`), nil
	}

	_ = tr.Deliver(context.Background(), []byte(`{"batch":1}`))
	_ = tr.Deliver(context.Background(), []byte(`{"batch":2}`))

	endpoint = "https://atlas.example/v1/signal/client-events"
	if err := tr.DrainSpool(context.Background()); err != nil {
		t.Fatalf("drain after pairing: %v", err)
	}
	if posted != 2 {
		t.Fatalf("delivered %d batches after pairing, want the 2 that were held", posted)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 0 {
		t.Fatalf("%d spool files remain after a successful drain", len(files))
	}
}
