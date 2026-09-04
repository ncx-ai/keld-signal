package projects

import (
	"regexp"
	"sort"
	"strings"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// Dimension keys this package reads off a block's already-published
// workstreams map. DimWorkspace is the ALLOCATION dimension the sidecar
// publishes as `project` (the directory/checkout identity resolved from
// `cwd`) — named "workspace" in docs/v3/contracts.md's prose to avoid
// colliding with this package's own Project type, which is a different
// thing (a person's declared project, not the directory-derived dimension).
const (
	DimRepo      = "repo"
	DimBranch    = "branch"
	DimWorkspace = "project"
)

// Method is how a block was named — the same closed vocabulary
// internal/agent/ledger.Method publishes, restated here rather than imported
// so this package has no build dependency on D2's ledger store, which is
// edited elsewhere in this worktree while this lane runs. The string values
// are identical by construction (both derive from docs/v3/contracts.md), so
// converting one to the other at the daemon-wiring seam is a cast, not a
// lookup table.
type Method string

const (
	MethodRepo      Method = "repo"
	MethodTicket    Method = "ticket"
	MethodEmbedding Method = "embedding"
	MethodNone      Method = ""
)

// Reason is the subset of internal/agent/ledger.Reason this package can
// produce, restated for the same reason Method is.
type Reason string

const (
	ReasonNone               Reason = ""
	ReasonConflict           Reason = "conflict"
	ReasonNoRuleMatched      Reason = "no_rule_matched"
	ReasonWeightsUnavailable Reason = "weights_unavailable"
)

// Result is one block's attribution decision.
type Result struct {
	ProjectID string
	Method    Method
	Reason    Reason
	// Conflict holds every matching project id, sorted, when Reason ==
	// ReasonConflict — NEVER just the first one silently chosen.
	Conflict []string
}

// Vector is step 3 of the attribution order: an injectable, optional
// embedding match. nil in this package by design — this lane does not import
// the encoder (internal/agent/enrich/sidecar, internal/agent/attrib) at all.
// A caller wires a real implementation only when the org's vector-attribution
// toggle is on; Attribute falls through to unattributed when Vector is nil.
type Vector interface {
	// Attribute is handed the block's dims and the visible (non-hidden,
	// workstream-on) candidate projects, and returns the pass's own final
	// Result for this block — including a weights-unavailable Reason if the
	// encoder could not run, since only the implementation knows that.
	Attribute(dims map[string]enrich.Labeled, candidates []Project) Result
}

// attributedValue reads dims[key], returning it only when the workstream
// dimension actually reached the attributed floor. "Only 'attributed' may be
// read as the window's answer" (enrich.WorkstreamStatuses' own rule) applies
// here identically: a `thin`/`tie`/`no_majority`/`absent` repo or branch
// dimension is not evidence to attribute a person's work off of. A Status
// left empty (a caller's hand-built test dims, or a facet with no status
// vocabulary) is accepted, since this package must not invent a status the
// dims map never had.
func attributedValue(dims map[string]enrich.Labeled, key string) (string, bool) {
	l, ok := dims[key]
	if !ok || l.Value == "" {
		return "", false
	}
	if l.Status != "" && l.Status != enrich.WorkstreamAttributed {
		return "", false
	}
	return l.Value, true
}

// normalizeRepo lowercases and strips the scheme/user/`.git` noise so
// "https://github.com/ncx-ai/keld-signal.git", "git@github.com:ncx-ai/keld-signal"
// and "github.com/ncx-ai/keld-signal" (the daemon's own normalised form,
// AGENTS.md's `host/owner/repo`) all reduce to the same identity.
func normalizeRepo(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "ssh://")
	s = strings.TrimPrefix(s, "git@")
	s = strings.TrimSuffix(s, ".git")
	s = strings.ReplaceAll(s, ":", "/")
	return strings.Trim(s, "/")
}

// repoSegment is one "/"-separated path element of a repo-shaped string:
// alphanumeric, optionally with internal '.', '_' or '-' (so "github.com" and
// "keld-signal" both match, but an empty segment or one with whitespace does
// not).
var repoSegment = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)

// allDigits is a segment that is nothing but digits — "7", "2026" — the shape
// a date or a fraction leaves behind and no real org or repo name has.
var allDigits = regexp.MustCompile(`^[0-9]+$`)

