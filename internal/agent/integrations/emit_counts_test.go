package integrations

import "testing"
import "time"

func brokenFixture() []Integration {
	return []Integration{{ID: "codex", State: Broken, BrokenLane: SurfaceHook}}
}

// TestLaneCountsAbsentMeansAbsentNotZero.
//
// ⚠️ THE DAEMON HAS NO PRODUCER FOR LaneCounts — detector.go passes nil — so
// every integration.broken this client has ever sent carried hook_n/watcher_n/
// otel_n/reader_n = 0. Four zeros read as a MEASUREMENT: "nothing arrived on
// any lane", which is the single most incriminating thing this event can say,
// and nobody counted anything.
//
// That is the confident-negative-from-a-check-nobody-performed failure this
// repo forbids by name (facets_degraded, thin/absent, "Absent means NOT
// RECORDED, never zero"). Until a producer exists the keys must be ABSENT.
func TestLaneCountsAbsentMeansAbsentNotZero(t *testing.T) {
	out, _ := Reconcile(time.Now(), brokenFixture(), nil, nil, time.Hour)
	if len(out) != 1 {
		t.Fatalf("expected one emission, got %d", len(out))
	}
	for _, k := range []string{"hook_n", "watcher_n", "otel_n", "reader_n"} {
		if v, ok := out[0].Fields[k]; ok {
			t.Errorf("%s published as %v with no counter behind it; absent is the honest answer", k, v)
		}
	}
	// The rest of the event is unaffected — this is about the counters only.
	if out[0].Fields["source"] != "codex" {
		t.Errorf("source lost: %v", out[0].Fields["source"])
	}
	if _, ok := out[0].Fields["window_h"]; !ok {
		t.Error("window_h should still be present")
	}
}

// TestLaneCountsPublishWhenMeasured: a real zero is still a real zero. When a
// producer supplies counts, 0 means "counted, none" and must publish.
func TestLaneCountsPublishWhenMeasured(t *testing.T) {
	counts := map[string]LaneCounts{"codex": {Hook: 0, Watcher: 3, OTel: 41, Reader: 0}}
	out, _ := Reconcile(time.Now(), brokenFixture(), nil, counts, time.Hour)
	if len(out) != 1 {
		t.Fatalf("expected one emission, got %d", len(out))
	}
	for k, want := range map[string]int{"hook_n": 0, "watcher_n": 3, "otel_n": 41, "reader_n": 0} {
		got, ok := out[0].Fields[k]
		if !ok {
			t.Errorf("%s missing when it WAS measured", k)
			continue
		}
		if got != want {
			t.Errorf("%s = %v, want %d", k, got, want)
		}
	}
}
