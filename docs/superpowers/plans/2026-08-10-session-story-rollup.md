# Session Story Rollup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace embedding the previous digest whole with three inputs separated by truth-status — a measured session record, a ladder of sampled paraphrases, and the recent window — and bound the expensive digest call by wall clock.

**Architecture:** Two cadences. Every few user turns a cheap **beat** states in one to three sentences what the work is about, derived from the recent window plus a deterministic session record — never from another beat. The expensive nine-section **report** is wall-clock bounded and written from the sampled beat series, the record, and the retain-list of named specifics — never from its own predecessor. So no model output is ever input to a later generation of the same kind, and there is no chain along which drift can compound.

**Tech Stack:** Go (host toolchain, `CGO_ENABLED=0`), `modernc.org/sqlite`, `llama-server` OpenAI-compatible endpoint with `response_format: json_schema`, Qwen3-4B-Instruct-2507 Q4_K_M.

Spec: `docs/superpowers/specs/2026-08-10-session-story-rollup-design.md`

## Global Constraints

- Scope is **one interactive session**. No cross-session or per-project tier.
- **Nothing reads a summary of a summary.** A beat reads a transcript window plus the measured record; a report reads beats, measurements and the retain-list. No generation may be given output of its own kind. The retain-list is the one exception: named tokens, not prose, each verifiable against the transcript.
- Privacy invariant: transcripts are read locally; only verified substrings enter the record. No raw prose is transmitted or stored outside `~/.keld`.
- `BeatCap` **200 runes**. Beat series selection cap **12 entries**. `KELD_DIGEST_BEAT_TURNS` default **3** user turns.
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
// It is the only authoritative input to a digest: beats are model-written and
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

Session-spanning, deterministic, bounded. The only authoritative digest input: beats are
model-written and the window is raw evidence. Subjects enter only by the
verbatim gate, so a term cannot arrive by being plausible, and Populated() names what
actually holds data so an absent focus does not read as an empty one — the failure that let
DigestFacts.Topics read empty for months."
```

---

### Task 2: Persist the session record

**Files:**
- Modify: `internal/agent/enrich/llmstudy/digeststore/store.go`
- Modify: `internal/agent/enrich/llmstudy/digeststore/store_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `(*Store) PutSessionRecord(sessionID string, seq int, body string) error`; `(*Store) SessionRecord(sessionID string) (body string, seq int, ok bool, err error)`.

The `beat` table arrives in Task 4, with the code that produces beats. No `story` column: the
paraphrase field it would have held is dropped, because beats supersede it.

- [ ] **Step 1: Write the failing test**

```go
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

// A digest snapshot must still round-trip unchanged.
func TestDigestSnapshotStillRoundTrips(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "d.db"))
	defer s.Close()
	if err := s.Put(Record{SessionID: "a", Seq: 1, Body: "{}", Signals: "{}"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Latest("a"); err != nil || !ok {
		t.Fatalf("latest: %v %v", ok, err)
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./internal/agent/enrich/llmstudy/digeststore/`
Expected: FAIL, `undefined: PutSessionRecord`.

- [ ] **Step 3: Implement**

Append to `schema`:

```sql
-- Current state, not history: one row per session, overwritten. Digests are snapshots
-- because their prose is a record; the session record is measured state.
CREATE TABLE IF NOT EXISTS session_record (
  session_id  TEXT    NOT NULL PRIMARY KEY,
  updated_seq INTEGER NOT NULL,
  body        TEXT    NOT NULL
);
```

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
Expected: all PASS, including the pre-existing snapshot and history tests.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/digeststore/
git commit -m "feat(digeststore): persist the measured session record

One overwritten row per session, because the record is current measured state rather than
history. Digests stay snapshots: their prose is a record."
```

---

### Task 3: Beats — the cheap frequent pass

**Files:**
- Create: `internal/agent/enrich/llmstudy/beat.go`
- Create: `internal/agent/enrich/llmstudy/beat_test.go`

**Interfaces:**
- Consumes: `SessionRecord.Block`, `Render`, `callValid`, `clipProse`, `insightsMatch`.
- Produces: `BeatCap`; `type Beat struct{ Ordinal int; Text string; ChangedSubject bool }`; `BeatPrompt(record, window string) string`; `BeatSchema() map[string]any`; `(*Llama) GenerateBeat(record, window string) (string, error)`; `BeatSaysNothingNew(text string, prev []Beat) bool`; `BeatTurnsFromEnv() int`.

- [ ] **Step 1: Write the failing test**

```go
package llmstudy

