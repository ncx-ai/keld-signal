package clientevents

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func testBaseCorr() Corr {
	return Corr{
		InstallID: "install-abc",
		RunID:     "run-1",
		Version:   "1.2.3",
		OS:        "linux",
		Arch:      "amd64",
	}
}

func openGate() Gate {
	return Gate{Enabled: true, MinSeverity: SevInfo, SampleRate: 1.0}
}

func TestEmitStampsBaseCorrAndClock(t *testing.T) {
	base := testBaseCorr()
	e := NewEmitter(base, 16)
	e.SetGate(openGate())

	fixed := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	e.clock = func() time.Time { return fixed }

	e.Emit("job.start", SevInfo, map[string]any{"reason": "ok"})

	batch := e.Drain()
	if len(batch) != 1 {
		t.Fatalf("expected 1 event, got %d", len(batch))
	}
	got := batch[0]
	if got.Corr.InstallID != base.InstallID || got.Corr.Version != base.Version ||
		got.Corr.OS != base.OS || got.Corr.Arch != base.Arch || got.Corr.RunID != base.RunID {
		t.Fatalf("expected stamped base Corr %+v, got %+v", base, got.Corr)
	}
	if !got.TS.Equal(fixed) {
		t.Fatalf("expected TS %v, got %v", fixed, got.TS)
	}
	if got.Code != "job.start" || got.Severity != SevInfo {
		t.Fatalf("unexpected code/severity: %+v", got)
	}
}

func TestEmitBelowMinSeverityDropped(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	e.SetGate(Gate{Enabled: true, MinSeverity: SevWarn, SampleRate: 1.0})
	e.clock = func() time.Time { return time.Now() }

	e.Emit("noisy.info", SevInfo, nil)
	if batch := e.Drain(); len(batch) != 0 {
		t.Fatalf("expected info below warn floor to be dropped, got %d events", len(batch))
	}

	e.Emit("important.error", SevError, nil)
	batch := e.Drain()
	if len(batch) != 1 {
		t.Fatalf("expected error at-or-above floor to be kept, got %d events", len(batch))
	}
}

func TestEmitAtMinSeverityKept(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	e.SetGate(Gate{Enabled: true, MinSeverity: SevWarn, SampleRate: 1.0})

	e.Emit("at.floor", SevWarn, nil)
	if batch := e.Drain(); len(batch) != 1 {
		t.Fatalf("expected warn at floor to be kept, got %d events", len(batch))
	}
}

func TestEmitDisabledGateDrops(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	e.SetGate(Gate{Enabled: false, MinSeverity: SevInfo, SampleRate: 1.0})

	e.Emit("whatever", SevCritical, nil)
	if batch := e.Drain(); len(batch) != 0 {
		t.Fatalf("expected drop when Enabled=false, got %d events", len(batch))
	}
}

func TestEmitZeroGateBeforeSetGateDrops(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	// No SetGate call at all — zero-value Gate{} must be disabled.
	e.Emit("whatever", SevCritical, nil)
	if batch := e.Drain(); len(batch) != 0 {
		t.Fatalf("expected drop before any SetGate, got %d events", len(batch))
	}
}

func TestEmitSampleRateZeroDrops(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	e.SetGate(Gate{Enabled: true, MinSeverity: SevInfo, SampleRate: 0.0})
	e.randFloat = func() float64 { return 0.0 } // even the smallest rand value must not pass 0.0 < 0.0

	e.Emit("sampled.out", SevInfo, nil)
	if batch := e.Drain(); len(batch) != 0 {
		t.Fatalf("expected SampleRate=0 to always drop, got %d events", len(batch))
	}
}

func TestEmitSampleRateOneKeeps(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	e.SetGate(Gate{Enabled: true, MinSeverity: SevInfo, SampleRate: 1.0})
	e.randFloat = func() float64 { return 0.999999 } // even near-1 rand must pass < 1.0

	e.Emit("sampled.in", SevInfo, nil)
	if batch := e.Drain(); len(batch) != 1 {
		t.Fatalf("expected SampleRate=1.0 to always keep, got %d events", len(batch))
	}
}

