package projects

import (
	"reflect"
	"testing"
)

func landed(ids ...string) Result {
	var r Result
	for _, id := range ids {
		r.Projects = append(r.Projects, Assigned{ProjectID: id, Method: MethodRepo})
	}
	if !r.Attributed() {
		r.Reason = ReasonNoRuleMatched
	}
	return r
}

// Block X ($5) is in A and B, block Y ($5) in A only. A project counts every
// block it holds in full: A $10 over two blocks, B $5 over one. There is no
// group figure since Revision 4.
func TestRollupCountsEveryBlockInFullInEachProject(t *testing.T) {
	got := Rollup([]RollupBlock{
		{Minutes: 20, Tokens: 100, USD: 5, Result: landed("A", "B")},
		{Minutes: 10, Tokens: 50, USD: 5, Result: landed("A")},
	})
	want := Totals{Projects: []ProjectTotal{
		{ID: "A", Blocks: 2, Minutes: 30, Tokens: 150, USD: 10},
		{ID: "B", Blocks: 1, Minutes: 20, Tokens: 100, USD: 5},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("totals = %+v, want %+v", got, want)
	}
}

func TestRollupIgnoresUnattributed(t *testing.T) {
	got := Rollup([]RollupBlock{{Minutes: 20, Tokens: 100, USD: 5, Result: landed()}})
	if len(got.Projects) != 0 || got.Projects == nil {
		t.Fatalf("an unattributed block counts toward nothing (and the list is empty, not null): %+v", got)
	}
}

// Output order is stable, by id, so the page does not reshuffle between
// refreshes.
func TestRollupOrderIsStable(t *testing.T) {
	got := Rollup([]RollupBlock{
		{USD: 1, Result: landed("z")},
		{USD: 1, Result: landed("a")},
	})
	if got.Projects[0].ID != "a" || got.Projects[1].ID != "z" {
		t.Fatalf("order must be by id: %+v", got)
	}
}
