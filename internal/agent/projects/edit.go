package projects

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// Every mutation in this file is a LOCAL edit to ~/.keld/state/projects.json
// (or, for SetWorkstreamOff, ~/.keld/agent-config.json) and nothing else.
// docs/v3/contracts.md's verified note is explicit that Atlas has no route a
// machine's ingest token can write a project or a tag through today
// (`PATCH /api/workstreams/{key}` needs an admin USER SESSION) — so this
// package makes NO outbound call of any kind. Re-attribution after an edit is
// TOTAL by construction rather than by a cache-invalidation step: Attribute is
// a pure function of (dims, current Document.Projects, workstreamOff,
// vector), so once the caller re-runs it over every block with the document
// this file just saved, no block can be reading a stale project — there is no
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

// ErrWorkstreamOff is returned by PlaceSameAs when the target project's
// workstream is switched off — such a project must not gain new rules while
// its bucket is excluded from matching, or a "same as" click would silently
// resurrect it.
var ErrWorkstreamOff = fmt.Errorf("projects: target project's workstream is off")

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

// Bundle creates one new project from title/workstream plus the rules the
// selected suggestions carry, and adds it to d. suggestions is the CURRENT
// suggestion set the caller just computed with Suggest — suggestion ids are
// never persisted in the document, so resolving one to its {kind, value} pair
// requires the fresh list that minted it.
//
// After Bundle, calling Attribute again for every block that fed those
// suggestions returns the new project — nothing further to invalidate (see
// this file's package-level comment).
func Bundle(d Document, title, workstream string, suggestionIDs []string, suggestions []Suggestion) (Document, Project, error) {
	p := Project{
		Title:      title,
		Workstream: workstream,
		Origin:     OriginUser,
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
	next.Workstreams = ensureWorkstream(d.Workstreams, p.Workstream)
	return next, p, nil
}

// ensureWorkstream adds the project's workstream to the document if the org has
// not declared one by that key.
//
// ⚠️ **A PROJECT FILED UNDER A WORKSTREAM THAT DOES NOT EXIST IS AN INVISIBLE
// PROJECT.** The Projects pane renders projects by looping over workstreams and
// showing each one's members, so a project whose workstream is in no list is
// never drawn — it exists in this file, it attributes blocks, and the person who
// made it sees nothing.
//
// Measured on a real machine: two projects on disk, `"workstreams": null`, and a
// pane reading "YOUR PROJECTS" followed by nothing and "WORKSTREAMS ON 0 of 0".
// From the outside that is indistinguishable from the suggestion having been
// thrown away, which is exactly how it was reported.
//
// It happens whenever the org has declared no workstreams — every machine with
// Send to Atlas off, which is the default for anyone trying Signal locally —
// because the list is pushed down from Atlas and nothing local ever seeded it.
// `bundleSuggestion` falls back to the key "development", so that was the name
// of a workstream that never existed anywhere.
//
// Origin is LOCAL: this is the machine inventing a bucket to keep its own work
// visible, and it must never be mistaken for something the org declared. If
// Atlas later declares a workstream with the same key, the match is by key and
// the org's own entry is the one already present, so this adds nothing.
func ensureWorkstream(existing []Workstream, key string) []Workstream {
	if key == "" {
		return existing
	}
	for _, w := range existing {
		if w.Key == key {
			return existing
		}
	}
	return append(append([]Workstream(nil), existing...), Workstream{
		Key:      key,
		Name:     workstreamDisplayName(key),
		Origin:   WorkstreamOriginLocal,
		Question: "Which project is this work for?",
	})
}

// workstreamDisplayName turns a key into something a person reads:
// "development" -> "Development", "product_design" -> "Product design". The
// key stays the identity; only the label changes.
func workstreamDisplayName(key string) string {
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

// SameAsCandidates lists the projects a suggestion may be placed under: not
// hidden, not in a workstream that is off. Mirrors Visible so the "place same
// as" picker can never offer a target Attribute itself would ignore.
func SameAsCandidates(d Document, workstreamOff func(key string) bool) []Project {
	return SameAsCandidatesWithRemote(d, nil, workstreamOff)
}

// SameAsCandidatesWithRemote is SameAsCandidates over the merged candidate set —
// the org's values with local overlays applied — so an Atlas-origin project is
// offered as a target. That is the decided scope of "same as" (2026-09-05): an
// unattributed local suggestion merges into ANY attributed project, and when
// that project is the org's the merge lives in a local overlay.
func SameAsCandidatesWithRemote(d Document, remote []Project, workstreamOff func(key string) bool) []Project {
	return Visible(MergeCandidates(d.Projects, remote), workstreamOff)
}

// PlaceSameAs adds the rule a suggestion represents to an existing project.
// Refuses (ErrWorkstreamOff) when the target's workstream is off, and
// (ErrProjectNotFound) when hidden or missing — both cases SameAsCandidates
// already excludes, so a caller that only offers those candidates cannot hit
// either refusal by surprise.
func PlaceSameAs(d Document, suggestionID, targetProjectID string, suggestions []Suggestion, workstreamOff func(key string) bool) (Document, error) {
	return PlaceSameAsWithRemote(d, nil, suggestionID, targetProjectID, suggestions, workstreamOff)
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
func PlaceSameAsWithRemote(d Document, remote []Project, suggestionID, targetProjectID string, suggestions []Suggestion, workstreamOff func(key string) bool) (Document, error) {
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
		if projectWorkstreamOff(*rv, workstreamOff) {
			return d, ErrWorkstreamOff
		}
		d.Projects = append(append([]Project(nil), d.Projects...), Project{
			ID:         rv.ID,
			Title:      rv.Title,
			Team:       rv.Team,
			Workstream: rv.Workstream,
			Origin:     OriginAtlas,
		})
		i = len(d.Projects) - 1
	}
	target := d.Projects[i]
	if target.Hidden {
		return d, ErrProjectNotFound
	}
	if projectWorkstreamOff(target, workstreamOff) {
		return d, ErrWorkstreamOff
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

// SetWorkstreamOff writes settings.Settings.WorkstreamsOff — the AUTHORITATIVE
// exclusion list Attribute's workstreamOff parameter reads (see
// internal/agent/settings/v3.go's WorkstreamOff) — adding or removing key.
// This is the one edit in this file that does not touch projects.json: the
// PUT /v1/workstreams/{key}/off route writes agent-config.json instead, so
// this package never has two copies of the same fact to keep in sync. It
// round-trips the WHOLE settings.Settings struct (read via settings.Load,
// written back in full) rather than a hand-rolled partial-file patch, so any
// other key already in the file survives untouched — the same merge
// discipline settings.WriteInstallDefaults already applies to a different
// subset of keys.
func SetWorkstreamOff(key string, off bool) error {
	s := settings.Load()
	present := false
	out := make([]string, 0, len(s.WorkstreamsOff)+1)
	for _, k := range s.WorkstreamsOff {
		if strings.EqualFold(strings.TrimSpace(k), strings.TrimSpace(key)) {
			present = true
			if !off {
				continue // drop it: switching back on
			}
		}
		out = append(out, k)
	}
	if off && !present {
		out = append(out, key)
	}
	s.WorkstreamsOff = out

	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := paths.AgentConfigPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}
