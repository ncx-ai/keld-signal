package integrations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSink records what an emitter was asked to publish.
type fakeSink struct {
	got []Emission
}

func (f *fakeSink) Emit(code, severity string, fields map[string]any) {
	f.got = append(f.got, Emission{Code: code, Severity: severity, Fields: fields})
}

func (f *fakeSink) EmitExempt(code, severity string, fields map[string]any) {
	f.got = append(f.got, Emission{Code: code, Severity: severity, Fields: fields, Exempt: true})
}

func brokenCodex(version string) Integration {
	return Integration{
		ID:          "codex",
		DisplayName: "Codex",
		Installed:   true,
		Configured:  true,
		Supported:   true,
		State:       Broken,
		BrokenLane:  SurfaceHook,
		ToolVersion: version,
	}
}

func idleCodex() Integration {
	return Integration{ID: "codex", DisplayName: "Codex", Installed: true, Configured: true, Supported: true, State: Idle}
}

// TestBrokenTransitionEmitsExactlyOneEvent is AC-5's first half: entering
// broken emits one integration.broken carrying the source, the silent lane,
// the tool version, the window and the per-lane counts.
func TestBrokenTransitionEmitsExactlyOneEvent(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	counts := map[string]LaneCounts{"codex": {Hook: 0, Watcher: 2, OTel: 41}}

	ems, states := Reconcile(now, []Integration{brokenCodex("0.153.4")}, nil, counts, 24*time.Hour)
	if len(ems) != 1 {
		t.Fatalf("want exactly 1 emission, got %d: %+v", len(ems), ems)
	}
	e := ems[0]
	if e.Code != CodeBroken {
		t.Fatalf("code = %q, want %q", e.Code, CodeBroken)
	}
	for k, want := range map[string]any{
		"source":       "codex",
		"surface":      "hook",
		"tool_version": "0.153.4",
		"window_h":     float64(24),
		"hook_n":       0,
		"watcher_n":    2,
		"otel_n":       41,
		"reader_n":     0,
	} {
		if got := e.Fields[k]; got != want {
			t.Errorf("fields[%q] = %#v, want %#v", k, got, want)
		}
	}

	// Same state again: nothing is emitted.
	again, _ := Reconcile(now.Add(time.Hour), []Integration{brokenCodex("0.153.4")}, states, counts, 24*time.Hour)
	if len(again) != 0 {
		t.Fatalf("a second identical compute emitted %d events, want 0: %+v", len(again), again)
	}
}

// TestRecoveredOnLeavingBroken is AC-5's second half.
func TestRecoveredOnLeavingBroken(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	_, states := Reconcile(now, []Integration{brokenCodex("0.153.4")}, nil, nil, 24*time.Hour)

	ems, states2 := Reconcile(now.Add(time.Hour), []Integration{idleCodex()}, states, nil, 24*time.Hour)
	if len(ems) != 1 || ems[0].Code != CodeRecovered {
		t.Fatalf("want exactly one %s, got %+v", CodeRecovered, ems)
	}
	if got := ems[0].Fields["state"]; got != string(Idle) {
		t.Errorf("recovered fields[state] = %#v, want %q", got, Idle)
	}
	if got := ems[0].Fields["source"]; got != "codex" {
		t.Errorf("recovered fields[source] = %#v", got)
	}
	// And recovery is not re-announced.
	again, _ := Reconcile(now.Add(2*time.Hour), []Integration{idleCodex()}, states2, nil, 24*time.Hour)
	if len(again) != 0 {
		t.Fatalf("recovery re-announced: %+v", again)
	}
}

// TestNoEventForAnyOtherTransition pins the code set: only entering and
// leaving broken is an event. idle→working is a normal day.
func TestNoEventForAnyOtherTransition(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	working := idleCodex()
	working.State = Working

	_, states := Reconcile(now, []Integration{idleCodex()}, nil, nil, 24*time.Hour)
	ems, states2 := Reconcile(now.Add(time.Hour), []Integration{working}, states, nil, 24*time.Hour)
	if len(ems) != 0 {
		t.Fatalf("idle→working emitted %+v, want nothing", ems)
	}
	back, _ := Reconcile(now.Add(2*time.Hour), []Integration{idleCodex()}, states2, nil, 24*time.Hour)
	if len(back) != 0 {
		t.Fatalf("working→idle emitted %+v, want nothing", back)
	}
}

// TestFirstSightOfANonBrokenToolIsSilent — a machine that has never emitted
// must not announce every tool it finds.
func TestFirstSightOfANonBrokenToolIsSilent(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	rows := []Integration{idleCodex(), {ID: "pi", State: Unsupported}, {ID: "cursor", State: NotInstalled}}
	ems, states := Reconcile(now, rows, nil, nil, 24*time.Hour)
	if len(ems) != 0 {
		t.Fatalf("first sight emitted %+v, want nothing", ems)
	}
	if len(states) != 3 {
		t.Fatalf("states recorded %d rows, want 3", len(states))
	}
}

