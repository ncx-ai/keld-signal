package daemon

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/promptlog"
)

// One event per REASON per run, not per record: a machine unpaired for an hour
// is one fact. The count rides the event, and the first drop is said at once.
func TestMirrorDropsAreReportedOncePerReasonWithTheCount(t *testing.T) {
	em := clientevents.NewEmitter(clientevents.Corr{InstallID: "test"}, 16)
	em.SetGate(clientevents.Gate{Enabled: true, MinSeverity: clientevents.SevInfo, SampleRate: 1})
	counts := map[string]int64{}
	report := mirrorDropReporter(em, func() map[string]int64 { return counts })

	for i := 0; i < 3; i++ {
		counts[promptlog.DropNotPaired]++
		report(promptlog.DropNotPaired)
	}
	counts[promptlog.DropUnreachable]++
	report(promptlog.DropUnreachable)
	got := em.Drain()

	if len(got) != 2 {
		t.Fatalf("emitted %d events for 4 drops across 2 reasons, want 2: %+v", len(got), got)
	}
	if got[0].Code != "telemetry.mirror_dropped" || got[0].Severity != clientevents.SevInfo || got[0].Fields["reason"] != promptlog.DropNotPaired {
		t.Fatalf("first event = %+v, want info telemetry.mirror_dropped reason=not_paired", got[0])
	}
	if got[0].Fields["count"] != int64(1) {
		t.Fatalf("the first drop is reported at once, with count 1; got %v", got[0].Fields["count"])
	}
	if got[1].Severity != clientevents.SevWarn || got[1].Fields["reason"] != promptlog.DropUnreachable {
		t.Fatalf("second event = %+v, want warn reason=unreachable — usage going missing on a paired machine", got[1])
	}
}
