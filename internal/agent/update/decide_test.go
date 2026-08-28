package update

import (
	"testing"
	"time"
)

var now = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

func on(version string) Target {
	return Target{Version: version, Enabled: true}
}

func TestDecideActsOnANewPin(t *testing.T) {
	d := Decide("v0.4.1", on("v0.4.2"), State{}, now, false, time.Hour)
	if !d.Act || d.Version != "v0.4.2" || d.Reason != ReasonOK {
		t.Fatalf("got %+v", d)
	}
}

// A dev build is never auto-updated: it is not a release, so there is no
// version relationship to reason about and replacing it would destroy whatever
// the developer built.
func TestDecideRefusesDevBuild(t *testing.T) {
	d := Decide("dev", on("v0.4.2"), State{}, now, false, time.Hour)
	if d.Act || d.Reason != ReasonDevBuild {
		t.Fatalf("got %+v", d)
	}
}

func TestDecideRefusesWhenDisabledRemotely(t *testing.T) {
	d := Decide("v0.4.1", Target{Version: "v0.4.2", Enabled: false}, State{}, now, false, time.Hour)
	if d.Act || d.Reason != ReasonDisabled {
		t.Fatalf("got %+v", d)
	}
}

// Local refusal wins over remote permission. The reverse is never true: a
// local setting cannot enable updates the org has not enabled.
func TestDecideLocalKillSwitchBeatsRemoteEnable(t *testing.T) {
	d := Decide("v0.4.1", on("v0.4.2"), State{}, now, true, time.Hour)
	if d.Act || d.Reason != ReasonDisabledLocal {
		t.Fatalf("got %+v", d)
	}
}

func TestDecideRefusesWithNoPin(t *testing.T) {
	d := Decide("v0.4.1", Target{Enabled: true}, State{}, now, false, time.Hour)
	if d.Act || d.Reason != ReasonNoTarget {
		t.Fatalf("got %+v", d)
	}
}

func TestDecideUpToDate(t *testing.T) {
	d := Decide("v0.4.2", on("v0.4.2"), State{}, now, false, time.Hour)
	if d.Act || d.Reason != ReasonUpToDate {
		t.Fatalf("got %+v", d)
	}
}

// "0.4.2" and "v0.4.2" are the same version. Comparison is identity against
// the pin after normalizing one leading v — not semver ordering, which is what
// a floor would need and which has parsing edge cases a pin does not.
func TestDecideNormalizesTheLeadingV(t *testing.T) {
	d := Decide("0.4.2", on("v0.4.2"), State{}, now, false, time.Hour)
	if d.Act || d.Reason != ReasonUpToDate {
		t.Fatalf("got %+v", d)
	}
}

// A PIN IS NOT A FLOOR. Moving backwards is the rollback lever that is the
// entire reason the version source is Atlas rather than releases/latest, so a
// lower pin must act rather than being refused as a downgrade.
func TestDecideActsOnADowngradePin(t *testing.T) {
	d := Decide("v0.5.0", on("v0.4.2"), State{}, now, false, time.Hour)
	if !d.Act || d.Version != "v0.4.2" {
		t.Fatalf("a downgrade pin is the rollback lever; got %+v", d)
	}
}

// Atlas still pins the bad version after a rollback, so without this the next
// poll re-applies it forever: swap, crash, roll back, swap.
func TestDecideRefusesAVersionThatAlreadyRolledBack(t *testing.T) {
	s := State{FailedVersions: []string{"v0.4.2"}}
	d := Decide("v0.4.1", on("v0.4.2"), s, now, false, time.Hour)
	if d.Act || d.Reason != ReasonFailedPreviously {
		t.Fatalf("got %+v", d)
	}
}

// ...but the moment the pin MOVES, the machine is free again. A failed version
// must not poison a different one.
func TestDecideActsWhenThePinMovesPastAFailure(t *testing.T) {
	s := State{FailedVersions: []string{"v0.4.2"}}
	d := Decide("v0.4.1", on("v0.4.3"), s, now, false, time.Hour)
	if !d.Act {
		t.Fatalf("got %+v", d)
	}
}

func TestDecideHonoursTheMinInterval(t *testing.T) {
	s := State{LastAttempt: now.Add(-30 * time.Minute)}
	d := Decide("v0.4.1", on("v0.4.2"), s, now, false, time.Hour)
	if d.Act || d.Reason != ReasonTooSoon {
		t.Fatalf("got %+v", d)
	}
	later := Decide("v0.4.1", on("v0.4.2"), s, now.Add(31*time.Minute), false, time.Hour)
	if !later.Act {
		t.Fatalf("floor should have expired: %+v", later)
	}
}

// An update already staged and awaiting its restart must not be re-attempted
// by a settings poll that lands in the gap.
func TestDecideRefusesWhileAnUpdateIsPending(t *testing.T) {
	s := State{PendingConfirm: true, To: "v0.4.2", AttemptedAt: now}
	d := Decide("v0.4.1", on("v0.4.3"), s, now, false, time.Hour)
	if d.Act || d.Reason != ReasonPending {
		t.Fatalf("got %+v", d)
	}
}

// Refusal order matters: a dev build must report dev_build even when every
// other refusal would also apply, so an operator reading the reason is told
// the durable fact rather than an incidental one.
func TestDecideReportsTheMostDurableRefusalFirst(t *testing.T) {
	s := State{LastAttempt: now, FailedVersions: []string{"v0.4.2"}}
	d := Decide("dev", on("v0.4.2"), s, now, true, time.Hour)
	if d.Reason != ReasonDevBuild {
		t.Fatalf("got %+v", d)
	}
}

func TestDecideEmptyCurrentVersionIsTreatedAsDev(t *testing.T) {
	d := Decide("", on("v0.4.2"), State{}, now, false, time.Hour)
	if d.Act || d.Reason != ReasonDevBuild {
		t.Fatalf("got %+v", d)
	}
}
