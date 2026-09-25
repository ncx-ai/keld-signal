// vocab:keep-file — writes and reads 3.0.6's stored names (workstreams, workstream, workstreams_off).
package daemon

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/projects"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/atlas"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// A home 3.0.6 wrote with the Marketing group switched off: two projects in it
// (one keyed by the group's key, one filed under a group whose NAME is what
// was switched off) and one in Development.
const offGroupsDoc = `{
  "version": 1,
  "workstreams": [
    {"key": "development", "name": "Development", "origin": "local"},
    {"key": "marketing", "name": "Marketing", "origin": "local", "off": true},
    {"key": "brand-team", "name": "Brand", "origin": "local"}
  ],
  "projects": [
    {"id": "p_signal", "title": "Signal", "repos": ["ncx-ai/keld-signal"], "workstream": "development", "origin": "user", "atlas_value_id": null},
    {"id": "p_site", "title": "Website", "repos": ["ncx-ai/keld-site"], "workstream": "marketing", "origin": "user", "atlas_value_id": null},
    {"id": "p_logo", "title": "Logo", "repos": ["ncx-ai/keld-logo"], "workstream": "brand-team", "origin": "user", "atlas_value_id": null}
  ]
}
`

const offGroupsConfig = `{
  "ml_backend": "deterministic",
  "workstreams_off": ["marketing", "Brand"]
}
`

// R4-AC-4. Groups leave the product, so the "counts for my work" switch goes
// with them — and a project whose group was switched off must not silently
// start counting again. On daemon start it becomes a HIDDEN project: it still
// attributes nothing, the person can unhide it on the page, and 3.0.6 honours
// hidden too. workstreams_off is left exactly as it was, so a rollback still
// sees those groups off. And the pass is idempotent: a second start writes
// nothing.
func TestAnOffGroupsProjectsBecomeHidden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	t.Setenv(settings.EnvProjectsFile, "")
	docPath := filepath.Join(paths.StateDir(), projects.FileName)
	if err := os.MkdirAll(filepath.Dir(docPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(docPath, []byte(offGroupsDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.AgentConfigPath(), []byte(offGroupsConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	v := newV3(settings.Load(), atlas.Off{})

	d, err := v.projects.Load()
	if err != nil {
		t.Fatal(err)
	}
	hidden := map[string]bool{}
	for _, p := range d.Projects {
		hidden[p.ID] = p.Hidden
	}
	if !hidden["p_site"] {
		t.Fatalf("p_site's group key is in workstreams_off; it must now be hidden: %+v", d.Projects)
	}
	if !hidden["p_logo"] {
		t.Fatalf("p_logo's group NAME is in workstreams_off; it must now be hidden: %+v", d.Projects)
	}
	if hidden["p_signal"] {
		t.Fatalf("p_signal's group is on; it must be unchanged: %+v", d.Projects)
	}

	// Hidden attributes nothing — on the rule pass the page and the ledger use.
	pass, err := ingress.NewAttribution(v.projects)
	if err != nil {
		t.Fatal(err)
	}
	for repo, want := range map[string]bool{
		"github.com/ncx-ai/keld-site":   false,
		"github.com/ncx-ai/keld-logo":   false,
		"github.com/ncx-ai/keld-signal": true,
	} {
		got := pass.Of(map[string]enrich.Labeled{projects.DimRepo: {
			Value: repo, Confidence: 1, Status: enrich.DimensionAttributed}}).Attributed()
		if got != want {
			t.Fatalf("a block on %s attributed = %v, want %v", repo, got, want)
		}
	}

	// The off-list is left for a rollback to read.
	cfg, err := os.ReadFile(paths.AgentConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg) != offGroupsConfig {
		t.Fatalf("agent-config.json must be untouched:\n%s", cfg)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(cfg, &raw); err != nil || raw["workstreams_off"] == nil {
		t.Fatalf("workstreams_off must still be there: %s", cfg)
	}

	// Idempotent: a second start finds nothing to change and does not write.
	before, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(docPath, old, old); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(docPath)
	mtime := st.ModTime()

	newV3(settings.Load(), atlas.Off{})

	after, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	st, _ = os.Stat(docPath)
	if !bytes.Equal(before, after) || !st.ModTime().Equal(mtime) {
		t.Fatalf("a second start must not rewrite projects.json (mtime %v -> %v)", mtime, st.ModTime())
	}
}
