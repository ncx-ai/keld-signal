package integrations

import (
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) *time.Time { t := now.Add(-d); return &t }
func yes() *bool                     { b := true; return &b }
func no() *bool                      { b := false; return &b }

// entry fetches a catalogue row or fails: every row of the decision table is
// stated against a real catalogue entry, never a hand-built one, so a change
// to the catalogue's expected lanes is caught here rather than in production.
func entry(t *testing.T, id string) Entry {
	t.Helper()
	e, ok := Get(id)
	if !ok {
		t.Fatalf("catalogue has no %q", id)
	}
	return e
}

// one runs Compute over a single entry and returns its row.
//
// ⚠️ **WITH `ToolOTLP` ON, BECAUSE THIS IS THE DECISION TABLE AND THE TABLE HAS
// AN otel ROW.** The lane is opt-in as of the Developer switch, so on a default
// machine it is not expected and rows 5 and 6 are not reachable at all — which
// is a fact about the switch, pinned in toolotlp_test.go, not about the rule.
// Asking for the lane here keeps every row of §4 exercised as written.
func one(t *testing.T, e Entry, f Facts) Integration {
	t.Helper()
	rows := Compute(now, []Entry{e}, map[string]Facts{e.ID: f}, Options{Window: 24 * time.Hour, ToolOTLP: true})
	if len(rows) != 1 {
		t.Fatalf("Compute returned %d rows, want 1", len(rows))
	}
	return rows[0]
}

// configured is the common prefix of rows 3b onwards: keld's blocks are on
// disk and the newest session started AFTER they were written.
func configured() Facts {
	return Facts{
		Configured: true,
		Wiring: WiringFacts{
			ConfigPresent:        true,
			ConfigMatchesAdapter: true,
			PointsAtProxy:        true,
			ConfigMtime:          now.Add(-48 * time.Hour),
			NewestSessionStart:   now.Add(-2 * time.Hour),
		},
	}
}

