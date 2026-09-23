// vocab:keep-file — the v1 document shape this migration reads.
package workstreams

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// legacyV1 is a pre-rename ~/.keld/state/workstreams.json exactly as the v1 code
// wrote it: `workstreams` held the GROUPS and `projects` the workstreams, each
// naming its group under `workstream`. The same bytes live in
// internal/agent/daemon/testdata/prerename-home, produced by main's own Save.
const legacyV1 = `{
  "version": 1,
  "workstreams": [
    {"key": "development", "name": "Development", "origin": "local"},
    {"key": "marketing", "name": "Marketing", "origin": "local", "off": true}
  ],
  "projects": [
    {"id": "p_signal_client", "title": "Signal client", "repos": ["ncx-ai/keld-signal"],
     "ticket_key": "SIG", "workstream": "development", "origin": "user", "atlas_value_id": null},
    {"id": "p_site", "title": "Website", "repos": ["ncx-ai/keld-site"],
     "workstream": "marketing", "origin": "user", "hidden": true, "atlas_value_id": null}
  ]
}`

func writeLegacy(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, LegacyFileName)
	if err := os.WriteFile(p, []byte(legacyV1), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertMigratedShape(t *testing.T, d Document) {
	t.Helper()
	if len(d.Groups) != 2 || d.Groups[0].Key != "development" || d.Groups[1].Key != "marketing" || !d.Groups[1].Off {
		t.Fatalf("groups not carried over: %+v", d.Groups)
	}
	if len(d.Workstreams) != 2 {
		t.Fatalf("want 2 workstreams, got %+v", d.Workstreams)
	}
	if d.Workstreams[0].ID != "p_signal_client" || d.Workstreams[0].Group != "development" || d.Workstreams[0].TicketKey != "SIG" {
		t.Fatalf("first workstream lost a field: %+v", d.Workstreams[0])
	}
	if d.Workstreams[1].Group != "marketing" || !d.Workstreams[1].Hidden {
		t.Fatalf("second workstream lost a field: %+v", d.Workstreams[1])
	}
}

func TestMigrateLegacyMovesTheDocumentAndKeepsABackup(t *testing.T) {
	dir := t.TempDir()
	legacy := writeLegacy(t, dir)

	migrated, err := MigrateLegacy(dir)
	if err != nil || !migrated {
		t.Fatalf("MigrateLegacy = %v, %v; want true, nil", migrated, err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("the legacy file must be moved aside, stat err = %v", err)
	}
	if _, err := os.Stat(legacy + LegacyBackupSuffix); err != nil {
		t.Fatalf("the legacy file must be KEPT as a backup: %v", err)
	}
	d, err := Load(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != CurrentVersion {
		t.Fatalf("migrated document version = %d, want %d", d.Version, CurrentVersion)
	}
	assertMigratedShape(t, d)
}

func TestMigratedFileIsWrittenInTheNewKeys(t *testing.T) {
	dir := t.TempDir()
	writeLegacy(t, dir)
	if _, err := MigrateLegacy(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["projects"]; ok {
		t.Fatalf("the new document must not carry the retired `projects` key: %s", b)
	}
	var items []map[string]any
	if err := json.Unmarshal(raw["workstreams"], &items); err != nil || len(items) != 2 {
		t.Fatalf("`workstreams` must hold the two workstreams: %v %s", err, raw["workstreams"])
	}
	if items[0]["group"] != "development" {
		t.Fatalf("a workstream names its group under `group`: %v", items[0])
	}
	if _, ok := items[0]["workstream"]; ok {
		t.Fatalf("the retired per-item `workstream` key must not be written: %v", items[0])
	}
}

func TestMigrateLegacyIsANoOpOnceTheNewFileExists(t *testing.T) {
	dir := t.TempDir()
	legacy := writeLegacy(t, dir)
	if err := Save(filepath.Join(dir, FileName), Document{Version: CurrentVersion}); err != nil {
		t.Fatal(err)
	}
	migrated, err := MigrateLegacy(dir)
	if err != nil || migrated {
		t.Fatalf("MigrateLegacy = %v, %v; want false, nil", migrated, err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("an unmigrated legacy file beside a new one must be left alone: %v", err)
	}
}

func TestMigrateLegacyWithNothingToMigrate(t *testing.T) {
	migrated, err := MigrateLegacy(t.TempDir())
	if err != nil || migrated {
		t.Fatalf("MigrateLegacy on an empty dir = %v, %v; want false, nil", migrated, err)
	}
}

// Load of the new path falls back to reading the legacy file, read-only, so a
// reader that runs before the daemon's startup migration still sees the
// person's setup rather than an empty one.
func TestLoadFallsBackToTheLegacyFileWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	legacy := writeLegacy(t, dir)
	d, err := Load(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	assertMigratedShape(t, d)
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("a read must never move the legacy file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Fatalf("a read must never write the new file, stat err = %v", err)
	}
}

func TestDefaultPathIsTheNewFileName(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if filepath.Base(DefaultPath()) != FileName || FileName != "workstreams.json" {
		t.Fatalf("DefaultPath = %s", DefaultPath())
	}
}
