package localagent

import (
	"testing"
	"time"
)

var base = time.Date(2026, 8, 27, 13, 0, 0, 0, time.UTC)

// The signature of the real bug: configured, the credential was rewritten, and
// nothing has arrived since.
func TestReportsWhenConfiguredAndSilentSinceTheRewrite(t *testing.T) {
	s := TelemetryState{
		Known: true, Configured: true,
		HookWritten: base.Add(-30 * time.Minute),
		LastForward: base.Add(-90 * time.Minute),
		Now:         base,
	}
	if s.ProblemLine() == "" {
		t.Fatal("silent-since-before-the-rewrite produced no finding")
	}
}

// Telemetry arriving AFTER the rewrite is the healthy case — the tools restarted.
func TestNoFindingOnceTelemetryArrivesAfterTheRewrite(t *testing.T) {
	s := TelemetryState{
		Known: true, Configured: true,
		HookWritten: base.Add(-30 * time.Minute),
		LastForward: base.Add(-2 * time.Minute),
		Now:         base,
	}
	if p := s.ProblemLine(); p != "" {
		t.Fatalf("healthy machine reported: %q", p)
	}
}

// ⚠️ A laptop that wakes with a skewed clock must not make doctor LIE. Unordered
// timestamps support no conclusion, and "cannot tell" is the honest answer.
func TestClockSkewProducesNoFinding(t *testing.T) {
	future := TelemetryState{
		Known: true, Configured: true,
		HookWritten: base.Add(2 * time.Hour),
		LastForward: base.Add(-5 * time.Minute),
		Now:         base,
	}
	if p := future.ProblemLine(); p != "" {
		t.Fatalf("a future credential timestamp produced a confident finding: %q", p)
	}
	forward := TelemetryState{
		Known: true, Configured: true,
		HookWritten: base.Add(-2 * time.Hour),
		LastForward: base.Add(3 * time.Hour),
		Now:         base,
	}
	if p := forward.ProblemLine(); p != "" {
		t.Fatalf("a future forward timestamp produced a confident finding: %q", p)
	}
}

// A freshly set-up machine is silent and fine.
func TestFreshInstallProducesNoFinding(t *testing.T) {
	s := TelemetryState{
		Known: true, Configured: true,
		HookWritten: base.Add(-2 * time.Minute),
		Now:         base,
	}
	if p := s.ProblemLine(); p != "" {
		t.Fatalf("a two-minute-old install was reported as broken: %q", p)
	}
}

// Never-forwarded but long past settling IS the finding — this is what a machine
// looks like when its tools were never restarted after setup.
func TestNeverForwardedAfterSettlingReports(t *testing.T) {
	s := TelemetryState{
		Known: true, Configured: true,
		HookWritten: base.Add(-2 * time.Hour),
		Now:         base,
	}
	if s.ProblemLine() == "" {
		t.Fatal("a machine that has never forwarded produced no finding")
	}
}

// ⚠️ Unknown must NEVER produce a finding: a direct-push install, or one whose
// proxy failed to bind, genuinely cannot be judged from here.
func TestUnknownOrUnconfiguredProduceNoFinding(t *testing.T) {
	for name, s := range map[string]TelemetryState{
		"unknown":      {Known: false, Configured: true, HookWritten: base.Add(-time.Hour), Now: base},
		"unconfigured": {Known: true, Configured: false, HookWritten: base.Add(-time.Hour), Now: base},
		"no clock":     {Known: true, Configured: true, HookWritten: base.Add(-time.Hour)},
		"no hook time": {Known: true, Configured: true, Now: base},
	} {
		if p := s.ProblemLine(); p != "" {
			t.Errorf("%s produced a finding: %q", name, p)
		}
	}
}
