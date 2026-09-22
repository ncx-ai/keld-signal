package integrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/telemetry"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// The live pair every test in this file repairs TOWARDS — the same one
// detectorFor hands the detector.
func liveParams() tools.SetupParams {
	// ⚠️ ToolOTLP IS ON IN EVERY FIXTURE HERE, TO MATCH repairingDetector. A
	// planted config must be the config the switch in force would produce: with
	// the switch on and no OTLP block in the file, the detector is right to
	// rewrite, and a test asserting "left alone" would be asserting a defect.
	return tools.SetupParams{
		Endpoint:    "http://127.0.0.1:14318",
		IngestToken: "local-secret",
		BinPath:     "/usr/local/bin/keld",
		ToolOTLP:    true,
	}
}

// staleParams is what an OLDER keld left in the tools' configs on 2026-09-18:
// the same endpoint, a credential the running proxy rejects.
func staleParams() tools.SetupParams {
	p := liveParams()
	p.IngestToken = "stale-secret"
	return p
}

// plantConfigured writes one tool's config as keld itself would have written
// it with `p`, appends `extra` (whatever the TOOL later wrote for itself), and
// records the result in the manifest the way ApplyEntry does — stamped `at`, so
// a test can watch configured_at MOVE.
func plantConfigured(t *testing.T, adapterName string, p tools.SetupParams, extra string, at time.Time) string {
	t.Helper()
	a, err := tools.Get(adapterName)
	if err != nil {
		t.Fatal(err)
	}
	path := a.ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := a.Apply(nil, p, false)
	if plan.Conflict != "" {
		t.Fatalf("planting %s: %s", adapterName, plan.Conflict)
	}
	if err := os.WriteFile(path, []byte(plan.AfterText+extra), 0o644); err != nil {
		t.Fatal(err)
	}
	// MERGED, never rebuilt — the rule ApplyEntry itself states. A test that
	// planted two tools by rebuilding would un-record the first one and then
	// watch the detector configure it from scratch, which passes for the wrong
	// reason.
	m, err := config.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if m.Tools == nil {
		m.Tools = map[string]config.ToolManifest{}
	}
	m.Tools[adapterName] = config.ToolManifest{Name: adapterName, ConfigPath: path, Managed: plan.Managed, ConfiguredAt: &at}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	return path
}

// repairingDetector is detectorFor with the proxy probe answered by a stub. It
// is ALWAYS stubbed: a test that reached the real loopback port would pass or
// fail depending on whether the developer's own daemon happens to be up.
func repairingDetector(em Emitter, outcome telemetry.ProbeOutcome) *Detector {
	d := detectorFor(em)
	// ⚠️ THE SWITCH IS ON FOR EVERY TEST HERE, AND THAT IS THE WHOLE SCOPE OF
	// THIS FEATURE. Telemetry drift is a wrong CREDENTIAL inside keld's block,
	// and with `tool_otlp` off (the product default) no block is written at
	// all, so there is nothing that can drift: the only apply the switch-off
	// world produces is ReasonOTLPSwitch removing a block an earlier keld left
	// behind. Discovered when these tests went red on the merge with the
	// Developer switch, which is the correct answer rather than a broken one.
	d.ToolOTLP = func() bool { return true }
	d.Probe = func(endpoint, secret string) (telemetry.ProbeOutcome, int) {
		code := 200
		if outcome == telemetry.ProbeRejected {
			code = 401
		}
		return outcome, code
	}
	d.Log = func(string, ...any) {}
	return d
}

func configuredAtOf(t *testing.T, adapter string) time.Time {
	t.Helper()
	m, err := config.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	tm, ok := m.Tools[adapter]
	if !ok {
		t.Fatalf("manifest no longer records %s", adapter)
	}
	if tm.ConfiguredAt == nil {
		t.Fatalf("%s has no configured_at", adapter)
	}
	return *tm.ConfiguredAt
}

