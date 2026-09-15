package integrations

import (
	"context"
	"testing"
	"time"
)

// ⚠️ `Reconcile` HAD NO CALLER, so `integration.broken` could never fire on any
// machine. Found by the phase-2 review on 2026-09-15: the transition rule, its
// four codes and ten tests were all green, and nothing ran it. Goal G3 — a break
// reaches us as an event with the tool version attached — did not hold.
//
// This is the same defect as the unmounted report route one file over, and the
// same defect the detector had by starting after the onboarding wait. Written,
// tested, never wired.
//
// The detector's poll is the natural home: it already walks the catalogue on a
// timer, and a state transition is exactly what a poll is for.
func TestTheDetectorLoopReconcilesAndEmits(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	var got []Emission
	d := &Detector{
		Entries: nil, // no catalogue walk; this test is about the reconcile half
		Poll:    10 * time.Millisecond,
		Sink:    sinkFunc(func(e Emission) { got = append(got, e) }),
		Snapshot: func() []Integration {
			return []Integration{{ID: "codex", State: Broken, BrokenLane: SurfaceHook, ToolVersion: "0.153.4"}}
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	d.Run(ctx)

	if len(got) == 0 {
		t.Fatal("the detector loop emitted nothing for a broken tool; Reconcile has no caller")
	}
	var broken *Emission
	for i := range got {
		if got[i].Code == CodeBroken {
			broken = &got[i]
		}
	}
	if broken == nil {
		t.Fatalf("no %s emission; got %+v", CodeBroken, got)
	}
	if broken.Fields["source"] != "codex" || broken.Fields["tool_version"] != "0.153.4" {
		t.Errorf("fields = %+v, want source=codex and the tool version", broken.Fields)
	}
}

// And it must not re-emit while nothing changes, or a broken tool floods the
// fleet at the poll rate. Reconcile already decides this; the loop must persist
// what it decided.
func TestASteadyBrokenToolEmitsOnceNotPerPoll(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	var n int
	d := &Detector{
		Poll: 5 * time.Millisecond,
		Sink: sinkFunc(func(e Emission) {
			if e.Code == CodeBroken {
				n++
			}
		}),
		Snapshot: func() []Integration {
			return []Integration{{ID: "codex", State: Broken, BrokenLane: SurfaceHook}}
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	d.Run(ctx)

	if n != 1 {
		t.Fatalf("integration.broken emitted %d times across ~40 polls, want exactly 1", n)
	}
}

type sinkFunc func(Emission)

func (f sinkFunc) Emit(code, severity string, fields map[string]any) {
	f(Emission{Code: code, Severity: severity, Fields: fields})
}
func (f sinkFunc) EmitExempt(code, severity string, fields map[string]any) {
	f(Emission{Code: code, Severity: severity, Fields: fields, Exempt: true})
}
