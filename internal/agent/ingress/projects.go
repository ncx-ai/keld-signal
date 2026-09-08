package ingress

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/projects"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// maxProjectsBody mirrors ingress.go's own /enrich cap: these bodies are a
// handful of ids and strings, never anything resembling prompt text.
const maxProjectsBody = 1 << 20 // 1 MiB

// ProjectsRoute registers the six /v1/projects and /v1/workstreams routes
// docs/v3/contracts.md's "Projects" section specifies, behind auth. s is the
// only dependency: the file-backed Document store, plus its two OPTIONAL
// getters (Blocks, RemoteProjects) the daemon wiring may set later. Neither
// being set degrades gracefully to an honest empty (no blocks/values known
// yet), never an error — see their doc comments in
// internal/agent/projects/model.go.
//
// ⚠️ Named ProjectsRoute, not Route: this file lives in package ingress
// (every file in a directory shares one package), which already declares
// `type Route` in route.go — a function literally named Route here would be
// a redeclaration. Every v3 lane's constructor is named after what it
// registers (see Handler's own doc comment for the sibling routes:
// /v1/ledger, /v1/settings, /v1/projects, /v1/config, the page) and returns
// an ingress.Route value for Handler's `extras ...Route` to take.
//
// ⚠️ EVERY MUTATING ROUTE HERE IS A LOCAL EDIT, PERIOD. docs/v3/contracts.md's
// verified note ("What Atlas actually offers today") found that Atlas has no
// route a machine's ingest token can write a project or a tag through — the
// admin editor is a user-session-gated route this daemon cannot call. So none
// of these handlers make an outbound call, and this file does not import
// internal/atlas at all. Every response from bundle/rules/hide/place/
// workstream-off carries `{"local_only": true, "atlas_editor_url": …}` so the
// page can say plainly that the change has not reached the org.
func ProjectsRoute(s *projects.Store) Route {
	return Route(func(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
		mux.Handle("GET /v1/projects", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleGetProjects(w, r, s)
		})))
		mux.Handle("POST /v1/projects/bundle", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleBundle(w, r, s)
		})))
		mux.Handle("POST /v1/projects/{id}/rules", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleRules(w, r, s)
		})))
		mux.Handle("POST /v1/projects/{id}/hide", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleHide(w, r, s)
		})))
		// Fold a LOCAL project into another (normally one of the org's). The
		// rules move with it and the local entry goes — see
		// projects.MapProjectTo for why keeping it would make every one of its
		// blocks a conflict.
		mux.Handle("POST /v1/projects/{id}/same-as", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleProjectSameAs(w, r, s)
		})))
		mux.Handle("POST /v1/projects/place", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlePlace(w, r, s)
		})))
		mux.Handle("PUT /v1/workstreams/{key}/off", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleWorkstreamOff(w, r)
		})))
	})
}

// --- shared helpers -------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxProjectsBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "malformed_body")
		return false
	}
	return true
}

// localOnly stamps every mutating response with the fact
// docs/v3/contracts.md's verified note requires: this daemon has no route to
// tell Atlas about a project or a tag, so the page must never imply the org
// was taught anything.
func localOnly(v map[string]any) map[string]any {
	if v == nil {
		v = map[string]any{}
	}
	v["local_only"] = true
	v["atlas_editor_url"] = atlasEditorURL()
	return v
}

func atlasEditorURL() string {
	return strings.TrimRight(paths.APIBase(), "/") + "/workstreams"
}

// startOfWeek is Monday 00:00 UTC of t's week — the "current week" coverage
// figure's window.
func startOfWeek(t time.Time) time.Time {
	t = t.UTC()
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7 // Sunday -> 7, so Monday is always day 1 of its own week
	}
	d := t.AddDate(0, 0, -(wd - 1))
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
}

// workstreamOffFunc resolves the AUTHORITATIVE exclusion predicate — reading
// agent-config.json fresh per request, since a request may arrive right
// after a PUT /v1/workstreams/{key}/off changed it. "call it, don't
// reimplement" — internal/agent/settings/v3.go's WorkstreamOff.
func workstreamOffFunc() func(string) bool {
	return settings.Load().WorkstreamOff
}

// candidatesFor is every project Attribute/Suggest may consider: this
// machine's own declared Document.Projects plus, when the daemon wiring has
// supplied one, the org's pooled workstream values converted via
// projects.FromRemoteProjects. A nil RemoteProjects getter contributes
// nothing — an honest "not known yet", never an error.
func candidatesFor(s *projects.Store, d projects.Document) []projects.Project {
	return projects.MergeCandidates(d.Projects, remoteCandidates(s))
}

