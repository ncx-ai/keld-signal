package gate

import (
	"os"
	"testing"
)

// TestSweepHasNotRegressed is the gate. Point it at a pair of sweep logs and it compares both
// arms against the committed baseline, failing on any regression.
//
//	GATE_ON_LOG=/path/on.log GATE_OFF_LOG=/path/off.log \
//	  go test ./internal/agent/enrich/llmstudy/gate/ -run HasNotRegressed -v
//
// It SKIPS with no logs supplied, so `go test ./...` stays green on a tree nobody has swept —
// this is a research harness and the gate cannot block CI on a measurement that takes half an
// hour of GPU-less inference to produce. What it must do instead is make a regression
// impossible to miss for the person who did run the sweep, which is why it fails rather than
// logs, and why the failure message leads with the regressed rows.
//
// Both arms are compared when both are supplied and each one that is supplied is compared on
// its own. Supplying only one arm is legal (a fast check) and is reported as such: the
// published "the anchor costs 6 points of retention" figure came from a comparison where one
// arm silently differed in a second way, so this never pretends to have judged an arm it was
// not given.
func TestSweepHasNotRegressed(t *testing.T) {
	onPath, offPath := os.Getenv("GATE_ON_LOG"), os.Getenv("GATE_OFF_LOG")
	if onPath == "" && offPath == "" {
		t.Skip("set GATE_ON_LOG and/or GATE_OFF_LOG to a sweep log to run the gate")
	}
	base, err := LoadBaseline()
	if err != nil {
		t.Fatal(err)
	}
	var on, off *Sweep
	if onPath != "" {
		if on, err = ParseFile(onPath); err != nil {
			t.Fatal(err)
		}
		if on.Arm != "on" {
			t.Fatalf("GATE_ON_LOG is an %q-arm log, not the ON arm", on.Arm)
		}
	}
	if offPath != "" {
		if off, err = ParseFile(offPath); err != nil {
			t.Fatal(err)
		}
		if off.Arm != "off" {
			t.Fatalf("GATE_OFF_LOG is an %q-arm log, not the OFF arm", off.Arm)
		}
	}
	res := CompareBoth(base, on, off)
	if len(res.Rows) == 0 {
		t.Fatal("nothing compared: the baseline is missing the arm(s) supplied")
	}
	t.Logf("baseline: %s (%s)\ncorpus: %s\n%s", base.Comment, base.Commit, base.Corpus, res.Render())
	if regs := res.Regressions(); len(regs) > 0 {
		t.Errorf("%d threshold regression(s) against the committed baseline — revert the change "+
			"or justify each one with the flagged items as evidence", len(regs))
	}
}

// TestWriteBaselineFromLogs records a new baseline from a pair of sweep logs.
//
//	GATE_ON_LOG=on.log GATE_OFF_LOG=off.log GATE_WRITE_BASELINE=baseline.json \
//	  GATE_BASELINE_COMMIT=$(git rev-parse --short HEAD) GATE_BASELINE_COMMENT=... \
//	  GATE_BASELINE_CORPUS=... go test ./...gate/ -run WriteBaseline -v
//
// A SEPARATE test from the gate on purpose. Re-baselining has to be an act somebody performs
// deliberately and commits as a reviewable diff — not a flag on the comparison, where the
// person looking at a regression is one keystroke from making it disappear. That is the same
// reasoning the study applies to every other threshold: the number you are judged against
// must not be movable by the thing being judged.
func TestWriteBaselineFromLogs(t *testing.T) {
	out := os.Getenv("GATE_WRITE_BASELINE")
	if out == "" {
		t.Skip("set GATE_WRITE_BASELINE to record a new baseline")
	}
	nb := &Baseline{
		Comment: os.Getenv("GATE_BASELINE_COMMENT"),
		Commit:  os.Getenv("GATE_BASELINE_COMMIT"),
		Corpus:  os.Getenv("GATE_BASELINE_CORPUS"),
	}
	var err error
	if p := os.Getenv("GATE_ON_LOG"); p != "" {
		if nb.On, err = ParseFile(p); err != nil {
			t.Fatal(err)
		}
	}
	if p := os.Getenv("GATE_OFF_LOG"); p != "" {
		if nb.Off, err = ParseFile(p); err != nil {
			t.Fatal(err)
		}
	}
	if nb.On == nil || nb.Off == nil {
		t.Fatal("both arms are required: a one-armed baseline cannot judge an ablation")
	}
	// The parsed Label is the log's path, which is a scratch directory on whoever ran it.
	// Committed, that is noise in a diff and a dangling reference in six months; the arm plus
	// the Corpus field is what identifies the run.
	nb.On.Label, nb.Off.Label = "baseline ON arm", "baseline OFF arm"
	buf, err := nb.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s — review the diff before committing it", out)
}
