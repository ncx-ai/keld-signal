package projects

import "strings"

// Reconcile applies the one rule the whole project lifecycle rests on:
// AN ATLAS PROJECT'S RULES TAKE PRECEDENCE OVER A LOCAL PROJECT'S.
//
// A project is a set of metadata and a block enters it by matching ANY item, so
// "covered" is set inclusion and nothing more. Once Atlas takes precedence, a
// local project effectively holds only what Atlas does not cover, and the two
// cases stop being two things:
//
//   - the remainder is EMPTY → the local project is DELETED. It tracked nothing
//     Atlas does not already cover, and its blocks keep attributing because the
//     rules that placed them are still matched — by Atlas.
//   - the remainder is NOT empty → the local project keeps exactly that. What it
//     keeps is also the thing worth proposing to an admin, and it rides the
//     block from there.
//
// ⚠️ **WITHOUT PRECEDENCE, THE SECOND CASE IS A BROKEN MACHINE.** Local {A,B,C}
// and Atlas {A,B} both claim A, and two visible projects claiming one repository
// is ReasonConflict: the matcher reports every match and refuses to pick, so the
// block lands in NEITHER. That state would persist until an admin acted, which
// could be days. Trimming is what stops a rule ever being claimed twice.
//
// ⚠️ **DELETE, NOT MERGE — and this replaced a version that merged.** Copying
// the covered rules onto an overlay before removing the local project is
// redundant under this model: Atlas already holds them, by definition of
// covered. The overlay only made the document larger and gave a later reader two
// places to look for one rule.
//
// ⚠️ **TRIMMING IS NOT REVERSIBLE HERE.** If Atlas later drops a rule this
// trimmed away, the local project does not get it back — the machine has no
// record that it once held it, because the whole point is that it stopped
// holding it. The work returns as a suggestion instead, which is the honest
// outcome: nothing claims it, so it is unplaced again.
func Reconcile(d Document, remote []Project, workstreamOff func(key string) bool) (Document, []Removed, []Trimmed) {
	covered := coveredRules(remote, workstreamOff)
	if len(covered) == 0 {
		// No org projects at all — Send to Atlas off, or a first run before any
		// poll. An ABSENT list is not an empty one, and treating it as coverage
		// would delete every local project on a machine that has simply not
		// spoken to Atlas yet.
		return d, nil, nil
	}

	var removed []Removed
	var trimmed []Trimmed
	out := make([]Project, 0, len(d.Projects))
	for _, p := range d.Projects {
		if p.Origin == OriginAtlas || p.Hidden {
			out = append(out, p)
			continue
		}
		rules := DeclaredRepos(p)
		if len(rules) == 0 {
			// ⚠️ A project with NO rules is not "covered by everything". The
			// empty set is contained in every set, so the naive reading deletes
			// anything a person named before giving it a rule.
			out = append(out, p)
			continue
		}
		var keep, lost []string
		for _, r := range rules {
			if covered[normRule(r)] {
				lost = append(lost, r)
			} else {
				keep = append(keep, r)
			}
		}
		switch {
		case len(lost) == 0:
			out = append(out, p) // nothing shared; untouched
		case len(keep) == 0:
			removed = append(removed, Removed{ID: p.ID, Title: p.Title, Rules: rules})
		default:
			next := p
			next.Repos = keep
			out = append(out, next)
			trimmed = append(trimmed, Trimmed{ID: p.ID, Title: p.Title, Kept: keep, Covered: lost})
		}
	}
	if len(removed) == 0 && len(trimmed) == 0 {
		return d, nil, nil
	}
	d.Projects = out
	return d, removed, trimmed
}

// Removed names a local project this machine deleted because Atlas covered
// every rule it had.
type Removed struct {
	ID    string
	Title string
	Rules []string
}

// Trimmed names a local project that kept only what Atlas does not cover. Kept
// is what still attributes here — and what an admin would be shown.
type Trimmed struct {
	ID      string
	Title   string
	Kept    []string
	Covered []string
}

// coveredRules is every rule held by a VISIBLE org project.
//
// ⚠️ A workstream that is switched off covers NOTHING. Its projects are
// excluded from matching entirely, so counting their rules as coverage would
// delete a local project and leave its blocks in no project at all — the exact
// opposite of what coverage is supposed to guarantee.
func coveredRules(remote []Project, workstreamOff func(key string) bool) map[string]bool {
	out := map[string]bool{}
	for _, p := range remote {
		if p.Hidden || projectWorkstreamOff(p, workstreamOff) {
			continue
		}
		for _, r := range EffectiveRepos(p) {
			if v := normRule(r); v != "" {
				out[v] = true
			}
		}
	}
	return out
}

// normRule folds a rule the way repository remotes are compared everywhere else
// in this package. Without it, coverage silently misses on a difference of case
// and a project is kept that should have gone.
func normRule(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