// TestComputeDecisionTable pins every row of spec §4, including 3b and 7b.
func TestComputeDecisionTable(t *testing.T) {
	trusted := func(f Facts) Facts { f.Wiring.HookTrusted, f.Wiring.HookTrustKnown = true, true; return f }

	cases := []struct {
		row   string
		id    string
		facts func() Facts
		state State
		lane  SurfaceKind
	}{
		{
			row: "1 · no config dir", id: "claude_code",
			facts: func() Facts { return Facts{} },
			state: NotInstalled,
		},
		{
			row: "2 · installed, manifest does not record it", id: "claude_code",
			facts: func() Facts { return Facts{Wiring: WiringFacts{ConfigPresent: true}} },
			state: NotConfigured,
		},
		{
			row: "3 · newest session older than the config", id: "claude_code",
			facts: func() Facts {
				f := configured()
				f.Wiring.NewestSessionStart = f.Wiring.ConfigMtime.Add(-time.Hour)
				// Lanes are live; the restart notice still wins.
				f.Lanes = LaneFacts{LastHookPointer: ago(time.Minute), LastTelemetryForward: ago(time.Minute)}
				return f
			},
			state: RestartRequired,
		},
		{
			row: "3b · Codex hooks not trusted, nothing via hook", id: "codex",
			facts: func() Facts {
				f := configured()
				f.Wiring.HookTrustKnown, f.Wiring.HookTrusted = true, false
				f.Lanes = LaneFacts{LastTelemetryForward: ago(time.Hour)}
				return f
			},
			state: ApprovalRequired,
		},
		{
			row: "4 · configured, restarted, nothing on any lane", id: "claude_code",
			facts: configured,
			state: Idle,
		},
		{
			row: "5 · telemetry but no pointer", id: "claude_code",
			facts: func() Facts {
				f := configured()
				f.Lanes = LaneFacts{LastTelemetryForward: ago(time.Hour)}
				return f
			},
			state: Broken, lane: SurfaceHook,
		},
		{
			row: "6 · pointers and rows but no telemetry", id: "claude_code",
			facts: func() Facts {
				f := configured()
				f.Lanes = LaneFacts{
					LastHookPointer:       ago(time.Hour),
					LastWatcherPointer:    ago(time.Hour),
					RowsForRecentPointers: yes(),
				}
				return f
			},
			state: Broken, lane: SurfaceOTel,
		},
		{
			row: "7 · everything but the reader, and the reader is expected", id: "claude_code",
			facts: func() Facts {
				f := configured()
				f.Lanes = LaneFacts{
					LastHookPointer:       ago(time.Hour),
					LastWatcherPointer:    ago(time.Hour),
					LastTelemetryForward:  ago(time.Hour),
					RowsForRecentPointers: no(),
				}
				return f
			},
			state: Broken, lane: SurfaceReader,
		},
		{
			// ⚠️ The example moved from Codex to Gemini on 2026-09-15, when
			// readers/codex.py landed and Codex's reader became expected. Gemini
			// has no reader, so its silence still cannot break the tool. Gemini
			// runs no hook either, so its expected lanes are otel and watcher.
			row: "7b · same, but the reader is NOT expected (Gemini today)", id: "gemini_cli",
			facts: func() Facts {
				f := configured()
				f.Lanes = LaneFacts{
					LastWatcherPointer:    ago(time.Hour),
					LastTelemetryForward:  ago(time.Hour),
					RowsForRecentPointers: no(),
				}
				return f
			},
			state: Working,
		},
		{
			// The case the whole 2026-09-15 Codex run exists to produce, and it
			// could not be written before: with a reader on disk Codex expects
			// all four lanes, so this row is only green when the hook is trusted,
			// the pointer arrived, telemetry forwarded and the store holds rows.
			row: "8b · Codex, fully wired: trusted hook and all four lanes", id: "codex",
			facts: func() Facts {
				f := trusted(configured())
				f.Lanes = LaneFacts{
					LastHookPointer:       ago(time.Hour),
					LastWatcherPointer:    ago(time.Hour),
					LastTelemetryForward:  ago(time.Hour),
					RowsForRecentPointers: yes(),
				}
				return f
			},
			state: Working,
		},
		{
			row: "8 · every expected lane saw it", id: "claude_code",
			facts: func() Facts {
				f := configured()
				f.Lanes = LaneFacts{
					LastHookPointer:       ago(time.Hour),
					LastWatcherPointer:    ago(time.Hour),
					LastTelemetryForward:  ago(time.Hour),
					RowsForRecentPointers: yes(),
				}
				return f
			},
			state: Working,
		},
		{
			row: "9 · an unsupported catalogue entry", id: "cursor",
			facts: func() Facts { return Facts{Wiring: WiringFacts{ConfigPresent: true}} },
			state: Unsupported,
		},
	}

	for _, tc := range cases {
		t.Run(tc.row, func(t *testing.T) {
			got := one(t, entry(t, tc.id), tc.facts())
			if got.State != tc.state {
				t.Fatalf("state = %q, want %q", got.State, tc.state)
			}
			if got.BrokenLane != tc.lane {
				t.Fatalf("broken_lane = %q, want %q", got.BrokenLane, tc.lane)
			}
			if tc.state != Broken && got.BrokenLane != "" {
				t.Fatalf("broken_lane %q is set on a %q row", got.BrokenLane, got.State)
			}
		})
	}
}

// AC-4's first refusal: silence is idle. Fuzzed over windows and instants —
// whatever the window, a machine on which nothing happened is never broken.
func TestIdleIsNeverBroken(t *testing.T) {
	r := rand.New(rand.NewSource(20260915))
	for i := 0; i < 500; i++ {
		window := time.Duration(1+r.Intn(72)) * time.Hour
		f := configured()
		// Every lane's last sighting is OUTSIDE the window, or never.
		if r.Intn(2) == 0 {
			f.Lanes.LastHookPointer = ago(window + time.Duration(r.Intn(1000))*time.Hour)
		}
		if r.Intn(2) == 0 {
			f.Lanes.LastWatcherPointer = ago(window + time.Duration(r.Intn(1000))*time.Hour)
		}
		if r.Intn(2) == 0 {
			f.Lanes.LastTelemetryForward = ago(window + time.Duration(r.Intn(1000))*time.Hour)
		}
		e := entry(t, "claude_code")
		rows := Compute(now, []Entry{e}, map[string]Facts{e.ID: f}, Options{Window: window})
		if rows[0].State == Broken {
			t.Fatalf("i=%d window=%v: a machine with nothing inside the window read %q", i, window, Broken)
		}
		if rows[0].State != Idle {
			t.Fatalf("i=%d window=%v: state = %q, want %q", i, window, rows[0].State, Idle)
		}
	}
}

