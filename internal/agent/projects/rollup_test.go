package projects

import (
	"reflect"
	"testing"
)

func landed(ids ...string) Result {
	var r Result
	for i := 0; i+1 < len(ids); i += 2 {
		r.Projects = append(r.Projects, Assigned{ProjectID: ids[i], Group: ids[i+1], Method: MethodRepo})
	}
	if !r.Attributed() {
		r.Reason = ReasonNoRuleMatched
	}
	return r
}

// AC-9. Block X ($5) is in A and B, block Y ($5) in A only, both in group G.
// The group counts each block ONCE ($10); a project counts every block it
// holds in full (A $10, B $5); one block is shared.
func TestRollupCountsGroupOnce(t *testing.T) {
	got := Rollup([]RollupBlock{
		{Minutes: 20, Tokens: 100, USD: 5, Result: landed("A", "G", "B", "G")},
		{Minutes: 10, Tokens: 50, USD: 5, Result: landed("A", "G")},
	})
	wantGroups := []GroupTotal{{Key: "G", Blocks: 2, Minutes: 30, Tokens: 150, USD: 10, SharedBlocks: 1}}
	if !reflect.DeepEqual(got.Groups, wantGroups) {
		t.Fatalf("groups = %+v, want %+v", got.Groups, wantGroups)
	}
	wantWS := []ProjectTotal{
		{ID: "A", Group: "G", Blocks: 2, Minutes: 30, Tokens: 150, USD: 10},
		{ID: "B", Group: "G", Blocks: 1, Minutes: 20, Tokens: 100, USD: 5},
	}
	if !reflect.DeepEqual(got.Projects, wantWS) {
		t.Fatalf("projects = %+v, want %+v", got.Projects, wantWS)
	}
}

// Across groups nothing is shared: each group counts the block once.
func TestRollupAcrossGroupsCountsTheBlockInEach(t *testing.T) {
	got := Rollup([]RollupBlock{{Minutes: 20, Tokens: 100, USD: 5,
		Result: landed("products:atlas", "products", "features:billing", "features")}})
	if len(got.Groups) != 2 || got.Groups[0].USD != 5 || got.Groups[1].USD != 5 ||
		got.Groups[0].SharedBlocks != 0 || got.Groups[1].SharedBlocks != 0 {
		t.Fatalf("each group counts the block once, sharing nothing: %+v", got.Groups)
	}
}

func TestRollupIgnoresUnattributed(t *testing.T) {
	got := Rollup([]RollupBlock{{Minutes: 20, Tokens: 100, USD: 5, Result: landed()}})
	if len(got.Groups) != 0 || len(got.Projects) != 0 {
		t.Fatalf("an unattributed block counts toward nothing: %+v", got)
	}
}

// Output order is stable: groups and projects by key/id, so the page does
// not reshuffle between refreshes.
func TestRollupOrderIsStable(t *testing.T) {
	got := Rollup([]RollupBlock{
		{USD: 1, Result: landed("z", "g2")},
		{USD: 1, Result: landed("a", "g1")},
	})
	if got.Groups[0].Key != "g1" || got.Projects[0].ID != "a" {
		t.Fatalf("order must be by key/id: %+v", got)
	}
}