// ⚠️ THE 2026-09-18 INCIDENT, AS A TEST. ~/.keld/agent.json held telemetry
// secret 26908e20… while ~/.codex/config.toml and ~/.claude/settings.json both
// held a5629e92…, written by an older keld still on PATH; a probe POST to the
// running proxy with the tools' value returned 401. The daemon could see both
// values the whole time and did nothing, because the detector never edits a
// config the manifest already records.
//
// That refusal is right for the PERSON'S OWN telemetry section and wrong for
// keld's own block, which keld wrote and owns.
func TestAStaleSecretInsideKeldsBlockIsRepairedOnce(t *testing.T) {
	isolate(t)
	before := time.Now().UTC().Add(-48 * time.Hour)
	cfg := plantConfigured(t, "codex", staleParams(), "", before)
	stale := mustRead(t, cfg)

	em := &fakeEmitter{}
	d := repairingDetector(em, telemetry.ProbeOK)
	d.Tick()

	got := mustRead(t, cfg)
	if strings.Contains(got, "stale-secret") {
		t.Fatalf("the stale credential survived the poll:\n%s", got)
	}
	if !strings.Contains(got, "local-secret") {
		t.Fatalf("the live credential was not written:\n%s", got)
	}

	// The pristine config is kept, exactly as a first-time setup keeps it.
	backup := filepath.Join(paths.BackupsDir(), "codex", "config.toml")
	if kept := mustRead(t, backup); kept != stale {
		t.Fatalf("the backup is not the config that was replaced:\n%s", kept)
	}

	// configured_at MOVES, because the restart question is now about this write.
	if at := configuredAtOf(t, "codex"); !at.After(before) {
		t.Fatalf("configured_at = %s, want later than %s", at, before)
	}

	if n := em.count(EventConfigured); n != 1 {
		t.Fatalf("%d %s events, want 1", n, EventConfigured)
	}
	if got := em.fields[0]["reason"]; got != ReasonTelemetryDrift {
		t.Fatalf("event reason = %v, want %q", got, ReasonTelemetryDrift)
	}

	// One repair, not one per poll.
	after := mustRead(t, cfg)
	d.Tick()
	if mustRead(t, cfg) != after {
		t.Fatal("a later poll rewrote a config it had already repaired")
	}
	if n := em.count(EventConfigured); n != 1 {
		t.Fatalf("%d %s events after a repeat poll, want 1", n, EventConfigured)
	}
}

// Drift OUTSIDE keld's markers is the person's own file. keld owns what is
// between its markers and nothing else, so a value that disagrees out there is
// not keld's to correct — the Set up button is a human asking.
func TestDriftOutsideKeldsMarkersIsLeftAlone(t *testing.T) {
	isolate(t)
	// keld's block is correct; the person keeps their own exporter beside it.
	own := "\n[my_own_exporter]\n" +
		"endpoint = \"https://otel.example.invalid/v1/logs\"\n" +
		"headers = { \"x-keld-ingest-token\" = \"somebody-elses-secret\" }\n"
	cfg := plantConfigured(t, "codex", liveParams(), own, time.Now().UTC().Add(-time.Hour))
	before := mustRead(t, cfg)

	em := &fakeEmitter{}
	repairingDetector(em, telemetry.ProbeOK).Tick()

	if got := mustRead(t, cfg); got != before {
		t.Fatalf("the detector rewrote a config whose only disagreement is outside its own markers:\n%s", got)
	}
	if em.count(EventConfigured) != 0 {
		t.Fatal("the detector announced repairing a config it must not have touched")
	}
}

// A conflict in the person's OWN telemetry section: their [otel] table sits
// beside keld's block, so the adapter cannot write without replacing something
// it does not own. Nothing is rewritten, whatever the secret inside keld's
// block says.
func TestAConflictInThePersonsOwnSectionIsLeftAlone(t *testing.T) {
	isolate(t)
	mine := "\n[otel]\nenvironment = \"mine\"\n"
	cfg := plantConfigured(t, "codex", staleParams(), mine, time.Now().UTC().Add(-time.Hour))
	before := mustRead(t, cfg)

	em := &fakeEmitter{}
	repairingDetector(em, telemetry.ProbeOK).Tick()

	if got := mustRead(t, cfg); got != before {
		t.Fatalf("the detector edited a file holding the person's own [otel]:\n%s", got)
	}
	if em.count(EventConfigured) != 0 {
		t.Fatal("the detector announced a repair it must not have made")
	}
}