// AC-4's second refusal: a session older than its config is a restart notice,
// not a fault — even when the lanes would otherwise say broken.
func TestRestartRequiredBeatsBroken(t *testing.T) {
	f := configured()
	f.Wiring.NewestSessionStart = f.Wiring.ConfigMtime.Add(-time.Hour)
	f.Lanes = LaneFacts{LastTelemetryForward: ago(time.Minute)} // row 5's shape
	got := one(t, entry(t, "claude_code"), f)
	if got.State != RestartRequired {
		t.Fatalf("state = %q, want %q", got.State, RestartRequired)
	}
	if got.BrokenLane != "" {
		t.Fatalf("broken_lane = %q on a restart_required row", got.BrokenLane)
	}
	if s := surfaceOf(t, got, SurfaceHook); s.WaitingOn != WaitingOnRestart || s.Instruction != InstructionRestart {
		t.Fatalf("hook surface waiting_on=%q instruction=%q", s.WaitingOn, s.Instruction)
	}
}

// AC-9: an untrusted Codex hook is approval_required, never broken — and the
// hook lane never reads working before approval.
func TestApprovalRequiredBeatsBroken(t *testing.T) {
	f := configured()
	f.Wiring.HookTrustKnown, f.Wiring.HookTrusted = true, false
	f.Lanes = LaneFacts{LastTelemetryForward: ago(time.Minute)} // row 5's shape
	got := one(t, entry(t, "codex"), f)
	if got.State != ApprovalRequired {
		t.Fatalf("state = %q, want %q", got.State, ApprovalRequired)
	}
	if got.BrokenLane != "" {
		t.Fatalf("broken_lane = %q on an approval_required row", got.BrokenLane)
	}
	hook := surfaceOf(t, got, SurfaceHook)
	if hook.WaitingOn != WaitingOnApproval {
		t.Fatalf("hook waiting_on = %q, want %q", hook.WaitingOn, WaitingOnApproval)
	}
	if hook.Instruction != InstructionApproval {
		t.Fatalf("hook instruction = %q, want AC-9's sentence verbatim", hook.Instruction)
	}
	if hook.LastSeen != nil {
		t.Fatal("the hook lane reported a sighting while waiting for approval")
	}
}

// Hook trust that is UNKNOWN must never produce approval_required: telling
// every older-Codex machine to approve something its Codex cannot show it is
// the confident-negative failure this codebase refuses everywhere else.
func TestUnknownHookTrustNeverAsksForApproval(t *testing.T) {
	f := configured()
	f.Wiring.HookTrustKnown, f.Wiring.HookTrusted = false, false
	if got := one(t, entry(t, "codex"), f); got.State == ApprovalRequired {
		t.Fatalf("state = %q from a trust answer that said it could not tell", got.State)
	}
}

// A lane the tool cannot feed contributes NEITHER half of broken. Cowork's
// otel lane is the live case: it is shown and never expected, because Cowork's
// egress to Atlas is blocked by design.
func TestUnexpectedLaneCannotBreak(t *testing.T) {
	e := entry(t, "cowork")
	f := Facts{
		Configured: true,
		Wiring: WiringFacts{
			ConfigPresent: true, ConfigMtime: now.Add(-48 * time.Hour),
			NewestSessionStart: now.Add(-time.Hour),
		},
		Lanes: LaneFacts{LastWatcherPointer: ago(time.Hour)},
	}
	got := one(t, e, f)
	if got.State != Working {
		t.Fatalf("state = %q, want %q — otel is not expected for Cowork, so its silence cannot break it", got.State, Working)
	}
	if s := surfaceOf(t, got, SurfaceOTel); s.Expected {
		t.Fatal("Cowork's otel surface is marked expected")
	}

	// And the mirror: an unexpected lane that IS active cannot supply the
	// "one lane saw it" half either. Gemini's READER is unexpected today — it
	// was Codex's watcher until readers/codex.py landed on 2026-09-15.
	cx := entry(t, "gemini_cli")
	cf := configured()
	cf.Lanes = LaneFacts{RowsForRecentPointers: yes()}
	if got := one(t, cx, cf); got.State != Idle {
		t.Fatalf("state = %q, want %q — an unexpected lane's activity is not evidence the tool is working", got.State, Idle)
	}
}

// Row 7b's other half: the reader is shown, unexpected, and SAYS SO.
func TestUnexpectedReaderExplainsItself(t *testing.T) {
	f := configured()
	f.Lanes = LaneFacts{LastWatcherPointer: ago(time.Hour), LastTelemetryForward: ago(time.Hour), RowsForRecentPointers: no()}
	got := one(t, entry(t, "gemini_cli"), f)
	s := surfaceOf(t, got, SurfaceReader)
	if s.Expected {
		t.Fatal("Gemini's reader surface is expected — it would read broken·reader on every machine")
	}
	if s.WaitingOn != WaitingOnReader || s.Instruction != InstructionReader {
		t.Fatalf("reader surface waiting_on=%q instruction=%q", s.WaitingOn, s.Instruction)
	}
}

