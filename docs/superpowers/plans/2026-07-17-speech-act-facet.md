# Speech-act facet + adversarial eval expansion — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a new emitted `speech_act` enrichment facet (command/question/statement/fragment), plus an adversarial `s1` eval class and speech-act metrics, so the future speech-act conditioning lever becomes measurable.

**Architecture:** `speech_act` is a Wave1 extractor that classifies the current prompt (`ctx.Text` only) against readable `LabelDef` descriptions, mapping the winning text back to a canonical id — identical machinery to task_type (A6) but over `ctx.Text` instead of `Preamble()+Text`. It is emitted in `Profile` (schema bump 4→5). The eval harness gains a `speech_act` gold field, an adversarial `s1` confound class, `speech_act_accuracy` (free via the scored-fields list), and `s1_downstream_baseline` (the headroom number for the follow-up conditioning lever). Label wording is finalized by a bakeoff, not guessed.

**Tech Stack:** Go 1.26 (`/opt/homebrew/bin/go` — `export PATH="/opt/homebrew/bin:$PATH"`); the GLiNER2 sidecar for live eval; the `enrichtest` fake for unit tests.

## Global Constraints

- Go only. Toolchain: `export PATH="/opt/homebrew/bin:$PATH"` before any `go` command.
- Canonical `speech_act` ids are **`command` / `question` / `statement` / `fragment`** — stable (Atlas contract); never emit anything else as the value.
- `LabelDef` label **text** (what the model scores against) is chosen by the bakeoff (Task 5); the ids never change.
- Bi-encoder guardrails (from A2/A6): label descriptions stay **short**, use **positive discriminative tokens**, avoid negation.
- Measure-first, strict no-regression: keep a change only if speech-act metrics are captured AND every *other* facet's accuracy is flat vs. the pre-change eval.
- `speech_act` classifies **`ctx.Text` only**, never the preamble.
- Conditioning (using speech_act to resolve task_type/activity) is OUT OF SCOPE — a follow-up spec.

---

### Task 1: The `speech_act` facet — vocabulary, extractor, Profile wiring

**Files:**
- Modify: `internal/agent/enrich/labels.go` (add `SpeechActDefs`, bump `SchemaVersion`)
- Modify: `internal/agent/enrich/labels_test.go` (schema test 4→5)
- Modify: `internal/agent/enrich/pass.go` (extract `classifyLabeled` text-param helper)
- Create: `internal/agent/enrich/speechact.go` (`SpeechActExtractor`)
- Modify: `internal/agent/enrich/extractors.go` (add to `Wave1()`)
- Modify: `internal/agent/enrich/types.go` (Profile fields)
- Modify: `internal/agent/enrich/pipeline.go` (assemble the fields)
- Modify: `internal/agent/enrich/enrichtest/enrichtest.go` (fake `speech_act` priors)
- Create: `internal/agent/enrich/speechact_test.go`
- Modify: `internal/agent/enrich/pipeline_test.go` (extractor count 7→8)

**Interfaces:**
- Consumes: `LabelDef{ID, Text string}` (pass.go); `classifyPass` machinery; `JobContext`.
- Produces:
  - `SpeechActDefs []LabelDef` (labels.go) — 4 defs, ids `command`/`question`/`statement`/`fragment`.
  - `classifyLabeled(ctx *JobContext, name string, labels []LabelDef, text string) (Labeled, []Labeled)` (pass.go).
  - `SpeechActExtractor` implementing `Extractor`; `Run` returns `map[string]any{"speech_act": Labeled, "speech_act_alt": []Labeled}`.
  - `Profile.SpeechAct Labeled` + `Profile.SpeechActAlt []Labeled`.

- [ ] **Step 1: Write the failing test** (`internal/agent/enrich/speechact_test.go`)