// remoteCandidates is the org's values as attribution candidates, or nil when
// the daemon wiring has supplied no getter (Atlas off, or never polled).
func remoteCandidates(s *projects.Store) []projects.Project {
	if s.RemoteProjects == nil {
		return nil
	}
	return projects.FromRemoteProjects(s.RemoteProjects())
}

// Attribution is ONE live recomputation of the deterministic attribution
// pass, held open across as many blocks as a caller has: the projects
// document, the org's pooled values, and the workstream-off predicate, each
// read exactly once and then applied.
//
// ⚠️ **IT EXISTS SO THE TWO SURFACES CANNOT ANSWER DIFFERENTLY.** Attribution
// used to run once, at cut time, and the stored cell was never revisited — so
// a block cut before its project was declared stayed `no_rule_matched`
// forever, while the Projects pane recomputed the same match live and
// reported it attributed. Measured on a real machine: 97 of 105 attributed on
// the pane against 8 on the Today rows. Both surfaces now go through this one
// value, which makes agreement a property rather than a race. The stored cell
// is still written at cut time — it is the delivery record `sent`/`received`
// hang off — it is simply no longer what is DISPLAYED.
//
// ⚠️ **A DOCUMENT THAT CANNOT BE READ IS AN ERROR, NEVER AN EMPTY ONE.**
// "nobody has declared a project" and "we could not tell" are different
// answers, and only the first may render as no project; the second must
// render as unknown. That is why NewAttribution returns an error rather than
// a zero Attribution.
type Attribution struct {
	// Document is the local projects file as it was read.
	Document projects.Document
	// Candidates is what Attribute may consider: the local document's
	// projects merged with the org's pooled values.
	Candidates []projects.Project
	// Off is the authoritative workstream-exclusion predicate, read from
	// agent-config.json at the same instant.
	Off func(string) bool
}

// NewAttribution reads everything one pass needs, once.
func NewAttribution(s *projects.Store) (Attribution, error) {
	d, err := s.Load()
	if err != nil {
		return Attribution{}, err
	}
	return Attribution{
		Document:   d,
		Candidates: candidatesFor(s, d),
		Off:        workstreamOffFunc(),
	}, nil
}

// Of is one block's decision, from that block's already-published workstream
// dims. The Vector pass is nil: this is the deterministic lane, and a nil
// Vector is what makes "unattributed" mean "no rule matched" rather than
// "the encoder was not asked".
func (a Attribution) Of(dims map[string]enrich.Labeled) projects.Result {
	return projects.Attribute(dims, a.Candidates, a.Off, nil)
}

// currentSuggestions recomputes the suggestion list exactly as GET
// /v1/projects would, so a mutating route resolving a suggestion id sees the
// same ids that route just handed the page. A nil Blocks getter yields no
// suggestions at all (nothing to group), which is why Bundle/PlaceSameAs
// against an unknown id fail with ErrUnknownSuggestion rather than a panic.
func currentSuggestions(s *projects.Store, d projects.Document) ([]projects.Suggestion, error) {
	if s.Blocks == nil {
		return nil, nil
	}
	blocks, err := s.Blocks.SinceWeekStart()
	if err != nil {
		return nil, err
	}
	pass := Attribution{Document: d, Candidates: candidatesFor(s, d), Off: workstreamOffFunc()}
	var unattributed []projects.UnattributedBlock
	for _, b := range blocks {
		if pass.Of(b.Dims).ProjectID == "" {
			unattributed = append(unattributed, projects.UnattributedBlock{
				Dims: b.Dims, Minutes: b.Minutes, Tokens: b.Tokens,
			})
		}
	}
	return projects.Suggest(unattributed), nil
}

// --- handlers --------------------------------------------------------------

func handleGetProjects(w http.ResponseWriter, r *http.Request, s *projects.Store) {
	// The SAME live pass the Today rows are rendered from (see Attribution) —
	// not a second implementation of it.
	pass, err := NewAttribution(s)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_unreadable")
		return
	}
	d, off, candidates := pass.Document, pass.Off, pass.Candidates

	workstreams := make([]projects.Workstream, len(d.Workstreams))
	for i, ws := range d.Workstreams {
		ws.Off = off(ws.Key)
		workstreams[i] = ws
	}

	since := startOfWeek(time.Now())
	attributed, total := 0, 0
	var suggestions []projects.Suggestion
	var observed []string

	if s.Blocks != nil {
		blocks, err := s.Blocks.SinceWeekStart()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "blocks_unreadable")
			return
		}
		observed = observedRepos(blocks)
		var unattributed []projects.UnattributedBlock
		for _, b := range blocks {
			total++
			res := pass.Of(b.Dims)
			if res.ProjectID != "" {
				attributed++
				continue
			}
			unattributed = append(unattributed, projects.UnattributedBlock{
				Dims: b.Dims, Minutes: b.Minutes, Tokens: b.Tokens,
			})
		}
		suggestions = projects.Suggest(unattributed)
	}

	// ⚠️ **THE ORG'S VALUES ARE PROJECTS ON THIS PAGE, NOT ONLY CANDIDATES.**
	// This used to return d.Projects — the LOCAL document — while attributing
	// against candidatesFor(), which merges the org's pooled workstream values
	// from the settings poll. So a machine paired with an org that had declared
	// eight projects showed "Your projects: none" while silently attributing
	// blocks to them. Found by the D8 end-to-end against the real dev Atlas:
	// projects=0 with eight values on the wire. The page's "Your projects · from
	// Atlas" section exists to show exactly these, so they are returned, and the
	// buckets they belong to (their `team`, which carries the workstream's name
	// on the wire) are added to `workstreams` when the local document does not
	// already name them.
	writeJSON(w, http.StatusOK, map[string]any{
		"workstreams": withRemoteBuckets(workstreams, candidates, off),
		"projects":    projectViews(candidates, observed),
		"suggestions": suggestions,
		"coverage": map[string]any{
			"attributed": attributed,
			"total":      total,
			"since":      since.Format(time.RFC3339),
		},
	})
}

