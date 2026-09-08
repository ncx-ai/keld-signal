package attrib

import (
	"context"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
)

// THE OUTCOME HOOK — the seam that lets a recorder hear what this pass
// CONCLUDED, not only that it gave up.
//
// It exists because until it did, the vectorised pass reached the delivery
// ledger on exactly one path: the quarantine hook. A recorder that hears only
// about failures cannot represent agreement, so the only mark this pass could
// ever leave was a bad one — and it left that mark on the deterministic
// pass's own cell. Both halves of that are now fixed; this file covers the
// half that reports success.

func collectOutcomes(a *Attributor) *[]Outcome {
	var got []Outcome
	a.WithOutcomeHook(func(o Outcome) { got = append(got, o) })
	return &got
}

// A named project reaches the hook with its id and its confidence, and with
// the job's own coordinates — nothing else. The hook is what a ledger writes
// the `ok` vector cell from.
func TestOutcomeHookReportsTheNamedProjectAndItsConfidence(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Put(Job{SessionID: "s-ok", Path: "/tmp/x.jsonl", Start: 42, End: 102}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	a := New(st, successClient("proj_pay"), &fakeSender{}, nil, "actor@x", digesterFor("s-ok", 42, 102))
	got := collectOutcomes(a)

	a.drainOnce(context.Background())

	if len(*got) != 1 {
		t.Fatalf("outcome hook fired %d times, want 1", len(*got))
	}
	o := (*got)[0]
	if o.SessionID != "s-ok" || o.Start != 42 {
		t.Fatalf("outcome carried %+v, want the job's own coordinates", o)
	}
	if o.Status != enrich.ProjectsAttributed || o.ProjectID != "proj_pay" || o.Confidence != 0.9 {
		t.Fatalf("outcome = %+v, want attributed/proj_pay/0.9", o)
	}
}

// A held answer is reported too, and reported as ITSELF. `pending` and
// `degraded:weights_unavailable` are the states a machine sits in for the
// whole of a multi-gigabyte download, and a recorder that could not tell them
// from a failure would show the encoder as broken for hours while it was
// merely downloading.
func TestOutcomeHookReportsHeldAnswersAsThemselves(t *testing.T) {
	for _, status := range []string{enrich.ProjectsPending, enrich.ProjectsDegradedWeights} {
		t.Run(status, func(t *testing.T) {
			st := NewStore(t.TempDir())
			if err := st.Put(Job{SessionID: "s-held", Path: "/tmp/x.jsonl", Start: 42, End: 102}); err != nil {
				t.Fatalf("Put: %v", err)
			}
			cl := &fakeClient{ok: true, res: sidecar.AttributeResult{Status: status}}
			a := New(st, cl, &fakeSender{}, nil, "actor@x", digesterFor("s-held", 42, 102))
			got := collectOutcomes(a)

			a.drainOnce(context.Background())

			if len(*got) != 1 || (*got)[0].Status != status {
				t.Fatalf("outcomes = %+v, want one %q", *got, status)
			}
			if (*got)[0].ProjectID != "" {
				t.Fatalf("a held answer named no project; got %q", (*got)[0].ProjectID)
			}
		})
	}
}

// A held answer keeps being reported on every sweep, not only the first.
//
// DegradedPublished suppresses a duplicate row on the WIRE, where an identical
// resend costs a request. A local recorder's write is idempotent, and falling
// silent for the rest of a download would make the record go stale rather than
// quiet — "last heard from an hour ago" and "still waiting" look the same to a
// reader only if nobody keeps saying it.
func TestOutcomeHookKeepsReportingAHeldAnswerOnEverySweep(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Put(Job{SessionID: "s-degraded", Path: "/tmp/x.jsonl", Start: 42, End: 102}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	cl := &fakeClient{ok: true, res: sidecar.AttributeResult{Status: enrich.ProjectsDegradedWeights}}
	a := New(st, cl, &fakeSender{}, nil, "actor@x", digesterFor("s-degraded", 42, 102))
	got := collectOutcomes(a)

	ctx := context.Background()
	for range 3 {
		a.drainOnce(ctx)
	}
	if len(*got) != 3 {
		t.Fatalf("outcome hook fired %d times over 3 sweeps, want 3 — a stale record reads as a stalled one", len(*got))
	}
}

// NEGATIVE: a genuine error is NOT an outcome. There is no answer to report —
// the sidecar never gave one — and reporting a made-up status here would put a
// state in the ledger that no pass concluded. That path is the quarantine
// hook's, and only after MaxAttempts.
func TestOutcomeHookIsSilentWhenTheSidecarNeverAnswered(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Put(Job{SessionID: "s-err", Path: "/tmp/x.jsonl", Start: 42, End: 102}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	a := New(st, errorClient(), &fakeSender{}, nil, "actor@x", digesterFor("s-err", 42, 102))
	got := collectOutcomes(a)
	var quarantines int
	a.WithQuarantineHook(func(string, float64) { quarantines++ })

	ctx := context.Background()
	for range MaxAttempts {
		a.drainOnce(ctx)
	}
	if len(*got) != 0 {
		t.Fatalf("a transport failure is not an answer; outcome hook fired %d times with %+v", len(*got), *got)
	}
	if quarantines != 1 {
		t.Fatalf("the failure must still be reported, once, through the quarantine seam; got %d", quarantines)
	}
}

// NEGATIVE: an outcome is never reported for an answer that failed to publish.
// A recorder learning of an attribution the wire never carried would report a
// second opinion Atlas has no copy of.
func TestOutcomeHookIsSilentWhenThePublishFailed(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Put(Job{SessionID: "s-pubfail", Path: "/tmp/x.jsonl", Start: 42, End: 102}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sender := &fakeSender{failN: 1}
	a := New(st, successClient("proj_pay"), sender, nil, "actor@x", digesterFor("s-pubfail", 42, 102))
	got := collectOutcomes(a)

	a.drainOnce(context.Background())
	if len(*got) != 0 {
		t.Fatalf("the publish failed, so there is nothing to report; got %+v", *got)
	}

	// The next sweep publishes, and only then is the outcome reported.
	a.drainOnce(context.Background())
	if len(*got) != 1 || (*got)[0].ProjectID != "proj_pay" {
		t.Fatalf("after a successful publish the outcome must be reported; got %+v", *got)
	}
}

// An Attributor with no outcome hook is exactly what every existing caller is,
// and it must be unaffected — no panic, no behaviour change.
func TestAnAttributorWithNoOutcomeHookIsUnaffected(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Put(Job{SessionID: "s-nohook", Path: "/tmp/x.jsonl", Start: 42, End: 102}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	a := New(st, successClient("proj_pay"), &fakeSender{}, nil, "actor@x", digesterFor("s-nohook", 42, 102))
	a.drainOnce(context.Background())
	if jobs, _ := st.List(); len(jobs) != 0 {
		t.Fatalf("the job should have been published and deleted; %d left", len(jobs))
	}
}

// A panicking observer must not take down the sweep. This hook is wired to a
// ledger write, and the rule one package over (chainOnPublished) applies
// unchanged: a recorder must never be able to break the path that gets work to
// Atlas in order to record that the work happened.
func TestAPanickingOutcomeHookDoesNotBreakTheSweep(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Put(Job{SessionID: "s-panic", Path: "/tmp/x.jsonl", Start: 42, End: 102}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	a := New(st, successClient("proj_pay"), &fakeSender{}, nil, "actor@x", digesterFor("s-panic", 42, 102))
	a.WithOutcomeHook(func(Outcome) { panic("a ledger that cannot be written must not stop delivery") })

	a.drainOnce(context.Background())

	if jobs, _ := st.List(); len(jobs) != 0 {
		t.Fatalf("the sweep must have completed and deleted the published job; %d left", len(jobs))
	}
}
