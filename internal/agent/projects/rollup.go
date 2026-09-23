package projects

import "sort"

// RollupBlock is one closed block as the totals need it: what it cost and
// where the rule pass put it.
type RollupBlock struct {
	Minutes float64
	Tokens  int64
	USD     float64
	Result  Result
}

// GroupTotal is a group's total: every block that landed in ANY of its
// projects, each counted ONCE. SharedBlocks is how many of those landed in
// two or more of its projects — the reason the group's project totals
// add up to more than the group.
type GroupTotal struct {
	Key          string  `json:"key"`
	Blocks       int     `json:"blocks"`
	Minutes      float64 `json:"minutes"`
	Tokens       int64   `json:"tokens"`
	USD          float64 `json:"usd"`
	SharedBlocks int     `json:"shared_blocks"`
}

// ProjectTotal is a project's total: every block it holds, in full.
type ProjectTotal struct {
	ID      string  `json:"id"`
	Group   string  `json:"group"`
	Blocks  int     `json:"blocks"`
	Minutes float64 `json:"minutes"`
	Tokens  int64   `json:"tokens"`
	USD     float64 `json:"usd"`
}

// Totals is Rollup's answer.
type Totals struct {
	Groups   []GroupTotal   `json:"groups"`
	Projects []ProjectTotal `json:"projects"`
}

// Rollup is THE totals rule, and the only one: a group counts each block once,
// a project counts each of its blocks in full.
//
// ⚠️ **SO PROJECT TOTALS ADD UP TO MORE THAN THEIR GROUP, BY DESIGN.** A
// block may land in several projects of one group (overlap is the model
// since 2026-09-23 — see Result), and each of them really did get that work.
// The group is the figure that never double counts: "what did this area
// cost". A project answers "how much work touched this". Summing
// projects is not a total, and the page says so beside the numbers.
func Rollup(blocks []RollupBlock) Totals {
	groups := map[string]*GroupTotal{}
	ws := map[string]*ProjectTotal{}
	for _, b := range blocks {
		perGroup := map[string]int{}
		for _, a := range b.Result.Projects {
			w := ws[a.ProjectID]
			if w == nil {
				w = &ProjectTotal{ID: a.ProjectID, Group: a.Group}
				ws[a.ProjectID] = w
			}
			w.Blocks++
			w.Minutes += b.Minutes
			w.Tokens += b.Tokens
			w.USD += b.USD
			perGroup[a.Group]++
		}
		for key, n := range perGroup {
			g := groups[key]
			if g == nil {
				g = &GroupTotal{Key: key}
				groups[key] = g
			}
			g.Blocks++
			g.Minutes += b.Minutes
			g.Tokens += b.Tokens
			g.USD += b.USD
			if n > 1 {
				g.SharedBlocks++
			}
		}
	}
	out := Totals{Groups: []GroupTotal{}, Projects: []ProjectTotal{}}
	for _, g := range groups {
		out.Groups = append(out.Groups, *g)
	}
	for _, w := range ws {
		out.Projects = append(out.Projects, *w)
	}
	sort.Slice(out.Groups, func(i, j int) bool { return out.Groups[i].Key < out.Groups[j].Key })
	sort.Slice(out.Projects, func(i, j int) bool { return out.Projects[i].ID < out.Projects[j].ID })
	return out
}