// ⚠️ THE TOOLS REWRITE THEIR OWN CONFIG FILES — measured 2026-09-18, Codex
// writing `hooks.state` trust entries at session start and Claude Code
// rewriting settings.json unprompted. Drift is a comparison of the VALUES keld
// wrote, never of the file, or every session start would trip a repair.
func TestAToolRewritingItsOwnSettingsIsNotDrift(t *testing.T) {
	isolate(t)
	codexOwn := "\n[hooks.state.\"codex:SessionStart:0:0\"]\ntrusted = true\nhash = \"deadbeef\"\n"
	codexCfg := plantConfigured(t, "codex", liveParams(), codexOwn, time.Now().UTC().Add(-time.Hour))
	codexBefore := mustRead(t, codexCfg)

	// Claude Code's own keys, written into the same settings.json.
	claudeCfg := plantConfigured(t, "claude_code", liveParams(), "", time.Now().UTC().Add(-time.Hour))
	withOwnKeys := strings.Replace(mustRead(t, claudeCfg), "{\n", "{\n  \"model\": \"opus\",\n  \"statusLine\": {\"type\": \"command\"},\n", 1)
	if err := os.WriteFile(claudeCfg, []byte(withOwnKeys), 0o644); err != nil {
		t.Fatal(err)
	}

	em := &fakeEmitter{}
	repairingDetector(em, telemetry.ProbeOK).Tick()

	if got := mustRead(t, codexCfg); got != codexBefore {
		t.Fatalf("Codex's own hooks.state read as drift:\n%s", got)
	}
	if got := mustRead(t, claudeCfg); got != withOwnKeys {
		t.Fatalf("Claude Code's own keys read as drift:\n%s", got)
	}
	if em.count(EventConfigured) != 0 {
		t.Fatal("a tool's own housekeeping was announced as a repair")
	}
}

// One try per daemon life, the bound HookCommandBroken already has. A config
// that keeps drifting back — an older keld still on PATH rewriting it — must
// not be fought over on every poll.
func TestTheRepairIsNotAttemptedTwiceInOneDaemonLife(t *testing.T) {
	isolate(t)
	cfg := plantConfigured(t, "codex", staleParams(), "", time.Now().UTC().Add(-time.Hour))

	em := &fakeEmitter{}
	d := repairingDetector(em, telemetry.ProbeOK)
	d.Tick()
	if !strings.Contains(mustRead(t, cfg), "local-secret") {
		t.Fatal("the first poll did not repair")
	}

	// The older keld writes its value back.
	a, _ := tools.Get("codex")
	again := a.Apply(nil, staleParams(), false)
	if err := os.WriteFile(cfg, []byte(again.AfterText), 0o644); err != nil {
		t.Fatal(err)
	}
	d.Tick()

	if got := mustRead(t, cfg); strings.Contains(got, "local-secret") {
		t.Fatalf("the detector repaired a second time in one daemon life:\n%s", got)
	}
	if n := em.count(EventConfigured); n != 1 {
		t.Fatalf("%d %s events, want 1", n, EventConfigured)
	}
}

// ⚠️ DO NOT REPAIR WHAT YOU CANNOT VERIFY. Writing a credential the running
// proxy has not accepted is how 2026-09-18 happened; doing it unattended, from
// a background poll, is the same move with nobody reading the output. With
// nothing answering on the loopback port the state is REPORTED and the file is
// left exactly as it was — and the work is HELD, not quarantined: the next poll
// repairs it once the proxy answers.
func TestNothingIsRewrittenWhenTheProxyCannotBeReached(t *testing.T) {
	isolate(t)
	cfg := plantConfigured(t, "codex", staleParams(), "", time.Now().UTC().Add(-time.Hour))
	before := mustRead(t, cfg)

	em := &fakeEmitter{}
	var lines []string
	d := repairingDetector(em, telemetry.ProbeUnverified)
	d.Log = func(f string, a ...any) { lines = append(lines, f) }
	d.Tick()
	d.Tick()

	if got := mustRead(t, cfg); got != before {
		t.Fatalf("a credential was written without the proxy confirming it:\n%s", got)
	}
	if em.count(EventConfigured) != 0 {
		t.Fatal("a repair was announced that did not happen")
	}
	if len(lines) != 1 {
		t.Fatalf("%d log lines over two polls, want 1 — a refusal repeated every minute is one nobody reads", len(lines))
	}

	// The proxy comes up: the same detector repairs, because an unverifiable
	// machine was never a permanent refusal.
	d.Probe = func(string, string) (telemetry.ProbeOutcome, int) { return telemetry.ProbeOK, 200 }
	d.Tick()
	if !strings.Contains(mustRead(t, cfg), "local-secret") {
		t.Fatal("the repair did not happen once the proxy answered")
	}
}

