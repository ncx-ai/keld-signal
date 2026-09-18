package integrations

import (
	"os"
	"strconv"
	"time"
)

// DefaultWindow is how far back a lane fact counts as "the tool was seen".
//
// ⚠️ 24 h IS A STARTING GUESS, NOT A MEASUREMENT. It is deliberately wide:
// every error it makes is in the direction of `idle`, and AC-4's whole point is
// that a quiet machine must not be called broken. It is tuned once fleet
// `integration.broken` events say how quiet real machines get.
const DefaultWindow = 24 * time.Hour

// WindowEnv overrides it on one machine (a Go duration: "6h", "30m").
const WindowEnv = "KELD_INTEGRATIONS_WINDOW"

// Options are Compute's knobs. A zero Options is the shipped behaviour.
type Options struct {
	// Window is the lane look-back. Zero means DefaultWindow, honouring
	// KELD_INTEGRATIONS_WINDOW.
	Window time.Duration
	// AutoSetup is reported on the Response, not used by the rule.
	AutoSetup bool
}

// Window resolves the effective look-back: the explicit option, else the env
// var, else DefaultWindow. A malformed or non-positive env value is IGNORED
// rather than clamped — a zero window would make every configured machine
// read idle forever and say nothing about why.
func (o Options) window() time.Duration {
	if o.Window > 0 {
		return o.Window
	}
	if raw := os.Getenv(WindowEnv); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return time.Duration(n) * time.Hour
		}
	}
	return DefaultWindow
}

// Facts is everything Compute is told about ONE tool. It is a plain value with
// no readers of its own, so every test states a decision-table row directly
// and the rule is exercised without a filesystem.
type Facts struct {
	// Configured — keld's manifest records this tool.
	Configured  bool
	Wiring      WiringFacts
	Lanes       LaneFacts
	ToolVersion string
	BackupPath  string
	// Repair — what keld last repaired in this tool's config, nil when it has
	// never repaired one. Read from disk (LoadRepairs), because the detector
	// that repairs and the route that reports never share memory.
	Repair *Repair
}

// Compute decides one state per tool. IT IS THE ONLY PLACE THAT DECIDES (AC-8):
// the loopback route, `keld signal doctor` and the client-events emitter all
// call it, and the pane renders what it said verbatim. A second copy of this
// rule — in Go or in JavaScript — is the defect, not the duplication.
//
// The decision follows spec §4's table in its own order. Two refusals matter
// more than the happy path and are checked BEFORE any lane is read:
//
//   - a session older than its config is `restart_required`, never `broken`;
//   - an untrusted Codex hook is `approval_required`, never `broken`.
//
// And `broken` itself needs BOTH halves: one EXPECTED lane active inside the
// window and another EXPECTED lane silent. Neither half alone is evidence —
// silence on everything is a quiet user, and activity on everything is a
// working tool.
func Compute(now time.Time, entries []Entry, facts map[string]Facts, opts Options) []Integration {
	window := opts.window()
	out := make([]Integration, 0, len(entries))
	for _, e := range entries {
		out = append(out, computeOne(now, window, e, facts[e.ID]))
	}
	return out
}

func computeOne(now time.Time, window time.Duration, e Entry, f Facts) Integration {
	level := e.SupportLevel()
	expected := map[SurfaceKind]bool{}
	for _, k := range e.ExpectedLanes(level) {
		expected[k] = true
	}
	active := laneActivity(e, f, now, window)

	state, brokenLane := decide(e, f, expected, active)

	in := Integration{
		ID:           e.ID,
		DisplayName:  e.DisplayName,
		Installed:    f.Wiring.ConfigPresent,
		Configured:   f.Configured,
		Supported:    e.Supported,
		StorageClass: e.StorageClass,
		State:        state,
		BrokenLane:   brokenLane,
		// ⚠️ ONLY UNDER RestartRequired. The facts reader can name a stale
		// session whenever it finds one, but a row that is `working` or `idle`
		// carrying a "this session is stale" id would be stating a problem it
		// had just decided there isn't. The verdict owns the evidence.
		StaleSessionID: staleSessionID(state, f),
		Repaired:       repaired(now, window, state, f),
		ToolVersion:    f.ToolVersion,
		BackupPath:     f.BackupPath,
		Surfaces:       make([]Surface, 0, len(e.Surfaces)),
	}
	for _, spec := range e.Surfaces {
		s := Surface{
			Kind:       spec.Kind,
			Documented: spec.Documented,
			Wired:      wired(e, spec.Kind, f),
			Expected:   expected[spec.Kind],
			LastSeen:   lastSeen(spec.Kind, f),
		}
		s.WaitingOn = waitingOn(e, spec.Kind, state, expected[spec.Kind])
		s.Instruction = Instructions[s.WaitingOn]
		in.Surfaces = append(in.Surfaces, s)
	}
	return in
}

