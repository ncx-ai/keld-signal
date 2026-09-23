package workstreams

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

func TestLoadMissingFileIsAnEmptyDocumentNotAnError(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	d, err := Load(DefaultPath())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if d.Version != CurrentVersion {
		t.Fatalf("version = %d, want %d", d.Version, CurrentVersion)
	}
	if len(d.Workstreams) != 0 || len(d.Groups) != 0 {
		t.Fatalf("fresh document not empty: %+v", d)
	}
}

func TestLoadMalformedFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KELD_HOME", dir)
	path := DefaultPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load: want an error for malformed JSON, got nil")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	path := DefaultPath()
	atlasID := "atlas_123"
	doc := Document{
		Version: CurrentVersion,
		Groups: []Group{
			{Key: "development", Name: "Development", Origin: GroupOriginLocal},
		},
		Workstreams: []Workstream{
			{
				ID: "p_signal", Title: "Keld Signal", Repos: []string{repoKeldSignal},
				Group: "development", Origin: OriginUser, AtlasValueID: &atlasID,
			},
		},
	}
	if err := Save(path, doc); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %v, want 0600", perm)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Workstreams) != 1 || got.Workstreams[0].ID != "p_signal" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.Workstreams[0].AtlasValueID == nil || *got.Workstreams[0].AtlasValueID != atlasID {
		t.Fatalf("atlas_value_id lost in round trip: %+v", got.Workstreams[0])
	}
}

func TestStoreUpdateIsAtomicPerCall(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	s := NewStore(DefaultPath())

	if _, err := s.Update(func(d Document) (Document, error) {
		d.Workstreams = append(d.Workstreams, Workstream{ID: "p1", Title: "One"})
		return d, nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	d, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(d.Workstreams) != 1 || d.Workstreams[0].ID != "p1" {
		t.Fatalf("store contents = %+v", d)
	}
}

// T25: the saved file is byte-compatible with KELD_PROJECTS_FILE — a plain
// JSON array of Document.Projects, extended with v3-only fields the older
// loader ignores, decodes cleanly through settings.LoadWorkstreamsFile and every
// shared field survives.
func TestWorkstreamsFileByteCompatibleWithKeldWorkstreamsFile(t *testing.T) {
	atlasID := "atlas_999"
	doc := Document{
		Version: CurrentVersion,
		Workstreams: []Workstream{
			{
				ID:          "p_sdk_work",
				Title:       "SDK work",
				Description: "SDK and telemetry client repos",
				Team:        "Platform",
				Repos:       []string{repoSDKTestbench, repoAtlasTSTel, repoAtlasPyTel},
				Keywords:    []string{"sdk"},
				TicketKey:   "SDK",
				// v3-only fields the settings loader must ignore, not choke on.
				Group:        "development",
				Origin:       OriginUser,
				Hidden:       false,
				AtlasValueID: &atlasID,
			},
		},
	}

	b, err := json.Marshal(doc.Workstreams)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "keld-projects.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := settings.LoadWorkstreamsFile(path)
	if err != nil {
		t.Fatalf("settings.LoadWorkstreamsFile: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("loaded %d projects, want 1", len(got))
	}
	want := doc.Workstreams[0]
	rp := got[0]
	if rp.ID != want.ID || rp.Title != want.Title || rp.Description != want.Description ||
		rp.Team != want.Team || rp.TicketKey != want.TicketKey {
		t.Fatalf("scalar fields did not survive: got %+v, want fields from %+v", rp, want)
	}
	if len(rp.Repos) != len(want.Repos) {
		t.Fatalf("repos = %+v, want %+v", rp.Repos, want.Repos)
	}
	for i := range want.Repos {
		if rp.Repos[i] != want.Repos[i] {
			t.Fatalf("repos[%d] = %q, want %q", i, rp.Repos[i], want.Repos[i])
		}
	}
	if len(rp.Keywords) != 1 || rp.Keywords[0] != "sdk" {
		t.Fatalf("keywords = %+v, want [sdk]", rp.Keywords)
	}
}
