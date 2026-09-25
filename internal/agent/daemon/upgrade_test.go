// vocab:keep-file — 3.0.6's stored names are read and written in place (R3-AC-3).
package daemon

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
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

type projectsPane struct {
	// Groups is decoded only to prove it is ABSENT (R4-AC-2): Signal has no
	// groups since Revision 4.
	Groups   json.RawMessage `json:"groups"`
	Projects []struct {
		ID, Title string
		Hidden    bool
	} `json:"projects"`
}

func getProjectsPane(t *testing.T) projectsPane {
	t.Helper()
	mux := http.NewServeMux()
	open := func(next http.Handler) http.Handler { return next }
	for _, r := range newV3(settings.Load(), atlas.Off{}).routes() {
		r(mux, open)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/projects", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/projects = %d: %s", rec.Code, rec.Body)
	}
	var pane projectsPane
	if err := json.Unmarshal(rec.Body.Bytes(), &pane); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body)
	}
	return pane
}

func serveProjects(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	open := func(next http.Handler) http.Handler { return next }
	for _, r := range newV3(settings.Load(), atlas.Off{}).routes() {
		r(mux, open)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	return rec
}

// R3-AC-3 + R4-AC-5. A home 3.0.6 wrote (the fixture is main's own Save
// output, with `workstreams_off` in agent-config.json) is read IN PLACE, and an
// edit from the page is written back in the shape 3.0.6 reads — no
// workstreams.json, no .pre-rename, no groups_off. ⚠️ This is what makes an
// auto-update rollback to 3.0.6 safe: that build finds exactly the file it
// wrote, with the edit in it.
//
// ⚠️ **AND EVERY VISIBLE PROJECT STILL RENDERS THERE (Revision 4).** Signal
// has no groups any more, but 3.0.6 draws a project only under a group the
// file declares. So after the edits — a rule added, and a brand-new project
// created, which has no group at all — every visible project's `workstream`
// must name a group in the file's `workstreams`, and the person's own groups
// must still be there. The group-off edit this test used to make is gone with
// its route.
func TestA306HomeIsReadAndWrittenInPlace(t *testing.T) {
	home := copyHome(t, "testdata/home-306")
	t.Setenv(settings.EnvProjectsFile, "")

	pane := getProjectsPane(t)
	if pane.Groups != nil {
		t.Fatalf("the catalog must carry no groups since Revision 4: %s", pane.Groups)
	}
	hidden := map[string]bool{}
	for _, w := range pane.Projects {
		hidden[w.ID] = w.Hidden
	}
	for id, want := range map[string]bool{
		"p_signal_client":              false,
		"p_site":                       true,
		"keld_projects:atlas_platform": false,
	} {
		h, ok := hidden[id]
		if !ok || h != want {
			t.Fatalf("project %s: present=%v hidden=%v, want hidden=%v (all: %+v)", id, ok, h, want, pane.Projects)
		}
	}

	// Two edits from the page: a rule added to a project, and a new project.
	if rec := serveProjects(t, http.MethodPost, "/v1/projects/p_signal_client/rules",
		`{"add":[{"kind":"repo","value":"github.com/ncx-ai/keld-docs"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("add rule = %d: %s", rec.Code, rec.Body)
	}
	rec := serveProjects(t, http.MethodPost, "/v1/projects/bundle", `{"title":"Docs site","suggestions":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("bundle = %d: %s", rec.Code, rec.Body)
	}
	var created struct {
		Project map[string]any `json:"project"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	newID, _ := created.Project["id"].(string)
	if newID == "" {
		t.Fatalf("bundle created no project: %s", rec.Body)
	}
	if _, ok := created.Project["group"]; ok {
		t.Fatalf("the bundle response must not name a group: %s", rec.Body)
	}

	state := filepath.Join(home, "state")
	entries, _ := os.ReadDir(state)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	for _, n := range names {
		if n == "workstreams.json" || strings.HasSuffix(n, ".pre-rename") {
			t.Fatalf("a 3.0.6 home must be written in place, found %s in %v", n, names)
		}
	}

	// Decoded with 3.0.6's OWN shape, spelled out here rather than borrowed from
	// the code under test: groups under `workstreams`, each project's group
	// under `workstream`.
	var v306 struct {
		Version     int `json:"version"`
		Workstreams []struct {
			Key string `json:"key"`
		} `json:"workstreams"`
		Projects []struct {
			ID         string   `json:"id"`
			Repos      []string `json:"repos"`
			Workstream string   `json:"workstream"`
			Hidden     bool     `json:"hidden"`
		} `json:"projects"`
	}
	b, err := os.ReadFile(filepath.Join(state, "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &v306); err != nil {
		t.Fatal(err)
	}
	if v306.Version != 1 || len(v306.Workstreams) != 2 ||
		v306.Workstreams[0].Key != "development" || v306.Workstreams[1].Key != "marketing" {
		t.Fatalf("3.0.6 must read version 1 with the person's own groups under `workstreams`: %s", b)
	}
	declared := map[string]bool{}
	for _, g := range v306.Workstreams {
		declared[g.Key] = true
	}
	seen := map[string]bool{}
	for _, p := range v306.Projects {
		seen[p.ID] = true
		if !p.Hidden && !declared[p.Workstream] {
			t.Fatalf("visible project %s is under %q, which the file does not declare — 3.0.6 would not render it: %s",
				p.ID, p.Workstream, b)
		}
		if p.ID == "p_signal_client" {
			if p.Workstream != "development" {
				t.Fatalf("p_signal_client lost its group under `workstream`: %+v", p)
			}
			if !contains(p.Repos, "github.com/ncx-ai/keld-docs") {
				t.Fatalf("the page's edit must be in the file 3.0.6 reads: %+v", p)
			}
		}
	}
	if !seen[newID] || !seen["p_signal_client"] {
		t.Fatalf("projects missing from the file 3.0.6 reads (want %s and p_signal_client): %s", newID, b)
	}

	cfg, err := os.ReadFile(paths.AgentConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(cfg, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["groups_off"]; ok {
		t.Fatalf("switched-off groups must be stored under 3.0.6's workstreams_off only: %s", cfg)
	}
	var off []string
	_ = json.Unmarshal(raw["workstreams_off"], &off)
	if len(off) != 1 || off[0] != "marketing" {
		t.Fatalf("workstreams_off = %v, want 3.0.6's [marketing] left as it was", off)
	}
}

// A document that declares NO group — the state of a machine where nobody
// had made one — gains exactly one internal group on save, and a new project
// is filed under it, so a rollback to 3.0.6 still draws it.
func TestANewProjectInAGrouplessHomeIsFiledUnderOneInternalGroup(t *testing.T) {
	home := copyHome(t, "testdata/home-306-empty")
	t.Setenv(settings.EnvProjectsFile, "")
	if rec := serveProjects(t, http.MethodPost, "/v1/projects/bundle", `{"title":"Docs site"}`); rec.Code != http.StatusOK {
		t.Fatalf("bundle = %d: %s", rec.Code, rec.Body)
	}
	b, err := os.ReadFile(filepath.Join(home, "state", "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v306 struct {
		Workstreams []struct{ Key, Name, Origin string } `json:"workstreams"`
		Projects    []struct {
			Workstream string `json:"workstream"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(b, &v306); err != nil {
		t.Fatal(err)
	}
	if len(v306.Workstreams) != 1 || v306.Workstreams[0].Key != "projects" ||
		v306.Workstreams[0].Name != "Projects" || v306.Workstreams[0].Origin != "local" {
		t.Fatalf("want one internal group {projects, Projects, local}: %s", b)
	}
	if len(v306.Projects) != 1 || v306.Projects[0].Workstream != "projects" {
		t.Fatalf("the new project must be filed under the internal group: %s", b)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// The real 3.0.6 document on the machine this was built on: version 1 and two
// nulls. It must read as an honest empty, not an error.
func TestTheReal306EmptyDocumentReadsAsEmpty(t *testing.T) {
	copyHome(t, "testdata/home-306-empty")
	pane := getProjectsPane(t)
	if pane.Groups != nil || len(pane.Projects) != 0 {
		t.Fatalf("an empty 3.0.6 document must read as empty: %+v", pane)
	}
}

// KELD_PROJECTS_FILE points the daemon at a project list.
func TestTheProjectsFileEnvIsRead(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "list.json")
	if err := os.WriteFile(p, []byte(`[{"id":"w1","title":"One","description":"d"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(settings.EnvProjectsFile, p)
	r := resolveProjects(nil)
	if !r.ok || len(r.list) != 1 || r.list[0].ID != "w1" {
		t.Fatalf("resolveProjects via KELD_PROJECTS_FILE = %+v", r)
	}
}