```go
package enrich

import "testing"

// wantLabelModel returns the chosen readable label as top for every task, so we
// can prove the winning text maps back to its canonical id.
type wantLabelModel struct{ want string }

func (m wantLabelModel) Classify(text string, tasks map[string][]string) map[string][]Ranked {
	out := map[string][]Ranked{}
	for name, labels := range tasks {
		if len(labels) == 0 {
			continue
		}
		top := labels[0]
		for _, l := range labels {
			if l == m.want {
				top = l
			}
		}
		out[name] = []Ranked{{Label: top, Confidence: 1}}
	}
	return out
}
func (wantLabelModel) Entities(string, map[string]string) []Entity { return nil }
func (wantLabelModel) Extract(string, map[string]string, map[string][]string) ExtractResult {
	return ExtractResult{}
}

func TestSpeechActMapsWinningTextToID(t *testing.T) {
	// pick the description text belonging to id "question"; expect value "question".
	var qText string
	for _, d := range SpeechActDefs {
		if d.ID == "question" {
			qText = d.Text
		}
	}
	if qText == "" {
		t.Fatal("SpeechActDefs missing a 'question' entry")
	}
	out, err := (SpeechActExtractor{}).Run(NewJobContext("how do I reverse a list?", "claude_code", Meta{Tool: "claude_code"}, wantLabelModel{want: qText}))
	if err != nil {
		t.Fatal(err)
	}
	if got := out["speech_act"].(Labeled).Value; got != "question" {
		t.Fatalf("speech_act value = %q, want question", got)
	}
}

func TestSpeechActClassifiesTextNotPreamble(t *testing.T) {
	// classifyLabeled must be handed ctx.Text only — assert the model never sees
	// the preamble's context block for this facet.
	c := &captureText{}
	meta := Meta{Repo: "acme/api", Tool: "claude_code", RecentPrompts: []string{"add validation"}}
	if _, err := (SpeechActExtractor{}).Run(NewJobContext("ship it", "claude_code", meta, c)); err != nil {
		t.Fatal(err)
	}
	if c.seen != "ship it" {
		t.Fatalf("speech_act must classify ctx.Text only; model saw %q", c.seen)
	}
}

// captureText records the text handed to Classify.
type captureText struct{ seen string }

func (c *captureText) Classify(text string, tasks map[string][]string) map[string][]Ranked {
	c.seen = text
	out := map[string][]Ranked{}
	for n, ls := range tasks {
		if len(ls) > 0 {
			out[n] = []Ranked{{Label: ls[0], Confidence: 1}}
		}
	}
	return out
}
func (c *captureText) Entities(string, map[string]string) []Entity { return nil }
func (c *captureText) Extract(string, map[string]string, map[string][]string) ExtractResult {
	return ExtractResult{}
}

func TestSpeechActDefsCoverIDs(t *testing.T) {
	want := map[string]bool{"command": true, "question": true, "statement": true, "fragment": true}
	if len(SpeechActDefs) != len(want) {
		t.Fatalf("SpeechActDefs has %d entries, want %d", len(SpeechActDefs), len(want))
	}
	for _, d := range SpeechActDefs {
		if !want[d.ID] {
			t.Fatalf("unexpected speech_act id %q", d.ID)
		}
		delete(want, d.ID)
	}
	for id := range want {
		t.Fatalf("SpeechActDefs missing id %q", id)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/ -run TestSpeechAct -v`
Expected: FAIL — `undefined: SpeechActDefs`, `undefined: SpeechActExtractor`.

- [ ] **Step 3: Add the vocabulary and bump the schema** (`labels.go`)