// The proxy ANSWERING 401 is the other half: the value about to be written is
// one the daemon itself rejects, so writing it would replace a broken config
// with a differently broken config.
func TestNothingIsRewrittenWhenTheProxyRejectsTheCredential(t *testing.T) {
	isolate(t)
	cfg := plantConfigured(t, "codex", staleParams(), "", time.Now().UTC().Add(-time.Hour))
	before := mustRead(t, cfg)

	em := &fakeEmitter{}
	repairingDetector(em, telemetry.ProbeRejected).Tick()

	if got := mustRead(t, cfg); got != before {
		t.Fatalf("a credential the proxy rejects was written anyway:\n%s", got)
	}
	if em.count(EventConfigured) != 0 {
		t.Fatal("a repair was announced that did not happen")
	}
}

// The pane's half: a repaired row says so, and says the tool needs one restart
// — never `broken` with no explanation, which is exactly what the row read on
// 2026-09-18 while both values sat on disk in front of the daemon.
func TestARepairedRowSaysSoUnderRestartRequired(t *testing.T) {
	e, _ := Get("codex")
	now := time.Now().UTC()
	r := &Repair{Reason: ReasonTelemetryDrift, At: now}
	f := Facts{
		Configured: true,
		Repair:     r,
		Wiring: WiringFacts{
			ConfigPresent:      true,
			ConfiguredAt:       now,
			NewestSessionStart: now.Add(-time.Hour),
		},
	}
	in := computeOne(now, DefaultWindow, e, f, false)
	if in.State != RestartRequired {
		t.Fatalf("state = %s, want %s", in.State, RestartRequired)
	}
	if in.Repaired == nil {
		t.Fatal("a repaired row published no repair note")
	}
	if in.Repaired.Reason != ReasonTelemetryDrift {
		t.Fatalf("reason = %q, want %q", in.Repaired.Reason, ReasonTelemetryDrift)
	}
	if in.Repaired.Note == "" {
		t.Fatal("the note is what the pane prints; the pane maps nothing")
	}
}

// ⚠️ `broken` IS THE STATE THE INCIDENT PRODUCED, so it is the one that most
// needs the sentence. The row said `broken · otel` while the daemon could see
// both the stale credential and the live one, and a reader had no way to learn
// that anything had been done about it.
//
// Reproduced end to end on an isolated KELD_HOME: with no Codex transcript on
// the machine there is no session to call stale, so the repaired row reads
// `broken` rather than `restart_required` — and under the first draft of this
// rule it published `repaired: null`.
func TestARepairedRowSaysSoWhenTheRowIsBroken(t *testing.T) {
	e, _ := Get("codex") // the real catalogue row, so the lanes are the real ones
	now := time.Now().UTC()
	hook := now.Add(-48 * time.Hour) // before the repair: silent since
	// ⚠️ Older than settleWindow. A lane seen a minute ago means the machine is
	// still in flight, and a still-silent sibling is no longer evidence of a
	// break — which is right, and would make this fixture stop reproducing the
	// incident it is about.
	otel := now.Add(-10 * time.Minute)
	rows := true
	f := Facts{
		Configured: true,
		Repair:     &Repair{Reason: ReasonTelemetryDrift, At: now},
		Wiring:     WiringFacts{ConfigPresent: true, ConfiguredAt: now.Add(-3 * time.Hour)},
		Lanes: LaneFacts{
			LastHookPointer: &hook, LastTelemetryForward: &otel,
			LastWatcherPointer: &otel, RowsForRecentPointers: &rows,
		},
	}
	in := computeOne(now, DefaultWindow, e, f, false)
	if in.State != Broken {
		t.Fatalf("state = %s, want %s — the fixture no longer reproduces the incident", in.State, Broken)
	}
	if in.Repaired == nil || in.Repaired.Note == "" {
		t.Fatal("a broken row published no repair note — `broken` with no explanation is the defect")
	}
}