import (
	"strings"
	"testing"
)

// A beat is given the window AND the measured record. Without the record it describes a local
// action ("read three CSVs") instead of what the action was for.
func TestBeatPromptCarriesWindowAndRecord(t *testing.T) {
	rec := SessionRecord{Turns: 12}.WithProject("meridian")
	p := BeatPrompt(rec.Block(), "user: reconcile the Larkin accrual\n")
	if !strings.Contains(p, "meridian") {
		t.Error("beat prompt omits the measured record")
	}
	if !strings.Contains(p, "Larkin") {
		t.Error("beat prompt omits the window")
	}
	// The chain this design avoids: a beat must never be handed another beat.
	if strings.Contains(strings.ToLower(p), "previous beat") {
		t.Error("beat prompt refers to an earlier beat")
	}
	for _, want := range []string{"one to three sentences", "what the work is about"} {
		if !strings.Contains(p, want) {
			t.Errorf("beat prompt omits %q", want)
		}
	}
}

func TestBeatSchemaIsASingleRequiredString(t *testing.T) {
	sc := BeatSchema()
	props := sc["properties"].(map[string]any)
	if len(props) != 1 || props["beat"] == nil {
		t.Fatalf("a beat is one field, got %v", props)
	}
	if req := sc["required"].([]string); len(req) != 1 || req[0] != "beat" {
		t.Errorf("beat must be required, got %v", req)
	}
}

// A run of acknowledgements must not pad the series and bury the moments that matter.
func TestBeatSayingNothingNewIsDropped(t *testing.T) {
	prev := []Beat{{Ordinal: 1, Text: "The work is reconciling the March ledger for Meridian."}}
	if !BeatSaysNothingNew("Work continues reconciling Meridian's March ledger.", prev) {
		t.Error("a restatement was not detected")
	}
	if BeatSaysNothingNew("The work has moved to the AR ageing provision policy.", prev) {
		t.Error("a genuinely new beat was dropped")
	}
	if BeatSaysNothingNew("anything", nil) {
		t.Error("the first beat can never be a restatement")
	}
}

func TestBeatIsClippedToItsCap(t *testing.T) {
	if got := len([]rune(clipProse(strings.Repeat("word ", 200), BeatCap))); got > BeatCap {
		t.Errorf("beat not clipped: %d runes", got)
	}
}

func TestBeatTurnsDefaultsToThree(t *testing.T) {
	t.Setenv("KELD_DIGEST_BEAT_TURNS", "")
	if got := BeatTurnsFromEnv(); got != 3 {
		t.Errorf("want 3, got %d", got)
	}
	t.Setenv("KELD_DIGEST_BEAT_TURNS", "7")
	if got := BeatTurnsFromEnv(); got != 7 {
		t.Errorf("want 7, got %d", got)
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run Beat`
Expected: FAIL, `undefined: BeatPrompt`.

- [ ] **Step 3: Implement**

```go
package llmstudy

import (
	"os"
	"strconv"
	"strings"
)

// BeatCap bounds one beat. One to three sentences; the cap is a backstop, not the target.
const BeatCap = 200

// Beat is one cheap statement of what the work is about, derived from its own window.
type Beat struct {
	Ordinal        int    `json:"ordinal"`
	Text           string `json:"text"`
	ChangedSubject bool   `json:"changed_subject"`
}

// BeatPrompt asks the cheap question. Deliberately NOT given a previous beat: a beat reads the
// transcript and the measured record only, which is what keeps the series free of a chain
// along which drift could compound.
func BeatPrompt(record, window string) string {
	var b strings.Builder
	b.WriteString("State what the work in this conversation is about, in one to three sentences.\n\n")
	b.WriteString("SESSION RECORD (measured — authoritative):\n")
	b.WriteString(record)
	b.WriteString("\nRECENT CONVERSATION:\n")
	b.WriteString(window)
	b.WriteString(`
Rules:
  - Say what the work is ABOUT — the subject and its purpose. Not a list of actions taken.
  - Use the record to place the work. An action is only meaningful as part of something.
  - Every noun must come from the conversation or the record above. Nothing in these
    instructions is subject matter.
  - No preamble, no headings. One to three sentences of plain prose.

Respond with JSON only.
`)
	return b.String()
}

// BeatSchema constrains the response to one required string.
func BeatSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"beat": map[string]any{"type": "string", "minLength": digestMinProse},
		},
		"required":             []string{"beat"},
		"additionalProperties": false,
	}
}