Add after the `TaskTypes` / `Domains` block (initial Text is a reasonable starting point; Task 5's bakeoff finalizes it):

```go
// SpeechActDefs — the speech_act facet (what KIND of utterance the current
// prompt is), classified against ctx.Text only. Ids are stable (Atlas contract);
// the readable Text is bakeoff-tuned (short, positive, discriminative — no
// negation). See docs/superpowers/specs/2026-07-17-speech-act-facet-design.md.
var SpeechActDefs = []LabelDef{
	{"command", "a command or instruction to do something"},
	{"question", "a question asking for information or an answer"},
	{"statement", "a statement or observation describing something"},
	{"fragment", "a short follow-up, acknowledgement, or code snippet"},
}
```

Change the schema constant (`labels.go`):

```go
// ... and v5, which ADDS the emitted speech_act facet (a genuine contract
// change: a new Profile field, not just a derivation change).
const SchemaVersion = 5
```

(Extend the existing SchemaVersion doc comment's final sentence to mention v5 as above.)

- [ ] **Step 4: Update the schema test** (`labels_test.go`)

```go
func TestSchemaVersion(t *testing.T) {
	if SchemaVersion != 5 {
		t.Fatalf("SchemaVersion = %d, want 5", SchemaVersion)
	}
}
```

- [ ] **Step 5: Extract a text-param classify helper** (`pass.go`)

Replace the body of `classifyPass` so it delegates to a new `classifyLabeled` that takes the text explicitly:

```go
// classifyLabeled runs one classification over readable label text on the GIVEN
// text and maps the winning readable label back to its dotted id. Returns the
// top Labeled (id) and ranked alternates (ids).
func classifyLabeled(ctx *JobContext, name string, labels []LabelDef, text string) (Labeled, []Labeled) {
	if len(labels) == 0 {
		return Labeled{}, nil
	}
	texts := make([]string, len(labels))
	idByText := make(map[string]string, len(labels))
	for i, l := range labels {
		texts[i] = l.Text
		idByText[l.Text] = l.ID
	}
	res := ctx.Model.Classify(text, map[string][]string{name: texts})
	ranked := res[name]
	if len(ranked) == 0 {
		return Labeled{Value: labels[0].ID, Confidence: 0}, nil
	}
	out := make([]Labeled, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, Labeled{Value: idByText[r.Label], Confidence: r.Confidence, Producer: versioned(name)})
	}
	return out[0], out[1:]
}

// classifyPass classifies over Meta.Preamble()+ctx.Text (so repo/tool inform the
// guess). Facets needing raw text only (e.g. speech_act) call classifyLabeled.
func classifyPass(ctx *JobContext, name string, labels []LabelDef) (Labeled, []Labeled) {
	return classifyLabeled(ctx, name, labels, ctx.Meta.Preamble()+ctx.Text)
}
```

(Delete the old inline body of `classifyPass` — everything it did now lives in `classifyLabeled`.)

- [ ] **Step 6: Create the extractor** (`speechact.go`)

```go
package enrich

// SpeechActExtractor classifies the KIND of utterance the current prompt is —
// command / question / statement / fragment — a subject-independent structural
// signal. It classifies ctx.Text ONLY (not the preamble): mood is a property of
// the actual ask, and the context metadata would only muddy it. Emitted facet
// (schema v5); a follow-up spec may also use it to condition task_type/activity.
type SpeechActExtractor struct{}

func (SpeechActExtractor) Name() string    { return "speech_act" }
func (SpeechActExtractor) Version() string { return versioned("speech_act") }

func (SpeechActExtractor) Run(ctx *JobContext) (map[string]any, error) {
	top, alts := classifyLabeled(ctx, "speech_act", SpeechActDefs, ctx.Text)
	return map[string]any{"speech_act": top, "speech_act_alt": alts}, nil
}
```

- [ ] **Step 7: Register in Wave1** (`extractors.go`)

Add `SpeechActExtractor{}` to the `Wave1()` slice:

```go
func Wave1() []Extractor {
	return []Extractor{
		TaskTypeExtractor{}, SensitivityExtractor{}, DomainEntitiesExtractor{},
		passExtractor{Pass{Name: "activity_type", Labels: Activities}},
		passExtractor{Pass{Name: "personal", Labels: Personal}},
		funcGuessExtractor{}, SpeechActExtractor{},
	}
}
```

- [ ] **Step 8: Add the Profile fields** (`types.go`)

In the `Profile` struct, after the `FunctionGuess` line (keep the existing ones):

```go
	FunctionGuess     Labeled           `json:"function_guess"`
	SpeechAct         Labeled           `json:"speech_act"`
	SpeechActAlt      []Labeled         `json:"speech_act_alt,omitempty"`
```

- [ ] **Step 9: Assemble the fields in the pipeline** (`pipeline.go`)

In the returned `Profile{...}`, after the `FunctionGuess:` line:

```go
		FunctionGuess:     labeledFrom(ctx.Get("function_guess"), "function_guess", "function_guess"),
		SpeechAct:         labeledFrom(ctx.Get("speech_act"), "speech_act", "speech_act"),
		SpeechActAlt:      altsNamed(ctx.Get("speech_act"), "speech_act_alt"),
```

- [ ] **Step 10: Give the fake `speech_act` priors** (`enrichtest/enrichtest.go`)

In `NewFake()`, after `taskKW` is built and aliased, add a `speechKW` map and register it. Insert a `speech_act` entry into the `keywords` map literal:

```go
	// speech_act keyword priors keyed by canonical id, aliased to the A6-style
	// description texts so this double works for both id and description label sets.
	speechKW := map[string][]string{
		"command":   {"add", "write", "fix", "implement", "create", "refactor", "build", "make", "set up", "update"},
		"question":  {"?", "how", "why", "what", "should", "which", "can you", "do i"},
		"statement": {"is broken", "keeps", "fails", "failing", "doubled", "the build", "there is", "we have"},
		"fragment":  {"ok", "ship it", "also", "same", "too", "and the", "ditto", "yes"},
	}
	for _, d := range SpeechActDefs {
		if kws, ok := speechKW[d.ID]; ok && d.Text != d.ID {
			speechKW[d.Text] = kws
		}
	}
```

And add to the `keywords: map[string]map[string][]string{ ... }` literal (beside `"task_type"` and `"domain"`):

```go
			"speech_act": speechKW,
```

Note the `import "github.com/ncx-ai/keld-signal/internal/agent/enrich"` package alias in that file is `enrich` — reference `enrich.SpeechActDefs`. (This file is package `enrichtest`.)

- [ ] **Step 11: Bump the extractor-count assertion** (`pipeline_test.go`)

In `TestRunProducesEnrichedProfile`:

```go
	if len(p.ExtractorVersions) != 8 {
		t.Fatalf("want 8 extractor versions, got %d", len(p.ExtractorVersions))
	}
```

- [ ] **Step 12: Run the enrich package tests**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/...`
Expected: PASS (speechact_test, pipeline_test, labels_test, enrichtest all green).

- [ ] **Step 13: Build everything**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go build ./...`
Expected: no output (success).

- [ ] **Step 14: Commit**

```bash
git add internal/agent/enrich/
git commit -m "feat(enrich): add emitted speech_act facet (command/question/statement/fragment, schema v5)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: Eval schema + speech-act metrics

**Files:**
- Modify: `internal/agent/enrich/eval/eval.go` (GoldRow/Pred fields, `fieldOf`, `RunModel*`, `S1DownstreamBaseline`)
- Create: `internal/agent/enrich/eval/speechact_eval_test.go`

**Interfaces:**
- Consumes: `enrich.Run` returns `Profile` with `.SpeechAct.Value` (Task 1).
- Produces:
  - `GoldRow.SpeechAct string` (json `speech_act`); `Pred.SpeechAct string`.
  - `fieldOf` handles `"speech_act"` for both `GoldRow` and `Pred`.
  - `S1DownstreamBaseline(gold []GoldRow, pred []Pred) float64`.
  - `SpeechActPerMood(gold []GoldRow, pred []Pred) map[string][2]int` — per-id `[correct,total]`.

- [ ] **Step 1: Write the failing test** (`eval/speechact_eval_test.go`)

```go
package eval

import "testing"

func TestSpeechActScoredField(t *testing.T) {
	gold := []GoldRow{{Text: "a", SpeechAct: "question"}, {Text: "b", SpeechAct: "command"}}
	pred := []Pred{{SpeechAct: "question"}, {SpeechAct: "statement"}}
	m := Score(gold, pred, []string{"speech_act"})
	if got := m["speech_act"]["accuracy"]; got != 0.5 {
		t.Fatalf("speech_act accuracy = %.3f, want 0.5", got)
	}
}

func TestS1DownstreamBaseline(t *testing.T) {
	gold := []GoldRow{
		{Class: "s1", TaskType: "reasoning"},                    // trapped facet: task_type
		{Class: "s1", TaskType: "rag_qa", Activity: "retrieve"}, // two trapped facets
		{Class: "c1", TaskType: "codegen"},                      // not s1 → ignored
	}
	pred := []Pred{
		{TaskType: "codegen"},                    // wrong (1/1)
		{TaskType: "rag_qa", Activity: "generate"}, // task_type right, activity wrong (1/2)
		{TaskType: "codegen"},
	}
	// pairs: row0 task_type(wrong), row1 task_type(right)+activity(wrong) = 3 pairs, 2 wrong.
	if got := S1DownstreamBaseline(gold, pred); got != 2.0/3.0 {
		t.Fatalf("s1 downstream baseline = %.3f, want %.3f", got, 2.0/3.0)
	}
}

func TestSpeechActPerMood(t *testing.T) {
	gold := []GoldRow{{SpeechAct: "question"}, {SpeechAct: "question"}, {SpeechAct: "fragment"}}
	pred := []Pred{{SpeechAct: "question"}, {SpeechAct: "statement"}, {SpeechAct: "fragment"}}
	m := SpeechActPerMood(gold, pred)
	if m["question"] != [2]int{1, 2} {
		t.Fatalf("question = %v, want [1 2]", m["question"])
	}
	if m["fragment"] != [2]int{1, 1} {
		t.Fatalf("fragment = %v, want [1 1]", m["fragment"])
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/eval/ -run 'TestSpeechActScoredField|TestS1DownstreamBaseline|TestSpeechActPerMood' -v`
Expected: FAIL — `unknown field SpeechAct`, `undefined: S1DownstreamBaseline`.

- [ ] **Step 3: Add the gold + pred fields** (`eval.go`)

In `GoldRow` (after `FunctionGuess`):

```go
	FunctionGuess string   `json:"function_guess"`
	SpeechAct     string   `json:"speech_act"`
	Subcategory   string   `json:"subcategory"`
```

In `Pred` (after `FunctionGuess`):

```go
	FunctionGuess string
	SpeechAct     string
	Subcategory   string
```

- [ ] **Step 4: Handle `speech_act` in `fieldOf`** (`eval.go`)

Add a `case "speech_act"` to BOTH the `GoldRow` and `Pred` switch blocks:

```go
	case GoldRow:
		switch f {
		// ... existing cases ...
		case "speech_act":
			return v.SpeechAct
		}
	case Pred:
		switch f {
		// ... existing cases ...
		case "speech_act":
			return v.SpeechAct
		}
```

- [ ] **Step 5: Populate `Pred.SpeechAct`** (`eval.go`)

In BOTH `RunModel` and `RunModelWithContext`, add `SpeechAct: p.SpeechAct.Value,` to the `Pred{...}` literal (e.g. after `FunctionGuess: p.FunctionGuess.Value,`).

- [ ] **Step 6: Add the s1 baseline metric** (`eval.go`, append near `LeakageRate`)

```go
// S1DownstreamBaseline measures the CURRENT (unconditioned) downstream error on
// the speech-act adversarial class s1: over s1 rows, the fraction of trapped
// (row, facet) pairs where prediction != gold. The trapped facets are whichever
// of task_type / activity_type the row sets a gold value for. This is the
// headroom number the future speech-act conditioning lever must beat. 0 when no
// s1 rows.
func S1DownstreamBaseline(gold []GoldRow, pred []Pred) float64 {
	n := len(gold)
	if len(pred) < n {
		n = len(pred)
	}
	var pairs, wrong int
	for i := 0; i < n; i++ {
		if gold[i].Class != "s1" {
			continue
		}
		for _, f := range []string{"task_type", "activity_type"} {
			g := fieldOf(gold[i], f)
			if g == "" {
				continue
			}
			pairs++
			if g != fieldOf(pred[i], f) {
				wrong++
			}
		}
	}
	if pairs == 0 {
		return 0
	}
	return float64(wrong) / float64(pairs)
}

// SpeechActPerMood returns, per gold speech_act id, [correct, total] over rows
// carrying a gold speech_act. Surfaces which moods (typically fragment/statement)
// the facet handles worst.
func SpeechActPerMood(gold []GoldRow, pred []Pred) map[string][2]int {
	n := len(gold)
	if len(pred) < n {
		n = len(pred)
	}
	out := map[string][2]int{}
	for i := 0; i < n; i++ {
		g := gold[i].SpeechAct
		if g == "" {
			continue
		}
		c := out[g]
		c[1]++
		if pred[i].SpeechAct == g {
			c[0]++
		}
		out[g] = c
	}
	return out
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/eval/ -run 'TestSpeechActScoredField|TestS1DownstreamBaseline|TestSpeechActPerMood' -v`
Expected: PASS.

- [ ] **Step 8: Full eval-package + build check**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/eval/ && go build ./...`
Expected: PASS, no build errors.

- [ ] **Step 9: Commit**

```bash
git add internal/agent/enrich/eval/
git commit -m "feat(eval): speech_act gold field, scoring, and s1_downstream_baseline metric

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: Wire the metrics into `keld-agent eval`

**Files:**
- Modify: `internal/agentcli/evalcmd.go`

**Interfaces:**
- Consumes: `eval.Score` over the `fields` list (includes `speech_act`); `eval.S1DownstreamBaseline` (Task 2).
- Produces: `keld-agent eval [--confound]` prints `speech_act` per-facet accuracy, and under `--confound` an `s1_downstream_baseline=` value.

- [ ] **Step 1: Add `speech_act` to the scored fields** (`evalcmd.go`)

```go
	fields := []string{"task_type", "domain", "sensitivity", "activity_type", "function_guess", "speech_act", "subcategory"}
```

(This alone makes `speech_act accuracy=…` print in the existing per-field loop.)

- [ ] **Step 2: Print the s1 baseline under `--confound`** (`evalcmd.go`)

In the `if withConfound { ... }` block, after the existing leakage/false_eng `Fprintf`:

```go
		fmt.Fprintf(out, "  s1_downstream_baseline=%.3f\n", eval.S1DownstreamBaseline(rows, pred))
		fmt.Fprintf(out, "  speech_act per-mood=%v\n", eval.SpeechActPerMood(rows, pred))
```

- [ ] **Step 3: Build**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go build ./... && go vet ./internal/agentcli/`
Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add internal/agentcli/evalcmd.go
git commit -m "feat(eval): report speech_act accuracy + s1_downstream_baseline in keld-agent eval

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Author the eval data — `s1` adversarial rows + gold backfill

**Files:**
- Modify: `internal/agent/enrich/eval/confound.jsonl` (append ~20 `s1` rows)
- Modify: `internal/agent/enrich/eval/gold.jsonl` (backfill `speech_act` on all rows)

**This task has a REVIEW GATE:** the drafted rows + labels must be reviewed by the user before Task 5 locks anything against them. A mislabeled "correct" downstream answer makes every later metric lie.

**Interfaces:**
- Consumes: `GoldRow.SpeechAct` (Task 2); classes are filtered by `.Class` in metrics.
- Produces: `confound.jsonl` containing `class:"s1"` rows; every `gold.jsonl` row carrying a `speech_act` value (or blank if genuinely ambiguous).

- [ ] **Step 1: Append the `s1` adversarial rows to `confound.jsonl`**

Draft set (author/adjust during execution; each row's `speech_act` is gold, and the set `task_type`/`activity_type` are the trapped downstream golds). Add these lines to the END of `internal/agent/enrich/eval/confound.jsonl`:

```json
{"class":"s1","source":"claude_code","text":"How would you add cursor pagination to this results endpoint?","speech_act":"question","task_type":"reasoning","activity_type":"retrieve","domain":"software"}
{"class":"s1","source":"claude_code","text":"Should we cache this query in Redis or Memcached?","speech_act":"question","task_type":"reasoning","activity_type":"analyze","domain":"software"}
{"class":"s1","source":"claude_code","text":"What's the difference between a mutex and a semaphore here?","speech_act":"question","task_type":"rag_qa","activity_type":"retrieve","domain":"software"}
{"class":"s1","source":"claude_code","text":"Why does the worker drop events under load?","speech_act":"question","task_type":"reasoning","activity_type":"analyze","domain":"software"}
{"class":"s1","source":"generic","text":"Which vendor should we pick for the payroll integration?","speech_act":"question","task_type":"reasoning","activity_type":"analyze","domain":"business"}
{"class":"s1","source":"claude_code","text":"The migration keeps deadlocking under load.","speech_act":"statement","task_type":"reasoning","activity_type":"analyze","domain":"software"}
{"class":"s1","source":"claude_code","text":"Latency doubled after the last deploy.","speech_act":"statement","task_type":"reasoning","activity_type":"analyze","domain":"software"}
{"class":"s1","source":"claude_code","text":"The build is green but the staging pods keep crash-looping.","speech_act":"statement","task_type":"reasoning","activity_type":"analyze","domain":"software"}
{"class":"s1","source":"generic","text":"Our Q3 conversion rate is down eight percent versus plan.","speech_act":"statement","task_type":"reasoning","activity_type":"analyze","domain":"business"}
{"class":"s1","source":"claude_code","text":"There is a race between the cache warmer and the invalidation sweep.","speech_act":"statement","task_type":"reasoning","activity_type":"analyze","domain":"software"}
{"class":"s1","source":"claude_code","text":"and the other endpoint too","speech_act":"fragment","task_type":"codegen","activity_type":"generate","domain":"software"}
{"class":"s1","source":"claude_code","text":"ok ship it","speech_act":"fragment","task_type":"agentic_tool_use","domain":"software"}
{"class":"s1","source":"claude_code","text":"same for the staging config","speech_act":"fragment","task_type":"codegen","domain":"software"}
{"class":"s1","source":"claude_code","text":"yep, that one","speech_act":"fragment","task_type":"other","domain":"software"}
{"class":"s1","source":"generic","text":"the second bullet, shorter","speech_act":"fragment","task_type":"summarization","domain":"business"}
{"class":"s1","source":"claude_code","text":"Add a rate limiter to the gRPC endpoints.","speech_act":"command","task_type":"codegen","activity_type":"generate","domain":"software"}
{"class":"s1","source":"claude_code","text":"Refactor the scheduler into a durable queue.","speech_act":"command","task_type":"codegen","activity_type":"generate","domain":"software"}
{"class":"s1","source":"generic","text":"Draft a cold outreach email for logistics prospects.","speech_act":"command","task_type":"other","activity_type":"generate","domain":"business"}
{"class":"s1","source":"claude_code","text":"Explain how the retry backoff interacts with the circuit breaker.","speech_act":"command","task_type":"reasoning","activity_type":"analyze","domain":"software"}
{"class":"s1","source":"generic","text":"Is this clause enforceable under New York law?","speech_act":"question","task_type":"reasoning","activity_type":"analyze","domain":"legal"}
```

- [ ] **Step 2: Backfill `speech_act` onto every `gold.jsonl` row**

Labeling rubric (apply to each of the 73 rows; leave blank only if genuinely un-labelable):
- Ends with `?` OR opens with how/why/what/should/which/can/does/is-it → `question`.
- Starts with an imperative verb (Write/Add/Fix/Summarize/Translate/Refactor/Implement/Configure/…) → `command`.
- A declarative assertion with no request verb ("The X is Y", "Here is my …", "X keeps …") → `statement`.
- A terse continuation / acknowledgement / bare snippet (no full clause) → `fragment`.

Add `"speech_act":"<label>"` to each JSON object. Example (existing row → labeled):

```json
{"text":"Write a Python function that reverses a linked list.","task_type":"codegen","speech_act":"command", ...}
{"text":"...ends with a question?...","task_type":"rag_qa","speech_act":"question", ...}
```

- [ ] **Step 3: Validate the data parses and inspect the distribution**

Run:
```bash
export PATH="/opt/homebrew/bin:$PATH"; cd ~/keld-signal
python3 - <<'PY'
import json,collections
for f in ["internal/agent/enrich/eval/gold.jsonl","internal/agent/enrich/eval/confound.jsonl"]:
    rows=[json.loads(l) for l in open(f) if l.strip()]
    print(f, "rows:", len(rows))
    print("  speech_act dist:", dict(collections.Counter(r.get("speech_act","") for r in rows)))
    print("  s1 rows:", sum(1 for r in rows if r.get("class")=="s1"))
PY
go test ./internal/agent/enrich/eval/    # embeds parse cleanly
```
Expected: both files parse; `confound.jsonl` shows ~20 `s1` rows spread across the 4 speech_acts; `gold.jsonl` speech_act distribution is dominated by `command`/`question`; eval package tests PASS.

- [ ] **Step 4: REVIEW GATE — present the rows to the user**

Show the user the `s1` rows and a sample of the gold backfill; get explicit approval (or edits) on the `speech_act` labels AND the trapped downstream labels before continuing. Apply any requested changes and re-run Step 3.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/eval/gold.jsonl internal/agent/enrich/eval/confound.jsonl
git commit -m "test(eval): add s1 speech-act adversarial class + backfill speech_act on gold

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 5: Bakeoff the label wording, then validate on the full eval

**Files:**
- Create (throwaway): `internal/agentcli/speechact_bakeoff_test.go`
- Modify: `internal/agent/enrich/labels.go` (lock `SpeechActDefs.Text` to the bakeoff winner)
- Modify: `docs/superpowers/HANDOFF.md` (record numbers + winning wording)

**Interfaces:**
- Consumes: live GLiNER2 sidecar via `localagent.ResolveModel`; `eval.LoadGold`/`LoadConfound`; `enrich.SpeechActDefs` and candidate variants.
- Produces: finalized `SpeechActDefs.Text`; recorded `speech_act_accuracy` (overall + per-mood), `s1_downstream_baseline`, and a no-regression table.

- [ ] **Step 1: Start a warm 8-thread dev sidecar** (per the HANDOFF recipe)

```bash
export PATH="/opt/homebrew/bin:$PATH"; cd ~/keld-signal
pkill -f keld-agent-exp 2>/dev/null; /usr/local/bin/keld-agent stop 2>/dev/null
go build -o /tmp/keld-agent-exp ./cmd/keld-agent
OMP_NUM_THREADS=8 MKL_NUM_THREADS=8 OPENBLAS_NUM_THREADS=8 NUMEXPR_NUM_THREADS=8 \
  KELD_SIDECAR_MAX_THREADS=8 KELD_SIDECAR_BIN=/usr/local/keld/keld-agent-sidecar/keld-agent-sidecar \
  /tmp/keld-agent-exp run >/tmp/exp-daemon.log 2>&1 &
sleep 5; /tmp/keld-agent-exp enrich "warm the model" >/dev/null 2>&1   # trigger model load
```

- [ ] **Step 2: Write the bakeoff diagnostic** (throwaway; `internal/agentcli/speechact_bakeoff_test.go`)

```go
package agentcli

// THROWAWAY — delete after locking SpeechActDefs. Runs against the LIVE sidecar.
// Scores candidate speech_act label wordings on the labeled rows (gold backfill +
// s1), reporting overall + per-mood accuracy so we pick the best-understood text.
// Run: go test ./internal/agentcli/ -run TestSpeechActBakeoff -v -count=1

import (
	"fmt"
	"sort"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/eval"
	"github.com/ncx-ai/keld-signal/internal/localagent"
)

func TestSpeechActBakeoff(t *testing.T) {
	info, _ := agentcfg.Read()
	model, note, err := localagent.ResolveModel(info)
	if err != nil {
		t.Fatalf("resolve model: %v", err)
	}
	t.Logf("model: %s", note)

	// Rows with a gold speech_act, from gold + confound.
	gold, _ := eval.LoadGold()
	conf, _ := eval.LoadConfound()
	type row struct{ text, sa string }
	var rows []row
	for _, r := range append(gold, conf...) {
		if r.SpeechAct != "" {
			rows = append(rows, row{r.Text, r.SpeechAct})
		}
	}

	defs := func(cmd, q, st, fr string) []enrich.LabelDef {
		return []enrich.LabelDef{{"command", cmd}, {"question", q}, {"statement", st}, {"fragment", fr}}
	}
	cands := map[string][]enrich.LabelDef{
		"current":   enrich.SpeechActDefs,
		"plain":     defs("a command", "a question", "a statement", "a fragment"),
		"verb":      defs("an instruction to do something", "a question", "an observation", "a short follow-up"),
		"act":       defs("telling someone to do something", "asking a question", "stating a fact", "a brief snippet or reply"),
	}

	classifyTop := func(d []enrich.LabelDef, text string) string {
		idByText := map[string]string{}
		var texts []string
		for _, x := range d {
			idByText[x.Text] = x.ID
			texts = append(texts, x.Text)
		}
		r := model.Classify(text, map[string][]string{"speech_act": texts})["speech_act"]
		if len(r) == 0 {
			return ""
		}
		return idByText[r[0].Label]
	}

	var names []string
	for n := range cands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		perMood := map[string][2]int{} // id -> [correct,total]
		correct := 0
		for _, rw := range rows {
			got := classifyTop(cands[n], rw.text)
			c := perMood[rw.sa]
			c[1]++
			if got == rw.sa {
				c[0]++
				correct++
			}
			perMood[rw.sa] = c
		}
		t.Logf("%-8s overall=%.3f (%d/%d)  per-mood=%v", n, float64(correct)/float64(len(rows)), correct, len(rows), perMood)
	}
}
```

- [ ] **Step 3: Run the bakeoff and pick the winner**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agentcli/ -run TestSpeechActBakeoff -v -count=1`
Expected: a table of overall + per-mood accuracy per candidate. Choose the wording with the best overall accuracy AND no collapsed mood (avoid a candidate that scores 0 on `fragment` or `statement`). If `current` wins, keep it.

- [ ] **Step 4: Lock `SpeechActDefs.Text` to the winner** (`labels.go`)

Edit the `Text` fields of `SpeechActDefs` to the winning wording (ids unchanged). Add a one-line comment noting it was bakeoff-selected.

- [ ] **Step 5: Delete the throwaway bakeoff**

```bash
rm internal/agentcli/speechact_bakeoff_test.go
```

- [ ] **Step 6: Full no-regression eval (both gates)**

Run:
```bash
export PATH="/opt/homebrew/bin:$PATH"; cd ~/keld-signal
go build -o /tmp/keld-agent-exp ./cmd/keld-agent
/tmp/keld-agent-exp eval --confound --context
/tmp/keld-agent-exp eval --context
```
Expected: `speech_act accuracy=` reported and reasonable (soft bar ≥0.8 overall); `s1_downstream_baseline=` printed; and every OTHER facet's accuracy is unchanged vs. the pre-Task-1 baseline (`task_type`, `domain`, `sensitivity`, `activity_type`, `function_guess`, `subcategory` flat; `leakage`/`false_eng` unchanged). A low speech_act accuracy is a finding to record, not a silent pass.

- [ ] **Step 7: Run the full unit suite**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./...`
Expected: all PASS.

- [ ] **Step 8: Record results in the HANDOFF** (`docs/superpowers/HANDOFF.md`)

Add a short "speech_act facet (v-next)" section: the winning wording + runner-ups, the measured `speech_act_accuracy` (overall + per-mood), the `s1_downstream_baseline`, and confirmation of no-regression. Note the conditioning lever is the next step, to be designed against the recorded baseline.

- [ ] **Step 9: Commit**

```bash
git add internal/agent/enrich/labels.go docs/superpowers/HANDOFF.md
git commit -m "feat(enrich): lock bakeoff-selected speech_act wording; record baseline

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

- [ ] **Step 10: Restore the launchd daemon (dev teardown)**

```bash
pkill -f keld-agent-exp 2>/dev/null; /usr/local/bin/keld-agent start 2>/dev/null || true
```

---

## Notes for the implementer

- The whole feature is behind no runtime flag by design — `speech_act` is a new *additive* emitted facet (blank/low-confidence is harmless to consumers), and there is no old behavior to preserve, so an escape hatch would be dead weight. (Contrast A4/A6, which *changed* an existing facet's derivation and thus shipped a disable switch.)
- Do NOT implement conditioning (feeding speech_act into task_type/activity). That is a separate spec, gated on the `s1_downstream_baseline` this plan produces.
- Schema is now 5. If a later change alters an existing facet's derivation, bump again per the labels.go convention.