// RepoLike reports whether s has the SHAPE of a repository identity — a
// CHEAP PRE-FILTER FOR CANDIDACY ONLY, never the attribution decision itself.
// docs/v3/contracts.md's correction is explicit that Atlas strips authored-tag
// prefixes before the daemon ever sees a value's keywords ("repository:
// acme/web" arrives as "acme/web"), so a repository has to be recognised by
// shape or not at all. But shape alone cannot tell "ncx-ai/keld-signal" from
// "internal/agent" or "design/ux" — both are two bare words joined by a
// slash — and a real adversarial pass over free tags an admin might actually
// type (repolike_adversarial_test.go) found RepoLike saying yes to exactly
// those: source paths ("internal/agent", "src/main"), prose ("and/or"),
// dates ("q3/2026") and discipline pairs ("design/ux").
//
// ⚠️ THE FIX IS NOT A CLEVERER RepoLike — it is that RepoLike no longer
// DECIDES anything (see EffectiveRepos, CandidateRepos and Rules below): a
// candidate this function admits only ever becomes a rule once
// matchRepoRule confirms it against a repo this machine has actually
// observed on a real block. That is what closes the two failures a false
// "yes" here used to cause directly — a project card showing "and/or" as one
// of its rules, and two unrelated projects both tagged "design/ux" reading as
// a conflict — without pretending shape can rule out the genuine residual
// risk (a real remote literally named `.../internal/agent` would still, and
// unavoidably, match a keyword typed as a source-path-looking tag; that is
// accepted, not solved, and is exactly why nothing downstream trusts a
// candidate until it has matched).
//
// So this stays a LOOSE, cheap filter, tightened only enough to reject shapes
// that can never be a real remote regardless of what matches: 2 to 5
// "/"-separated segments, each a non-empty bare identifier with no
// whitespace, not every segment purely numeric (a bare number is a date or a
// fraction, never a host/org/repo component).
func RepoLike(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\n\r") {
		return false
	}
	parts := strings.Split(s, "/")
	if len(parts) < 2 || len(parts) > 5 {
		return false
	}
	allNumeric := true
	for _, p := range parts {
		if p == "" || !repoSegment.MatchString(p) {
			return false
		}
		if !allDigits.MatchString(p) {
			allNumeric = false
		}
	}
	if allNumeric {
		return false
	}
	// ⚠️ **AND THAT IS DELIBERATELY WHERE IT STOPS.** A bare two-segment tag is
	// where shape genuinely runs out: "and/or", "src/main" and "design/ux"
	// have EXACTLY the shape "ncx-ai/keld-signal" does — two bare words joined
	// by a slash — and no rule over the string alone separates them.
	//
	// This function briefly tried: it required punctuation ('.', '_' or '-')
	// in one segment, on the reasoning that org and repo slugs are
	// conventionally kebab-case while prose is not. That passed the
	// adversarial prose list and BROKE THE REAL CASE. Atlas's tag editor puts
	// `repository: acme/web` in front of an admin as its own example, and its
	// test fixture is `acme/website`; the prefix is stripped before the daemon
	// sees either, so what arrives is a two-word, unpunctuated pair. Rejecting
	// those means an org that tagged its values exactly as the product told it
	// to gets no deterministic attribution — silently, because the value still
	// arrives and still looks right in Atlas. A silent total loss for a
	// correctly configured org is worse than admitting a candidate that is
	// inert unless it collides with a real remote.
	// Pinned by repolike_atlas_examples_test.go.
	//
	// So candidacy stays loose and the DECISION lives downstream: a candidate
	// becomes a rule only once matchRepoRule confirms it against a repository
	// this machine has actually observed (MatchedCandidates / Rules). "and/or"
	// is admitted here and never matches anything, so it never appears on a
	// project card and never manufactures a conflict — which is what the
	// adversarial test now asserts, having originally asserted the wrong thing.
	return true
}

// matchRepoRule reports whether rule (a project's declared repo, full
// `host/org/name` or a bare `org/name` recognised via RepoLike) identifies
// the same repository as block (a block's `repo` dim, always the daemon's
// full normalised `host/owner/repo` — see AGENTS.md). Comparison is on the
// TRAILING min(len(rule), len(block)) path segments, so a full three-segment
// rule requires a full match while a bare two-segment `org/name` rule matches
// any host whose repo ends in that org/name — "match on the trailing org/name
// segment" per docs/v3/contracts.md's correction.
func matchRepoRule(rule, block string) bool {
	r := normalizeRepo(rule)
	b := normalizeRepo(block)
	if r == "" || b == "" {
		return false
	}
	rs := strings.Split(r, "/")
	bs := strings.Split(b, "/")
	n := len(rs)
	if len(bs) < n {
		n = len(bs)
	}
	if n == 0 {
		return false
	}
	return strings.Join(rs[len(rs)-n:], "/") == strings.Join(bs[len(bs)-n:], "/")
}

