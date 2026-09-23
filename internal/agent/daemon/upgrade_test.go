// vocab:keep-file — the upgrade from pre-rename stored names (AC-12).
package daemon

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/agent/workstreams"
	"github.com/ncx-ai/keld-signal/internal/atlas"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// copyHome copies a testdata ~/.keld into a fresh KELD_HOME.
func copyHome(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KELD_HOME", dst)
	return dst
}

type workstreamsPane struct {
	Groups []struct {
		Key, Name, Origin string
		Off               bool
	} `json:"groups"`
	Workstreams []struct {
		ID, Title, Group string
		Hidden           bool
	} `json:"workstreams"`
}

func getWorkstreamsPane(t *testing.T) workstreamsPane {
	t.Helper()
	mux := http.NewServeMux()
	open := func(next http.Handler) http.Handler { return next }
	for _, r := range newV3(settings.Load(), atlas.Off{}).routes() {
		r(mux, open)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/workstreams", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/workstreams = %d: %s", rec.Code, rec.Body)
	}
	var pane workstreamsPane
	if err := json.Unmarshal(rec.Body.Bytes(), &pane); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body)
	}
	return pane
}

// AC-12. A machine set up before the rename — its projects.json written by the
// v1 code (the fixture is main's own Save output) and `workstreams_off` in
// agent-config.json — shows the same groups and workstreams after the upgrade,
// with the same group still switched off, and keeps its old file as a backup.
func TestUpgradeFromPreRenameHome(t *testing.T) {
	home := copyHome(t, "testdata/prerename-home")
	t.Setenv(settings.EnvWorkstreamsFile, "")
	t.Setenv(settings.EnvWorkstreamsFileLegacy, "")

	pane := getWorkstreamsPane(t)

	if len(pane.Groups) != 2 || pane.Groups[0].Key != "development" || pane.Groups[1].Key != "marketing" {
		t.Fatalf("groups after upgrade: %+v", pane.Groups)
	}
	if pane.Groups[0].Off || !pane.Groups[1].Off {
		t.Fatalf("marketing was switched off before the upgrade and must still be: %+v", pane.Groups)
	}
	byID := map[string]string{}
	for _, w := range pane.Workstreams {
		byID[w.ID] = w.Group
	}
	want := map[string]string{
		"p_signal_client":              "development",
		"p_site":                       "marketing",
		"keld_projects:atlas_platform": "development",
	}
	for id, g := range want {
		if byID[id] != g {
			t.Fatalf("workstream %s: group %q, want %q (all: %+v)", id, byID[id], g, pane.Workstreams)
		}
	}

	state := filepath.Join(home, "state")
	if _, err := os.Stat(filepath.Join(state, workstreams.FileName)); err != nil {
		t.Fatalf("the daemon must have written %s: %v", workstreams.FileName, err)
	}
	if _, err := os.Stat(filepath.Join(state, workstreams.LegacyFileName+workstreams.LegacyBackupSuffix)); err != nil {
		t.Fatalf("the old document must be kept as a backup: %v", err)
	}
	if !settings.Load().GroupOff("marketing") {
		t.Fatal("settings must still read marketing as off")
	}
	_ = paths.StateDir()
}

// The real pre-rename document on the machine this was built on: version 1 and
// two nulls. It must upgrade to an honest empty, not an error.
func TestUpgradeFromTheRealEmptyPreRenameDocument(t *testing.T) {
	copyHome(t, "testdata/prerename-home-empty")
	pane := getWorkstreamsPane(t)
	if len(pane.Groups) != 0 || len(pane.Workstreams) != 0 {
		t.Fatalf("an empty legacy document must upgrade to empty: %+v", pane)
	}
}

// KELD_WORKSTREAMS_FILE still points the daemon at a workstream list.
func TestTheLegacyWorkstreamsFileEnvIsStillRead(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "list.json")
	if err := os.WriteFile(p, []byte(`[{"id":"w1","title":"One","description":"d"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(settings.EnvWorkstreamsFile, "")
	t.Setenv(settings.EnvWorkstreamsFileLegacy, p)
	r := resolveWorkstreams(nil)
	if !r.ok || len(r.list) != 1 || r.list[0].ID != "w1" {
		t.Fatalf("resolveWorkstreams via the legacy env = %+v", r)
	}
}
