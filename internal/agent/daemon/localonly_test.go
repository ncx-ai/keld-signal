package daemon

import (
	"context"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// ⚠️ **THIS IS THE TEST THAT WAS MISSING, and its absence let a machine that
// had asked for local-only dial Atlas once every two seconds.**
//
// The unit test beside it (TestAtlasOffNeverDials) proved the NEW connector
// cannot reach the network, and passed the whole time — because the enrichment
// worker, the tick and the settings poll never went through that connector.
// A boundary only binds what is routed through it. So this asserts the thing
// that actually matters: with Atlas off, the value those paths publish through
// is one that HAS no transport.
func TestLocalOnlySenderReplacesTheRealOneEverywhere(t *testing.T) {
	t.Setenv(settings.AtlasEnv, "")
	real := publish.New("http://atlas.example.invalid", func() string { return "t" }, "a")

	off := false
	got := senderFor(settings.Settings{SendToAtlas: &off}, real)
	if _, isLocal := got.(*localOnlySender); !isLocal {
		t.Fatalf("with Atlas off the worker must publish through the local-only sender, got %T", got)
	}
	if _, isWindow := got.(WindowSender); !isWindow {
		t.Fatal("the tick publishes windows through the same value; it must satisfy WindowSender")
	}

	on := senderFor(settings.Settings{}, real)
	if on != Sender(real) {
		t.Fatalf("with Atlas on the real publisher must be used, got %T", on)
	}
}

// Discarding must look like success to the worker. An error would make it treat
// the job as failed, re-spool it and eventually quarantine it — turning "you
// asked us not to send this" into a growing spool of poison rows and a stream
// of failure events on a machine where nothing is wrong.
func TestLocalOnlySenderReportsSuccessAndCounts(t *testing.T) {
	s := &localOnlySender{}
	for i := 0; i < 3; i++ {
		if err := s.Send(publish.Enrichment{}); err != nil {
			t.Fatalf("discard must read as success, got %v", err)
		}
	}
	if err := s.SendBlocks([]publish.BlockEnrichment{{}}); err != nil {
		t.Fatalf("blocks: %v", err)
	}
	if err := s.SendWindow(publish.WindowEnrichment{}); err != nil {
		t.Fatalf("window: %v", err)
	}
	if got := s.Dropped(); got != 3 {
		t.Fatalf("dropped count is what the page reports as held back, got %d want 3", got)
	}
}

// The settings poll is not merely one more request: it carries the auto-update
// pin, the org's feature toggles and its project vocabulary. A local-only
// machine that polled would still be reachable from the server for a binary
// swap, which is the opposite of what the toggle promises.
func TestSettingsPollIsSkippedEntirelyWhenAtlasIsOff(t *testing.T) {
	ran := make(chan struct{}, 1)
	pollSettingsIfOnline(context.Background(), false, func(context.Context) { ran <- struct{}{} })
	select {
	case <-ran:
		t.Fatal("the settings poll must not run with Send to Atlas off")
	default:
	}

	pollSettingsIfOnline(context.Background(), true, func(context.Context) { ran <- struct{}{} })
	<-ran // must run when Atlas is on
}
