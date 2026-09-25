package projects

import (
	"fmt"
	"regexp"
	"strings"
)

// Every mutation in this file is a LOCAL edit to ~/.keld/state/projects.json
// and nothing else.
// docs/v3/contracts.md's verified note is explicit that Atlas has no route a
// machine's ingest token can write a project or a tag through today
// (`PATCH /api/workstreams/{key}` needs an admin USER SESSION) — so this
// package makes NO outbound call of any kind. Re-attribution after an edit is
// TOTAL by construction rather than by a cache-invalidation step: Attribute is
// a pure function of (dims, current Document.Projects, vector), so once the
// caller re-runs it over every block with the document this file just saved,
// no block can be reading a stale project — there is no
// memoised answer anywhere in this package for an edit to invalidate.

// RuleKind is the kind of rule AddRules/RemoveRules add or remove.
type RuleKind string

const (
	RuleKindRepo   RuleKind = "repo"
	RuleKindTicket RuleKind = "ticket"
)

// Rule is one repo or ticket-key rule, the unit AddRules/RemoveRules/Bundle/
// PlaceSameAs operate on.
type Rule struct {
	Kind  RuleKind `json:"kind"`
	Value string   `json:"value"`
}

// ErrProjectNotFound is returned by every edit that targets a project id the
// document does not have.
var ErrProjectNotFound = fmt.Errorf("projects: project not found")

// ErrUnknownSuggestion is returned by Bundle/PlaceSameAs when a suggestion id
// does not appear in the suggestions slice the caller passed.
var ErrUnknownSuggestion = fmt.Errorf("projects: unknown suggestion id")

func findProject(d Document, id string) (int, bool) {
	for i := range d.Projects {
		if d.Projects[i].ID == id {
			return i, true
		}
	}
	return -1, false
}

func findSuggestion(suggestions []Suggestion, id string) (Suggestion, bool) {
	for _, s := range suggestions {
		if s.ID == id {
			return s, true
		}
	}
	return Suggestion{}, false
}

// ruleFromSuggestion maps a suggestion's kind/value onto the Rule it
// contributes to a project. A workspace-kind suggestion has no deterministic
// rule counterpart (Attribute's four-step order has no "workspace rule" —
// only repo and ticket are matchable rules), so it is recorded as a Keyword
// instead: informational, never matched on, exactly evidence.go's own
// discipline for anything that is not a declared rule.
func ruleFromSuggestion(s Suggestion) (Rule, bool) {
	switch s.Kind {
	case SuggestKindRepo:
		return Rule{Kind: RuleKindRepo, Value: s.Value}, true
	case SuggestKindTicket:
		return Rule{Kind: RuleKindTicket, Value: s.Value}, true
	default:
		return Rule{}, false
	}
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugNonAlnum.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		s = "project"
	}
	return s
}

// newProjectID mints a human-legible, unique id from title: "p_" + a slug of
// title, disambiguated with a numeric suffix if a project with that id
// already exists (bundling two projects both titled "SDK work" must not
// collide).
func newProjectID(d Document, title string) string {
	base := "p_" + slugify(title)
	id := base
	for n := 2; ; n++ {
		if _, ok := findProject(d, id); !ok {
			return id
		}
		id = fmt.Sprintf("%s_%d", base, n)
	}
}