func TestRingCapsAtCapacityDropOldest(t *testing.T) {
	const capacity = 8
	e := NewEmitter(testBaseCorr(), capacity)
	e.SetGate(openGate())

	// Vary the code on every emit so coalescing never collapses these.
	for i := 0; i < capacity+5; i++ {
		e.Emit(codeForIndex(i), SevInfo, nil)
	}

	batch := e.Drain()
	if len(batch) != capacity {
		t.Fatalf("expected ring capped at %d, got %d", capacity, len(batch))
	}
	// The oldest 5 codes (0..4) must have been dropped; the ring should hold
	// the most recent `capacity` codes (5..capacity+4).
	wantFirst := codeForIndex(5)
	if batch[0].Code != wantFirst {
		t.Fatalf("expected oldest surviving code %q, got %q", wantFirst, batch[0].Code)
	}
	wantLast := codeForIndex(capacity + 4)
	if batch[len(batch)-1].Code != wantLast {
		t.Fatalf("expected newest code %q, got %q", wantLast, batch[len(batch)-1].Code)
	}
}

func codeForIndex(i int) string {
	return "code." + strconv.Itoa(i)
}

func TestEmitRunsRedaction(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	e.SetGate(openGate())

	e.Emit("path.event", SevInfo, map[string]any{"path": "/home/dg/keld/secret.json"})

	batch := e.Drain()
	if len(batch) != 1 {
		t.Fatalf("expected 1 event, got %d", len(batch))
	}
	got, _ := batch[0].Fields["path"].(string)
	if strings.Contains(got, "/home/dg/keld/secret.json") {
		t.Fatalf("expected redacted path, got verbatim: %q", got)
	}
}

func TestEmitDoesNotMutateCallerFields(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	e.SetGate(openGate())

	in := map[string]any{"path": "/home/dg/keld/secret.json"}
	e.Emit("path.event", SevInfo, in)

	if in["path"] != "/home/dg/keld/secret.json" {
		t.Fatalf("caller's fields map was mutated: %v", in["path"])
	}
}

func TestEmitCoalescesSameCodeAndSeverity(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	e.SetGate(openGate())

	e.Emit("flood.code", SevWarn, nil)
	e.Emit("flood.code", SevWarn, nil)

	batch := e.Drain()
	if len(batch) != 1 {
		t.Fatalf("expected coalesced single event, got %d", len(batch))
	}
	count, ok := batch[0].Fields["count"]
	if !ok {
		t.Fatalf("expected Fields[count] to be set on coalesced event, fields=%v", batch[0].Fields)
	}
	if count != 2 {
		t.Fatalf("expected count==2 after first coalesce, got %v (%T)", count, count)
	}
}

func TestEmitCoalesceThriceIncrementsCount(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	e.SetGate(openGate())

	e.Emit("flood.code", SevWarn, nil)
	e.Emit("flood.code", SevWarn, nil)
	e.Emit("flood.code", SevWarn, nil)

	batch := e.Drain()
	if len(batch) != 1 {
		t.Fatalf("expected 1 coalesced event, got %d", len(batch))
	}
	if batch[0].Fields["count"] != 3 {
		t.Fatalf("expected count==3, got %v", batch[0].Fields["count"])
	}
}

func TestEmitDifferentSeverityDoesNotCoalesce(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	e.SetGate(openGate())

	e.Emit("same.code", SevWarn, nil)
	e.Emit("same.code", SevError, nil)

	batch := e.Drain()
	if len(batch) != 2 {
		t.Fatalf("expected 2 distinct events (different severity), got %d", len(batch))
	}
}

func TestEmitDifferentCodeDoesNotCoalesce(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	e.SetGate(openGate())

	e.Emit("code.a", SevWarn, nil)
	e.Emit("code.b", SevWarn, nil)

	batch := e.Drain()
	if len(batch) != 2 {
		t.Fatalf("expected 2 distinct events (different code), got %d", len(batch))
	}
}

