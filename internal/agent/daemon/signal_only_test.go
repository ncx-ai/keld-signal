package daemon

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/projects"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/atlas"
)

// SIGNAL LABELS ON ITS OWN (Revision 2, 2026-09-25).
//
// Signal attributes blocks only to the projects defined in Signal — the
// local projects.json. The org's Atlas workstreams still arrive on the
// settings poll and are still HELD, but nothing on the recorded rule pass or
// the `project_matches` path reads them, and a poll no longer trims the
// person's own rules. Every test below builds through newV3 so it exercises
// the shipped wiring, not a hand-assembled v3.

const (
	repoAtlasOnly = "github.com/acme/atlas-only"
	repoSignal    = "github.com/acme/signal-only"
	atlasWSID     = "keld_projects:atlas_only"
)

// enabledAtlas is the Off connector reporting itself live, so newV3 wires the
// RemoteProjects getter exactly as it does on a paired machine. No method
// here dials anything.
type enabledAtlas struct{ atlas.Off }

func (enabledAtlas) Enabled() bool { return true }

func orgRemote(repos ...string) *settings.Remote {
	list := []settings.RemoteProject{}
	for i, r := range repos {
		id := atlasWSID
		if i > 0 {
			id = atlasWSID + "_" + string(rune('a'+i))
		}
		list = append(list, settings.RemoteProject{
			ID: id, Title: "Atlas " + id, Team: "Org Projects", Repos: []string{r},
		})
	}
	return &settings.Remote{Projects: &list}
}

// signalOnlyFixture is a paired v3 under an isolated KELD_HOME holding one
// Signal project on repoSignal, with the org's list naming repoAtlasOnly
// installed in the remote state the getter reads.
func signalOnlyFixture(t *testing.T) *v3 {
	t.Helper()
	t.Setenv("KELD_HOME", t.TempDir())
	v := newV3(settings.Settings{}, enabledAtlas{})
	if v.projects.RemoteProjects == nil {
		t.Fatal("test setup: a paired machine must wire the RemoteProjects getter")
	}
	declareProject(t, v, "p_signal", "Signal work", repoSignal)
	v.remote.Store(orgRemote(repoAtlasOnly))
	if got := v.projects.RemoteProjects(); len(got) != 1 || got[0].ID != atlasWSID {
		t.Fatalf("test setup: the getter must return the org's list, got %+v", got)
	}
	return v
}

func storedAttributed(t *testing.T, v *v3, k ledger.BlockKey) map[string]any {
	t.Helper()
	snap, err := v.ledger.Read(time.Time{}, 50)
	if err != nil {
		t.Fatalf("ledger Read: %v", err)
	}
	for _, b := range snap.Blocks {
		if b.Key.Session == k.Session && b.Key.Start == k.Start {
			return b.Cells["attributed"]
		}
	}
	t.Fatalf("block %+v not in the ledger", k)
	return nil
}

func blockOn(repo string) enrich.BlockCharacterisation {
	return enrich.BlockCharacterisation{Analysis: enrich.WindowAnalysis{Dimensions: map[string]enrich.Labeled{
		projects.DimRepo: {Value: repo, Confidence: 1, Status: enrich.DimensionAttributed},
	}}}
}

// assertRecordedPassIsSignalOnly is R2-AC-1's observable, shared with R2-AC-4.
func assertRecordedPassIsSignalOnly(t *testing.T, v *v3) {
	t.Helper()
	onAtlas := cutBlock(t, v, "sess-atlas", 90, repoAtlasOnly)
	onSignal := cutBlock(t, v, "sess-signal", 60, repoSignal)

	a := storedAttributed(t, v, onAtlas)
	if a["reason"] != string(ledger.ReasonNoRuleMatched) || joinWS(a) != "" {
		t.Fatalf("block on the Atlas-only repo recorded %#v, want no_rule_matched with no project", a)
	}
	s := storedAttributed(t, v, onSignal)
	if joinWS(s) != "p_signal" {
		t.Fatalf("block on the Signal repo recorded %#v, want p_signal", s)
	}
}