func applyRule(p *Project, r Rule, add bool) {
	switch r.Kind {
	case RuleKindRepo:
		if add {
			// An explicit "add rule" action is always a DECLARATION, never a
			// candidate — it goes to Repos regardless of whether r.Value also
			// happens to sit in Keywords (see projects.Rules/MatchedCandidates
			// for the candidate path a Keyword-only repo-shaped tag takes).
			if !containsFold(p.Repos, r.Value) {
				p.Repos = append(p.Repos, r.Value)
			}
		} else {
			// A removal has to reach the rule wherever it actually lives: a
			// declared Repos entry, OR a Keyword that had matched a real
			// observation and was therefore showing as a rule (Rules). Not
			// removing from Keywords here would leave a "removed" rule able
			// to re-match the next time a block is attributed — the same
			// stale-rule defect this file's package comment says a removal
			// must not leave behind.
			p.Repos = removeFold(p.Repos, r.Value)
			p.Keywords = removeFold(p.Keywords, r.Value)
		}
	case RuleKindTicket:
		if add {
			p.TicketKey = r.Value
		} else if strings.EqualFold(p.TicketKey, r.Value) {
			p.TicketKey = ""
		}
	}
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

func removeFold(list []string, v string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if !strings.EqualFold(s, v) {
			out = append(out, s)
		}
	}
	return out
}

// Bundle creates one new project from a title plus the rules the selected
// suggestions carry, and adds it to d. suggestions is the CURRENT suggestion
// set the caller just computed with Suggest — suggestion ids are never
// persisted in the document, so resolving one to its {kind, value} pair
// requires the fresh list that minted it.
//
// ⚠️ **NO GROUP (Revision 4, 2026-09-25).** A new project used to be filed
// under a group the request named, and the group was declared here if the
// document lacked it. Signal has only projects now; the new project carries
// none, and Save files it under the document's default group so a rollback to
// 3.0.6 — which draws a project only under a declared group — still shows it
// (see model.go's toStored).
//
// After Bundle, calling Attribute again for every block that fed those
// suggestions returns the new project — nothing further to invalidate (see
// this file's package-level comment).
func Bundle(d Document, title string, suggestionIDs []string, suggestions []Suggestion) (Document, Project, error) {
	p := Project{
		Title:  title,
		Origin: OriginUser,
	}
	for _, sid := range suggestionIDs {
		s, ok := findSuggestion(suggestions, sid)
		if !ok {
			return d, Project{}, fmt.Errorf("%w: %s", ErrUnknownSuggestion, sid)
		}
		if r, ok := ruleFromSuggestion(s); ok {
			applyRule(&p, r, true)
		} else {
			// Workspace suggestion: no rule form, kept as evidence-only.
			if !containsFold(p.Keywords, s.Value) {
				p.Keywords = append(p.Keywords, s.Value)
			}
		}
	}
	p.ID = newProjectID(d, title)
	next := d
	next.Projects = append(append([]Project(nil), d.Projects...), p)
	return next, p, nil
}

// groupDisplayName turns a key into something a person reads:
// "development" -> "Development", "product_design" -> "Product design". The
// key stays the identity; only the label changes. Used only by Save, to
// declare a group a stored project names but the document does not.
func groupDisplayName(key string) string {
	out := strings.Map(func(r rune) rune {
		if r == '_' || r == '-' {
			return ' '
		}
		return r
	}, key)
	out = strings.TrimSpace(out)
	if out == "" {
		return key
	}
	return strings.ToUpper(out[:1]) + out[1:]
}

// AddRules adds repo/ticket rules to an existing project.
func AddRules(d Document, projectID string, add []Rule) (Document, error) {
	i, ok := findProject(d, projectID)
	if !ok {
		return d, ErrProjectNotFound
	}
	next := d
	next.Projects = append([]Project(nil), d.Projects...)
	p := next.Projects[i]
	for _, r := range add {
		applyRule(&p, r, true)
	}
	next.Projects[i] = p
	return next, nil
}

// RemoveRules removes repo/ticket rules from an existing project. A removed
// repo's blocks are unattributed the next time Attribute runs over them, and
// Suggest groups them back under the SAME id they had before — SuggestionID
// is a pure function of (kind, value), so nothing about removal changes it.
func RemoveRules(d Document, projectID string, remove []Rule) (Document, error) {
	i, ok := findProject(d, projectID)
	if !ok {
		return d, ErrProjectNotFound
	}
	next := d
	next.Projects = append([]Project(nil), d.Projects...)
	p := next.Projects[i]
	for _, r := range remove {
		applyRule(&p, r, false)
	}
	next.Projects[i] = p
	return next, nil
}