// An unknown reader answer (no analysis backend, unreachable sidecar) is
// neither half: a check that did not run must not publish a confident
// negative, and must not vouch for one either.
func TestUnknownReaderContributesNeitherHalf(t *testing.T) {
	f := configured()
	f.Lanes = LaneFacts{
		LastHookPointer: ago(time.Hour), LastWatcherPointer: ago(time.Hour),
		LastTelemetryForward: ago(time.Hour), RowsForRecentPointers: nil,
	}
	if got := one(t, entry(t, "claude_code"), f); got.State != Working {
		t.Fatalf("state = %q, want %q — a reader we could not ask about is not a silent lane", got.State, Working)
	}
}

// A newest-session-start we could not read is UNKNOWN, and must not be read as
// "long ago" — that would call every never-run tool restart_required.
func TestUnknownSessionStartIsNotARestartNotice(t *testing.T) {
	f := configured()
	f.Wiring.NewestSessionStart = time.Time{}
	if got := one(t, entry(t, "claude_code"), f); got.State != Idle {
		t.Fatalf("state = %q, want %q", got.State, Idle)
	}
}

func TestComputeCoversTheWholeCatalogueInOrder(t *testing.T) {
	rows := Compute(now, Catalogue, map[string]Facts{}, Options{})
	if len(rows) != len(Catalogue) {
		t.Fatalf("Compute returned %d rows for a %d-entry catalogue", len(rows), len(Catalogue))
	}
	for i, r := range rows {
		if r.ID != Catalogue[i].ID {
			t.Fatalf("row %d is %q, want %q — the pane lists the catalogue whole, in order", i, r.ID, Catalogue[i].ID)
		}
		if !contains(States, r.State) {
			t.Fatalf("%s: state %q is outside the closed vocabulary", r.ID, r.State)
		}
	}
}

func contains(ss []State, s State) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func surfaceOf(t *testing.T, in Integration, kind SurfaceKind) Surface {
	t.Helper()
	for _, s := range in.Surfaces {
		if s.Kind == kind {
			return s
		}
	}
	t.Fatalf("%s has no %q surface", in.ID, kind)
	return Surface{}
}

// Cowork has no adapter: keld writes nothing for it, so the manifest will
// never record it and `not_configured` would be a permanent Set up button on a
// row with nothing to set up.
func TestAnEntryWithNoAdapterIsNeverNotConfigured(t *testing.T) {
	e := entry(t, "cowork")
	if e.AdapterName != "" {
		t.Fatalf("catalogue changed: cowork's AdapterName is %q", e.AdapterName)
	}
	f := Facts{
		Configured: false,
		Wiring: WiringFacts{
			ConfigPresent: true, ConfigMtime: now.Add(-48 * time.Hour),
			NewestSessionStart: now.Add(-time.Hour),
		},
	}
	if got := one(t, e, f); got.State != Idle {
		t.Fatalf("state = %q, want %q", got.State, Idle)
	}
}

// AC-8, as a tripwire rather than a convention: the state vocabulary exists in
// exactly one Go package. A second copy of the rule elsewhere would have to
// name these strings, and this is what would notice.
func TestTheStateVocabularyLivesInThisPackageOnly(t *testing.T) {
	// ⚠️ `restart_required` is deliberately NOT in this list. It is already a
	// JSON key on /v1/settings and /v1/config with an unrelated meaning ("this
	// write needs a service restart"), so scanning for it would fail on two
	// files that predate this package and have nothing to do with it. The
	// other three are unambiguous.
	distinctive := []string{"not_configured", "approval_required", "not_installed"}
	root := filepath.Join("..", "..", "..")
	var offenders []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "sidecar", "docs", "ui":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(p) != ".go" || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		if strings.Contains(filepath.ToSlash(p), "internal/agent/integrations/") {
			return nil // this package is where they live
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, s := range distinctive {
			if strings.Contains(string(body), `"`+s+`"`) {
				offenders = append(offenders, p+" → "+s)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("the state vocabulary is named outside internal/agent/integrations — "+
			"a second copy of the rule is the one defect AC-8 exists to prevent:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