// projectView is a project as the page should read it: every declared field
// unchanged, plus `rules` — what projects.Rules actually names as this
// project's active rules (declared repos always, a repo-shaped keyword only
// once it has matched an observed block). Without this, the raw
// projects.Project the store holds would have the page reading Keywords or
// EffectiveRepos as if either were "the rules", which is precisely the
// internal-vocabulary failure ("and/or" or "design/ux" shown as a
// repository) the split in attribute.go exists to prevent.
type projectView struct {
	projects.Project
	Rules []string `json:"rules"`
}

func projectViews(ps []projects.Project, observed []string) []projectView {
	out := make([]projectView, len(ps))
	for i, p := range ps {
		out[i] = projectView{Project: p, Rules: projects.Rules(p, observed)}
	}
	return out
}

// observedRepos is every distinct, normalised `repo` dim value seen across
// blocks — the ONLY thing that can promote a repo-shaped keyword candidate
// into a rule (see projects.MatchedCandidates). A block with no attributed
// repo dim contributes nothing, the same "only read an attributed dimension"
// rule Attribute itself applies.
func observedRepos(blocks []projects.BlockSummary) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range blocks {
		l, ok := b.Dims[projects.DimRepo]
		if !ok || l.Value == "" {
			continue
		}
		if l.Status != "" && l.Status != enrich.WorkstreamAttributed {
			continue
		}
		if seen[l.Value] {
			continue
		}
		seen[l.Value] = true
		out = append(out, l.Value)
	}
	return out
}

func handleBundle(w http.ResponseWriter, r *http.Request, s *projects.Store) {
	var body struct {
		Title       string   `json:"title"`
		Workstream  string   `json:"workstream"`
		Suggestions []string `json:"suggestions"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		writeError(w, http.StatusBadRequest, "title_required")
		return
	}

	current, err := s.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_unreadable")
		return
	}
	suggestions, err := currentSuggestions(s, current)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "blocks_unreadable")
		return
	}

	var created projects.Project
	_, err = s.Update(func(d projects.Document) (projects.Document, error) {
		next, p, err := projects.Bundle(d, body.Title, body.Workstream, body.Suggestions, suggestions)
		created = p
		return next, err
	})
	if err != nil {
		if errors.Is(err, projects.ErrUnknownSuggestion) {
			writeError(w, http.StatusBadRequest, "unknown_suggestion")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_write_failed")
		return
	}

	writeJSON(w, http.StatusOK, localOnly(map[string]any{"project": created}))
}

func handleRules(w http.ResponseWriter, r *http.Request, s *projects.Store) {
	id := r.PathValue("id")
	var body struct {
		Add    []projects.Rule `json:"add"`
		Remove []projects.Rule `json:"remove"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}

	_, err := s.Update(func(d projects.Document) (projects.Document, error) {
		var err error
		if len(body.Add) > 0 {
			d, err = projects.AddRules(d, id, body.Add)
			if err != nil {
				return d, err
			}
		}
		if len(body.Remove) > 0 {
			d, err = projects.RemoveRules(d, id, body.Remove)
			if err != nil {
				return d, err
			}
		}
		return d, nil
	})
	if err != nil {
		if errors.Is(err, projects.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project_not_found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_write_failed")
		return
	}

	writeJSON(w, http.StatusOK, localOnly(nil))
}

func handleHide(w http.ResponseWriter, r *http.Request, s *projects.Store) {
	id := r.PathValue("id")
	var body struct {
		Hidden bool `json:"hidden"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}

	_, err := s.Update(func(d projects.Document) (projects.Document, error) {
		return projects.Hide(d, id, body.Hidden)
	})
	if err != nil {
		if errors.Is(err, projects.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project_not_found")
			return
		}
		writeError(w, http.StatusInternalServerError, "store_write_failed")
		return
	}

	writeJSON(w, http.StatusOK, localOnly(nil))
}

func handlePlace(w http.ResponseWriter, r *http.Request, s *projects.Store) {
	var body struct {
		Suggestion string `json:"suggestion"`
		SameAs     string `json:"same_as"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}

	current, err := s.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_unreadable")
		return
	}
	suggestions, err := currentSuggestions(s, current)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "blocks_unreadable")
		return
	}
	off := workstreamOffFunc()

	_, err = s.Update(func(d projects.Document) (projects.Document, error) {
		// An Atlas value may be the target: the merge lives in a local overlay
		// and nothing is sent to Atlas (decided 2026-09-05).
		return projects.PlaceSameAsWithRemote(d, remoteCandidates(s), body.Suggestion, body.SameAs, suggestions, off)
	})
	if err != nil {
		switch {
		case errors.Is(err, projects.ErrUnknownSuggestion):
			writeError(w, http.StatusBadRequest, "unknown_suggestion")
		case errors.Is(err, projects.ErrProjectNotFound):
			writeError(w, http.StatusNotFound, "project_not_found")
		case errors.Is(err, projects.ErrWorkstreamOff):
			writeError(w, http.StatusConflict, "workstream_off")
		default:
			writeError(w, http.StatusInternalServerError, "store_write_failed")
		}
		return
	}

	writeJSON(w, http.StatusOK, localOnly(nil))
}