// Hide sets a project's Hidden flag: local-only, excludes it from Attribute's
// matching, never deletes its rules.
func Hide(d Document, projectID string, hidden bool) (Document, error) {
	i, ok := findProject(d, projectID)
	if !ok {
		return d, ErrProjectNotFound
	}
	next := d
	next.Projects = append([]Project(nil), d.Projects...)
	next.Projects[i].Hidden = hidden
	return next, nil
}

// SameAsCandidates lists the projects a suggestion may be placed under: every
// one that is not hidden. Mirrors Visible so the "place same as" picker can
// never offer a target Attribute itself would ignore.
func SameAsCandidates(d Document) []Project {
	return SameAsCandidatesWithRemote(d, nil)
}

// SameAsCandidatesWithRemote is SameAsCandidates over the merged candidate set —
// the org's values with local overlays applied — so an Atlas-origin project is
// offered as a target. That is the decided scope of "same as" (2026-09-05): an
// unattributed local suggestion merges into ANY attributed project, and when
// that project is the org's the merge lives in a local overlay.
func SameAsCandidatesWithRemote(d Document, remote []Project) []Project {
	return Visible(MergeCandidates(d.Projects, remote))
}

// PlaceSameAs adds the rule a suggestion represents to an existing project.
// Refuses (ErrProjectNotFound) when the target is hidden or missing — both
// cases SameAsCandidates already excludes, so a caller that only offers those
// candidates cannot hit the refusal by surprise.
func PlaceSameAs(d Document, suggestionID, targetProjectID string, suggestions []Suggestion) (Document, error) {
	return PlaceSameAsWithRemote(d, nil, suggestionID, targetProjectID, suggestions)
}

// PlaceSameAsWithRemote is PlaceSameAs that can target one of the org's values.
//
// ⚠️ **When the target is an Atlas value that has no local entry yet, an OVERLAY
// entry is created** — same id, origin atlas, the org's title and team copied so
// the document reads sensibly on its own, and the suggestion's rule as its only
// rule. Nothing about the org's value is changed and nothing is sent anywhere:
// this is the local half of "same as", which is the only half there is (decided
// 2026-09-05; a user-rights question deliberately deferred). MergeCandidates
// unions the overlay onto the value at read time, so the next attribution pass
// puts the suggestion's blocks under the Atlas value's id — which is what Atlas
// already matches workstreams against.
func PlaceSameAsWithRemote(d Document, remote []Project, suggestionID, targetProjectID string, suggestions []Suggestion) (Document, error) {
	s, ok := findSuggestion(suggestions, suggestionID)
	if !ok {
		return d, fmt.Errorf("%w: %s", ErrUnknownSuggestion, suggestionID)
	}
	i, ok := findProject(d, targetProjectID)
	if !ok {
		// Not local. If it is one of the org's values, lay an overlay entry
		// down for it; otherwise it genuinely does not exist.
		var rv *Project
		for k := range remote {
			if remote[k].ID == targetProjectID {
				rv = &remote[k]
				break
			}
		}
		if rv == nil {
			return d, ErrProjectNotFound
		}
		if rv.Hidden {
			return d, ErrProjectNotFound
		}
		d.Projects = append(append([]Project(nil), d.Projects...), Project{
			ID:     rv.ID,
			Title:  rv.Title,
			Team:   rv.Team,
			Origin: OriginAtlas,
		})
		i = len(d.Projects) - 1
	}
	target := d.Projects[i]
	if target.Hidden {
		return d, ErrProjectNotFound
	}
	next := d
	next.Projects = append([]Project(nil), d.Projects...)
	p := next.Projects[i]
	if r, ok := ruleFromSuggestion(s); ok {
		applyRule(&p, r, true)
	} else if !containsFold(p.Keywords, s.Value) {
		p.Keywords = append(p.Keywords, s.Value)
	}
	next.Projects[i] = p
	return next, nil
}