func TestWithJobStampsSessionAndPrompt(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	e.SetGate(openGate())

	je := e.WithJob("sess-1", "prompt-1")
	je.Emit("job.event", SevInfo, nil)

	batch := e.Drain()
	if len(batch) != 1 {
		t.Fatalf("expected 1 event, got %d", len(batch))
	}
	if batch[0].Corr.SessionID != "sess-1" || batch[0].Corr.PromptID != "prompt-1" {
		t.Fatalf("expected stamped session/prompt, got %+v", batch[0].Corr)
	}
	// Base corr fields must still be present.
	if batch[0].Corr.InstallID != "install-abc" {
		t.Fatalf("expected base InstallID preserved, got %+v", batch[0].Corr)
	}
}

func TestEmitGaugeBypassesFloorButHonorsEnabled(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	e.SetGate(Gate{Enabled: true, MinSeverity: SevWarn, SampleRate: 1.0})

	e.EmitGauge("gauge.snapshot", map[string]any{"rss_mb": 42})
	batch := e.Drain()
	if len(batch) != 1 {
		t.Fatalf("expected gauge to bypass warn floor, got %d events", len(batch))
	}
	if batch[0].Severity != SevInfo {
		t.Fatalf("expected gauge severity SevInfo, got %q", batch[0].Severity)
	}

	e.SetGate(Gate{Enabled: false, MinSeverity: SevWarn, SampleRate: 1.0})
	e.EmitGauge("gauge.snapshot2", nil)
	if batch := e.Drain(); len(batch) != 0 {
		t.Fatalf("expected gauge dropped when Enabled=false, got %d events", len(batch))
	}
}

func TestEmitGaugeHonorsSampleRate(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	e.SetGate(Gate{Enabled: true, MinSeverity: SevWarn, SampleRate: 0.0})
	e.randFloat = func() float64 { return 0.0 }

	e.EmitGauge("gauge.sampled", nil)
	if batch := e.Drain(); len(batch) != 0 {
		t.Fatalf("expected gauge dropped when sampled out, got %d events", len(batch))
	}
}

func TestDrainEmptyRingReturnsEmpty(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	e.SetGate(openGate())

	batch := e.Drain()
	if len(batch) != 0 {
		t.Fatalf("expected empty batch, got %d", len(batch))
	}
}

func TestDrainClearsRing(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 16)
	e.SetGate(openGate())

	e.Emit("one", SevInfo, nil)
	first := e.Drain()
	if len(first) != 1 {
		t.Fatalf("expected 1 event in first drain, got %d", len(first))
	}
	second := e.Drain()
	if len(second) != 0 {
		t.Fatalf("expected ring cleared after drain, got %d events", len(second))
	}
}

func TestNewEmitterNonPositiveCapacityDefaults(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 0)
	e.SetGate(openGate())

	// Should not panic and should accept at least a reasonable number of
	// distinct-code events without dropping (sane default, e.g. 256).
	for i := 0; i < 200; i++ {
		e.Emit(codeForIndex(i), SevInfo, nil)
	}
	batch := e.Drain()
	if len(batch) != 200 {
		t.Fatalf("expected default capacity to hold at least 200 events, got %d", len(batch))
	}
}

func TestEmitNonBlockingUnderFlood(t *testing.T) {
	const capacity = 32
	e := NewEmitter(testBaseCorr(), capacity)
	e.SetGate(openGate())

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100000; i++ {
			e.Emit(codeForIndex(i%50), SevInfo, nil)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Emit flood did not complete promptly — possible blocking")
	}

	batch := e.Drain()
	if len(batch) > capacity {
		t.Fatalf("expected buffer bounded at capacity %d, got %d", capacity, len(batch))
	}
}

func TestEmitConcurrentWithSetGateAndDrainRace(t *testing.T) {
	e := NewEmitter(testBaseCorr(), 64)
	e.SetGate(openGate())

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Emitters
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			i := 0
			for {
				select {
				case <-stop:
					return
				default:
					e.Emit(codeForIndex((w*1000+i)%20), SevInfo, map[string]any{"n": i})
					i++
				}
			}
		}(w)
	}

	// Gate flipper
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			e.SetGate(Gate{Enabled: i%2 == 0, MinSeverity: SevInfo, SampleRate: 1.0})
		}
	}()

	// Drainer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = e.Drain()
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
