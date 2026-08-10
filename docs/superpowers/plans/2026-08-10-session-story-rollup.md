# Session Story Rollup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace embedding the previous digest whole with three inputs separated by truth-status — a measured session record, a ladder of sampled paraphrases, and the recent window — and bound the expensive digest call by wall clock.

**Architecture:** Each refinement emits a short `story` paraphrase of the report it produced, persisted alongside the snapshot. The next refinement receives a chronological ladder of those paraphrases sampled at turning points, a deterministic session record spanning the whole session, the retain-list of named specifics, and the recent window. The stored digest is never regenerated from a paraphrase (store full, feed compressed). A wall-clock floor rate-limits generation while the deterministic record stays current every window.

**Tech Stack:** Go (host toolchain, `CGO_ENABLED=0`), `modernc.org/sqlite`, `llama-server` OpenAI-compatible endpoint with `response_format: json_schema`, Qwen3-4B-Instruct-2507 Q4_K_M.

Spec: `docs/superpowers/specs/2026-08-10-session-story-rollup-design.md`

## Global Constraints

- Scope is **one interactive session**. No cross-session or per-project tier.
- **Store full, feed compressed.** No code path may regenerate a stored digest from a `story`.
- Privacy invariant: transcripts are read locally; only verified substrings enter the record. No raw prose is transmitted or stored outside `~/.keld`.
- `story` cap **600 runes** (recent, full) and **140 runes** (ladder middles). Ladder cap **8 entries**.
- Session record caps: `projects` **5**, `subjects` **12**.
- `KELD_DIGEST_MIN_INTERVAL` default **1h**, applies to every trigger reason including finalisation.
- Existing caps unchanged in this plan: `DefaultSynopsisCap 650`, `DefaultProseCap 900`, `DefaultHappenedCap 1400`, `DefaultStructureCap 1600`, `DefaultListCap 12`.
- `DefaultPromptCharBudget` and `ctx` may come **down** once the carried digest is gone — measure, do not assume.
- Study package only (`internal/agent/enrich/llmstudy`). Nothing here is wired into the daemon.
- Live model tests are `//go:build llmstudy` and require `DIGEST_URL`. Unit tests must pass with no model.
- `go test ./...` must stay green at every commit.

---

### Task 1: Session record — the measured spine

**Files:**
- Create: `internal/agent/enrich/llmstudy/session_record.go`
- Create: `internal/agent/enrich/llmstudy/session_record_test.go`

**Interfaces:**
- Consumes: `Window`, `Signals`, `Extract`, `Outcome` (existing), `distinctiveToken` from `digest_recency.go`, `VerifyTopics` from `topics.go`.
- Produces: `type SessionRecord`, `(SessionRecord) Observe(w Window, s Signals) SessionRecord`, `(SessionRecord) WithFocus(domain, function string, concentration float64) SessionRecord`, `(SessionRecord) NoteTurningPoint(seq int, reason TriggerReason) SessionRecord`, `(SessionRecord) Block() string`, `(SessionRecord) Populated() []string`.

- [ ] **Step 1: Write the failing test**

```go
package llmstudy

import (
	"strings"
	"testing"
)

func TestSessionRecordAccumulatesAcrossWindows(t *testing.T) {
	var r SessionRecord
	w1 := Window{Turns: []Turn{{RoleUser, "reconcile the Meridian ledger"}, {RoleTool, "Read bank-mar.csv"}}}
	w2 := Window{Turns: []Turn{{RoleUser, "now post the Larkin accrual"}, {RoleTool, "Write journals/mar-adj-04.csv"}}}
	r = r.Observe(w1, Extract(w1)).WithProject("meridian")
	r = r.Observe(w2, Extract(w2)).WithProject("meridian")

	if r.Turns != 4 {
		t.Errorf("turns must span the session, got %d", r.Turns)
	}
	if len(r.Projects) != 1 || r.Projects[0] != "meridian" {
		t.Errorf("projects must dedupe, got %v", r.Projects)
	}
	subs := strings.Join(r.Subjects, " ")
	if !strings.Contains(subs, "Meridian") || !strings.Contains(subs, "Larkin") {
		t.Errorf("subjects from BOTH windows must accumulate, got %v", r.Subjects)
	}
}

// A term may only enter by appearing verbatim in the transcript. Plausibility is how a
// fabricated specific would get in.
func TestSessionRecordSubjectsAreVerbatimOnly(t *testing.T) {
	w := Window{Turns: []Turn{{RoleUser, "reconcile the Meridian ledger"}}}
	r := SessionRecord{}.Observe(w, Extract(w))
	for _, s := range r.Subjects {
		if !strings.Contains("reconcile the Meridian ledger", s) {
			t.Errorf("subject %q is not a substring of the source", s)
		}
	}
}

// Bounded, or "minimal" stops being true.
func TestSessionRecordIsBounded(t *testing.T) {
	r := SessionRecord{}
	for i := 0; i < 40; i++ {
		w := Window{Turns: []Turn{{RoleUser, "touching ComponentNumber" + string(rune('A'+i%26)) + " today"}}}
		r = r.Observe(w, Extract(w)).WithProject("proj" + string(rune('a'+i%9)))
	}
	if len(r.Subjects) > MaxRecordSubjects {
		t.Errorf("subjects unbounded: %d", len(r.Subjects))
	}
	if len(r.Projects) > MaxRecordProjects {
		t.Errorf("projects unbounded: %d", len(r.Projects))
	}
}

// Turning points are facts, so a shift is recoverable rather than inferred from prose.
func TestSessionRecordRecordsTurningPoints(t *testing.T) {
	r := SessionRecord{}.
		NoteTurningPoint(2, TriggerFocusShift).
		NoteTurningPoint(3, TriggerVolume).
		NoteTurningPoint(5, TriggerFriction)
	if len(r.TurningPoints) != 2 {
		t.Fatalf("only shift and friction are turning points, got %v", r.TurningPoints)
	}
	if r.TurningPoints[0].Seq != 2 || r.TurningPoints[1].Seq != 5 {
		t.Errorf("wrong points recorded: %v", r.TurningPoints)
	}
}

// An absent field must read as absent. Topics read empty for months because nothing said a
// pass had never run.
func TestSessionRecordReportsWhichFieldsArePopulated(t *testing.T) {
	w := Window{Turns: []Turn{{RoleUser, "reconcile the Meridian ledger"}}}
	r := SessionRecord{}.Observe(w, Extract(w))
	if got := strings.Join(r.Populated(), ","); strings.Contains(got, "focus") {
		t.Errorf("focus must not report as populated before classification: %s", got)
	}
	r = r.WithFocus("finance", "accounting", 0.8)
	if got := strings.Join(r.Populated(), ","); !strings.Contains(got, "focus") {
		t.Errorf("focus must report as populated once set: %s", got)
	}
	if !strings.Contains(r.Block(), "focus:") {
		t.Error("Block must render a populated focus")
	}
	if strings.Contains(SessionRecord{}.Block(), "focus:") {
		t.Error("Block must omit an unpopulated focus rather than showing it empty")
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run SessionRecord`
Expected: FAIL, `undefined: SessionRecord`.

