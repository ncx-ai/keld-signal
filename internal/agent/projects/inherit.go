package projects

import "strings"

// Stage 5 of the project lifecycle: an org project has been approved that
// covers what a local project was standing in for, so the local one folds onto
// it and the machine stops asking.
//
// ⚠️ **FULL CONTAINMENT ONLY, AND THAT IS THE WHOLE SAFETY ARGUMENT.** This is
// the one step that changes a decision a person already made, without asking
// them. It is only safe when the org's project covers EVERY rule the local one
// had — because then the two are the same thing and keeping both is strictly
// worse: two visible projects sharing a repository rule is ReasonConflict, and
// both stop attributing.
//
// Anything less than full containment is left alone. Folding on partial overlap
// would move a rule the org never approved onto the org's project — a local
// invention wearing the org's name — and the person who wrote that rule would
// have no way to tell it apart from something an admin decided.
//
// ⚠️ **IT MUST RUN ON THE SAME POLL THAT INSTALLS THE DEFINITION.** If the fold
// lagged the definition by even one interval, the local project and the org
// project would both claim the repository for that interval, which is exactly
// the conflict this exists to avoid — and during it neither would attribute.

// Inherit folds every local project that an org project fully covers, and
// reports which ones moved.
//
// `remote` is the org's values as candidates (FromRemoteProjects). It returns
// the document unchanged when nothing qualifies, so a caller can compare and
// skip a write.
func Inherit(d Document, remote []Project, workstreamOff func(key string) bool) (Document, []Inherited) {
	if len(remote) == 0 {
		return d, nil
	}
	var moved []Inherited
	next := d
	for {
		// One fold per pass, re-reading the document each time: MapProjectTo
		// returns a NEW document and the indices shift, so iterating a stale
		// slice would fold against positions that no longer exist.
		src, dst, ok := nextInheritable(next, remote, workstreamOff)
		if !ok {
			return next, moved
		}
		after, err := MapProjectTo(next, remote, src.ID, dst.ID, workstreamOff)
		if err != nil {
			// A refusal here is not a failure of the lifecycle — a workstream
			// switched off, say. Skipping it and stopping is the honest
			// outcome: the local project keeps working exactly as before.
			return next, moved
		}
		moved = append(moved, Inherited{
			LocalID: src.ID, LocalTitle: src.Title,
			OrgID: dst.ID, OrgTitle: dst.Title,
			Repos: append([]string(nil), DeclaredRepos(src)...),
		})
		next = after
	}
}

// Inherited names one fold, for the client-event and for the page's own
// confirmation. It carries the rules that moved because "your project became
// the org's" is not a useful sentence without them.
type Inherited struct {
	LocalID    string
	LocalTitle string
	OrgID      string
	OrgTitle   string
	Repos      []string
}

// nextInheritable finds one local project fully covered by one org value.
func nextInheritable(d Document, remote []Project, workstreamOff func(key string) bool) (Project, Project, bool) {
	for _, local := range d.Projects {
		if local.Origin == OriginAtlas || local.Hidden {
			continue
		}
		rules := DeclaredRepos(local)
		if len(rules) == 0 {
			// ⚠️ A project with NO rules is not "covered by everything". An
			// empty set is contained in every set, so treating it as
			// inheritable would fold every ruleless local project into the
			// first org value that came along — a silent, arbitrary
			// reassignment of something a person named on purpose.
			continue
		}
		for _, org := range remote {
			if org.Hidden || projectWorkstreamOff(org, workstreamOff) {
				continue
			}
			if covers(EffectiveRepos(org), rules) {
				return local, org, true
			}
		}
	}
	return Project{}, Project{}, false
}

// covers reports whether every rule in `want` appears in `have`, case-folded
// the way repository remotes are compared everywhere else in this package.
func covers(have, want []string) bool {
	if len(want) == 0 {
		return false
	}
	set := make(map[string]bool, len(have))
	for _, h := range have {
		set[strings.ToLower(strings.TrimSpace(h))] = true
	}
	for _, w := range want {
		if !set[strings.ToLower(strings.TrimSpace(w))] {
			return false
		}
	}
	return true
}
