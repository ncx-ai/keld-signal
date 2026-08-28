package localagent

import (
	"strings"
	"testing"
	"time"
)

func sessNow() time.Time { return time.Date(2026, 8, 27, 21, 0, 0, 0, time.UTC) }

// base is the reportable case: a session running for an hour, written to a
// minute ago, with nothing ever forwarded for it.
func silentSession() SessionTelemetryState {
	return SessionTelemetryState{
		Known:      true,
		Configured: true,
		Now:        sessNow(),
		Active: []SessionSighting{{
			ID:        "3bc7bb39-3edf-408d-ba94-160982ca9a6f",
			StartedAt: sessNow().Add(-1 * time.Hour),
			LastSeen:  sessNow().Add(-1 * time.Minute),
		}},
		// A populated record that simply does not mention the session above:
		// this is the real machine state — one tool started after `keld signal
		// setup`, one before.
		Forwarded: map[string]time.Time{
			"39b953bc-6e27-45f5-9b80-820a08c984a3": sessNow().Add(-10 * time.Second),
		},
	}
}

func TestReportsASilentActiveSession(t *testing.T) {
	got := silentSession().ProblemLine()
	if got == "" {
		t.Fatal("a session running an hour with no telemetry must be reported")
	}
	if !strings.Contains(got, "3bc7bb39") {
		t.Errorf("finding should name the session; got %q", got)
	}
	if !strings.Contains(got, "Restart") {
		t.Errorf("finding must state the remedy; got %q", got)
	}
}

func TestSilentIsNotReportedWhenTheCheckCannotConclude(t *testing.T) {
	cases := map[string]func(*SessionTelemetryState){
		"no proxy runs here":         func(s *SessionTelemetryState) { s.Known = false },
		"telemetry not configured":   func(s *SessionTelemetryState) { s.Configured = false },
		"no clock":                   func(s *SessionTelemetryState) { s.Now = time.Time{} },
		"session started in future":  func(s *SessionTelemetryState) { s.Active[0].StartedAt = sessNow().Add(time.Hour) },
		"written in the future":      func(s *SessionTelemetryState) { s.Active[0].LastSeen = sessNow().Add(time.Hour) },
		"session not running now":    func(s *SessionTelemetryState) { s.Active[0].LastSeen = sessNow().Add(-2 * time.Hour) },
		"session too young to flush": func(s *SessionTelemetryState) { s.Active[0].StartedAt = sessNow().Add(-1 * time.Minute) },
		"no start time known":        func(s *SessionTelemetryState) { s.Active[0].StartedAt = time.Time{} },
		"no id":                      func(s *SessionTelemetryState) { s.Active[0].ID = "" },
	}
	for name, mutate := range cases {
		s := silentSession()
		mutate(&s)
		if got := s.ProblemLine(); got != "" {
			t.Errorf("%s: expected no finding, got %q", name, got)
		}
	}
}

func TestASessionThatHasForwardedIsHealthy(t *testing.T) {
	s := silentSession()
	s.Forwarded[s.Active[0].ID] = sessNow().Add(-5 * time.Minute)
	if got := s.ProblemLine(); got != "" {
		t.Errorf("a session that has reached Atlas must not be reported; got %q", got)
	}
}

// ⚠️ THE WHOLE POINT: the healthy session must not vouch for the silent one.
func TestOneHealthySessionDoesNotMaskASilentOne(t *testing.T) {
	s := silentSession()
	s.Active = append(s.Active, SessionSighting{
		ID:        "39b953bc-6e27-45f5-9b80-820a08c984a3",
		StartedAt: sessNow().Add(-2 * time.Hour),
		LastSeen:  sessNow().Add(-30 * time.Second),
	})
	s.Forwarded["39b953bc-6e27-45f5-9b80-820a08c984a3"] = sessNow().Add(-10 * time.Second)
	got := s.ProblemLine()
	if !strings.Contains(got, "3bc7bb39") {
		t.Fatalf("the silent session must still be reported; got %q", got)
	}
	if strings.Contains(got, "39b953bc") {
		t.Errorf("the healthy session must not be named; got %q", got)
	}
	if !strings.Contains(got, "1 active") {
		t.Errorf("count should be 1; got %q", got)
	}
}

// ⚠️ An upgraded machine has a last_forward but no per-session record yet.
// Reporting then would call every running tool broken the day this shipped.
func TestNoSessionRecordYieldsNoFinding(t *testing.T) {
	s := silentSession()
	s.Forwarded = map[string]time.Time{}
	if got := s.ProblemLine(); got != "" {
		t.Errorf("empty record must be inconclusive, got %q", got)
	}
	s.Forwarded = nil
	if got := s.ProblemLine(); got != "" {
		t.Errorf("nil record must be inconclusive, got %q", got)
	}
}