// DeclaredRepos is a project's explicitly declared repository rules — always
// rules, regardless of whether any block has ever matched one. This is what
// a person meant when they added the rule (Bundle/AddRules/PlaceSameAs all
// write here), so it is shown and matched unconditionally.
func DeclaredRepos(p Project) []string {
	return append([]string(nil), p.Repos...)
}

// CandidateRepos is whichever of a project's Keywords are repo-shaped
// (RepoLike) — a CANDIDATE list, never itself "the rules". A keyword arrives
// as free-authored text (an Atlas value's tags, or a person's own notes on a
// local project) and RepoLike is a loose, shape-only filter (see its own doc
// comment for exactly why it cannot be tighter) — so a candidate is worth
// TRYING against a real observation, never worth SHOWING as a fact about the
// project on its own. See MatchedCandidates and Rules.
func CandidateRepos(p Project) []string {
	var out []string
	for _, k := range p.Keywords {
		if RepoLike(k) {
			out = append(out, k)
		}
	}
	return out
}

// EffectiveRepos is every repository string Attribute may try against a
// SINGLE block's own `repo` dim: DeclaredRepos plus CandidateRepos. This is
// unaffected by the matched/candidate split below — a candidate that does
// not match the block being attributed was already inert, matchRepoRule
// requires a real match every time it is called, and Attribute never
// consults this list for anything but that one per-block comparison. It must
// NOT be used to populate a "rules" display or to compare two projects'
// declarations directly (see Rules) — that is exactly the shape that let an
// unmatched candidate like "design/ux" read as a fact.
func EffectiveRepos(p Project) []string {
	return append(DeclaredRepos(p), CandidateRepos(p)...)
}