// Working is the one verdict that clears it: both expected lanes have carried
// something since the config was written, so the tool has demonstrably read it
// and there is nothing left to restart.
func TestAWorkingRowPublishesNoRepairNote(t *testing.T) {
	e, _ := Get("codex")
	now := time.Now().UTC()
	seen := now.Add(-time.Minute)
	rows := true
	f := Facts{
		Configured: true,
		Repair:     &Repair{Reason: ReasonTelemetryDrift, At: now.Add(-time.Hour)},
		Wiring: WiringFacts{
			ConfigPresent:      true,
			ConfiguredAt:       now.Add(-2 * time.Hour),
			NewestSessionStart: now.Add(-time.Minute),
		},
		Lanes: LaneFacts{
			LastHookPointer: &seen, LastTelemetryForward: &seen,
			LastWatcherPointer: &seen, RowsForRecentPointers: &rows,
		},
	}
	in := computeOne(now, DefaultWindow, e, f, false)
	if in.State != Working {
		t.Fatalf("state = %s, want %s", in.State, Working)
	}
	if in.Repaired != nil {
		t.Fatal("a working row published a repair note it had just decided was finished")
	}
}

// ⚠️ AND IT MUST AGE OUT, or a tool nobody opens carries "restart this once"
// forever — the permanent-instruction-with-nothing-to-do failure this package
// refuses elsewhere (a zero NewestSessionStart is UNKNOWN, not "long ago"). The
// bound is the row's OWN lane look-back rather than a new constant: outside it
// nothing else on the row counts either.
//
// `restart_required` is exempt, because that verdict is direct evidence the
// restart still has not happened however long ago the repair was.
func TestAnOldRepairAgesOutButNotWhileTheRestartIsStillOutstanding(t *testing.T) {
	e, _ := Get("codex")
	now := time.Now().UTC()
	old := &Repair{Reason: ReasonTelemetryDrift, At: now.Add(-30 * 24 * time.Hour)}

	idle := Facts{
		Configured: true,
		Repair:     old,
		Wiring:     WiringFacts{ConfigPresent: true, ConfiguredAt: now.Add(-30 * 24 * time.Hour)},
	}
	if got := computeOne(now, DefaultWindow, e, idle, false); got.State != Idle {
		t.Fatalf("state = %s, want %s", got.State, Idle)
	} else if got.Repaired != nil {
		t.Fatal("a month-old repair is still being announced on a quiet row")
	}

	stale := idle
	stale.Wiring.NewestSessionStart = now.Add(-31 * 24 * time.Hour)
	got := computeOne(now, DefaultWindow, e, stale, false)
	if got.State != RestartRequired {
		t.Fatalf("state = %s, want %s", got.State, RestartRequired)
	}
	if got.Repaired == nil {
		t.Fatal("the restart is still outstanding, so the reason for it must still be stated")
	}
}

// The repair is recorded where the ROUTE can read it: the detector writes it in
// the daemon, and GET /v1/integrations is computed somewhere else entirely.
func TestARepairIsRecordedForTheRouteToRead(t *testing.T) {
	isolate(t)
	plantConfigured(t, "codex", staleParams(), "", time.Now().UTC().Add(-time.Hour))

	repairingDetector(&fakeEmitter{}, telemetry.ProbeOK).Tick()

	got := LoadRepairs()
	r, ok := got["codex"]
	if !ok {
		t.Fatalf("no repair recorded for codex; recorded %v", got)
	}
	if r.Reason != ReasonTelemetryDrift {
		t.Fatalf("reason = %q, want %q", r.Reason, ReasonTelemetryDrift)
	}
	if r.At.IsZero() {
		t.Fatal("a repair with no instant cannot be aged out")
	}
}