- [ ] **Step 3: Implement**

```go
package llmstudy

import (
	"fmt"
	"sort"
	"strings"
)

// Bounds. Every list is capped, or the record stops being minimal.
const (
	MaxRecordProjects = 5
	MaxRecordSubjects = 12
)

// TurningPoint is a digest that fired because the work changed direction.
type TurningPoint struct {
	Seq    int           `json:"seq"`
	Reason TriggerReason `json:"reason"`
}

// SessionRecord is the measured, session-spanning spine.
//
// It is the only authoritative input to a digest: the story ladder is model-paraphrased and
// the window is raw evidence. Every field here is either counted or verified verbatim against
// the transcript, so prose can be held against it.
//
// It also replaces a broken anchor. DigestFacts is window-scoped, and its Topics/Entities —
// the intended session-spanning view — come from WithEnrichment, a classification pass the
// digest path never makes, so they were empty in every measurement taken.
type SessionRecord struct {
	Projects []string `json:"projects"`
	Subjects []string `json:"subjects"` // verbatim-verified terms, by frequency
	Tools    []ToolCount `json:"tools"`

	Turns          int `json:"turns"`
	UserTurns      int `json:"user_turns"`
	ToolCalls      int `json:"tool_calls"`
	Corrections    int `json:"corrections"`

	Domain        string  `json:"domain"`
	Function      string  `json:"function"`
	Concentration float64 `json:"concentration"`
	hasFocus      bool

	TurningPoints []TurningPoint `json:"turning_points"`

	freq map[string]int
}

// Observe folds one window's measured signals into the record. Deterministic and free of any
// model, so it runs every window regardless of the digest rate limit — a reader always sees
// current counts beside an older narrative.
func (r SessionRecord) Observe(w Window, s Signals) SessionRecord {
	r.Turns += s.Turns
	r.UserTurns += s.UserTurns
	r.ToolCalls += s.ToolCalls
	r.Corrections += s.Corrections
	r.Tools = mergeToolCounts(r.Tools, w)

	if r.freq == nil {
		r.freq = map[string]int{}
	}
	src := Render(w)
	for _, t := range w.Turns {
		for _, tok := range subjectTokens(t.Text) {
			if !distinctiveToken(tok) {
				continue
			}
			// Verbatim gate: a term enters only by appearing in the source, never by being
			// plausible. Same rule the publish-side topic gate uses.
			if kept, _ := VerifyTopics([]string{tok}, src); len(kept) == 0 {
				continue
			}
			r.freq[tok]++
		}
	}
	r.Subjects = topByFrequency(r.freq, MaxRecordSubjects)
	return r
}

// WithProject records where the work is happening, most recent first, deduplicated.
func (r SessionRecord) WithProject(p string) SessionRecord {
	p = strings.TrimSpace(p)
	if p == "" {
		return r
	}
	out := []string{p}
	for _, x := range r.Projects {
		if x != p {
			out = append(out, x)
		}
	}
	if len(out) > MaxRecordProjects {
		out = out[:MaxRecordProjects]
	}
	r.Projects = out
	return r
}

// WithFocus attaches the EWMA focus. Separate from Observe because it depends on the
// classification pipeline, which may not be running.
func (r SessionRecord) WithFocus(domain, function string, concentration float64) SessionRecord {
	r.Domain, r.Function, r.Concentration, r.hasFocus = domain, function, concentration, true
	return r
}

// NoteTurningPoint records a digest that fired because direction changed. Volume and
// unsettled are not turning points — steady progress is not a change of direction.
func (r SessionRecord) NoteTurningPoint(seq int, reason TriggerReason) SessionRecord {
	if reason != TriggerFocusShift && reason != TriggerFriction {
		return r
	}
	r.TurningPoints = append(r.TurningPoints, TurningPoint{Seq: seq, Reason: reason})
	return r
}

// Populated names the fields that actually hold measured data, so an absent field reads as
// absent rather than as an empty one.
func (r SessionRecord) Populated() []string {
	var out []string
	if r.Turns > 0 {
		out = append(out, "counts")
	}
	if len(r.Projects) > 0 {
		out = append(out, "projects")
	}
	if len(r.Subjects) > 0 {
		out = append(out, "subjects")
	}
	if r.hasFocus {
		out = append(out, "focus")
	}
	if len(r.TurningPoints) > 0 {
		out = append(out, "turning_points")
	}
	return out
}

// Block renders the record for a prompt. Omits what is not populated.
func (r SessionRecord) Block() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("counts: turns=%d user_turns=%d tool_calls=%d corrections=%d\n",
		r.Turns, r.UserTurns, r.ToolCalls, r.Corrections))
	if len(r.Projects) > 0 {
		b.WriteString("projects: " + strings.Join(r.Projects, ", ") + "\n")
	}
	if r.hasFocus {
		b.WriteString(fmt.Sprintf("focus: domain=%s function=%s (settled %.0f%%)\n",
			orNone(r.Domain), orNone(r.Function), r.Concentration*100))
	}
	if len(r.Tools) > 0 {
		parts := make([]string, 0, len(r.Tools))
		for i, t := range r.Tools {
			if i == 6 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s x%d", t.Name, t.Count))
		}
		b.WriteString("tool profile: " + strings.Join(parts, ", ") + "\n")
	}
	if len(r.Subjects) > 0 {
		b.WriteString("recurring subjects: " + strings.Join(r.Subjects, ", ") + "\n")
	}
	if len(r.TurningPoints) > 0 {
		parts := make([]string, 0, len(r.TurningPoints))
		for _, tp := range r.TurningPoints {
			parts = append(parts, fmt.Sprintf("#%d %s", tp.Seq, tp.Reason))
		}
		b.WriteString("direction changed at: " + strings.Join(parts, ", ") + "\n")
	}
	return b.String()
}

// topByFrequency returns the n most frequent terms, ties broken alphabetically so the record
// is stable across runs.
func topByFrequency(freq map[string]int, n int) []string {
	keys := make([]string, 0, len(freq))
	for k := range freq {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if freq[keys[i]] != freq[keys[j]] {
			return freq[keys[i]] > freq[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > n {
		keys = keys[:n]
	}
	return keys
}

// mergeToolCounts folds a window's tool usage into a session-spanning profile.
func mergeToolCounts(prev []ToolCount, w Window) []ToolCount {
	counts := map[string]int{}
	for _, t := range prev {
		counts[t.Name] = t.Count
	}
	for _, t := range w.Turns {
		if t.Role != RoleTool {
			continue
		}
		n := 1
		if m := runSuffix.FindStringSubmatch(t.Text); m != nil {
			if v, err := strconv.Atoi(m[1]); err == nil {
				n = v
			}
		}
		counts[toolName(t.Text)] += n
	}
	out := make([]ToolCount, 0, len(counts))
	for name, c := range counts {
		out = append(out, ToolCount{Name: name, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}
```

