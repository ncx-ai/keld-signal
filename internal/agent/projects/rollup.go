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

// ProjectTotal is a project's total: every block it holds, in full.
type ProjectTotal struct {
	ID      string  `json:"id"`
	Blocks  int     `json:"blocks"`
	Minutes float64 `json:"minutes"`
	Tokens  int64   `json:"tokens"`
	USD     float64 `json:"usd"`
}

// Totals is Rollup's answer.
type Totals struct {
	Projects []ProjectTotal `json:"projects"`
}

// Rollup is THE totals rule, and the only one: a project counts each of its
// blocks in full.
//
// ⚠️ **SO PROJECT TOTALS ADD UP TO MORE THAN THE WORK, BY DESIGN.** A block
// may land in several projects (overlap is the model since 2026-09-23 — see
// Result), and each of them really did get that work: a project answers "how
// much work touched this". Summing projects is not a total. The figure that
// never double counts is coverage's "attributed N of M blocks", which counts a
// shared block once and is computed from the same live pass.
//
// ⚠️ **THERE ARE NO GROUP TOTALS ANY MORE (Revision 4, 2026-09-25).** Until
// then a group counted each block once and reported `shared_blocks`, the reason
// its projects summed past it. Groups left the product, and with them the only
// reader of either figure.
func Rollup(blocks []RollupBlock) Totals {
	ws := map[string]*ProjectTotal{}
	for _, b := range blocks {
		for _, a := range b.Result.Projects {
			w := ws[a.ProjectID]
			if w == nil {
				w = &ProjectTotal{ID: a.ProjectID}
				ws[a.ProjectID] = w
			}
			w.Blocks++
			w.Minutes += b.Minutes
			w.Tokens += b.Tokens
			w.USD += b.USD
		}
	}
	out := Totals{Projects: []ProjectTotal{}}
	for _, w := range ws {
		out.Projects = append(out.Projects, *w)
	}
	sort.Slice(out.Projects, func(i, j int) bool { return out.Projects[i].ID < out.Projects[j].ID })
	return out
}