// MapProjectTo folds a LOCAL project into another project — normally one of the
// org's — and removes the local entry.
//
// ⚠️ **THE RULES MOVE, WHICH IS WHY NOTHING BECOMES UNATTRIBUTED.** The obvious
// worry about "map it and delete it" is that the blocks the local project was
// holding fall out of every project. They do not: a block is attributed by a
// RULE (a repo remote, a ticket key), not by the project's identity, so moving
// the rules moves the blocks with them. The count under the target goes up by
// exactly what the local one had.
//
// ⚠️ **AND THE LOCAL ENTRY MUST GO, RATHER THAN STAY AS A REFERENCE.** Keeping
// it beside its target is the worse option and provably so: two visible
// projects sharing a repository rule is `ReasonConflict`, which reports EVERY
// matching id and deliberately refuses to pick one. A local "reference" beside
// its Atlas twin would turn every one of its blocks into a conflict —
// attributed to neither. That is the existing rule in Attribute, not a
// preference.
//
// Placing onto an org value lays down the same OVERLAY entry PlaceSameAsWithRemote
// uses: an entry whose id IS the Atlas value id, carrying its own repos, unioned
// with the value's own keywords at read time and never sent anywhere. Nothing
// about this reaches Atlas — see docs/v3/contracts.md's "no Atlas write-back".
func MapProjectTo(d Document, remote []Project, localProjectID, targetProjectID string) (Document, error) {
	if localProjectID == "" || targetProjectID == "" {
		return d, ErrProjectNotFound
	}
	// NEGATIVE: mapping a project onto itself would delete it and put its rules
	// back on the entry that was just removed. Refused rather than silently
	// doing nothing, because the caller asked for something incoherent.
	if localProjectID == targetProjectID {
		return d, ErrProjectNotFound
	}
	li, ok := findProject(d, localProjectID)
	if !ok {
		return d, ErrProjectNotFound
	}
	src := d.Projects[li]
	// ⚠️ **AN OVERLAY IS A VALID SOURCE, AND UNTIL REVISION 2 (2026-09-25) IT
	// WAS REFUSED** as "an org project, not ours to fold away". That held while
	// Atlas workstreams were on the page and in the rule pass, so the org's
	// value stood beside the overlay. Signal now labels only with its own
	// projects, and a "Same as" overlay is one of them — shown and attributed
	// as the person's own — so refusing it would leave a project that cannot
	// be merged. Nothing reaches Atlas either way: the entry is local.

	next := d
	next.Projects = append([]Project(nil), d.Projects...)

	ti, ok := findProject(next, targetProjectID)
	if !ok {
		var rv *Project
		for k := range remote {
			if remote[k].ID == targetProjectID {
				rv = &remote[k]
				break
			}
		}
		if rv == nil || rv.Hidden {
			return d, ErrProjectNotFound
		}
		next.Projects = append(next.Projects, Project{
			ID:     rv.ID,
			Title:  rv.Title,
			Team:   rv.Team,
			Origin: OriginAtlas,
		})
		ti = len(next.Projects) - 1
	}
	target := next.Projects[ti]
	if target.Hidden {
		return d, ErrProjectNotFound
	}

	for _, r := range src.Repos {
		applyRule(&target, Rule{Kind: DimRepo, Value: r}, true)
	}
	if src.TicketKey != "" && target.TicketKey == "" {
		target.TicketKey = src.TicketKey
	}
	for _, k := range src.Keywords {
		if !containsFold(target.Keywords, k) {
			target.Keywords = append(target.Keywords, k)
		}
	}
	next.Projects[ti] = target

	// Remove the local entry LAST, by id: `ti` was computed against the slice
	// that still contains it, so deleting first would invalidate the index.
	out := next.Projects[:0:0]
	for _, x := range next.Projects {
		if x.ID == localProjectID {
			continue
		}
		out = append(out, x)
	}
	next.Projects = out
	return next, nil
}
