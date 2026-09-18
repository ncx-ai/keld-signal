package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
)

// ⚠️ **"COLLECTING, NOT PAIRED" MUST BE STATED AND MUST NOT READ AS A FAULT.**
// The daemon now collects from its first second and waits for the pairing only
// in order to send, so on a machine between install and login the `atlas` row is
// being asked about an Atlas nobody has named yet. Two wrong answers are
// available: `failed`, which accuses a healthy machine, and nothing at all,
// which renders as unknown — "we could not tell" about the one fact the machine
// knows perfectly well. It is n/a with its reason, the same call the `sidecar`
// row makes for a machine with no sidecar installed.
func TestTheAtlasRowSaysNotPairedRatherThanFailedOrNothing(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	setSidecarProbe(nil)

	v := &v3{ledger: ledger.New(), atlasOn: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startHealth(ctx, v, nil, true, func() bool { return false })

	snap, _ := v.ledger.Read(time.Time{}, 10)
	got := healthByKey(snap)["atlas"]
	if got.Status != string(ledger.StatusNA) || got.Detail != string(ledger.ReasonNotPaired) {
		t.Fatalf("atlas row = %#v, want n/a with reason %q", got, ledger.ReasonNotPaired)
	}
}

// And a paired machine that has not yet made a call is still UNKNOWN rather
// than not_paired: "reachable" and "never tried" are different facts, and this
// row already refuses to invent the first. The new branch must not swallow that.
func TestAPairedButSilentAtlasRowStaysUnknown(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	setSidecarProbe(nil)

	v := &v3{ledger: ledger.New(), atlasOn: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startHealth(ctx, v, nil, true, func() bool { return true })

	snap, _ := v.ledger.Read(time.Time{}, 10)
	if got, ok := healthByKey(snap)["atlas"]; ok {
		t.Fatalf("atlas row = %#v on a paired machine that has made no call; it must say nothing", got)
	}
}