// GenerateBeat produces one beat.
func (l *Llama) GenerateBeat(record, window string) (string, error) {
	var out struct {
		Beat string `json:"beat"`
	}
	if err := l.callValid(BeatPrompt(record, window), BeatSchema(), &out, func() error {
		if strings.TrimSpace(out.Beat) == "" {
			return firstProblem([]string{"beat is empty"})
		}
		return nil
	}); err != nil {
		return "", err
	}
	return clipProse(out.Beat, BeatCap), nil
}

// BeatSaysNothingNew reports a beat that restates the most recent one.
//
// Compared on significant words, the same test that collapses duplicate insights, because a
// restatement arrives reworded rather than identical. Only the most recent beat is compared:
// a subject the session RETURNS to later is genuine history and should appear again.
func BeatSaysNothingNew(text string, prev []Beat) bool {
	if len(prev) == 0 {
		return false
	}
	return insightsMatch(text, prev[len(prev)-1].Text)
}

// BeatTurnsFromEnv reads KELD_DIGEST_BEAT_TURNS, defaulting to 3 user turns.
func BeatTurnsFromEnv() int {
	if v := os.Getenv("KELD_DIGEST_BEAT_TURNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 3
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/enrich/llmstudy/ -run Beat -v`
Expected: all five PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/beat.go internal/agent/enrich/llmstudy/beat_test.go
git commit -m "feat(digest): cheap beats every few turns

One to three sentences on what the work is about, from the recent window plus the measured
record — never from another beat, which is what keeps the series free of a chain along which
drift could compound. Roughly 60-90 output tokens against ~2,000 for a report, so twenty
beats cost about one report and give twenty points of history instead of one. A beat that
restates its predecessor is discarded rather than stored."
```

---

### Task 4: The beat series — storage and sampling

**Files:**
- Create: `internal/agent/enrich/llmstudy/beat_series.go`
- Create: `internal/agent/enrich/llmstudy/beat_series_test.go`
- Modify: `internal/agent/enrich/llmstudy/digeststore/store.go`
- Modify: `internal/agent/enrich/llmstudy/digeststore/store_test.go`

**Interfaces:**
- Consumes: `Beat`, `BeatCap`, `clipProse`, `insightsMatch`.
- Produces: `MaxBeatSelection`; `AppendBeat(prev []Beat, text string) ([]Beat, bool)`; `SelectBeats(all []Beat, max int) []Beat`; `RenderBeats(sel []Beat) string`; `(*Store) PutBeat(sessionID string, b BeatRow) error`; `(*Store) Beats(sessionID string) ([]BeatRow, error)`.

- [ ] **Step 1: Write the failing test**

```go
package llmstudy

import (
	"strings"
	"testing"
)

func beats(n int, changedAt ...int) []Beat {
	ch := map[int]bool{}
	for _, c := range changedAt {
		ch[c] = true
	}
	var out []Beat
	for i := 1; i <= n; i++ {
		out = append(out, Beat{Ordinal: i, Text: "beat " + strings.Repeat("x", i), ChangedSubject: ch[i]})
	}
	return out
}

// Appending marks whether the subject changed — the signal the report samples on, and the one
// the recency work was previously blocked on.
func TestAppendBeatMarksSubjectChange(t *testing.T) {
	var bs []Beat
	bs, ok := AppendBeat(bs, "The work is reconciling the March ledger for Meridian.")
	if !ok || !bs[0].ChangedSubject {
		t.Fatalf("the first beat establishes the subject: ok=%v %v", ok, bs)
	}
	bs, ok = AppendBeat(bs, "Work continues on Meridian's March ledger reconciliation.")
	if ok {
		t.Errorf("a restatement must not be stored: %v", bs)
	}
	bs, ok = AppendBeat(bs, "The work has moved to the AR ageing provision policy.")
	if !ok || !bs[len(bs)-1].ChangedSubject {
		t.Errorf("a new subject must be stored and marked: %v", bs)
	}
	if got := bs[len(bs)-1].Ordinal; got != 2 {
		t.Errorf("ordinals must be contiguous over STORED beats, got %d", got)
	}
}

func TestSelectBeatsKeepsFirstAndLatest(t *testing.T) {
	got := SelectBeats(beats(30), 6)
	if got[0].Ordinal != 1 {
		t.Errorf("the first beat was dropped: %v", got[0])
	}
	if got[len(got)-1].Ordinal != 30 {
		t.Errorf("the latest beat was dropped: %v", got[len(got)-1])
	}
	if len(got) > 6 {
		t.Errorf("cap exceeded: %d", len(got))
	}
}

func TestSelectBeatsPrefersSubjectChanges(t *testing.T) {
	got := SelectBeats(beats(30, 9, 18), 6)
	var have []int
	for _, b := range got {
		have = append(have, b.Ordinal)
	}
	for _, want := range []int{9, 18} {
		var found bool
		for _, h := range have {
			if h == want {
				found = true
			}
		}
		if !found {
			t.Errorf("subject change at %d missing from %v", want, have)
		}
	}
}

func TestSelectBeatsFallsBackToSpacing(t *testing.T) {
	got := SelectBeats(beats(30), 6)
	if len(got) != 6 {
		t.Fatalf("want the cap filled by spacing, got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Ordinal <= got[i-1].Ordinal {
			t.Fatalf("must be chronological and unique: %v", got)
		}
	}
}

func TestRenderBeatsMarksSubjectChanges(t *testing.T) {
	out := RenderBeats([]Beat{
		{Ordinal: 1, Text: "started on the ledger", ChangedSubject: true},
		{Ordinal: 4, Text: "steady progress", ChangedSubject: false},
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("one line per beat, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "subject") {
		t.Errorf("a subject change must be marked: %q", lines[0])
	}
	if strings.Contains(lines[1], "subject") {
		t.Errorf("steady progress must not be marked: %q", lines[1])
	}
}

func TestRenderEmptyBeatsIsEmpty(t *testing.T) {
	if RenderBeats(nil) != "" {
		t.Error("an empty series must render nothing, not a header")
	}
}
```

And in `digeststore`:

```go
func TestBeatsRoundTripInOrder(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 1; i <= 3; i++ {
		if err := s.PutBeat("a", BeatRow{Ordinal: i, CreatedTS: int64(i),
			Text: fmt.Sprintf("beat %d", i), ChangedSubject: i == 1}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Beats("a")
	if err != nil || len(got) != 3 {
		t.Fatalf("want 3 beats in order, got %d (%v)", len(got), err)
	}
	if got[0].Ordinal != 1 || got[2].Ordinal != 3 || !got[0].ChangedSubject {
		t.Errorf("beats came back wrong: %+v", got)
	}
}

// Re-putting the same ordinal is idempotent, so a retried generation is not a duplicate.
func TestPutBeatIsIdempotent(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "d.db"))
	defer s.Close()
	for i := 0; i < 2; i++ {
		if err := s.PutBeat("a", BeatRow{Ordinal: 1, Text: "same", CreatedTS: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.Beats("a"); len(got) != 1 {
		t.Errorf("want 1 row, got %d", len(got))
	}
}
```

- [ ] **Step 2: Run both to confirm they fail**

Run: `go test ./internal/agent/enrich/llmstudy/ -run Beat` and `go test ./internal/agent/enrich/llmstudy/digeststore/`
Expected: FAIL, `undefined: AppendBeat` / `undefined: BeatRow`.

- [ ] **Step 3: Implement**

```go
package llmstudy

import (
	"fmt"
	"sort"
	"strings"
)

// MaxBeatSelection caps how many beats a report reads. At BeatCap runes each that is ~2,400
// worst case, against the 4,742 the embedded report cost.
const MaxBeatSelection = 12

// AppendBeat stores a beat unless it restates the previous one, marking whether it changed the
// subject. Ordinals are contiguous over STORED beats, so a discarded restatement leaves no gap.
//
// ChangedSubject is the signal the report samples on, and it is measured here by comparing
// against the accumulated beats rather than taken from the classification pipeline's EWMA
// focus — which the digest path does not run. The EWMA is better where available; this makes
// the signal usable without it.
func AppendBeat(prev []Beat, text string) ([]Beat, bool) {
	text = strings.TrimSpace(clipProse(text, BeatCap))
	if text == "" {
		return prev, false
	}
	if BeatSaysNothingNew(text, prev) {
		return prev, false
	}
	changed := true
	for _, b := range prev {
		if insightsMatch(text, b.Text) {
			// A subject the session has already covered is a return, not a change.
			changed = false
			break
		}
	}
	return append(prev, Beat{Ordinal: len(prev) + 1, Text: text, ChangedSubject: changed}), true
}

// SelectBeats chooses which beats a report reads: the first, every subject change, the most
// recent, and even spacing to fill the cap.
func SelectBeats(all []Beat, max int) []Beat {
	if max <= 0 {
		max = MaxBeatSelection
	}
	if len(all) <= max {
		return all
	}
	pick := map[int]bool{0: true, len(all) - 1: true}
	for i := len(all) - 2; i > 0 && len(pick) < max; i-- {
		if all[i].ChangedSubject {
			pick[i] = true
		}
	}
	if len(pick) < max {
		step := float64(len(all)-1) / float64(max-1)
		for k := 1; k < max-1 && len(pick) < max; k++ {
			pick[int(float64(k)*step)] = true
		}
	}
	idx := make([]int, 0, len(pick))
	for i := range pick {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	out := make([]Beat, 0, len(idx))
	for _, i := range idx {
		out = append(out, all[i])
	}
	return out
}

// RenderBeats renders the series oldest-first, marking where the subject changed so a report
// can see the trajectory rather than only the endpoints.
func RenderBeats(sel []Beat) string {
	if len(sel) == 0 {
		return ""
	}
	var b strings.Builder
	for _, x := range sel {
		mark := ""
		if x.ChangedSubject {
			mark = " (subject changed)"
		}
		b.WriteString(fmt.Sprintf("[%d]%s %s\n", x.Ordinal, mark, oneLine(x.Text)))
	}
	return b.String()
}

// oneLine flattens a beat so one entry stays one line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
```

In `digeststore`, add the table and accessors:

```sql
CREATE TABLE IF NOT EXISTS beat (
  session_id      TEXT    NOT NULL,
  ordinal         INTEGER NOT NULL,
  created_ts      INTEGER NOT NULL,
  changed_subject INTEGER NOT NULL,
  text            TEXT    NOT NULL,
  PRIMARY KEY(session_id, ordinal)
);
```

```go
// BeatRow is one stored beat.
type BeatRow struct {
	Ordinal        int
	CreatedTS      int64
	ChangedSubject bool
	Text           string
}

// PutBeat writes a beat, overwriting the same ordinal so a retried generation is idempotent
// rather than a duplicate.
func (s *Store) PutBeat(sessionID string, b BeatRow) error {
	if sessionID == "" || b.Ordinal <= 0 {
		return fmt.Errorf("digeststore: session_id required and ordinal must be >= 1")
	}
	_, err := s.db.Exec(`
INSERT INTO beat (session_id, ordinal, created_ts, changed_subject, text) VALUES (?,?,?,?,?)
ON CONFLICT(session_id, ordinal) DO UPDATE SET
  created_ts=excluded.created_ts, changed_subject=excluded.changed_subject, text=excluded.text`,
		sessionID, b.Ordinal, b.CreatedTS, b.ChangedSubject, b.Text)
	return err
}

// Beats returns a session's beats in order.
func (s *Store) Beats(sessionID string) ([]BeatRow, error) {
	rows, err := s.db.Query(`SELECT ordinal, created_ts, changed_subject, text
FROM beat WHERE session_id = ? ORDER BY ordinal ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BeatRow
	for rows.Next() {
		var b BeatRow
		if err := rows.Scan(&b.Ordinal, &b.CreatedTS, &b.ChangedSubject, &b.Text); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
```

Drop the `story` column from Task 2 — it is no longer produced.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/enrich/llmstudy/... -run Beat -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/
git commit -m "feat(digest): beat series storage and sampling

Selection prefers subject changes over even spacing, because that is what a trajectory is
made of, and always keeps the first beat so a session cannot lose its own origin. Subject
change is measured by comparing against the accumulated beats, which is the signal the
recency work was blocked on when it was to come from the classification pipeline's EWMA."
```

---

### Task 5: Rewire the refinement prompt

**Files:**
- Modify: `internal/agent/enrich/llmstudy/digest_refine.go`
- Modify: `internal/agent/enrich/llmstudy/digest_refine_test.go`
- Modify: `internal/agent/enrich/llmstudy/digest_fit.go`

**Interfaces:**
- Consumes: `SessionRecord.Block`, `SelectBeats`, `RenderBeats`, `Identifiers`.
- Produces: `DigestUpdatePromptFrom(prev Digest, in RefineInput) string`; `type RefineInput struct{ SessionLabel string; Record SessionRecord; Beats []Beat; SessionView, NewTurns string; Why TriggerReason }`; `(*Llama) RefineFrom(prev Digest, in RefineInput) (Digest, error)`. `CarryForward` deleted.

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
		Beats:        []Beat{{Ordinal: 1, Text: "work began on the March close", ChangedSubject: true}},
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
		t.Error("the beat series is missing")
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
		Beats:        []Beat{{Ordinal: 1, Text: "LADDERMARK"}},
		NewTurns:     "user: WINDOWMARK\n",
	}
	p := DigestUpdatePromptFrom(Digest{Done: "x"}, in)
	rec, bea, win := strings.Index(p, "turns=9"), strings.Index(p, "LADDERMARK"), strings.Index(p, "WINDOWMARK")
	if !(rec < bea && bea < win) {
		t.Errorf("want record < beats < window, got %d %d %d", rec, bea, win)
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
	Beats        []Beat
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

	if bs := RenderBeats(SelectBeats(in.Beats, MaxBeatSelection)); bs != "" {
		b.WriteString("\nBEATS, oldest first — each written from its own window (indicative):\n")
		b.WriteString(bs)
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

// RefineFrom produces the next report. No paraphrase: beats supply the history.
func (l *Llama) RefineFrom(prev Digest, in RefineInput) (Digest, error) {
	var up digestUpdate
	repair := func(d Digest) Digest { return dropStaleOpenItems(applyClosures(d, up.Closed)) }
	if err := l.callValid(DigestUpdatePromptFrom(prev, in), DigestUpdateSchema(), &up,
		func() error { return firstProblem(ValidateDigest(repair(up.Digest))) }); err != nil {
		return Digest{}, err
	}
	merged := mergeWithRetirement(prev, repair(up.Digest), up.Retired)
	return CapSections(merged, DefaultProseCap, DefaultListCap), nil
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
`NoteTurningPoint` per report. Generate a beat every `BeatTurnsFromEnv()` user turns via
`GenerateBeat`, append with `AppendBeat`, and persist with `PutBeat`. Call `RefineFrom` with
the accumulated beats. Persist the record so a run is replayable.

Report the beat count and how many were discarded as restatements — a series that discards
most of what it generates means the cadence is too tight for these sessions.

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
across in Task 8. `Beat` is produced in Task 3, stored and sampled in Task 4, and consumed by `RefineInput` in
Task 5. `TurningPoint` (Task 1) still records why a REPORT fired; `Beat.ChangedSubject` records
whether the subject moved, and the two are independent signals.

**Risk to watch during execution.** Task 5 deletes `CarryForward` while Task 8 is what proves
the replacement works. If the sweep regresses T4 below 90%, the cause is most likely the
retain-list no longer being reinforced by embedded prose — increase the retain-list cap before
restoring any prose, since restoring prose reinstates the trade-off this plan exists to remove.
