package projects

import (
	"sort"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// attributed builds an enrich.Labeled the way a real block's workstreams map
// carries a dimension that reached the attribution floor.
func attributed(value string) enrich.Labeled {
	return enrich.Labeled{Value: value, Confidence: 1, Status: enrich.WorkstreamAttributed}
}

func dimsWith(pairs map[string]string) map[string]enrich.Labeled {
	out := make(map[string]enrich.Labeled, len(pairs))
	for k, v := range pairs {
		out[k] = attributed(v)
	}
	return out
}

// Real org repos, per the task brief.
const (
	repoKeldSignal   = "github.com/ncx-ai/keld-signal"
	repoKeldAtlas    = "github.com/ncx-ai/keld-atlas"
	repoSDKTestbench = "github.com/ncx-ai/sdk-testbench"
	repoAtlasTSTel   = "github.com/ncx-ai/atlas-telemetry-typescript"
	repoAtlasPyTel   = "github.com/ncx-ai/atlas-telemetry-python"
)

// T17: a repo rule attributes a block, before and without any encoder at
// all — Attribute is called with a nil Vector throughout this file — and the
// method it reports is "repo".
func TestRepoRuleAttributesWithoutAnyEncoder(t *testing.T) {
	p := Project{ID: "p_signal", Title: "Keld Signal", Repos: []string{repoKeldSignal}, Workstream: "development"}
	dims := dimsWith(map[string]string{DimRepo: repoKeldSignal})

	res := Attribute(dims, []Project{p}, nil, nil)

	if res.Method != MethodRepo {
		t.Fatalf("method = %q, want %q", res.Method, MethodRepo)
	}
	if res.ProjectID != "p_signal" {
		t.Fatalf("project id = %q, want p_signal", res.ProjectID)
	}
	if res.Reason != ReasonNone {
		t.Fatalf("reason = %q, want none", res.Reason)
	}
}

// T18 + T19 (bundle/re-attribute/suggestions-gone) + T20 (remove rule ->
// suggestion with the id it had): one end-to-end flow over the fixed profile
// of 5 org repos.
func TestSuggestBundleRemoveRoundTrip(t *testing.T) {
	// --- T18: four blocks on an unclaimed repo, vector off -> all
	// unattributed and ONE suggestion with summed count/minutes/tokens and a
	// stable id.
	unclaimed := repoKeldAtlas
	var blocks []UnattributedBlock
	for i := 0; i < 4; i++ {
		dims := dimsWith(map[string]string{DimRepo: unclaimed})
		res := Attribute(dims, nil, nil, nil)
		if res.ProjectID != "" || res.Reason != ReasonNoRuleMatched {
			t.Fatalf("block %d: got %+v, want unattributed/no_rule_matched", i, res)
		}
		blocks = append(blocks, UnattributedBlock{Dims: dims, Minutes: 10, Tokens: 1000})
	}
	suggestions := Suggest(blocks)
	if len(suggestions) != 1 {
		t.Fatalf("suggestions = %d, want 1: %+v", len(suggestions), suggestions)
	}
	sug := suggestions[0]
	wantID := SuggestionID(SuggestKindRepo, normalizeRepo(unclaimed))
	if sug.ID != wantID {
		t.Fatalf("suggestion id = %q, want %q (must be stable)", sug.ID, wantID)
	}
	if sug.Kind != SuggestKindRepo || sug.Value != normalizeRepo(unclaimed) {
		t.Fatalf("suggestion = %+v", sug)
	}
	if sug.Blocks != 4 || sug.Minutes != 40 || sug.Tokens != 4000 {
		t.Fatalf("suggestion totals = %+v, want blocks=4 minutes=40 tokens=4000", sug)
	}

	// --- T19: bundle three SDK-work repos into one project. Compute their
	// own suggestions first (independently of the unclaimed one above).
	sdkRepos := []string{repoSDKTestbench, repoAtlasTSTel, repoAtlasPyTel}
	var sdkBlocks []UnattributedBlock
	sdkDims := map[string]map[string]enrich.Labeled{}
	for _, r := range sdkRepos {
		d := dimsWith(map[string]string{DimRepo: r})
		sdkDims[r] = d
		sdkBlocks = append(sdkBlocks, UnattributedBlock{Dims: d, Minutes: 5, Tokens: 500})
	}
	sdkSuggestions := Suggest(sdkBlocks)
	if len(sdkSuggestions) != 3 {
		t.Fatalf("sdk suggestions = %d, want 3: %+v", len(sdkSuggestions), sdkSuggestions)
	}
	ids := make([]string, len(sdkSuggestions))
	for i, s := range sdkSuggestions {
		ids[i] = s.ID
	}

	doc := Document{Version: CurrentVersion}
	doc, created, err := Bundle(doc, "SDK work", "development", ids, sdkSuggestions)
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if len(created.Repos) != 3 {
		t.Fatalf("created project repos = %+v, want 3", created.Repos)
	}
	if len(doc.Projects) != 1 {
		t.Fatalf("document has %d projects, want 1", len(doc.Projects))
	}

	// All three repos' blocks now re-attribute to the new project.
	for _, r := range sdkRepos {
		res := Attribute(sdkDims[r], doc.Projects, nil, nil)
		if res.Method != MethodRepo || res.ProjectID != created.ID {
			t.Fatalf("repo %s: got %+v after bundle, want method repo / project %s", r, res, created.ID)
		}
	}

	// Suggestions for those three blocks are gone (all now attributed).
	var stillUnattributed []UnattributedBlock
	for _, r := range sdkRepos {
		res := Attribute(sdkDims[r], doc.Projects, nil, nil)
		if res.ProjectID == "" {
			stillUnattributed = append(stillUnattributed, UnattributedBlock{Dims: sdkDims[r]})
		}
	}
	if got := Suggest(stillUnattributed); len(got) != 0 {
		t.Fatalf("suggestions after bundle = %+v, want none", got)
	}

	// --- T20: remove one rule -> that repo's blocks return to suggestions
	// WITH THE ID THEY HAD before bundling.
	var removedRepoOriginalID string
	for _, s := range sdkSuggestions {
		if s.Value == normalizeRepo(repoSDKTestbench) {
			removedRepoOriginalID = s.ID
		}
	}
	if removedRepoOriginalID == "" {
		t.Fatalf("could not find original suggestion id for %s", repoSDKTestbench)
	}

	doc, err = RemoveRules(doc, created.ID, []Rule{{Kind: RuleKindRepo, Value: repoSDKTestbench}})
	if err != nil {
		t.Fatalf("RemoveRules: %v", err)
	}

	res := Attribute(sdkDims[repoSDKTestbench], doc.Projects, nil, nil)
	if res.ProjectID != "" {
		t.Fatalf("sdk-testbench still attributed after RemoveRules: %+v", res)
	}
	back := Suggest([]UnattributedBlock{{Dims: sdkDims[repoSDKTestbench], Minutes: 5, Tokens: 500}})
	if len(back) != 1 {
		t.Fatalf("suggestions after remove = %+v, want 1", back)
	}
	if back[0].ID != removedRepoOriginalID {
		t.Fatalf("suggestion id after remove = %q, want the ORIGINAL id %q", back[0].ID, removedRepoOriginalID)
	}

	// The other two repos are unaffected.
	for _, r := range []string{repoAtlasTSTel, repoAtlasPyTel} {
		res := Attribute(sdkDims[r], doc.Projects, nil, nil)
		if res.Method != MethodRepo || res.ProjectID != created.ID {
			t.Fatalf("repo %s: got %+v after removing a DIFFERENT rule, want it unaffected", r, res)
		}
	}
}

// T21: two projects claiming the same repo -> conflict naming BOTH, never
// silently the first.
func TestTwoProjectsClaimingOneRepoConflicts(t *testing.T) {
	a := Project{ID: "p_a", Title: "A", Repos: []string{repoKeldAtlas}}
	b := Project{ID: "p_b", Title: "B", Repos: []string{repoKeldAtlas}}
	dims := dimsWith(map[string]string{DimRepo: repoKeldAtlas})

	res := Attribute(dims, []Project{a, b}, nil, nil)

	if res.Reason != ReasonConflict {
		t.Fatalf("reason = %q, want conflict: %+v", res.Reason, res)
	}
	if res.ProjectID != "" {
		t.Fatalf("a conflict must not silently name a project, got %q", res.ProjectID)
	}
	want := []string{"p_a", "p_b"}
	sort.Strings(want)
	if len(res.Conflict) != 2 || res.Conflict[0] != want[0] || res.Conflict[1] != want[1] {
		t.Fatalf("conflict list = %+v, want both ids %+v", res.Conflict, want)
	}

	// Order of candidates must not matter — "never the first" means never
	// picking b's id when a is passed first either.
	res2 := Attribute(dims, []Project{b, a}, nil, nil)
	if res2.Reason != ReasonConflict || len(res2.Conflict) != 2 {
		t.Fatalf("conflict is order-dependent: %+v", res2)
	}
}

// T22: a workstream switched off excludes its projects from matching AND
// from the "place same as" candidate list.
func TestWorkstreamOffExcludesFromMatchingAndSameAs(t *testing.T) {
	off := func(key string) bool { return key == "marketing" }
	p := Project{ID: "p_mkt", Title: "Marketing site", Repos: []string{repoKeldSignal}, Workstream: "marketing"}
	dims := dimsWith(map[string]string{DimRepo: repoKeldSignal})

	res := Attribute(dims, []Project{p}, off, nil)
	if res.ProjectID != "" || res.Reason != ReasonNoRuleMatched {
		t.Fatalf("workstream-off project still matched: %+v", res)
	}

	same := SameAsCandidates(Document{Projects: []Project{p}}, off)
	for _, c := range same {
		if c.ID == p.ID {
			t.Fatalf("workstream-off project appeared in same-as candidates: %+v", same)
		}
	}

	doc := Document{Projects: []Project{p}}
	suggestions := []Suggestion{{ID: "s1", Kind: SuggestKindRepo, Value: normalizeRepo(repoKeldSignal)}}
	if _, err := PlaceSameAs(doc, "s1", p.ID, suggestions, off); err != ErrWorkstreamOff {
		t.Fatalf("PlaceSameAs onto an off-workstream project: err = %v, want ErrWorkstreamOff", err)
	}
}

// T23: evidence — branch, language, tool, workspace — is NEVER an
// attribution input, even when a project's rules happen to share the exact
// string value.
func TestEvidenceFieldsAreNeverMatchInputs(t *testing.T) {
	p := Project{ID: "p1", Repos: []string{"go", "git", "myworkspace"}, TicketKey: "GIT"}

	cases := map[string]map[string]enrich.Labeled{
		"language":  dimsWith(map[string]string{DimLanguage: "go"}),
		"tooling":   dimsWith(map[string]string{DimTooling: "git"}),
		"workspace": dimsWith(map[string]string{DimWorkspace: "myworkspace"}),
		"branch":    dimsWith(map[string]string{DimBranch: "git-no-ticket-here"}),
	}
	for name, dims := range cases {
		t.Run(name, func(t *testing.T) {
			res := Attribute(dims, []Project{p}, nil, nil)
			if res.ProjectID != "" || res.Method != MethodNone {
				t.Fatalf("%s dim attributed as if it were a rule: %+v", name, res)
			}
		})
	}
}

// T24: a Cowork-shaped block with no repo (and no ticket-bearing branch, no
// workspace) produces NO suggestion — never an empty or path-derived
// placeholder project.
func TestNoRepoBlockProducesNoSuggestion(t *testing.T) {
	dims := dimsWith(map[string]string{"model": "claude-opus-4-8", "output_type": "code"})
	got := Suggest([]UnattributedBlock{{Dims: dims, Minutes: 12, Tokens: 900}})
	if len(got) != 0 {
		t.Fatalf("suggestions = %+v, want none for a repo-less block", got)
	}
}

// T25 (byte-compatibility) lives in model_test.go, next to Load/Save.

// RepoLike table test, per the coordinator's correction.
func TestRepoLike(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"github.com/ncx-ai/keld-signal", true},
		{"ncx-ai/keld-signal", true},
		{"refactor", false},
		{"Q3", false},
		{"KELD-214", false},
		{"some sentence", false},
		{"", false},
		{"a/b/c/d/e/f", false}, // too many segments
	}
	for _, tc := range cases {
		if got := RepoLike(tc.in); got != tc.want {
			t.Errorf("RepoLike(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// Coordinator correction (a): a value whose keywords contain a repo-shaped
// tag attributes a block whose repo dim is the full remote.
func TestRemoteProjectAttributesByRepoShapedKeyword(t *testing.T) {
	values := []settings.RemoteProject{
		{ID: "v_web", Title: "Web", Team: "Development", Keywords: []string{"ncx-ai/keld-signal"}},
	}
	candidates := FromRemoteProjects(values)
	dims := dimsWith(map[string]string{DimRepo: repoKeldSignal})

	res := Attribute(dims, candidates, nil, nil)
	if res.Method != MethodRepo || res.ProjectID != "v_web" {
		t.Fatalf("got %+v, want method repo / project v_web", res)
	}
}

// Coordinator correction (b): a value whose keywords are all non-repo-like
// never attributes by repo.
func TestRemoteProjectWithNoRepoLikeKeywordsNeverAttributesByRepo(t *testing.T) {
	values := []settings.RemoteProject{
		{ID: "v_other", Title: "Other", Team: "Development", Keywords: []string{"refactor", "Q3", "KELD-214"}},
	}
	candidates := FromRemoteProjects(values)
	dims := dimsWith(map[string]string{DimRepo: repoKeldSignal})

	res := Attribute(dims, candidates, nil, nil)
	if res.ProjectID != "" {
		t.Fatalf("non-repo-like keywords attributed by repo: %+v", res)
	}
}

// --- second-round correction: RepoLike is candidacy only; a candidate is a
// rule (and can conflict) only once it has actually matched an observed
// repo. ---------------------------------------------------------------

// Two projects sharing a non-repo keyword must NEVER conflict — RepoLike
// rejects "design/ux" outright (repolike_adversarial_test.go), so it is not
// even a candidate, regardless of what block is being attributed. Two
// projects sharing a genuinely repo-shaped keyword DO conflict, but only once
// a real block on that exact repository is the one being attributed — the
// conflict is never a property of the declaration alone.
func TestConflictOnlyFromRealRepoMatchesNeverFromSharedFreeTags(t *testing.T) {
	a := Project{ID: "p_a", Title: "A", Keywords: []string{"design/ux"}}
	b := Project{ID: "p_b", Title: "B", Keywords: []string{"design/ux"}}
	dims := dimsWith(map[string]string{DimRepo: repoKeldSignal})

	if res := Attribute(dims, []Project{a, b}, nil, nil); res.Reason == ReasonConflict {
		t.Fatalf("two projects sharing a non-repo keyword conflicted: %+v", res)
	}

	c := Project{ID: "p_c", Title: "C", Keywords: []string{"ncx-ai/keld-signal"}}
	d := Project{ID: "p_d", Title: "D", Keywords: []string{"ncx-ai/keld-signal"}}
	if res := Attribute(dims, []Project{c, d}, nil, nil); res.Reason != ReasonConflict {
		t.Fatalf("two projects sharing a real repo-shaped keyword, with a block on that repo, did not conflict: %+v", res)
	}
}

// A repo-shaped keyword must never appear in Rules() — what the page shows
// as "the rules" — until it has actually matched an observed repo. Declared
// Repos, by contrast, are rules from the moment they are added.
func TestRulesOnlyShowsMatchedCandidatesNeverBareShapeMatches(t *testing.T) {
	p := Project{
		ID: "p1", Title: "One",
		Repos:    []string{repoKeldAtlas},
		Keywords: []string{"ncx-ai/keld-signal", "design/ux"},
	}

	// No observations yet: only the DECLARED repo is a rule.
	got := Rules(p, nil)
	if len(got) != 1 || got[0] != repoKeldAtlas {
		t.Fatalf("Rules with no observations = %+v, want only the declared repo", got)
	}

	// A real block on keld-signal has now been observed: the matching
	// keyword is promoted, the non-repo keyword never is (it was never even
	// a candidate).
	observed := []string{repoKeldSignal}
	got = Rules(p, observed)
	if len(got) != 2 {
		t.Fatalf("Rules after an observation = %+v, want declared + 1 matched candidate", got)
	}
	foundMatched := false
	for _, r := range got {
		if r == "design/ux" {
			t.Fatalf("a non-repo-shaped keyword appeared in Rules: %+v", got)
		}
		if r == "ncx-ai/keld-signal" {
			foundMatched = true
		}
	}
	if !foundMatched {
		t.Fatalf("matched candidate missing from Rules: %+v", got)
	}
}

// Removing a matched keyword rule (one that lives in Keywords, not Repos,
// because it arrived as a candidate that happened to match) must return its
// blocks to suggestions exactly like removing a declared repo does.
func TestRemovingAMatchedKeywordRuleReturnsBlocksToSuggestions(t *testing.T) {
	doc := Document{Projects: []Project{{ID: "p1", Title: "One", Keywords: []string{"ncx-ai/keld-signal"}}}}
	dims := dimsWith(map[string]string{DimRepo: repoKeldSignal})

	res := Attribute(dims, doc.Projects, nil, nil)
	if res.Method != MethodRepo || res.ProjectID != "p1" {
		t.Fatalf("expected attribution via matched keyword candidate, got %+v", res)
	}
	if got := Rules(doc.Projects[0], []string{repoKeldSignal}); len(got) != 1 {
		t.Fatalf("matched keyword did not appear in Rules before removal: %+v", got)
	}

	doc, err := RemoveRules(doc, "p1", []Rule{{Kind: RuleKindRepo, Value: "ncx-ai/keld-signal"}})
	if err != nil {
		t.Fatalf("RemoveRules: %v", err)
	}

	res2 := Attribute(dims, doc.Projects, nil, nil)
	if res2.ProjectID != "" {
		t.Fatalf("still attributed after removing the matched keyword rule: %+v", res2)
	}
	back := Suggest([]UnattributedBlock{{Dims: dims, Minutes: 1, Tokens: 1}})
	if len(back) != 1 {
		t.Fatalf("block did not return to suggestions after removal: %+v", back)
	}
	if got := Rules(doc.Projects[0], []string{repoKeldSignal}); len(got) != 0 {
		t.Fatalf("removed keyword still appears in Rules: %+v", got)
	}
}
