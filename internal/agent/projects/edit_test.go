package projects

import (
	"errors"
	"testing"
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
	res := Attribute(dims, doc.Projects, nil)
	if only(res).ProjectID != "" {
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
	if _, _, err := Bundle(doc, "Title", []string{"nope"}, nil); !errors.Is(err, ErrUnknownSuggestion) {
		t.Fatalf("Bundle with unknown suggestion: err = %v, want ErrUnknownSuggestion", err)
	}
}

func TestPlaceSameAsAddsRuleAndReattributes(t *testing.T) {
	doc := Document{Projects: []Project{{ID: "p1", Title: "One"}}}
	suggestions := []Suggestion{{ID: "sug1", Kind: SuggestKindRepo, Value: normalizeRepo(repoKeldSignal)}}

	doc, err := PlaceSameAs(doc, "sug1", "p1", suggestions)
	if err != nil {
		t.Fatalf("PlaceSameAs: %v", err)
	}
	if len(doc.Projects[0].Repos) != 1 || doc.Projects[0].Repos[0] != normalizeRepo(repoKeldSignal) {
		t.Fatalf("repo rule not added: %+v", doc.Projects[0])
	}

	dims := dimsWith(map[string]string{DimRepo: repoKeldSignal})
	res := Attribute(dims, doc.Projects, nil)
	if only(res).Method != MethodRepo || only(res).ProjectID != "p1" {
		t.Fatalf("block did not re-attribute after PlaceSameAs: %+v", res)
	}
}