// assertMatchesAreSignalOnly is R2-AC-7's observable, shared with R2-AC-4.
func assertMatchesAreSignalOnly(t *testing.T) {
	t.Helper()
	if got := projectMatchesFor(blockOn(repoAtlasOnly)); len(got) != 0 {
		t.Fatalf("project_matches for the Atlas-only repo = %+v, want none", got)
	}
	got := projectMatchesFor(blockOn(repoSignal))
	if len(got) != 1 {
		t.Fatalf("project_matches for the Signal repo = %+v, want exactly one", got)
	}
	if got[0].ID != "" {
		t.Fatalf("a Signal project's id is title-derived and must never ride the wire, got %q", got[0].ID)
	}
	if len(got[0].Repos) != 1 || got[0].Repos[0] != repoSignal {
		t.Fatalf("match repos = %v, want [%s]", got[0].Repos, repoSignal)
	}
}

// R2-AC-1.
func TestOnlySignalProjectsAttribute(t *testing.T) {
	v := signalOnlyFixture(t)
	assertRecordedPassIsSignalOnly(t, v)
}

// R2-AC-7.
func TestProjectMatchesAreSignalOnly(t *testing.T) {
	v := signalOnlyFixture(t)
	assertMatchesAreSignalOnly(t)

	// An overlay "Same as" made before this revision is a LOCAL entry carrying
	// an Atlas id; it attributes as a Signal project and its id still rides
	// the wire, because it is the org's own identifier, not a title slug.
	const overlayRepo = "github.com/acme/overlay"
	if _, err := v.projects.Update(func(d projects.Document) (projects.Document, error) {
		d.Projects = append(d.Projects, projects.Project{
			ID: "keld_projects:overlay", Title: "Overlay", Origin: projects.OriginAtlas,
			Group: "org_projects", Repos: []string{overlayRepo},
		})
		return d, nil
	}); err != nil {
		t.Fatalf("add overlay: %v", err)
	}
	got := projectMatchesFor(blockOn(overlayRepo))
	if len(got) != 1 || got[0].ID != "keld_projects:overlay" || got[0].Origin != projects.OriginAtlas {
		t.Fatalf("overlay match = %+v, want one match keeping its Atlas id", got)
	}
}

// R2-AC-5.
func TestAPollNoLongerTrimsSignalRules(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	v := newV3(settings.Settings{}, enabledAtlas{})
	// A Signal project on repo A — and the org declares repo A too. Under
	// Revision 1 the poll deleted this entry (the org "covered" every rule).
	declareProject(t, v, "p_signal", "Signal work", repoAtlasOnly)
	before, err := os.ReadFile(projects.DefaultPath())
	if err != nil {
		t.Fatalf("read document: %v", err)
	}

	v.observeRemote(orgRemote(repoAtlasOnly))
	v.observeRemote(orgRemote(repoAtlasOnly))

	after, err := os.ReadFile(projects.DefaultPath())
	if err != nil {
		t.Fatalf("read document after poll: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("a settings poll rewrote the person's own rules:\nbefore %s\nafter  %s", before, after)
	}
}

// R2-AC-4.
func TestAtlasWorkstreamsAreHeldNotUsed(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	v := newV3(settings.Settings{}, enabledAtlas{})
	declareProject(t, v, "p_signal", "Signal work", repoSignal)

	v.observeRemote(orgRemote(repoAtlasOnly, "github.com/acme/second"))

	held := v.projects.RemoteProjects()
	if len(held) != 2 || held[0].ID != atlasWSID || held[0].Repos[0] != repoAtlasOnly {
		t.Fatalf("the org's list must still be received and held, got %+v", held)
	}
	assertRecordedPassIsSignalOnly(t, v)
	assertMatchesAreSignalOnly(t)
}
