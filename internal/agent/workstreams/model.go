// Package projects is the on-device, deterministic project-attribution store
// for the v3 health page: ~/.keld/state/projects.json, its Load/Save, and the
// four-step attribution pass docs/v3/contracts.md's "Projects" section
// specifies.
//
// This is DELIBERATELY NOT the same thing as internal/agent/attrib +
// settings.RemoteWorkstream: that is Task 1's on-device EMBEDDING match (the
// encoder + optional verifier, driven by the daemon against the sidecar's
// /attribute), fed by KELD_PROJECTS_FILE / Atlas's remote `projects` key. This
// package is the RULE-based pass the health page's project inbox drives: a
// person declares repo/ticket-key rules by bundling suggestions, and every
// block is re-attributed live from those rules with no model in the loop.
// Step 3 of the four-step order below is where the two meet — an injectable
// Vector interface, nil in this package, that a caller wires to the embedding
// match only when the org's vector toggle is on (see attribute.go).
//
// Nothing in this package ever reads or stores prompt text, a span or an
// offset: a Project's rules are a normalised git remote and a ticket-key
// prefix, and everything else a machine could observe about a project
// (branches, languages, tools, workspaces seen) is EVIDENCE, computed on read
// from a block's already-published workstream dims — never a rule, never
// matched on (see evidence.go and attribute.go's evidence-is-not-a-rule test).
package workstreams

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// CurrentVersion is the workstreams.json document version. Bump it and write a
// migration in Load if the shape ever changes incompatibly.
//
// Version 2 is the rename to Atlas's words (2026-09-23): the file moved from
// projects.json to workstreams.json, the groups moved from `workstreams` to
// `groups`, the workstreams from `projects` to `workstreams`, and each
// workstream names its group under `group`. Version 1 is still read — see
// migrate.go.
const CurrentVersion = 2

// FileName is the document's name under the state dir.
const FileName = "workstreams.json"

// Origin values for a Project: how it came to exist.
const (
	OriginSuggested = "suggested"
	OriginUser      = "user"
	OriginAtlas     = "atlas"
)

// Origin values for a Workstream.
const (
	GroupOriginAtlas = "atlas"
	GroupOriginLocal = "local"
)

// Group is one of the org's declared workstreams (the "which project is
// this work for" categories a project belongs to), mirroring
// settings.RemoteWorkstream's Atlas-declared siblings for this document.
//
// Off is a DISPLAY MIRROR, not the authority: the authoritative flag is
// settings.Settings.GroupsOff (agent-config.json's `workstreams_off`),
// read live via settings.Settings.GroupOff — see attribute.go's
// groupOff parameter and the PUT /v1/groups/{key}/off route in
// ingress/workstreams.go, which writes THAT file, not this one. It is carried
// here too so a reader of this document alone (a backup, a support bundle)
// is not missing the fact; ingress/workstreams.go overwrites it with the live
// value before every GET /v1/workstreams response.
type Group struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	Question   string `json:"question,omitempty"`
	TemplateID string `json:"template_id,omitempty"`
	Origin     string `json:"origin,omitempty"`
	Off        bool   `json:"off,omitempty"`
}

// Workstream is one declared project. Repos and TicketKey ARE the rules — see
// attribute.go's Attribute. Everything else that could be said about a
// project (which branches/languages/tools/workspaces its blocks show) is
// evidence.go's job, computed on read, never persisted here and never an
// input to Attribute.
//
// Field-for-field, the first seven fields below are BYTE-COMPATIBLE with
// settings.RemoteWorkstream: same JSON names, same types, so a plain JSON array
// of Projects (Document.Projects marshaled on its own) decodes cleanly
// through settings.LoadWorkstreamsFile — it ignores the v3-only fields that
// follow, because that decoder does not reject unknown keys. See
// TestWorkstreamsFileByteCompatibleWithKeldWorkstreamsFile.
//
// ⚠️ Team carries TWO different facts depending on origin, because Atlas does
// not distinguish them on the wire (docs/v3/contracts.md, "What Atlas
// actually offers today", point 1): for a locally-declared project it is a
// real owning sub-team (informational only, never matched on); for a value
// converted by FromRemoteWorkstreams it is that value's WORKSTREAM'S NAME
// whenever the value has no owning team of its own. projectGroupOff
// checks both Workstream (the local key) and Team (the Atlas name proxy)
// against settings.Settings.GroupOff, case-insensitively, so a workstream
// switched off excludes a project however its bucket happens to be spelled.
type Workstream struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Team        string   `json:"team,omitempty"`
	Repos       []string `json:"repos,omitempty"`
	Keywords    []string `json:"keywords,omitempty"`
	TicketKey   string   `json:"ticket_key,omitempty"`

	// Group is this project's workstream KEY (Group.Key above), set
	// for a locally-declared project. An Atlas-sourced value (see
	// FromRemoteWorkstreams) never carries one — Atlas pools its values across
	// every workstream and derives the workstream from the value id itself
	// when a block is later matched server-side (docs/v3/contracts.md,
	// "What Atlas actually offers today", point 2) — so its bucket is carried
	// in Team instead (see Team's doc comment) and projectGroupOff checks
	// both.
	Group string `json:"group,omitempty"`
	// Origin says how this project came to exist: "suggested" (never
	// happens — a suggestion is not persisted until bundled), "user"
	// (bundled/edited by a person) or "atlas" (pushed down as an org
	// declaration). See OriginSuggested/OriginUser/OriginAtlas.
	Origin string `json:"origin,omitempty"`
	// Hidden means local-only: excluded from Attribute's matching (see
	// attribute.go) and never reported to Atlas. It does not delete the
	// project or its rules.
	Hidden bool `json:"hidden,omitempty"`
	// AtlasValueID is the org's own id for this project once it has been
	// reconciled with an Atlas-side value list entry. A pointer, and NOT
	// omitempty, so an unreconciled project publishes an explicit
	// `"atlas_value_id": null` rather than omitting the key — the shape
	// docs/v3/contracts.md shows.
	AtlasValueID *string `json:"atlas_value_id"`
}