// staleSessionID is the id the restart verdict was decided on, and "" for every
// other verdict — see the field comment on Integration.StaleSessionID.
func staleSessionID(state State, f Facts) string {
	if state != RestartRequired {
		return ""
	}
	return f.Wiring.StaleSessionID
}

// repaired is the note the pane prints: keld rewrote its own block in this
// tool's config, and the tool needs one restart to pick it up.
//
// ⚠️ IT IS NOT SCOPED TO RestartRequired, AND THE FIRST DRAFT OF THIS FUNCTION
// WAS. `broken` is the state the 2026-09-18 incident actually produced — the row
// read `broken · otel` while both the stale credential and the live one sat on
// disk in front of the daemon — and it is therefore the row that most needs the
// sentence. Reproduced end to end on an isolated KELD_HOME: with no Codex
// transcript on the machine there is no session to call stale, so the repaired
// row reads `broken`, and under the narrow rule it published nothing at all.
//
// Two conditions instead, and each is a refusal the rest of this file already
// makes:
//
//   - `working` CLEARS it. Every expected lane has carried something since the
//     config was written, so the tool has demonstrably read it; a row that has
//     just decided the tool is fine must not go on asking for a restart. (This
//     is staleSessionID's rule, stated against the evidence rather than against
//     one verdict.)
//   - it AGES OUT at the row's own lane look-back, so a tool nobody opens does
//     not carry "restart this once" forever — the permanent-instruction-with-
//     nothing-to-do failure a zero NewestSessionStart is refused for. The bound
//     is the window already in hand rather than a new constant: outside it
//     nothing else on this row counts either. `restart_required` is EXEMPT,
//     because that verdict is direct evidence the restart still has not
//     happened, however long ago the repair was.
//
// The SENTENCE is attached here rather than stored with the record, so a
// reworded note reaches every machine with the binary instead of only the ones
// that repair again afterwards. A reason with no sentence (ReasonFirstSetup, or
// one a newer daemon wrote and this one does not know) publishes nothing: the
// pane maps nothing, so a note it cannot be handed is a note that does not
// exist.
func repaired(now time.Time, window time.Duration, state State, f Facts) *Repair {
	if f.Repair == nil || state == Working {
		return nil
	}
	if state != RestartRequired && f.Repair.At.Before(now.Add(-window)) {
		return nil
	}
	note := RepairNotes[f.Repair.Reason]
	if note == "" {
		return nil
	}
	out := *f.Repair
	out.Note = note
	return &out
}

// laneState is a lane's answer inside the window. `unknown` exists because a
// check that did not run must contribute NEITHER half of broken: it may not
// publish a confident negative, and it may not vouch for the tool either.
type laneState int

const (
	laneUnknown laneState = iota
	laneActive
	laneSilent
)