// TestEmitPassesSeverityAndExemptionToTheSink — recovered rides EmitExempt for
// the reason service.recovered does: under the default warn floor only the
// failure would ever reach Atlas.
func TestEmitPassesSeverityAndExemptionToTheSink(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	sink := &fakeSink{}
	_, states := Reconcile(now, []Integration{brokenCodex("")}, nil, nil, 24*time.Hour)
	ems, _ := Reconcile(now.Add(time.Hour), []Integration{idleCodex()}, states, nil, 24*time.Hour)
	Emit(sink, ems)

	if len(sink.got) != 1 {
		t.Fatalf("sink saw %d, want 1", len(sink.got))
	}
	if sink.got[0].Severity != SeverityInfo || !sink.got[0].Exempt {
		t.Fatalf("recovered = %s/exempt=%v, want info/exempt=true", sink.got[0].Severity, sink.got[0].Exempt)
	}

	sink2 := &fakeSink{}
	b, _ := Reconcile(now, []Integration{brokenCodex("")}, nil, nil, 24*time.Hour)
	Emit(sink2, b)
	if len(sink2.got) != 1 || sink2.got[0].Severity != SeverityWarn || sink2.got[0].Exempt {
		t.Fatalf("broken = %+v, want warn and not exempt", sink2.got)
	}
}

// TestToolVersionIsStatedEvenWhenUnknown — AC-7 forbids guessing it; an empty
// string is the honest answer and the key is still present, so a reader can
// tell "unknown" from "this daemon is too old to say".
func TestToolVersionIsStatedEvenWhenUnknown(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	ems, _ := Reconcile(now, []Integration{brokenCodex("")}, nil, nil, 24*time.Hour)
	v, ok := ems[0].Fields["tool_version"]
	if !ok {
		t.Fatal("tool_version key absent")
	}
	if v != "" {
		t.Fatalf("tool_version = %#v, want the empty string", v)
	}
}

// TestConfiguredEmissionNamesTheToolAndNeverThePath — AC-3's event. The backup
// path is deliberately not a field: the redaction gate would strip it anyway,
// and a boolean is the fact a fleet view can aggregate.
func TestConfiguredEmissionNamesTheToolAndNeverThePath(t *testing.T) {
	e := ConfiguredEmission("codex", "codex", "/Users/someone/.keld/backups/config.toml.bak", true, true)
	if e.Code != CodeConfigured {
		t.Fatalf("code = %q", e.Code)
	}
	if e.Fields["source"] != "codex" || e.Fields["backup"] != true || e.Fields["restart_required"] != true || e.Fields["auto"] != true {
		t.Fatalf("fields = %#v", e.Fields)
	}
	for k, v := range e.Fields {
		if s, ok := v.(string); ok && strings.Contains(s, "/") {
			t.Fatalf("fields[%q] carries a path: %q", k, s)
		}
	}
}

// TestEmittedStatesRoundTripWithoutTruncatingTheDocument — WS-C1 writes its
// lane facts into the same file. Saving must be a read-modify-write of the
// whole document, never a truncate.
func TestEmittedStatesRoundTripWithoutTruncatingTheDocument(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	path := emitStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := `{"lanes":{"codex":{"hook":"2026-09-15T10:00:00Z"}},"schema":7}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	states := map[string]EmittedState{"codex": {State: Broken, Surface: SurfaceHook, At: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}}
	if err := SaveEmittedStates(states); err != nil {
		t.Fatal(err)
	}

	var doc map[string]json.RawMessage
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("document is not an object after save: %v\n%s", err, raw)
	}
	if string(doc["lanes"]) != `{"codex":{"hook":"2026-09-15T10:00:00Z"}}` {
		t.Fatalf("WS-C1's lanes key was rewritten: %s", doc["lanes"])
	}
	if string(doc["schema"]) != "7" {
		t.Fatalf("schema key lost: %s", doc["schema"])
	}

	back, err := LoadEmittedStates()
	if err != nil {
		t.Fatal(err)
	}
	if back["codex"].State != Broken || back["codex"].Surface != SurfaceHook {
		t.Fatalf("round trip lost the state: %+v", back)
	}
}

// TestLoadEmittedStatesOnAnAbsentFileIsEmptyNotAnError — a machine that has
// never emitted is a normal first run, not a fault.
func TestLoadEmittedStatesOnAnAbsentFileIsEmptyNotAnError(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	got, err := LoadEmittedStates()
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want empty", got)
	}
}

// TestDocumentsTheFourIntegrationCodes is AC-5's doc half, asserted exactly as
// the acceptance criterion words it:
//
//	grep -c 'integration\.' docs/signal-client-events.md == 4
func TestDocumentsTheFourIntegrationCodes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "signal-client-events.md"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "integration.") {
			n++
		}
	}
	if n != 4 {
		t.Fatalf("lines matching 'integration\\.' = %d, want 4", n)
	}
	for _, code := range []string{CodeBroken, CodeRecovered, CodeConfigured, CodeReport} {
		if !strings.Contains(string(raw), code) {
			t.Errorf("docs/signal-client-events.md does not document %q", code)
		}
	}
}
