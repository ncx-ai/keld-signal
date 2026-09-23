package integrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// fakeEmitter stands in for the client-events emitter WS-C2 owns. The detector
// takes an interface precisely so this workstream can assert the event without
// owning the transport.
type fakeEmitter struct {
	codes  []string
	fields []map[string]any
}

func (f *fakeEmitter) Emit(code string, fields map[string]any) {
	f.codes = append(f.codes, code)
	f.fields = append(f.fields, fields)
}

func (f *fakeEmitter) count(code string) int {
	n := 0
	for _, c := range f.codes {
		if c == code {
			n++
		}
	}
	return n
}

func detectorFor(em Emitter) *Detector {
	return &Detector{
		Entries:   Catalogue,
		AutoSetup: func() bool { return true },
		Params: func() (tools.SetupParams, error) {
			return tools.SetupParams{Endpoint: "http://127.0.0.1:14318", IngestToken: "local-secret", BinPath: "/usr/local/bin/keld"}, nil
		},
		Emit: em,
	}
}

// AC-3: a tool whose config dir appears after the daemon started is listed
// within one poll, configured with a backup, recorded in the manifest, and
// announced.
func TestDetectorConfiguresAToolThatAppearsAfterTheDaemonStarted(t *testing.T) {
	home := isolate(t)
	em := &fakeEmitter{}
	d := detectorFor(em)

	// First poll: ~/.codex does not exist. Nothing appears, nothing is written.
	if appeared := d.Tick(); len(appeared) != 0 {
		t.Fatalf("first poll reported %v as newly present on an empty machine", appeared)
	}
	if em.count(EventConfigured) != 0 {
		t.Fatal("an event was emitted for a tool that is not installed")
	}

	// The tool is installed.
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(cfg, []byte("model = \"gpt-5\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	appeared := d.Tick()
	if !containsStr(appeared, "codex") {
		t.Fatalf("second poll reported %v, want codex listed within one poll", appeared)
	}

	// The adapter ran: keld's blocks are in the config now.
	body, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The HOOK, not the OTEL block: the tool's own OTLP export is behind the
	// Developer switch and off by default, so a default detector writes the
	// hook and nothing else. Which block each position writes is asserted in
	// toolotlp_detector_test.go; what this test is about is that the adapter
	// ran at all.
	if !strings.Contains(string(body), "__hook --source codex") {
		t.Fatalf("the adapter did not write keld's hook block:\n%s", body)
	}

	// A backup of the pristine config exists.
	backup := filepath.Join(paths.BackupsDir(), "codex", "config.toml")
	pristine, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("no backup at %s: %v", backup, err)
	}
	if string(pristine) != "model = \"gpt-5\"\n" {
		t.Fatalf("the backup is not the pristine config: %q", pristine)
	}

	// The manifest records it, keyed on the ADAPTER name.
	m, err := config.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	tm, ok := m.Tools["codex"]
	if !ok {
		t.Fatalf("manifest records %v, not codex", keysOf(m.Tools))
	}
	if tm.BackupPath == nil || *tm.BackupPath != backup {
		t.Fatalf("manifest backup_path = %v, want %s", tm.BackupPath, backup)
	}

	// Exactly one integration.configured, carrying the source.
	if n := em.count(EventConfigured); n != 1 {
		t.Fatalf("%d %s events, want 1", n, EventConfigured)
	}
	if got := em.fields[0]["source"]; got != "codex" {
		t.Fatalf("event source = %v, want codex", got)
	}

	// A third poll changes nothing: the manifest already records it.
	before := mustRead(t, cfg)
	d.Tick()
	if mustRead(t, cfg) != before {
		t.Fatal("a later poll rewrote a config the manifest already records")
	}
	if n := em.count(EventConfigured); n != 1 {
		t.Fatalf("%d %s events after a repeat poll, want 1", n, EventConfigured)
	}
}

// With the toggle off the row is listed and NOTHING is written — the other
// position of one toggle, not a different code path.
func TestDetectorWritesNothingWhenAutoSetupIsOff(t *testing.T) {
	home := isolate(t)
	em := &fakeEmitter{}
	d := detectorFor(em)
	d.AutoSetup = func() bool { return false }

	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(cfg, []byte("model = \"gpt-5\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if appeared := d.Tick(); !containsStr(appeared, "codex") {
		t.Fatalf("the row must still be LISTED with auto-setup off; got %v", appeared)
	}
	if got := mustRead(t, cfg); got != "model = \"gpt-5\"\n" {
		t.Fatalf("the config was edited with auto-setup off:\n%s", got)
	}
	if _, err := os.Stat(paths.BackupsDir()); err == nil {
		t.Fatal("a backup was written with auto-setup off")
	}
	m, _ := config.LoadManifest()
	if _, ok := m.Tools["codex"]; ok {
		t.Fatal("the manifest was written with auto-setup off")
	}
	if em.count(EventConfigured) != 0 {
		t.Fatal("an event was emitted with auto-setup off")
	}
}

// "The daemon never edits a config while the manifest already records that
// tool" — the invariant stated in the discovery's architecture section.
func TestDetectorNeverTouchesAToolTheManifestAlreadyRecords(t *testing.T) {
	home := isolate(t)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(codexDir, "config.toml")
	body := "model = \"gpt-5\"\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &config.Manifest{Tools: map[string]config.ToolManifest{
		"codex": {Name: "codex", ConfigPath: cfg, Managed: map[string]any{}},
	}}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	em := &fakeEmitter{}
	d := detectorFor(em)
	d.Tick()

	if got := mustRead(t, cfg); got != body {
		t.Fatalf("the detector rewrote a config the manifest already records:\n%s", got)
	}
	if em.count(EventConfigured) != 0 {
		t.Fatal("the detector announced configuring a tool it must not have touched")
	}
}

// Unsupported entries and entries with no adapter (Cowork rides Claude
// Desktop) are LISTED and never applied — there is nothing to apply.
func TestDetectorNeverAppliesToAnEntryWithNoAdapter(t *testing.T) {
	home := isolate(t)
	for _, dir := range []string{".cursor", ".antigravity", filepath.Join(".pi", "agent")} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	em := &fakeEmitter{}
	d := detectorFor(em)
	appeared := d.Tick()
	for _, id := range []string{"cursor", "antigravity", "pi"} {
		if !containsStr(appeared, id) {
			t.Fatalf("%s was not listed; appeared = %v", id, appeared)
		}
	}
	if em.count(EventConfigured) != 0 {
		t.Fatal("the detector configured an entry that has no adapter")
	}
	m, _ := config.LoadManifest()
	if len(m.Tools) != 0 {
		t.Fatalf("the manifest gained %v", keysOf(m.Tools))
	}
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func keysOf(m map[string]config.ToolManifest) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