// laneActivity answers each lane for one tool.
//
// hook / watcher / otel are answered from INSTANTS ON DISK, so a nil is the
// fact "nothing has ever arrived" rather than "this daemon has not seen one
// yet" — which is why silence is usable evidence at all (see Lanes).
//
// The reader is the one lane whose absence can mean we could not ask.
func laneActivity(e Entry, f Facts, now time.Time, window time.Duration) map[SurfaceKind]laneState {
	// ⚠️ THE LOOK-BACK NEVER REACHES BACK PAST THE CONFIG. Activity a tool
	// produced BEFORE Signal configured it says nothing about whether that
	// config works, and counting it reports a tool broken seconds after setup:
	// pre-config telemetry supplies the "one expected lane saw it" half while a
	// hook wired one second ago supplies the silent one. Reproduced by the
	// conformance chain on 2026-09-15 for both Codex and Claude Code.
	//
	// A lane that has existed for one second has not been SILENT; it has not
	// been asked — the same distinction AC-4 draws between idle and broken.
	//
	// It is a clamp, not a grace period: once anything lands after the config,
	// a silent expected lane breaks the tool inside the window as before. An
	// UNREADABLE mtime (zero) falls back to the plain window rather than
	// suppressing every break forever.
	cut := now.Add(-window)
	if m := f.Wiring.ConfiguredAt; !m.IsZero() && m.After(cut) {
		cut = m
	}
	within := func(t *time.Time) laneState {
		if t == nil || t.Before(cut) {
			return laneSilent
		}
		return laneActive
	}
	m := map[SurfaceKind]laneState{
		SurfaceHook:      within(f.Lanes.LastHookPointer),
		SurfaceWatcher:   within(f.Lanes.LastWatcherPointer),
		SurfaceOTel:      within(f.Lanes.LastTelemetryForward),
		SurfaceExtension: laneUnknown, // nothing ships one; it can never speak
		SurfaceReader:    laneUnknown,
	}
	if f.Lanes.RowsForRecentPointers != nil {
		if *f.Lanes.RowsForRecentPointers {
			m[SurfaceReader] = laneActive
		} else {
			m[SurfaceReader] = laneSilent
		}
	}
	_ = e
	return m
}

// decide walks spec §4's table in its own order.
func decide(e Entry, f Facts, expected map[SurfaceKind]bool, active map[SurfaceKind]laneState) (State, SurfaceKind) {
	// Row 9, first: an unsupported entry is a catalogue row whatever else is
	// true of the machine. Cursor with its config dir present is still
	// `unsupported`, not `not_installed` — Signal names the tool and its
	// storage class and claims nothing more.
	if !e.Supported {
		return Unsupported, ""
	}
	// Row 1.
	if !f.Wiring.ConfigPresent {
		return NotInstalled, ""
	}
	// Row 2. The "or auto-setup → restart_required" half of that row is the
	// DETECTOR's doing, not a branch here: once it has applied the adapter the
	// manifest records the tool and the session predates the new config, so
	// row 3 produces restart_required on the very next poll.
	//
	// ⚠️ IT DOES NOT APPLY TO AN ENTRY WITH NO ADAPTER. Cowork is configured as
	// part of Claude Desktop and keld writes nothing for it — the watcher
	// reading its transcripts host-side is the only lane that can ever feed —
	// so there is no config to write and the manifest will never record it.
	// Calling it `not_configured` would put a permanent Set up button on a row
	// with nothing to set up, and a permanent doctor finding beside it.
	// ⚠️ **A BROKEN HOOK COMMAND IS NOT-CONFIGURED, and that is how an upgrade
	// reaches a machine it is forbidden to rewrite.** An upgrade deliberately
	// preserves tool configs — chain B asserts "preserved byte for byte" — so a
	// keld that fixes the hook QUOTING cannot repair the machines an older keld
	// configured. Measured on windows-latest: the upgrade lands, the configs
	// survive, and enrichment stays dark because the fixed binary is running
	// against a command it may not touch.
	//
	// Saying not_configured puts the repair on the path that already exists:
	// the detector applies the adapter, through the ONE setup path, and the row
	// then moves to restart_required on the next poll exactly as a new install
	// does. No new mechanism, no change to what an upgrade itself does.
	//
	// It cannot loop: `HookCommandNeedsRepair` is the same rule `HookCommand`
	// writes to, and a test pins that what the writer produces is never seen as
	// needing repair.
	if e.AdapterName != "" && (!f.Configured || f.Wiring.HookCommandBroken) {
		return NotConfigured, ""
	}
	// Row 3. ⚠️ A ZERO NewestSessionStart IS UNKNOWN, NOT "LONG AGO". A tool
	// that has never been run, or whose transcripts we cannot read, has no
	// stale session to restart; calling it restart_required would put a
	// permanent instruction on a row where there is nothing to do.
	//
	// ⚠️ AND A SESSION THAT HAS SINCE FORWARDED TELEMETRY HAS RESTARTED,
	// whatever its start instant says. The start is read from the transcript's
	// first line and a RESUME keeps the transcript, so a session carried into a
	// new process — which read the new config on the way in — reports the old
	// process's start time for as long as it lives. Telemetry arriving for that
	// session id after the config was written is direct proof the running
	// process adopted it: the loopback proxy is what the config points at.
	// Without this the instruction is unfollowable — restarting the tool does
	// not clear it, and the row states a repair that cannot work.
	if !f.Wiring.NewestSessionStart.IsZero() && !f.Wiring.ConfiguredAt.IsZero() &&
		f.Wiring.NewestSessionStart.Before(f.Wiring.ConfiguredAt) &&
		!f.Wiring.NewestSessionAdopted {
		return RestartRequired, ""
	}
	// Row 3b. Requires KNOWN trust: `known=false` is "we cannot tell", and
	// telling an older-Codex machine to approve hooks it cannot show would be
	// the same confident negative in the other direction. Requires the hook
	// lane to be silent too — hooks that are actually running (or bypassed
	// with --dangerously-bypass-hook-trust) need no approval.
	if f.Wiring.HookTrustKnown && !f.Wiring.HookTrusted &&
		expected[SurfaceHook] && active[SurfaceHook] != laneActive {
		return ApprovalRequired, ""
	}

	// Rows 4 to 8. Both halves, over EXPECTED lanes only.
	anyActive := false
	var firstSilent SurfaceKind
	for _, spec := range e.Surfaces { // catalogue order, so broken_lane is stable
		if !expected[spec.Kind] {
			continue
		}
		switch active[spec.Kind] {
		case laneActive:
			anyActive = true
		case laneSilent:
			if firstSilent == "" {
				firstSilent = spec.Kind
			}
		}
	}
	if !anyActive {
		return Idle, "" // row 4 — a quiet user is not a bug
	}
	if firstSilent != "" {
		return Broken, firstSilent // rows 5, 6, 7
	}
	return Working, "" // rows 7b and 8
}