// handleProjectSameAs maps a local project onto another project. Same error
// vocabulary as handlePlace, because the failures are the same ones.
func handleProjectSameAs(w http.ResponseWriter, r *http.Request, s *projects.Store) {
	id := r.PathValue("id")
	var body struct {
		SameAs string `json:"same_as"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(body.SameAs) == "" {
		writeError(w, http.StatusBadRequest, "id_and_same_as_required")
		return
	}
	off := workstreamOffFunc()
	_, err := s.Update(func(d projects.Document) (projects.Document, error) {
		// An Atlas value may be the target: the merge lives in a local overlay
		// and nothing is sent to Atlas (decided 2026-09-05).
		return projects.MapProjectTo(d, remoteCandidates(s), id, body.SameAs, off)
	})
	if err != nil {
		switch {
		case errors.Is(err, projects.ErrProjectNotFound):
			writeError(w, http.StatusNotFound, "project_not_found")
		case errors.Is(err, projects.ErrWorkstreamOff):
			writeError(w, http.StatusConflict, "workstream_off")
		default:
			writeError(w, http.StatusInternalServerError, "store_write_failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, localOnly(nil))
}

func handleWorkstreamOff(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var body struct {
		Off bool `json:"off"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(key) == "" {
		writeError(w, http.StatusBadRequest, "key_required")
		return
	}
	if err := projects.SetWorkstreamOff(key, body.Off); err != nil {
		writeError(w, http.StatusInternalServerError, "settings_write_failed")
		return
	}
	writeJSON(w, http.StatusOK, localOnly(nil))
}

// withRemoteBuckets appends a workstream entry for every bucket the org's
// values belong to that the local document does not already declare, so the
// page can group "Your projects · from Atlas" under the org's own names. A
// remote project's bucket is its Team when its Workstream is empty — that is
// where wire_projects puts the workstream's name (docs/v3/contracts.md).
func withRemoteBuckets(local []projects.Workstream, candidates []projects.Project, off func(string) bool) []projects.Workstream {
	// Seeded with KEYS only. It used to hold lower-cased keys AND names, so a
	// bucket could be skipped because some other bucket's NAME collided with
	// this one's key — a membership test about two different things.
	seen := map[string]bool{}
	for _, ws := range local {
		seen[strings.ToLower(ws.Key)] = true
		seen[projects.WorkstreamKey(ws.Name)] = true
	}
	out := append([]projects.Workstream(nil), local...)
	for _, p := range candidates {
		if p.Origin != projects.OriginAtlas {
			continue
		}
		// The NAME is the human label Atlas serves (its `team`); the KEY is
		// that name normalised. They are two different things and were being
		// conflated: `p.Workstream` now HOLDS the key (see
		// projects.FromRemoteProjects), so using it as the name would print
		// "keld-projects" as a heading, and using the name as the key would
		// group nothing.
		name := p.Team
		if name == "" {
			name = p.Workstream
		}
		key := projects.WorkstreamKey(name)
		if p.Workstream != "" {
			key = p.Workstream
		}
		if name == "" || key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, projects.Workstream{
			Key:    key,
			Name:   name,
			Origin: "atlas",
			Off:    off(name),
		})
	}
	return out
}
