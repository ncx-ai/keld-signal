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
	return next, p, nil
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
	return Visible(d.Projects, workstreamOff)
}

// PlaceSameAs adds the rule a suggestion represents to an existing project.
// Refuses (ErrWorkstreamOff) when the target's workstream is off, and
// (ErrProjectNotFound) when hidden or missing — both cases SameAsCandidates
// already excludes, so a caller that only offers those candidates cannot hit
// either refusal by surprise.
func PlaceSameAs(d Document, suggestionID, targetProjectID string, suggestions []Suggestion, workstreamOff func(key string) bool) (Document, error) {
	s, ok := findSuggestion(suggestions, suggestionID)
	if !ok {
		return d, fmt.Errorf("%w: %s", ErrUnknownSuggestion, suggestionID)
	}
	i, ok := findProject(d, targetProjectID)
	if !ok {
		return d, ErrProjectNotFound
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
