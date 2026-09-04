package projects

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

func TestHideExcludesFromMatchingButKeepsRules(t *testing.T) {
	doc := Document{Projects: []Project{{ID: "p1", Title: "One", Repos: []string{repoKeldSignal}}}}
	doc, err := Hide(doc, "p1", true)
	if err != nil {
		t.Fatalf("Hide: %v", err)
	}
	if !doc.Projects[0].Hidden {
		t.Fatalf("Hidden not set: %+v", doc.Projects[0])
	}
	if len(doc.Projects[0].Repos) != 1 {
		t.Fatalf("rules dropped by Hide: %+v", doc.Projects[0])
	}

	dims := dimsWith(map[string]string{DimRepo: repoKeldSignal})
	res := Attribute(dims, doc.Projects, nil, nil)
	if res.ProjectID != "" {
		t.Fatalf("hidden project still attributed: %+v", res)
	}
}

func TestEditsOnUnknownProjectFail(t *testing.T) {
	doc := Document{}
	if _, err := AddRules(doc, "missing", []Rule{{Kind: RuleKindRepo, Value: repoKeldSignal}}); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("AddRules on missing project: err = %v, want ErrProjectNotFound", err)
	}
	if _, err := RemoveRules(doc, "missing", []Rule{{Kind: RuleKindRepo, Value: repoKeldSignal}}); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("RemoveRules on missing project: err = %v, want ErrProjectNotFound", err)
	}
	if _, err := Hide(doc, "missing", true); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("Hide on missing project: err = %v, want ErrProjectNotFound", err)
	}
}

func TestBundleUnknownSuggestionFails(t *testing.T) {
	doc := Document{}
	if _, _, err := Bundle(doc, "Title", "development", []string{"nope"}, nil); !errors.Is(err, ErrUnknownSuggestion) {
		t.Fatalf("Bundle with unknown suggestion: err = %v, want ErrUnknownSuggestion", err)
	}
}

func TestPlaceSameAsAddsRuleAndReattributes(t *testing.T) {
	doc := Document{Projects: []Project{{ID: "p1", Title: "One"}}}
	suggestions := []Suggestion{{ID: "sug1", Kind: SuggestKindRepo, Value: normalizeRepo(repoKeldSignal)}}

	doc, err := PlaceSameAs(doc, "sug1", "p1", suggestions, nil)
	if err != nil {
		t.Fatalf("PlaceSameAs: %v", err)
	}
	if len(doc.Projects[0].Repos) != 1 || doc.Projects[0].Repos[0] != normalizeRepo(repoKeldSignal) {
		t.Fatalf("repo rule not added: %+v", doc.Projects[0])
	}

	dims := dimsWith(map[string]string{DimRepo: repoKeldSignal})
	res := Attribute(dims, doc.Projects, nil, nil)
	if res.Method != MethodRepo || res.ProjectID != "p1" {
		t.Fatalf("block did not re-attribute after PlaceSameAs: %+v", res)
	}
}

func TestSetWorkstreamOffTogglesAndSurvivesOtherSettings(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	// A pre-existing, unrelated setting must survive the round trip (merge,
	// not overwrite).
	before := settings.Load()
	before.Attribution = true
	// Seed agent-config.json directly (Settings' fields are exported and
	// match the file's JSON shape 1:1) rather than through SetWorkstreamOff
	// itself, which would make this test circular.
	b, err := json.MarshalIndent(before, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.AgentConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.AgentConfigPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SetWorkstreamOff("marketing", true); err != nil {
		t.Fatalf("SetWorkstreamOff(on): %v", err)
	}
	s := settings.Load()
	if !s.WorkstreamOff("marketing") {
		t.Fatalf("marketing not off after SetWorkstreamOff(true): %+v", s.WorkstreamsOff)
	}
	if !s.Attribution {
		t.Fatalf("unrelated setting (Attribution) was lost by SetWorkstreamOff")
	}

	if err := SetWorkstreamOff("marketing", false); err != nil {
		t.Fatalf("SetWorkstreamOff(off): %v", err)
	}
	s = settings.Load()
	if s.WorkstreamOff("marketing") {
		t.Fatalf("marketing still off after SetWorkstreamOff(false): %+v", s.WorkstreamsOff)
	}
}