// Document is the whole ~/.keld/state/workstreams.json file.
type Document struct {
	Version     int          `json:"version"`
	Groups      []Group      `json:"groups"`
	Workstreams []Workstream `json:"workstreams"`
}

// DefaultPath is ~/.keld/state/workstreams.json (KELD_HOME-relative via
// internal/paths, so tests isolate it with t.TempDir()+KELD_HOME like every
// other state file in this codebase).
func DefaultPath() string {
	return filepath.Join(paths.StateDir(), FileName)
}

// Load reads and decodes path. A MISSING file is not an error — it is a
// fresh, empty document (version stamped, no workstreams, no projects): the
// document does not exist until the first edit, and "nobody has declared a
// project yet" must not be confused with "the file could not be read". Any
// other read or decode error is returned, never silently swallowed into an
// empty document — the same distinction settings.LoadWorkstreamsFile already
// draws for the sibling KELD_PROJECTS_FILE loader.
func Load(path string) (Document, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return Document{}, err
		}
		// Not migrated yet: read the pre-rename file beside it, READ-ONLY.
		// The daemon migrates once at startup (MigrateLegacy); a reader that
		// runs before that must still see the person's setup, not an empty one.
		legacy, lerr := readLegacySibling(path)
		if lerr != nil || legacy == nil {
			if lerr != nil {
				return Document{}, lerr
			}
			return Document{Version: CurrentVersion}, nil
		}
		return *legacy, nil
	}
	d, err := decodeDocument(b)
	if err != nil {
		return Document{}, fmt.Errorf("workstreams file %s: %w", path, err)
	}
	return d, nil
}

// Save writes d to path atomically (unique temp file, then rename) at mode
// 0600, mirroring the durable-write idiom internal/agent/attrib.atomicWrite
// uses one package over. A crash mid-write can never leave a torn or
// zero-length projects.json.
func Save(path string, d Document) error {
	if d.Version == 0 {
		d.Version = CurrentVersion
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
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

// BlockSummary is the minimal shape this package needs from a block to
// compute suggestions and coverage: its already-published workstream dims
// (never text) plus its measured cost. It is deliberately NOT
// enrich.BlockCharacterisation or publish.BlockEnrichment — this package
// must not depend on either lane's full block shape, only the dims map
// Attribute/Suggest already consume.
type BlockSummary struct {
	SessionID string
	Start     int64
	Dims      map[string]enrich.Labeled
	Minutes   float64
	Tokens    int64
}

// BlocksSource is the read-only feed of this machine's recently-closed
// blocks that GET /v1/workstreams needs for `suggestions` and `coverage`. It is
// an injectable interface — nil by default — for the same reason Vector is
// in attribute.go: this package must not depend on the ledger store (D2) or
// the block emitter's own storage, both edited elsewhere in this worktree.
// The daemon wiring sets Store.Blocks once those are available; until then
// the route reports an honest zero (no blocks known), never a fabricated
// count.
type BlocksSource interface {
	// SinceWeekStart returns every block closed since the start of the
	// current reporting week, for coverage and for grouping unattributed
	// blocks into suggestions.
	SinceWeekStart() ([]BlockSummary, error)
}

// Store is a mutex-guarded, file-backed Document: what
// internal/agent/ingress/workstreams.go's Route holds and what every HTTP
// handler reads and writes through, so concurrent requests never race a
// torn read against a half-written file.
type Store struct {
	mu   sync.Mutex
	path string
	// Blocks is the optional BlocksSource — see its doc comment. nil until
	// the daemon wiring sets it.
	Blocks BlocksSource
	// RemoteWorkstreams, when set, returns the org's pooled workstream values
	// most recently seen on the settings poll (settings.Remote.Projects),
	// converted at read time via FromRemoteWorkstreams. This package cannot hold
	// that state itself — the settings poll lives in the daemon, which is
	// wired separately from this deliverable — so it is a getter the daemon
	// wiring supplies, mirroring Blocks. nil means "no remote projects known
	// yet", which GET /v1/workstreams treats as an honest empty, never an error.
	RemoteWorkstreams func() []settings.RemoteWorkstream
}

// NewStore builds a Store rooted at path. The file is not created until the
// first Save.
func NewStore(path string) *Store { return &Store{path: path} }

// Path returns the file this Store reads and writes.
func (s *Store) Path() string { return s.path }

// Load returns the current document.
func (s *Store) Load() (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Load(s.path)
}

// Save persists d.
func (s *Store) Save(d Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Save(s.path, d)
}

// Update reads the current document, applies fn, and — if fn succeeds —
// persists the result, all under the same lock, so a read-modify-write from
// one HTTP request can never interleave with another's.
func (s *Store) Update(fn func(Document) (Document, error)) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := Load(s.path)
	if err != nil {
		return Document{}, err
	}
	next, err := fn(d)
	if err != nil {
		return Document{}, err
	}
	if err := Save(s.path, next); err != nil {
		return Document{}, err
	}
	return next, nil
}