// wired answers, per lane, whether the configuration this lane needs is on
// disk right now — never whether we remember writing it (AC-1).
func wired(e Entry, kind SurfaceKind, f Facts) bool {
	switch kind {
	case SurfaceHook:
		return e.AdapterName != "" && f.Wiring.ConfigMatchesAdapter
	case SurfaceOTel:
		// Two conditions, not one: a config written by an OLDER keld can be
		// perfectly well-formed and still name a destination that no longer
		// exists. That is the stale-credential failure the telemetry proxy was
		// built to end, and it was invisible for 40 minutes on a real machine.
		return e.AdapterName != "" && f.Wiring.ConfigMatchesAdapter && f.Wiring.PointsAtProxy
	case SurfaceWatcher:
		// The daemon tails the tool's own directory; there is no tool config
		// to write, so the question is only whether the directory is there.
		return f.Wiring.ConfigPresent
	case SurfaceReader:
		return e.ReaderAvailable
	default:
		return false
	}
}

func lastSeen(kind SurfaceKind, f Facts) *time.Time {
	switch kind {
	case SurfaceHook:
		return f.Lanes.LastHookPointer
	case SurfaceWatcher:
		return f.Lanes.LastWatcherPointer
	case SurfaceOTel:
		return f.Lanes.LastTelemetryForward
	default:
		// The reader lane has no instant of its own — it answers "are there
		// rows for the recent pointers", which is not a sighting. Publishing a
		// borrowed instant there would be a fact about the pointer, not the row.
		return nil
	}
}

// waitingOn names what one lane is waiting for. The sentences are
// Instructions'; they live once, in vocabulary.go, because the pane, doctor
// and the wire doc all quote them.
func waitingOn(e Entry, kind SurfaceKind, state State, expected bool) WaitingOn {
	switch {
	case state == ApprovalRequired && kind == SurfaceHook:
		return WaitingOnApproval
	case state == RestartRequired && expected && (kind == SurfaceHook || kind == SurfaceOTel):
		// Only the lanes the tool reads out of its own config at startup. A
		// restart does not change what the daemon can tail.
		return WaitingOnRestart
	case kind == SurfaceReader && e.Supported && !expected:
		return WaitingOnReader
	default:
		return WaitingOnNothing
	}
}

// Respond wraps computed rows in the route's body, with the closed
// vocabularies beside them so a consumer derives its cases from the server
// rather than hard-coding them.
func Respond(now time.Time, rows []Integration, autoSetup bool) Response {
	return Response{
		Integrations: rows,
		Vocabulary:   Vocabulary{States: States, WaitingOn: WaitingOns},
		AutoSetup:    autoSetup,
		ComputedAt:   now.UTC(),
	}
}