// MatchedCandidates is the subset of CandidateRepos that has actually
// resolved against at least one repo this machine has OBSERVED (a real
// block's `repo` dim, normalised) — never merely a shape. observedRepos is
// typically every distinct `repo` dim value seen across the machine's recent
// blocks (see internal/agent/ingress/projects.go's GET handler). A candidate
// with no observation to match is neither promoted nor discarded — it simply
// is not yet a rule, honestly, the same "not yet known" reading this codebase
// gives everywhere else rather than defaulting to a confident answer.
func MatchedCandidates(p Project, observedRepos []string) []string {
	var out []string
	for _, c := range CandidateRepos(p) {
		for _, o := range observedRepos {
			if matchRepoRule(c, o) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// Rules is what a project card should show as "the rules": DeclaredRepos,
// which a person put there on purpose, ALWAYS — plus MatchedCandidates,
// which are shown only once they have actually caught something. An
// unmatched candidate (a keyword that merely looks repo-shaped) never
// appears here, however long it has existed — the honest fix for the defect
// RepoLike's shape alone could not close: a project card is not entitled to
// assert a keyword is a repository until a real block has confirmed it.
func Rules(p Project, observedRepos []string) []string {
	return append(DeclaredRepos(p), MatchedCandidates(p, observedRepos)...)
}

// ticketKeyPattern finds a Jira-style ticket reference: 2-10 letters/digits
// starting with a letter, a hyphen, then digits — e.g. "KELD-1234" inside
// "feature/KELD-1234-fix-thing".
var ticketKeyPattern = regexp.MustCompile(`(?i)\b([A-Za-z][A-Za-z0-9]{1,9})-([0-9]+)\b`)

// ticketKeyIn extracts the ticket-key PREFIX (uppercased) from a branch name,
// if any.
func ticketKeyIn(branch string) (string, bool) {
	m := ticketKeyPattern.FindStringSubmatch(branch)
	if m == nil {
		return "", false
	}
	return strings.ToUpper(m[1]), true
}

// projectWorkstreamOff reports whether p's bucket is switched off, checking
// BOTH Workstream (a local project's key) and Team (an Atlas value's
// workstream-name proxy — see Project's doc comment), because the wire gives
// no way to tell which spelling an operator's workstreams_off entry used.
func projectWorkstreamOff(p Project, workstreamOff func(key string) bool) bool {
	if workstreamOff == nil {
		return false
	}
	if p.Workstream != "" && workstreamOff(p.Workstream) {
		return true
	}
	if p.Team != "" && workstreamOff(p.Team) {
		return true
	}
	return false
}

// Visible returns the projects Attribute (and a same-as picker) may ever
// consider: not hidden, and not in a workstream switched off.
func Visible(projects []Project, workstreamOff func(key string) bool) []Project {
	out := make([]Project, 0, len(projects))
	for _, p := range projects {
		if p.Hidden || projectWorkstreamOff(p, workstreamOff) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func conflictIDs(matches []Project) []string {
	ids := make([]string, 0, len(matches))
	for _, p := range matches {
		ids = append(ids, p.ID)
	}
	sort.Strings(ids)
	return ids
}

// WorkstreamOffFunc adapts settings.Settings.WorkstreamOff to the
// func(string) bool this package's helpers take, so a caller does not have to
// write the closure itself. "call it, don't reimplement" — see
// internal/agent/settings/v3.go.
func WorkstreamOffFunc(s settings.Settings) func(string) bool {
	return s.WorkstreamOff
}

// FromRemoteProjects converts the org's pooled workstream VALUES — as they
// actually arrive today, via the settings poll's Remote.Projects or
// KELD_PROJECTS_FILE — into attribution candidates.
//
// docs/v3/contracts.md's verified note is the reason this exists rather than
// treating settings.RemoteProject as a Project directly: a real value is
// shaped {id, title, description, team, keywords} ONLY — Repos and TicketKey
// are always empty on the wire — so a repository rule has to be recovered
// from Keywords BY SHAPE (RepoLike), never a "repo:" prefix, because Atlas
// strips authored-tag prefixes before the daemon ever sees them. Team carries
// the value's workstream NAME when it has no owning team of its own (Atlas
// does not distinguish the two on the wire), which is why it rides straight
// into Project.Team rather than Workstream — see Project's doc comment and
// projectWorkstreamOff.
// NOTE: internal/atlas also converts settings.RemoteProject (its own
// FromRemoteProjects, grouping into atlas.Workstream/atlas.Value for a
// different consumer) -- this package deliberately does not import
// internal/atlas or reuse it: the attribution engine must work with zero
// dependency on the (optional, toggleable) Atlas connector, and this lane was
// explicitly told not to import that package. The two converters read the
// same wire shape independently rather than sharing one.
func FromRemoteProjects(values []settings.RemoteProject) []Project {
	out := make([]Project, 0, len(values))
	for _, v := range values {
		repos := append([]string(nil), v.Repos...)
		for _, k := range v.Keywords {
			if RepoLike(k) {
				repos = append(repos, k)
			}
		}
		out = append(out, Project{
			ID:          v.ID,
			Title:       v.Title,
			Description: v.Description,
			Team:        v.Team,
			Repos:       repos,
			Keywords:    v.Keywords,
			TicketKey:   v.TicketKey,
			Origin:      OriginAtlas,
		})
	}
	return out
}

// Attribute implements the four-step order docs/v3/contracts.md specifies,
// exactly:
//
//  1. block `repo` dim ∈ some non-hidden, workstream-on project's repos
//     (EffectiveRepos: declared Repos plus RepoLike Keywords) → that project,
//     method `repo`. Two matches → Reason ReasonConflict, listing every
//     matching id (sorted), NEVER silently the first.
//  2. else block `branch` dim carries a ticket key matching a project's
//     TicketKey → method `ticket`. Same conflict rule.
//  3. else, when vector is non-nil, its own pass decides (method `embedding`,
//     or ReasonWeightsUnavailable if it could not run).
//  4. else unattributed (ReasonNoRuleMatched).
//
// workstreamOff may be nil (treated as "nothing is off") — production wiring
// passes settings.Load().WorkstreamOff (WorkstreamOffFunc).
func Attribute(dims map[string]enrich.Labeled, candidates []Project, workstreamOff func(key string) bool, vector Vector) Result {
	visible := Visible(candidates, workstreamOff)

	if repo, ok := attributedValue(dims, DimRepo); ok {
		var matches []Project
		for _, p := range visible {
			for _, r := range EffectiveRepos(p) {
				if matchRepoRule(r, repo) {
					matches = append(matches, p)
					break
				}
			}
		}
		if len(matches) == 1 {
			return Result{ProjectID: matches[0].ID, Method: MethodRepo}
		}
		if len(matches) > 1 {
			return Result{Reason: ReasonConflict, Conflict: conflictIDs(matches)}
		}
	}

	if branch, ok := attributedValue(dims, DimBranch); ok {
		if key, ok := ticketKeyIn(branch); ok {
			var matches []Project
			for _, p := range visible {
				if p.TicketKey != "" && strings.EqualFold(p.TicketKey, key) {
					matches = append(matches, p)
				}
			}
			if len(matches) == 1 {
				return Result{ProjectID: matches[0].ID, Method: MethodTicket}
			}
			if len(matches) > 1 {
				return Result{Reason: ReasonConflict, Conflict: conflictIDs(matches)}
			}
		}
	}

	if vector != nil {
		return vector.Attribute(dims, visible)
	}

	return Result{Reason: ReasonNoRuleMatched}
}