Add `"strconv"` to the import block.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/enrich/llmstudy/ -run SessionRecord -v`
Expected: all five PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/session_record.go internal/agent/enrich/llmstudy/session_record_test.go
git commit -m "feat(digest): add the measured session record

Session-spanning, deterministic, bounded. The only authoritative digest input: the story
ladder is model-paraphrased and the window is raw evidence. Subjects enter only by the
verbatim gate, so a term cannot arrive by being plausible, and Populated() names what
actually holds data so an absent focus does not read as an empty one — the failure that let
DigestFacts.Topics read empty for months."
```

---

### Task 2: Persist the paraphrase and the record

**Files:**
- Modify: `internal/agent/enrich/llmstudy/digeststore/store.go`
- Modify: `internal/agent/enrich/llmstudy/digeststore/store_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `Record.Story string`; `(*Store) PutSessionRecord(sessionID string, seq int, body string) error`; `(*Store) SessionRecord(sessionID string) (string, int, bool, error)`.

- [ ] **Step 1: Write the failing test**

```go
func TestStoryIsPersistedWithTheSnapshot(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Put(Record{SessionID: "a", Seq: 1, Body: "{}", Signals: "{}",
		Story: "work was on the March close; specifically the bank rec"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Latest("a")
	if err != nil || !ok {
		t.Fatalf("latest: %v %v", ok, err)
	}
	if got.Story == "" {
		t.Error("story did not survive the round trip")
	}
}

// The record is current state, not history: one row per session, overwritten.
func TestSessionRecordIsOverwrittenNotAppended(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutSessionRecord("a", 1, `{"turns":10}`); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSessionRecord("a", 2, `{"turns":25}`); err != nil {
		t.Fatal(err)
	}
	body, seq, ok, err := s.SessionRecord("a")
	if err != nil || !ok {
		t.Fatalf("read: %v %v", ok, err)
	}
	if seq != 2 || !strings.Contains(body, "25") {
		t.Errorf("want the latest state, got seq=%d body=%s", seq, body)
	}
}

func TestUnknownSessionRecordIsNotAnError(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "d.db"))
	defer s.Close()
	if _, _, ok, err := s.SessionRecord("nope"); err != nil || ok {
		t.Errorf("want (false, nil) for an unknown session, got ok=%v err=%v", ok, err)
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./internal/agent/enrich/llmstudy/digeststore/`
Expected: FAIL, `unknown field Story` / `undefined: PutSessionRecord`.

- [ ] **Step 3: Implement**

In `schema`, add `story` to the `digest` table and a new table. There is no deployed data, so
the column is declared rather than migrated:

```sql
CREATE TABLE IF NOT EXISTS digest (
  ...
  story          TEXT    NOT NULL DEFAULT '',
  ...
);
-- Current state, not history: one row per session, overwritten. Digests are snapshots
-- because their prose is a record; the session record is measured state.
CREATE TABLE IF NOT EXISTS session_record (
  session_id  TEXT    NOT NULL PRIMARY KEY,
  updated_seq INTEGER NOT NULL,
  body        TEXT    NOT NULL
);
```

Add `Story string` to `Record` with the comment:

```go
	// Story is the paraphrase this snapshot produced, persisted so the ladder replays what
	// was ACTUALLY used rather than something re-derived by a later model.
	Story string
```

Extend `selectCols`, `scanRec`, and the `Put` insert/update to carry `story`, then:

```go
// PutSessionRecord overwrites the measured state for a session.
func (s *Store) PutSessionRecord(sessionID string, seq int, body string) error {
	if sessionID == "" {
		return fmt.Errorf("digeststore: session_id required")
	}
	_, err := s.db.Exec(`
INSERT INTO session_record (session_id, updated_seq, body) VALUES (?,?,?)
ON CONFLICT(session_id) DO UPDATE SET updated_seq=excluded.updated_seq, body=excluded.body`,
		sessionID, seq, body)
	return err
}

// SessionRecord returns the measured state and the digest seq that last consumed it. An
// unknown session is the normal first-digest case, not an error.
func (s *Store) SessionRecord(sessionID string) (body string, seq int, ok bool, err error) {
	row := s.db.QueryRow(`SELECT body, updated_seq FROM session_record WHERE session_id = ?`, sessionID)
	if err := row.Scan(&body, &seq); err == sql.ErrNoRows {
		return "", 0, false, nil
	} else if err != nil {
		return "", 0, false, err
	}
	return body, seq, true, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/enrich/llmstudy/digeststore/ -v`
Expected: all PASS, including the pre-existing round-trip and history tests.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/digeststore/
git commit -m "feat(digeststore): persist the paraphrase and the session record

story rides each snapshot so the ladder replays what was actually used rather than a later
re-derivation. session_record is one overwritten row per session because it is current
measured state, not history."
```

---

### Task 3: `story` on the refinement schema

**Files:**
- Modify: `internal/agent/enrich/llmstudy/digest_refine.go`
- Modify: `internal/agent/enrich/llmstudy/digest_synopsis_test.go`

**Interfaces:**
- Consumes: `DigestUpdateSchema`, `digestUpdate`, `callValid`.
- Produces: `digestUpdate.Story string`; `StoryCap`, `LadderEntryCap` constants; `(*Llama) RefineDigestWithReason` returns `(Digest, string, error)` — the digest and its paraphrase.

- [ ] **Step 1: Write the failing test**

```go
func TestRefinementSchemaRequiresAStory(t *testing.T) {
	sc := DigestUpdateSchema()
	props := sc["properties"].(map[string]any)
	if props["story"] == nil {
		t.Fatal("refinement schema has no story field")
	}
	var found bool
	for _, r := range sc["required"].([]string) {
		if r == "story" {
			found = true
		}
	}
	if !found {
		t.Error("story must be required, so every refinement produces its own handover")
	}
	// Machinery, not report content: it must not appear on the stored digest.
	if base := DigestSchema()["properties"].(map[string]any); base["story"] != nil {
		t.Error("the base digest schema must not carry story")
	}
}

func TestUpdatePromptAsksForTheParaphrase(t *testing.T) {
	p := DigestUpdatePromptWithReason(Digest{Done: "x"}, "work session", "user: next\n", "", "counts: turns=2\n", TriggerNone)
	for _, want := range []string{"story", "themes rather than actions"} {
		if !strings.Contains(p, want) {
			t.Errorf("refine prompt omits %q", want)
		}
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run "RequiresAStory|AsksForTheParaphrase"`
Expected: FAIL, `refinement schema has no story field`.

- [ ] **Step 3: Implement**

```go
const (
	// StoryCap bounds the most recent paraphrase; LadderEntryCap the older ones. The ladder is
	// cheaper than the 4,742-character embedded digest it replaces: 600 + 7x140 is ~1,580.
	StoryCap       = 600
	LadderEntryCap = 140
)
```

Add to `digestUpdate`:

```go
	// Story is a paraphrase of the report just produced, for the NEXT refinement to read.
	// Kept off Digest: it is machinery, and a reader wants the report, not a summary of it.
	Story string `json:"story"`
```

In `DigestUpdateSchema`, add `props["story"] = map[string]any{"type": "string", "minLength": digestMinProse}` and append `"story"` to `required`.

Append to `updateRules`:

```
  - story: a short paraphrase of the report you just wrote, for the next update to read as
    context. Name what the work has been about at the grain of THEMES rather than actions,
    where it now stands, and where it is heading. Three sentences at most. It is context, not
    a section of the report.
```

Change the signature so callers receive the paraphrase, and cap it:

```go
func (l *Llama) RefineDigestWithReason(prev Digest, sessionLabel, newTurns, sessionView, facts string, why TriggerReason) (Digest, string, error)
```

returning `CapSections(...), clipProse(up.Story, StoryCap), nil`. Update `RefineDigest` and
`RefineDigestWithView` to discard the second value so existing callers keep compiling.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/enrich/llmstudy/ -run "Story|Paraphrase" -v` then `go test ./...`
Expected: PASS, whole suite green.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/
git commit -m "feat(digest): every refinement emits its own paraphrase

A required story field on the refinement schema only, capped at 600 runes and kept off
Digest because it is machinery rather than report content. Produced in the same call, so
compression costs no extra inference."
```

---

### Task 4: The story ladder, sampled at turning points

**Files:**
- Create: `internal/agent/enrich/llmstudy/story_ladder.go`
- Create: `internal/agent/enrich/llmstudy/story_ladder_test.go`

**Interfaces:**
- Consumes: `TurningPoint`, `TriggerReason`, `clipProse`, `StoryCap`, `LadderEntryCap`.
- Produces: `type LadderEntry struct{ Seq int; Reason TriggerReason; Text string }`; `BuildLadder(stories []LadderEntry, max int) []LadderEntry`; `RenderLadder(entries []LadderEntry) string`; `MaxLadderEntries`.

- [ ] **Step 1: Write the failing test**

```go
package llmstudy

import (
	"strings"
	"testing"
)

func entries(n int, shiftAt ...int) []LadderEntry {
	shift := map[int]bool{}
	for _, s := range shiftAt {
		shift[s] = true
	}
	var out []LadderEntry
	for i := 1; i <= n; i++ {
		reason := TriggerVolume
		if shift[i] {
			reason = TriggerFocusShift
		}
		out = append(out, LadderEntry{Seq: i, Reason: reason, Text: "story number " + string(rune('a'+i-1))})
	}
	return out
}

// The first entry is where the work started — what lets the account say what the current
// work grew out of. Dropping it is how a session loses its own origin.
func TestLadderAlwaysKeepsTheFirstAndLast(t *testing.T) {
	got := BuildLadder(entries(20), 5)
	if got[0].Seq != 1 {
		t.Errorf("first entry dropped: %v", got[0])
	}
	if got[len(got)-1].Seq != 20 {
		t.Errorf("most recent entry dropped: %v", got[len(got)-1])
	}
	if len(got) > 5 {
		t.Errorf("cap exceeded: %d", len(got))
	}
}

// Turning points are the trajectory. Evenly spaced samples would mostly capture steady
// progress.
func TestLadderPrefersTurningPoints(t *testing.T) {
	got := BuildLadder(entries(20, 7, 13), 5)
	var seqs []int
	for _, e := range got {
		seqs = append(seqs, e.Seq)
	}
	for _, want := range []int{7, 13} {
		var found bool
		for _, s := range seqs {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Errorf("turning point %d missing from %v", want, seqs)
		}
	}
}

// A steady session still needs its shape, so spacing is the fallback.
func TestLadderFallsBackToEvenSpacing(t *testing.T) {
	got := BuildLadder(entries(20), 5)
	if len(got) != 5 {
		t.Fatalf("want the cap filled by spacing, got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq <= got[i-1].Seq {
			t.Errorf("ladder must be chronological: %v", got)
		}
	}
}

func TestLadderIsChronologicalAndUnique(t *testing.T) {
	got := BuildLadder(entries(12, 3, 4, 5, 6, 7, 8, 9, 10), 4)
	seen := map[int]bool{}
	for i, e := range got {
		if seen[e.Seq] {
			t.Errorf("duplicate seq %d", e.Seq)
		}
		seen[e.Seq] = true
		if i > 0 && e.Seq <= got[i-1].Seq {
			t.Errorf("out of order: %v", got)
		}
	}
}

// Only the newest entry gets the full budget.
func TestRenderLadderClipsOlderEntriesHarder(t *testing.T) {
	long := strings.Repeat("word ", 400)
	out := RenderLadder([]LadderEntry{
		{Seq: 1, Reason: TriggerFirst, Text: long},
		{Seq: 9, Reason: TriggerVolume, Text: long},
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want one line per entry, got %d", len(lines))
	}
	if len([]rune(lines[0])) > LadderEntryCap+40 {
		t.Errorf("older entry not clipped: %d runes", len([]rune(lines[0])))
	}
	if len([]rune(lines[1])) <= LadderEntryCap {
		t.Errorf("newest entry should carry the larger budget: %d runes", len([]rune(lines[1])))
	}
}

func TestEmptyLadderRendersNothing(t *testing.T) {
	if RenderLadder(nil) != "" {
		t.Error("an empty ladder must render nothing, not a header")
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run Ladder`
Expected: FAIL, `undefined: LadderEntry`.

- [ ] **Step 3: Implement**

```go
package llmstudy

import (
	"fmt"
	"sort"
	"strings"
)

// MaxLadderEntries caps the ladder. Worst case 600 + 7x140 is ~1,580 runes against the 4,742
// the embedded digest cost, so the ladder is cheaper than what it replaces while covering the
// whole session rather than one step of it.
const MaxLadderEntries = 8

// LadderEntry is one stored paraphrase and why its digest fired.
type LadderEntry struct {
	Seq    int
	Reason TriggerReason
	Text   string
}

// BuildLadder selects which paraphrases a refinement reads.
//
// Sampled at TURNING POINTS rather than evenly. A trajectory is made of the moments direction
// changed, and even spacing across a long session mostly captures steady progress. Even
// spacing remains the fallback so a steady session still shows its shape.
//
// The first entry is always kept: it is where the work started, and it is what lets the
// account say what the current work grew out of.
func BuildLadder(stories []LadderEntry, max int) []LadderEntry {
	if max <= 0 {
		max = MaxLadderEntries
	}
	if len(stories) <= max {
		return stories
	}
	sort.Slice(stories, func(i, j int) bool { return stories[i].Seq < stories[j].Seq })

	pick := map[int]bool{0: true, len(stories) - 1: true}
	// Turning points first, newest-first so the most relevant survive the cap.
	for i := len(stories) - 2; i > 0 && len(pick) < max; i-- {
		if stories[i].Reason == TriggerFocusShift || stories[i].Reason == TriggerFriction {
			pick[i] = true
		}
	}
	// Even spacing fills whatever the cap leaves.
	if len(pick) < max {
		step := float64(len(stories)-1) / float64(max-1)
		for k := 1; k < max-1 && len(pick) < max; k++ {
			pick[int(float64(k)*step)] = true
		}
	}
	idx := make([]int, 0, len(pick))
	for i := range pick {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	out := make([]LadderEntry, 0, len(idx))
	for _, i := range idx {
		out = append(out, stories[i])
	}
	return out
}

// RenderLadder renders the ladder oldest-first, with only the newest entry at full budget.
func RenderLadder(entries []LadderEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	for i, e := range entries {
		cap := LadderEntryCap
		if i == len(entries)-1 {
			cap = StoryCap
		}
		note := ""
		if e.Reason == TriggerFocusShift || e.Reason == TriggerFriction {
			note = fmt.Sprintf(" (%s)", e.Reason)
		}
		b.WriteString(fmt.Sprintf("[%d]%s %s\n", e.Seq, note, clipProse(oneLine(e.Text), cap)))
	}
	return b.String()
}

// oneLine flattens a paraphrase so one entry stays one line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/enrich/llmstudy/ -run Ladder -v`
Expected: all six PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/story_ladder.go internal/agent/enrich/llmstudy/story_ladder_test.go
git commit -m "feat(digest): story ladder sampled at turning points

A refinement reads several paraphrases across the session, not just the last one — a chain
where each step sees one step back is how drift compounds unnoticed. Sampled at focus shifts
and friction because that is what a trajectory is made of, with even spacing as the fallback,
and the first entry always kept so a session cannot lose its own origin."
```

---

### Task 5: Rewire the refinement prompt

**Files:**
- Modify: `internal/agent/enrich/llmstudy/digest_refine.go`
- Modify: `internal/agent/enrich/llmstudy/digest_refine_test.go`
- Modify: `internal/agent/enrich/llmstudy/digest_fit.go`

**Interfaces:**
- Consumes: `SessionRecord.Block`, `RenderLadder`, `Identifiers`.
- Produces: `DigestUpdatePromptFrom(prev Digest, in RefineInput) string`; `type RefineInput struct{ SessionLabel string; Record SessionRecord; Ladder []LadderEntry; SessionView, NewTurns string; Why TriggerReason }`; `(*Llama) RefineFrom(prev Digest, in RefineInput) (Digest, string, error)`. `CarryForward` deleted.

- [ ] **Step 1: Write the failing test**

```go
func TestRefinePromptCarriesNoPriorProse(t *testing.T) {
	prev := Digest{
		Synopsis: "a month-end close for Meridian", Done: "UNIQUEDONETOKEN posted",
		Happened: "UNIQUEHAPPENEDTOKEN went wrong", Structure: "UNIQUESTRUCTURETOKEN",
		Current: "x", Why: "y", Next: "z",
		Insights: []string{"UNIQUEINSIGHT"}, Unresolved: []string{"UNIQUEOPEN"},
	}
	in := RefineInput{
		SessionLabel: "work session",
		Record:       SessionRecord{Turns: 20}.WithProject("meridian"),
		Ladder:       []LadderEntry{{Seq: 1, Reason: TriggerFirst, Text: "work began on the March close"}},
		NewTurns:     "user: now do April\n",
	}
	p := DigestUpdatePromptFrom(prev, in)

	// The paraphrase replaces the prose. None of the report's body may be embedded.
	for _, tok := range []string{"UNIQUEDONETOKEN", "UNIQUEHAPPENEDTOKEN", "UNIQUESTRUCTURETOKEN"} {
		if strings.Contains(p, tok) {
			t.Errorf("prior prose %q is still embedded", tok)
		}
	}
	// The specifics and open items must still be handed over.
	if !strings.Contains(p, "Meridian") {
		t.Error("retain-list of named specifics is missing")
	}
	if !strings.Contains(p, "UNIQUEOPEN") {
		t.Error("prior open items must still be accounted for")
	}
	if !strings.Contains(p, "March close") {
		t.Error("the ladder is missing")
	}
	if !strings.Contains(p, "turns=20") {
		t.Error("the measured record is missing")
	}
}

// The measured record comes first: everything after it is indicative or evidence.
func TestRecordPrecedesNarrative(t *testing.T) {
	in := RefineInput{
		SessionLabel: "work session",
		Record:       SessionRecord{Turns: 9},
		Ladder:       []LadderEntry{{Seq: 1, Text: "LADDERMARK"}},
		NewTurns:     "user: WINDOWMARK\n",
	}
	p := DigestUpdatePromptFrom(Digest{Done: "x"}, in)
	rec, lad, win := strings.Index(p, "turns=9"), strings.Index(p, "LADDERMARK"), strings.Index(p, "WINDOWMARK")
	if !(rec < lad && lad < win) {
		t.Errorf("want record < ladder < window, got %d %d %d", rec, lad, win)
	}
}

// The no-shrink rule contradicts deliberate compression and must be gone.
func TestNoShrinkRuleIsRemoved(t *testing.T) {
	p := DigestUpdatePromptFrom(Digest{Done: "x"}, RefineInput{SessionLabel: "s", NewTurns: "user: hi\n"})
	for _, gone := range []string{"must not become shorter", "Refinement ADDS"} {
		if strings.Contains(p, gone) {
			t.Errorf("the no-shrink rule survives: %q", gone)
		}
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run "CarriesNoPriorProse|RecordPrecedes|NoShrink"`
Expected: FAIL, `undefined: RefineInput`.

- [ ] **Step 3: Implement**

Delete `CarryForward` and its test. Delete the no-shrink clause from `updateRules`. Delete
`clipSessionViewFor`'s priority arithmetic if it becomes dead. Then:

```go
// RefineInput is everything a refinement reads, grouped by truth-status: the record is
// measured, the ladder is paraphrased, the window is evidence.
type RefineInput struct {
	SessionLabel string
	Record       SessionRecord
	Ladder       []LadderEntry
	SessionView  string
	NewTurns     string
	Why          TriggerReason
}

// DigestUpdatePromptFrom builds the refinement prompt.
//
// No prior prose is embedded. The previous report reaches the model as a paraphrase ladder
// plus a deterministic retain-list — "the work has been on X, Y, Z; specifically A, B, C" —
// which is what allows compression to be deliberate instead of forbidden.
func DigestUpdatePromptFrom(prev Digest, in RefineInput) string {
	var b strings.Builder
	b.WriteString("You are updating a report on a work session, for the person doing the work and for a manager who was not present.\n\n")
	b.WriteString("Session context: ")
	b.WriteString(in.SessionLabel)

	// Measured first. Everything after this is indicative or evidence, and a model shown
	// authoritative counts first holds its prose consistent with them.
	b.WriteString("\n\nSESSION RECORD (measured — authoritative):\n")
	b.WriteString(in.Record.Block())
	if pop := in.Record.Populated(); len(pop) > 0 {
		b.WriteString("populated fields: " + strings.Join(pop, ", ") + "\n")
	}

	if l := RenderLadder(in.Ladder); l != "" {
		b.WriteString("\nTHE STORY SO FAR, oldest first (paraphrased — indicative):\n")
		b.WriteString(l)
	}
	if named := Identifiers(prev); len(named) > 0 {
		b.WriteString("\nSPECIFICS ALREADY REPORTED (each must still appear, unless the new part shows it was wrong):\n  ")
		b.WriteString(strings.Join(named, ", "))
		b.WriteString("\n")
	}
	if open := priorOpenItems(prev); len(open) > 0 {
		b.WriteString("\nOPEN ITEMS FROM THAT REPORT — account for EVERY one, in exactly one place:")
		b.WriteString("\n  keep it in unresolved if it is still open, or name it in closed if the new")
		b.WriteString("\n  part resolved it. Do not silently drop one.\n  ")
		b.WriteString(strings.Join(open, "\n  "))
		b.WriteString("\n")
	}
	if v := clipSessionViewFor(in.SessionView, b.Len()+updateTailLen()); v != "" {
		b.WriteString("\nWHOLE SESSION, sampled from start to now (coarse):\n")
		b.WriteString(v)
	}
	b.WriteString("\nNEW PART OF THE CONVERSATION (evidence):\n")
	b.WriteString(fitTurns(in.NewTurns, b.Len()+updateTailLen()))
	b.WriteString("\nProduce the UPDATED report, same sections:\n")
	b.WriteString(digestSections)
	b.WriteString(updateRules)
	b.WriteString(digestRules)
	b.WriteString("\nRespond with JSON only.\n")
	return b.String()
}

// RefineFrom produces the next report and its paraphrase.
func (l *Llama) RefineFrom(prev Digest, in RefineInput) (Digest, string, error) {
	var up digestUpdate
	repair := func(d Digest) Digest { return dropStaleOpenItems(applyClosures(d, up.Closed)) }
	if err := l.callValid(DigestUpdatePromptFrom(prev, in), DigestUpdateSchema(), &up,
		func() error { return firstProblem(ValidateDigest(repair(up.Digest))) }); err != nil {
		return Digest{}, "", err
	}
	merged := mergeWithRetirement(prev, repair(up.Digest), up.Retired)
	return CapSections(merged, DefaultProseCap, DefaultListCap), clipProse(up.Story, StoryCap), nil
}
```

Keep the focus-shift anchor block, still gated on `in.Why == TriggerFocusShift`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/enrich/llmstudy/ -v 2>&1 | grep -E "^(--- FAIL|ok|FAIL)"` then `go test ./...`
Expected: PASS. Remove any test that asserted `CarryForward` behaviour.

- [ ] **Step 5: Measure whether the budget can come down**

The carried report drops from ~4,742 characters to ~1,580 plus the retain-list, and the
budget was raised to 11,000 with `ctx` 6144 only because of it.

```bash
DIGEST_URL=http://127.0.0.1:8099 go test -tags llmstudy -count=1 \
  ./internal/agent/enrich/llmstudy/ -run DigestRefineQuality -v -timeout 90m 2>&1 | grep -E "^T[0-9]|prompt chars"
```

Record the observed maximum prompt length. If it sits well under 9,000, lower
`DefaultPromptCharBudget` to 9,000 and re-measure at `ctx 5120`; keep whichever holds T1 at
100%. Do not lower both at once.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/enrich/llmstudy/
git commit -m "feat(digest): refine from record + ladder + retain-list, not embedded prose

Deletes CarryForward and the no-shrink rule, which contradicted deliberate compression and
were the cause of the currency-versus-durability trade-off: the only options were keep the
prose verbatim or let the model rewrite it freely. The measured record comes first because
everything after it is indicative or evidence."
```

---

### Task 6: Bound the expensive operation by wall clock

**Files:**
- Modify: `internal/agent/enrich/llmstudy/digest_trigger.go`
- Modify: `internal/agent/enrich/llmstudy/digest_trigger_test.go`

**Interfaces:**
- Consumes: `TriggerPolicy`, `TriggerState`, `TriggerReason`.
- Produces: `TriggerPolicy.MinInterval time.Duration`; `TriggerState.Now, LastDigestAt time.Time`, `TriggerState.PendingReason TriggerReason`; `strongerReason(a, b TriggerReason) TriggerReason`; `MinIntervalFromEnv() time.Duration`.

- [ ] **Step 1: Write the failing test**

```go
func TestFloorSuppressesAnEarlyRefresh(t *testing.T) {
	p := DefaultTriggerPolicy()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	s := TriggerState{
		HasDigest: true, TurnsSince: 20, Now: now,
		LastDigestAt: now.Add(-10 * time.Minute),
		FocusDomain:  "b", PrevFocusDomain: "a",
	}
	if ok, why := p.ShouldRefresh(s); ok {
		t.Errorf("fired %s inside the %v floor", why, p.MinInterval)
	}
}

// A suppressed reason is DEFERRED, not dropped: a focus shift ten minutes after a digest must
// still be the cause of the next one rather than being lost to a later volume trigger.
func TestSuppressedReasonIsCarried(t *testing.T) {
	p := DefaultTriggerPolicy()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	s := TriggerState{
		HasDigest: true, TurnsSince: 20, Now: now,
		LastDigestAt:  now.Add(-2 * time.Hour),
		PendingReason: TriggerFocusShift,
	}
	ok, why := p.ShouldRefresh(s)
	if !ok {
		t.Fatal("the floor has elapsed; it must fire")
	}
	if why != TriggerFocusShift {
		t.Errorf("want the carried reason, got %s", why)
	}
}

// The first digest is not rate-limited: there is nothing to be stale relative to.
func TestFirstDigestIgnoresTheFloor(t *testing.T) {
	p := DefaultTriggerPolicy()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	if ok, why := p.ShouldRefresh(TriggerState{Now: now, LastDigestAt: now}); !ok || why != TriggerFirst {
		t.Errorf("first digest was suppressed: ok=%v why=%s", ok, why)
	}
}

// The floor applies to finalisation too — a stopped session is not going anywhere.
func TestFloorAppliesToFinalisation(t *testing.T) {
	p := DefaultTriggerPolicy()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	s := TriggerState{HasDigest: true, TurnsSince: p.StaleTurns + 1, Now: now,
		LastDigestAt: now.Add(-time.Minute)}
	if ok, _ := p.ShouldRefresh(s); ok {
		t.Error("finalisation bypassed the floor")
	}
}

func TestStrongerReasonOrdering(t *testing.T) {
	if strongerReason(TriggerVolume, TriggerFocusShift) != TriggerFocusShift {
		t.Error("focus shift must outrank volume")
	}
	if strongerReason(TriggerFriction, TriggerUnsettled) != TriggerFriction {
		t.Error("friction must outrank unsettled")
	}
	if strongerReason(TriggerNone, TriggerVolume) != TriggerVolume {
		t.Error("any reason must outrank none")
	}
}

// A zero MinInterval must not silently disable the floor via a zero-value policy.
func TestDefaultPolicyHasAnHourFloor(t *testing.T) {
	if got := DefaultTriggerPolicy().MinInterval; got != time.Hour {
		t.Errorf("want a 1h floor, got %v", got)
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run "Floor|Suppressed|Stronger|HasAnHour"`
Expected: FAIL, `unknown field Now in struct literal`.

- [ ] **Step 3: Implement**

```go
// MinInterval is a wall-clock floor on the only expensive operation here.
//
// Turn-based triggers alone do not bound cost: a burst of activity satisfies MaxTurns
// repeatedly within minutes, and each firing is a full multi-section generation. The floor
// applies to every reason, finalisation included — a session that has stopped is not going
// anywhere, so producing its final account an hour later costs nothing.
//
// The cheap path is unaffected: the session record is deterministic and recomputed every
// window, so a reader sees current counts beside an older narrative.
MinInterval time.Duration
```

Add to `TriggerState`:

```go
	// Now and LastDigestAt drive the floor. Passed in rather than read from the clock so the
	// policy stays pure and testable.
	Now          time.Time
	LastDigestAt time.Time

	// PendingReason is the strongest reason the floor has already suppressed. Deferring
	// rather than dropping is what stops a focus shift shortly after a digest from being
	// reported later as mere volume.
	PendingReason TriggerReason
```

In `ShouldRefresh`, after the `TriggerFirst` check and before returning any other reason:

```go
	computed := p.reasonFor(s)
	effective := strongerReason(s.PendingReason, computed)
	if effective == TriggerNone {
		return false, TriggerNone
	}
	if p.MinInterval > 0 && !s.LastDigestAt.IsZero() &&
		s.Now.Sub(s.LastDigestAt) < p.MinInterval {
		// Suppressed, not dropped. The caller persists `effective` as PendingReason and the
		// periodic sweep re-evaluates — without a timer a session that goes quiet with a
		// pending reason would never fire.
		return false, TriggerNone
	}
	return true, effective
```

Move the existing reason cascade into `reasonFor`, and add:

```go
// strongerReason ranks reasons so a deferred cause is not downgraded while it waits.
func strongerReason(a, b TriggerReason) TriggerReason {
	rank := map[TriggerReason]int{
		TriggerNone: 0, TriggerVolume: 1, TriggerUnsettled: 2,
		TriggerFriction: 3, TriggerFocusShift: 4, TriggerStale: 5, TriggerFirst: 6,
	}
	if rank[a] >= rank[b] {
		return a
	}
	return b
}

// MinIntervalFromEnv reads KELD_DIGEST_MIN_INTERVAL, defaulting to an hour.
func MinIntervalFromEnv() time.Duration {
	if v := os.Getenv("KELD_DIGEST_MIN_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return time.Hour
}
```

Set `MinInterval: MinIntervalFromEnv()` in `DefaultTriggerPolicy`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/enrich/llmstudy/ -run Trigger -v` then `go test ./...`
Expected: PASS, including the pre-existing trigger tests.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/digest_trigger.go internal/agent/enrich/llmstudy/digest_trigger_test.go
git commit -m "feat(digest): wall-clock floor on generation, default 1h

Turn-based triggers do not bound cost — a burst satisfies MaxTurns repeatedly within
minutes. A suppressed reason is deferred rather than dropped, so a focus shift shortly after
a digest still becomes the cause of the next one; that requires the periodic sweep to
re-evaluate, since a session going quiet would never fire a pending reason. The
deterministic record is unaffected and stays current every window."
```

---

### Task 7: Metrics that match the new design

**Files:**
- Modify: `internal/agent/enrich/llmstudy/digest_check.go`
- Modify: `internal/agent/enrich/llmstudy/digest_recency.go`
- Modify: `internal/agent/enrich/llmstudy/digest_eval_test.go`
- Create: `internal/agent/enrich/llmstudy/digest_consistency_test.go`

**Interfaces:**
- Produces: `StoryContradictsRecord(story string, r SessionRecord) []string`; `FabricatedNext(d Digest, source string) []string`; `RetainedFacts` reads `ProseFields`.

- [ ] **Step 1: Write the failing test**

```go
package llmstudy

import (
	"strings"
	"testing"
)

// The first currency check independent of judgement: a story whose subject contradicts the
// measured record is wrong against a measurement, not an opinion.
func TestStoryContradictingTheRecordIsDetected(t *testing.T) {
	r := SessionRecord{Turns: 40}.WithProject("keld-signal").WithFocus("engineering", "software", 0.9)
	r.Subjects = []string{"digest", "synopsis", "threshold"}

	bad := StoryContradictsRecord("Work has focused on invoice reconciliation and vendor payment terms.", r)
	if len(bad) == 0 {
		t.Error("a story sharing nothing with the measured subjects was not flagged")
	}
	ok := StoryContradictsRecord("Work has focused on the digest and its thresholds in keld-signal.", r)
	if len(ok) > 0 {
		t.Errorf("a story consistent with the record was flagged: %v", ok)
	}
}

// Abstain when the record has nothing to check against.
func TestConsistencyAbstainsWithoutSubjects(t *testing.T) {
	if got := StoryContradictsRecord("anything at all", SessionRecord{Turns: 3}); len(got) > 0 {
		t.Errorf("a verdict was returned with no measured subjects: %v", got)
	}
}

// Observed in real output: a next inventing schema fields never discussed. T7 only inspects
// unresolved, so nothing caught it.
func TestFabricatedNextIsDetected(t *testing.T) {
	src := "user: add a synopsis section to the digest\nassistant: added it\n"
	d := Digest{Next: "Define a schema with fields for ToolName, CallID, InputPayload and Timestamp."}
	if got := FabricatedNext(d, src); len(got) == 0 {
		t.Error("invented specifics in next were not flagged")
	}
	d2 := Digest{Next: "Extend the synopsis section and re-measure the digest thresholds."}
	if got := FabricatedNext(d2, src); len(got) > 0 {
		t.Errorf("a grounded next was flagged: %v", got)
	}
}

// RetainedFacts hand-enumerated the prose fields and so never saw synopsis — the same defect
// ProseFields was introduced to fix.
func TestRetainedFactsSeesEverySection(t *testing.T) {
	d := Digest{Synopsis: "the Meridian close"}
	if got := RetainedFacts(d, []string{"Meridian"}); got != 1 {
		t.Errorf("a fact present only in synopsis was not counted: %d", got)
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run "Contradict|FabricatedNext|RetainedFactsSees"`
Expected: FAIL, `undefined: StoryContradictsRecord`.

- [ ] **Step 3: Implement**

In `digest_check.go`, replace the hand-written field list in `RetainedFacts` with
`append(ProseFields(after), append(after.Insights, after.Unresolved...)...)`.

In `digest_recency.go`:

```go
// StoryContradictsRecord reports a paraphrase whose subject is absent from the measured
// record. Abstains when the record holds too little to check against, because a verdict on
// thin evidence is how every earlier version of a check like this over-reported.
func StoryContradictsRecord(story string, r SessionRecord) []string {
	if len(r.Subjects) < minConsistencyEvidence || strings.TrimSpace(story) == "" {
		return nil
	}
	terms := distinctiveTerms(story)
	if len(terms) == 0 {
		return nil
	}
	hay := strings.ToLower(strings.Join(append(append([]string{}, r.Subjects...), r.Projects...), " "))
	for t := range terms {
		if strings.Contains(hay, t) || inflectionPresent(t, t, hay) {
			return nil // one grounded subject term is enough; this is a contradiction check
		}
	}
	out := make([]string, 0, len(terms))
	for t := range terms {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

const minConsistencyEvidence = 3

// FabricatedNext reports named specifics in `next` that the conversation never mentions.
// Observed in real output as a next inventing schema field names; T7 inspects only
// `unresolved`, so nothing caught it.
func FabricatedNext(d Digest, source string) []string {
	return UnverifiedIdentifiers(Digest{Next: d.Next}, source)
}
```

Add `T12` and `T13` to the sweep's log block, and change `T4` to score specifics survival
(it already calls `RetainedFacts`; the fix above is what makes it complete).

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/enrich/llmstudy/ -run "Contradict|Fabricated|Retained" -v` then `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/
git commit -m "feat(digest): consistency and fabricated-next thresholds

Story-versus-record consistency is the first currency check that does not depend on
judgement. FabricatedNext closes a hole seen in real output, where next invented schema
field names and T7 could not see it. Also fixes RetainedFacts, which hand-enumerated the
prose fields and so never saw synopsis."
```

---

### Task 8: Measure, then record what actually happened

**Files:**
- Modify: `internal/agent/enrich/llmstudy/digest_eval_test.go`
- Modify: `internal/agent/enrich/llmstudy/digest_dump_test.go`
- Modify: `docs/superpowers/plans/2026-08-07-conversational-dimensions-findings.md`

- [ ] **Step 1: Wire the harness to the new inputs**

Thread a `SessionRecord` through the sweep, accumulating with `Observe` per window and
`NoteTurningPoint` per digest; collect each returned `story` into `[]LadderEntry`; call
`RefineFrom`. Persist `story` and the record via the store so the ladder replays what was used.

- [ ] **Step 2: Run the stratified sweep**

```bash
KELD_STUDY_SESSION_ID=<this session id> DIGEST_SESSIONS=14 \
DIGEST_URL=http://127.0.0.1:8099 go test -tags llmstudy -count=1 \
  ./internal/agent/enrich/llmstudy/ -run DigestRefineQuality -v -timeout 90m
```

Expected thresholds: T1 100%, T2 ≤2%, T3 ≤10%, T4 ≥90%, T7 ≤10%, T8 ≤2%, T9 ≤5%, T10 ≤5%,
T11 ≤10%, T12 ≤5%, T13 ≤5%.

- [ ] **Step 3: Log the flagged items, never only the counts**

Every threshold must print what it flagged. Four metrics in this study reported large numbers
that were ordinary English, and none of them was visible in the rate.

- [ ] **Step 4: Record Part 7 in the findings doc**

State the measured before/after for T4 and T11 specifically — the prediction is that T11
improves *without* the recency anchor because the framing is no longer pinned by verbatim
prose, and that prediction is exactly the kind that failed for the anchor. If it does not
hold, say so and leave the anchor gated.

Also record the observed maximum prompt length and whether the budget and `ctx` came back
down.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/ docs/
git commit -m "docs(study): Part 7 — story rollup measured"
```

---

## Self-review

**Spec coverage.** Three inputs by truth-status (Tasks 1, 4, 5); store-full/feed-compressed
(Tasks 2, 3, 5); turning-point sampling (Task 4); wall-clock floor with deferral (Task 6); T4
change, consistency gate, fabricated `next` (Task 7); measurement (Task 8). The spec's
"session record can go stale in model-dependent fields" risk is covered by `Populated()` in
Task 1.

**Not covered, deliberately.** Cross-session rollup (out of scope). Usefulness (T5) — needs a
human reader; no task can produce it. Non-engineering accuracy — needs readable Cowork
transcripts.

**Type consistency.** `RefineDigestWithReason` gains a third return value in Task 3 and is
superseded by `RefineFrom` in Task 5; Task 5 removes the older wrappers once the harness moves
across in Task 8. `LadderEntry` is produced in Task 4 and consumed by `RefineInput` in Task 5.
`TurningPoint` (Task 1) and `LadderEntry.Reason` (Task 4) both key on `TriggerReason`.

**Risk to watch during execution.** Task 5 deletes `CarryForward` while Task 8 is what proves
the replacement works. If the sweep regresses T4 below 90%, the cause is most likely the
retain-list no longer being reinforced by embedded prose — increase the retain-list cap before
restoring any prose, since restoring prose reinstates the trade-off this plan exists to remove.
