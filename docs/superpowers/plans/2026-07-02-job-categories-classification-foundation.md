# Job Categories — Classification Foundation (keld-agent) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend keld-agent's on-device GLiNER2 enrichment pipeline to produce the job-category taxonomy's prompt-intrinsic signals — a declarative "pass" framework plus `activity_type`, `personal`, `function_guess`, and conditioned `subcategory` — and ship them (with ranked scores) on the `/v1/enrichments` wire.

**Architecture:** Reuse the existing `internal/agent/enrich` pipeline (swappable `Model`, `Extractor` registry, `Profile`). Add (1) a `Meta{Repo,Tool}` context threaded into `Run` so classification passes can prepend a metadata preamble, (2) a declarative `Pass` (readable labels mapped back to dotted ids) run by one generic extractor, and (3) a second wave so the `subcategory` pass can condition on the `function_guess` result. Category itself is NOT produced here — Atlas assigns it from `team→function`; this plan produces the facets + a function guess + ranked subcategory scores for Atlas to reconcile.

**Tech Stack:** Go 1.x (keld-cli), existing GLiNER2 sidecar (`fastino/gliner2-large-v1`) behind the `enrich.Model` interface; pytest-free — Go `testing` only.

## Global Constraints

- **No `os.getenv`/`os.environ` outside config** — not applicable here; this plan adds no env vars.
- **Privacy invariant:** raw prompt text and raw sensitive spans NEVER leave the device; sensitivity/entity passes run on **raw** `ctx.Text` (never the preamble-prefixed text) so span offsets stay correct and masking is preserved.
- **Schema versioning:** any change to the label vocabulary or emitted fields bumps `enrich.SchemaVersion` and must re-run the eval harness before the bump is committed (currently `SchemaVersion = 1` in `internal/agent/enrich/labels.go`).
- **Category is out of scope on-device.** This plan emits `function_guess` (a hint for conditioning + Atlas fallback) and facets/subcategory — never an authoritative `category`.
- **Backward compatible wire:** new `Enrichment` fields are additive; existing consumers keep working.
- **Run Go tests via:** `cd ~/keld/keld-cli && go test ./internal/agent/...` (host Go is fine for keld-cli; the Python 3.14 restriction is for keld-atlas only).

---

## File Structure

- `internal/agent/enrich/types.go` — add `Meta`, extend `JobContext` and `Profile` (Activity/Personal/FunctionGuess/Subcategory + ranked scores).
- `internal/agent/enrich/meta.go` *(new)* — `Meta` + `Preamble()`.
- `internal/agent/enrich/labels.go` — new label vocab: `Activities`, `Personal`, `Functions`, `Subcats` (readable text ↔ dotted id).
- `internal/agent/enrich/pass.go` *(new)* — `LabelDef`, `Pass`, generic `passExtractor`, `Wave2()`.
- `internal/agent/enrich/extractors.go` — register new Wave1 passes (`activity_type`, `personal`, `function_guess`).
- `internal/agent/enrich/pipeline.go` — thread `Meta`; run Wave1 then Wave2; assemble new `Profile` fields.
- `internal/agent/daemon/daemon.go:80` — pass `enrich.Meta{Repo: j.Cwd, Tool: j.Source}` into `Run`.
- `internal/agent/publish/publish.go` — add new fields to `Enrichment` + `Build`.
- `internal/agent/enrich/eval/eval.go` + `eval/gold.jsonl` — score `activity_type` and `subcategory`.
- Callers to update for the new `Run` signature: `internal/agent/enrich/eval/eval.go:121`, `internal/agent/publish/publish_test.go:17`, `internal/agent/privacy_test.go:17`, `internal/agent/daemon/daemon.go:80`.

---

## Task 1: Thread `Meta{Repo,Tool}` into the pipeline

**Files:**
- Create: `internal/agent/enrich/meta.go`
- Modify: `internal/agent/enrich/types.go` (JobContext + NewJobContext), `internal/agent/enrich/pipeline.go:23` (Run signature), and the four callers above.
- Test: `internal/agent/enrich/meta_test.go`

**Interfaces:**
- Produces: `type Meta struct { Repo, Tool string }`; `func (Meta) Preamble() string`; `func Run(text, source string, meta Meta, m Model) Profile`; `JobContext.Meta Meta`.

- [ ] **Step 1: Write the failing test**

```go
// internal/agent/enrich/meta_test.go
package enrich

import "testing"

func TestPreamble(t *testing.T) {
	got := Meta{Repo: "keld/atlas", Tool: "Claude Code"}.Preamble()
	want := "[Context — repository: keld/atlas; tool: Claude Code]\nTask: "
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if empty := (Meta{}).Preamble(); empty != "[Context — repository: none]\nTask: " {
		t.Fatalf("empty repo should say none, got %q", empty)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run TestPreamble -v`
Expected: FAIL — `undefined: Meta`.

- [ ] **Step 3: Create `meta.go`**

```go
// internal/agent/enrich/meta.go
package enrich

import "strings"

// Meta is the non-prompt context a classification pass may reason over. It is
// deliberately small: repo (cwd) and tool (source) are what the device knows.
// Team/category are resolved server-side in Atlas, never here.
type Meta struct {
	Repo string
	Tool string
}

// Preamble renders a compact context line prepended to the text handed to
// CLASSIFICATION passes (never to entity/sensitivity passes, which need raw
// offsets). Empty repo renders "none" so the model sees a stable shape.
func (m Meta) Preamble() string {
	parts := []string{"repository: none"}
	if m.Repo != "" {
		parts[0] = "repository: " + m.Repo
	}
	if m.Tool != "" {
		parts = append(parts, "tool: "+m.Tool)
	}
	return "[Context — " + strings.Join(parts, "; ") + "]\nTask: "
}
```

- [ ] **Step 4: Thread `Meta` through `JobContext` and `Run`**

In `internal/agent/enrich/types.go`, add `Meta Meta` to `JobContext` and update the constructor:

```go
// JobContext threads input + per-stage outputs through the pipeline.
type JobContext struct {
	Text   string
	Source string
	Meta   Meta
	Model  Model

	results map[string]map[string]any
}

// NewJobContext builds a context for one prompt.
func NewJobContext(text, source string, meta Meta, m Model) *JobContext {
	return &JobContext{Text: text, Source: source, Meta: meta, Model: m, results: map[string]map[string]any{}}
}
```

In `internal/agent/enrich/pipeline.go`, change the signature and the constructor call:

```go
func Run(text, source string, meta Meta, m Model) Profile {
	ctx := NewJobContext(text, source, meta, m)
	// ... rest unchanged for now ...
```

- [ ] **Step 5: Update the four callers**

```go
// internal/agent/daemon/daemon.go:80
profile := enrich.Run(text, j.Source, enrich.Meta{Repo: j.Cwd, Tool: j.Source}, m)
```
```go
// internal/agent/enrich/eval/eval.go:121
p := enrich.Run(g.Text, "eval", enrich.Meta{}, m)
```
```go
// internal/agent/publish/publish_test.go:17
p := enrich.Run("key sk-live-ABCDEF0123456789 and write a function", "claude_code", enrich.Meta{}, enrich.NewDeterministic())
```
```go
// internal/agent/privacy_test.go:17
p := enrich.Run(raw, "claude_code", enrich.Meta{}, enrich.NewDeterministic())
```

- [ ] **Step 6: Run tests to verify pass**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/...`
Expected: PASS (all existing tests still green with the new signature).

- [ ] **Step 7: Commit**

```bash
cd ~/keld/keld-cli
git add internal/agent/enrich/meta.go internal/agent/enrich/meta_test.go internal/agent/enrich/types.go internal/agent/enrich/pipeline.go internal/agent/daemon/daemon.go internal/agent/enrich/eval/eval.go internal/agent/publish/publish_test.go internal/agent/privacy_test.go
git commit -m "feat(enrich): thread Meta{repo,tool} context into the pipeline

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Job-category label vocabulary

**Files:**
- Modify: `internal/agent/enrich/labels.go`
- Test: `internal/agent/enrich/labels_test.go` (add cases)

**Interfaces:**
- Produces: `Activities []LabelDef`, `Personal []LabelDef`, `Functions []LabelDef`, `Subcats map[string][]LabelDef` (function id → its subcategory `LabelDef`s). `LabelDef` is defined in Task 3; this task depends on Task 3's type, so **implement Task 3 first if reading in order** — or move the `LabelDef` type definition here. (Decision: define `LabelDef` in `pass.go`, Task 3; this task adds the vocab vars.)

- [ ] **Step 1: Write the failing test**

```go
// internal/agent/enrich/labels_test.go  (add to existing file)
func TestSubcatsCoverFunctions(t *testing.T) {
	for _, f := range Functions {
		if len(Subcats[f.ID]) == 0 {
			t.Errorf("function %q has no subcategories", f.ID)
		}
	}
	if len(Functions) != 12 {
		t.Fatalf("want 12 functions, got %d", len(Functions))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run TestSubcatsCoverFunctions -v`
Expected: FAIL — `undefined: Functions`.

- [ ] **Step 3: Add the vocabulary to `labels.go`**

Append to `internal/agent/enrich/labels.go` (readable `Text` drives zero-shot; `ID` is the dotted taxonomy id):

```go
// Activities — the activity_type facet (what cognitive operation).
var Activities = []LabelDef{
	{"generate", "generating new content from scratch: draft, write, code, ideate"},
	{"transform", "transforming existing content: rewrite, summarize, translate, reformat"},
	{"analyze", "analyzing and reasoning over inputs: compute, evaluate, decide"},
	{"retrieve", "gathering and researching information, looking things up"},
	{"converse", "interactive question answering or brainstorming"},
	{"review", "reviewing, critiquing, or checking existing work for errors"},
}

// Personal — binary work-vs-personal.
var Personal = []LabelDef{
	{"work", "a work-related professional task"},
	{"personal", "personal, entertainment, roleplay, or non-work activity"},
}

// Functions — the 12 business functions (ids match docs/job-categories.md).
var Functions = []LabelDef{
	{"eng", "software engineering: writing, debugging, testing, deploying software"},
	{"prod", "product management and design: requirements, specs, UX/UI"},
	{"data", "data analytics: analysis, modeling, dashboards, quantitative insight"},
	{"mkt", "marketing and content: copy, campaigns, brand, SEO, market research"},
	{"sales", "sales and revenue: prospecting, outreach, proposals, deal support"},
	{"support", "customer support: helping existing customers, troubleshooting, tickets"},
	{"delivery", "service delivery and operations: client/production work"},
	{"fin", "finance and accounting: bookkeeping, analysis, forecasting, billing"},
	{"legal", "legal, risk and compliance: contracts, regulation, risk"},
	{"hr", "people and HR: recruiting, hiring content, onboarding, performance"},
	{"it", "IT and security: internal helpdesk, security, sysadmin, scripting"},
	{"gen", "strategy, admin and general office work not tied to one function"},
}

// Subcats — subcategory LabelDefs keyed by function id.
var Subcats = map[string][]LabelDef{
	"eng": {
		{"eng.dev", "writing new feature or product code"},
		{"eng.debug", "debugging and troubleshooting existing code"},
		{"eng.test", "writing tests or doing QA"},
		{"eng.review", "reviewing or refactoring code"},
		{"eng.devops", "CI/CD, infrastructure, deployment"},
		{"eng.docs", "writing technical documentation"},
	},
	"prod": {
		{"prod.discovery", "product discovery and requirements"},
		{"prod.spec", "writing specs, PRDs, roadmaps"},
		{"prod.design", "UX or UI design"},
		{"prod.research", "user research"},
	},
	"data": {
		{"data.prep", "cleaning and preparing data"},
		{"data.analysis", "statistical analysis and modeling"},
		{"data.report", "reports and dashboards"},
		{"data.insight", "insights and recommendations"},
	},
	"mkt": {
		{"mkt.content", "content and copywriting"},
		{"mkt.campaign", "campaigns and channels"},
		{"mkt.seo", "SEO and web"},
		{"mkt.creative", "creative and brand"},
		{"mkt.research", "market and competitive research"},
	},
	"sales": {
		{"sales.prospect", "prospecting and lead research"},
		{"sales.outreach", "sales outreach and messaging"},
		{"sales.proposal", "proposals, RFPs, quotes"},
		{"sales.enable", "deal support, enablement, ROI justification"},
		{"sales.crm", "pipeline and CRM admin"},
	},
	"support": {
		{"support.chat", "conversational customer support"},
		{"support.tech", "technical troubleshooting for a customer"},
		{"support.triage", "ticket triage and routing"},
		{"support.kb", "help content and knowledge base"},
		{"support.success", "account and success management"},
	},
	"delivery": {
		{"delivery.client", "client or project delivery"},
		{"delivery.process", "process design and documentation"},
		{"delivery.supply", "supply chain and procurement"},
		{"delivery.quality", "quality and assurance"},
		{"delivery.domain", "domain-specific production"},
	},
	"fin": {
		{"fin.books", "bookkeeping and reconciliation"},
		{"fin.analysis", "financial analysis and modeling"},
		{"fin.close", "financial reporting and close"},
		{"fin.fpa", "FP&A, budgeting and forecasting"},
		{"fin.billing", "billing, AR, AP"},
	},
	"legal": {
		{"legal.contract", "contract drafting and review"},
		{"legal.research", "legal and regulatory research"},
		{"legal.compliance", "compliance and policy"},
		{"legal.risk", "risk assessment"},
	},
	"hr": {
		{"hr.recruit", "recruiting and sourcing candidates"},
		{"hr.content", "hiring content like job descriptions"},
		{"hr.onboard", "onboarding and training"},
		{"hr.support", "HR support and policy"},
		{"hr.perf", "performance and compensation"},
	},
	"it": {
		{"it.helpdesk", "internal IT support and helpdesk"},
		{"it.security", "security and threat analysis"},
		{"it.sysadmin", "systems administration"},
		{"it.automation", "automation and scripting"},
	},
	"gen": {
		{"gen.strategy", "business strategy and planning"},
		{"gen.pm", "program and project management"},
		{"gen.comms", "communications and email"},
		{"gen.notes", "meeting notes and summaries"},
		{"gen.translate", "translation and localization"},
		{"gen.uncat", "general or uncategorized work with no clear function"},
	},
}
```

- [ ] **Step 4: Bump the schema version**

In `internal/agent/enrich/labels.go` change `const SchemaVersion = 1` to `const SchemaVersion = 2`.

- [ ] **Step 5: Run test to verify pass**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run TestSubcatsCoverFunctions -v`
Expected: PASS. (Requires `LabelDef` from Task 3 to compile — land Task 3 in the same working set; commit after Task 3 Step 4 if executing strictly in order.)

- [ ] **Step 6: Commit** (after Task 3 compiles)

```bash
cd ~/keld/keld-cli
git add internal/agent/enrich/labels.go internal/agent/enrich/labels_test.go
git commit -m "feat(enrich): job-category label vocabulary + schema v2

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Declarative `Pass` + generic extractor

**Files:**
- Create: `internal/agent/enrich/pass.go`
- Test: `internal/agent/enrich/pass_test.go`

**Interfaces:**
- Produces:
  - `type LabelDef struct { ID, Text string }`
  - `type Pass struct { Name string; Labels []LabelDef; ConditionOn string; LabelsByCond map[string][]LabelDef }`
  - `func classifyPass(ctx *JobContext, name string, labels []LabelDef) (Labeled, []Labeled)` — returns top label (as **id**) + ranked alternates (ids), mapping the model's readable label back to its id.
  - `type passExtractor struct { p Pass }` implementing `Extractor`.

- [ ] **Step 1: Write the failing test**

```go
// internal/agent/enrich/pass_test.go
package enrich

import "testing"

// stubModel returns a fixed top label for a task.
type stubModel struct{ top map[string]string }

func (s stubModel) Classify(_ string, tasks map[string][]string) map[string][]Ranked {
	out := map[string][]Ranked{}
	for task, labels := range tasks {
		want := s.top[task]
		ranked := []Ranked{}
		for i, l := range labels {
			c := 0.4
			if l == want {
				c = 0.95
			}
			ranked = append([]Ranked{{Label: l, Confidence: c}}[:], ranked...)
			_ = i
		}
		out[task] = ranked
	}
	return out
}
func (s stubModel) Entities(string, map[string]string) []Entity { return nil }
func (s stubModel) Extract(string, map[string]string, map[string][]string) ExtractResult {
	return ExtractResult{}
}

func TestClassifyPassMapsReadableToID(t *testing.T) {
	labels := []LabelDef{{"generate", "generating new content"}, {"analyze", "analyzing inputs"}}
	m := stubModel{top: map[string]string{"activity_type": "analyzing inputs"}}
	ctx := NewJobContext("do some analysis", "eval", Meta{}, m)
	top, _ := classifyPass(ctx, "activity_type", labels)
	if top.Value != "analyze" {
		t.Fatalf("want id 'analyze', got %q", top.Value)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run TestClassifyPassMapsReadableToID -v`
Expected: FAIL — `undefined: LabelDef` / `classifyPass`.

- [ ] **Step 3: Create `pass.go`**

```go
// internal/agent/enrich/pass.go
package enrich

// LabelDef pairs a dotted taxonomy id with the readable phrase the zero-shot
// model actually classifies against.
type LabelDef struct {
	ID   string
	Text string
}

// Pass declares one classification stage as data. A plain pass classifies over
// Labels; a conditioned pass (ConditionOn != "") selects its label set from
// LabelsByCond using the id produced by the named prior pass.
type Pass struct {
	Name         string
	Labels       []LabelDef
	ConditionOn  string
	LabelsByCond map[string][]LabelDef
}

// classifyPass runs one classification over readable label text and maps the
// winning readable label back to its dotted id. Returns the top Labeled (id) and
// ranked alternates (ids). Uses the Meta preamble so repo/tool inform the guess.
func classifyPass(ctx *JobContext, name string, labels []LabelDef) (Labeled, []Labeled) {
	if len(labels) == 0 {
		return Labeled{}, nil
	}
	texts := make([]string, len(labels))
	idByText := make(map[string]string, len(labels))
	for i, l := range labels {
		texts[i] = l.Text
		idByText[l.Text] = l.ID
	}
	res := ctx.Model.Classify(ctx.Meta.Preamble()+ctx.Text, map[string][]string{name: texts})
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

// passExtractor adapts a plain (non-conditioned) Pass to the Extractor interface.
type passExtractor struct{ p Pass }

func (e passExtractor) Name() string    { return e.p.Name }
func (e passExtractor) Version() string { return versioned(e.p.Name) }

func (e passExtractor) Run(ctx *JobContext) (map[string]any, error) {
	top, alts := classifyPass(ctx, e.p.Name, e.p.Labels)
	return map[string]any{e.p.Name: top, e.p.Name + "_alt": alts}, nil
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run TestClassifyPass -v`
Expected: PASS.

- [ ] **Step 5: Commit** (bundles Task 2's vocab, which this makes compile)

```bash
cd ~/keld/keld-cli
git add internal/agent/enrich/pass.go internal/agent/enrich/pass_test.go internal/agent/enrich/labels.go internal/agent/enrich/labels_test.go
git commit -m "feat(enrich): declarative Pass framework + job-category vocab

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Register `activity_type`, `personal`, `function_guess` (Wave1) and extend `Profile`

**Files:**
- Modify: `internal/agent/enrich/extractors.go` (Wave1), `internal/agent/enrich/types.go` (Profile), `internal/agent/enrich/pipeline.go` (assemble)
- Test: `internal/agent/enrich/pipeline_test.go` (add case)

**Interfaces:**
- Consumes: `passExtractor`, `Activities`, `Personal`, `Functions` (Tasks 2-3).
- Produces: `Profile.Activity Labeled`, `Profile.Personal Labeled`, `Profile.FunctionGuess Labeled`.

- [ ] **Step 1: Write the failing test**

```go
// internal/agent/enrich/pipeline_test.go  (add)
func TestProfileHasActivityAndFunctionGuess(t *testing.T) {
	p := Run("write a python function to sort a list", "eval", Meta{}, NewDeterministic())
	if p.Activity.Value == "" {
		t.Error("expected an activity_type")
	}
	if p.FunctionGuess.Value == "" {
		t.Error("expected a function_guess")
	}
	if p.Personal.Value == "" {
		t.Error("expected a personal label")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run TestProfileHasActivityAndFunctionGuess -v`
Expected: FAIL — `p.Activity undefined`.

- [ ] **Step 3: Add fields to `Profile`**

In `internal/agent/enrich/types.go`, add to `Profile`:

```go
	Activity         Labeled           `json:"activity_type"`
	Personal         Labeled           `json:"personal"`
	FunctionGuess    Labeled           `json:"function_guess"`
	Subcategory      Labeled           `json:"subcategory"`
	SubcategoryAlt   []Labeled         `json:"subcategory_alt,omitempty"`
```

- [ ] **Step 4: Register the new Wave1 passes**

In `internal/agent/enrich/extractors.go`, extend `Wave1()`:

```go
func Wave1() []Extractor {
	return []Extractor{
		TaskTypeExtractor{}, SensitivityExtractor{}, DomainEntitiesExtractor{},
		passExtractor{Pass{Name: "activity_type", Labels: Activities}},
		passExtractor{Pass{Name: "personal", Labels: Personal}},
		passExtractor{Pass{Name: "function_guess", Labels: Functions}},
	}
}
```

- [ ] **Step 5: Assemble the new fields in `pipeline.go`**

In `Run`, add to the returned `Profile{...}` (alongside the existing fields):

```go
		Activity:      labeledFrom(ctx.Get("activity_type"), "activity_type", "activity_type"),
		Personal:      labeledFrom(ctx.Get("personal"), "personal", "personal"),
		FunctionGuess: labeledFrom(ctx.Get("function_guess"), "function_guess", "function_guess"),
```

- [ ] **Step 6: Run tests to verify pass**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run TestProfile -v`
Expected: PASS (the deterministic backend returns a label for every task via `fallbackLabel`, so all three fields are non-empty).

- [ ] **Step 7: Commit**

```bash
cd ~/keld/keld-cli
git add internal/agent/enrich/extractors.go internal/agent/enrich/types.go internal/agent/enrich/pipeline.go internal/agent/enrich/pipeline_test.go
git commit -m "feat(enrich): activity_type, personal, function_guess passes

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Two-wave pipeline + conditioned `subcategory` pass

**Files:**
- Modify: `internal/agent/enrich/pass.go` (Wave2 + conditioned run), `internal/agent/enrich/pipeline.go` (run Wave2 after Wave1), `internal/agent/enrich/extractors.go` (Wave2 registry)
- Test: `internal/agent/enrich/pass_test.go` (add), `internal/agent/enrich/pipeline_test.go` (add)

**Interfaces:**
- Consumes: `Profile.FunctionGuess` (Task 4), `Subcats` (Task 2).
- Produces: `func Wave2() []Extractor`; `Profile.Subcategory`, `Profile.SubcategoryAlt`. The subcategory pass reads the Wave1 `function_guess` result from `ctx` to pick its label set.

- [ ] **Step 1: Write the failing test**

```go
// internal/agent/enrich/pipeline_test.go  (add)
func TestSubcategoryConditionsOnFunctionGuess(t *testing.T) {
	// deterministic backend keys on "debug" -> eng.debug once function=eng.
	p := Run("debug why this handler throws a 500 error", "eval", Meta{}, NewDeterministic())
	if p.FunctionGuess.Value == "" {
		t.Fatal("no function guess")
	}
	// subcategory id must belong to the guessed function
	subs := Subcats[p.FunctionGuess.Value]
	ok := false
	for _, s := range subs {
		if s.ID == p.Subcategory.Value {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("subcategory %q not under function %q", p.Subcategory.Value, p.FunctionGuess.Value)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run TestSubcategoryConditions -v`
Expected: FAIL — `p.Subcategory` empty (Wave2 not run).

- [ ] **Step 3: Add the conditioned extractor + Wave2**

In `internal/agent/enrich/pass.go`:

```go
// condPassExtractor runs AFTER Wave1; it reads the conditioning pass's id from
// ctx and classifies over that condition's label subset.
type condPassExtractor struct{ p Pass }

func (e condPassExtractor) Name() string    { return e.p.Name }
func (e condPassExtractor) Version() string { return versioned(e.p.Name) }

func (e condPassExtractor) Run(ctx *JobContext) (map[string]any, error) {
	var condID string
	if out := ctx.Get(e.p.ConditionOn); out != nil {
		if l, ok := out[e.p.ConditionOn].(Labeled); ok {
			condID = l.Value
		}
	}
	labels := e.p.LabelsByCond[condID]
	if len(labels) == 0 {
		return map[string]any{e.p.Name: Labeled{}, e.p.Name + "_alt": []Labeled(nil)}, nil
	}
	top, alts := classifyPass(ctx, e.p.Name, labels)
	return map[string]any{e.p.Name: top, e.p.Name + "_alt": alts}, nil
}
```

In `internal/agent/enrich/extractors.go`:

```go
// Wave2 runs after Wave1 and may read Wave1 results (e.g. conditioning).
func Wave2() []Extractor {
	return []Extractor{
		condPassExtractor{Pass{Name: "subcategory", ConditionOn: "function_guess", LabelsByCond: Subcats}},
	}
}
```

- [ ] **Step 4: Run Wave2 in the pipeline**

In `internal/agent/enrich/pipeline.go`, after the Wave1 results are committed with `ctx.Set(...)` and before building the `Profile`, add a sequential Wave2 pass (Wave2 is small; run it serially so it can read Wave1 state safely):

```go
	// Wave2: extractors that depend on Wave1 output (run after commit).
	for _, ex := range Wave2() {
		if out, ok := runStage(ex, ctx); ok {
			ctx.Set(ex.Name(), out)
		} else {
			anyFailed = true
		}
	}
```

Then add to the `Profile{...}` literal:

```go
		Subcategory:    labeledFrom(ctx.Get("subcategory"), "subcategory", "subcategory"),
		SubcategoryAlt: altsNamed(ctx.Get("subcategory"), "subcategory_alt"),
```

Add a small helper next to `altsFrom` in `pipeline.go`:

```go
func altsNamed(out map[string]any, key string) []Labeled {
	if out != nil {
		if a, ok := out[key].([]Labeled); ok {
			return a
		}
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify pass**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run TestSubcategory -v`
Expected: PASS — subcategory id is under the guessed function.

- [ ] **Step 6: Run the full package**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd ~/keld/keld-cli
git add internal/agent/enrich/pass.go internal/agent/enrich/extractors.go internal/agent/enrich/pipeline.go internal/agent/enrich/pipeline_test.go
git commit -m "feat(enrich): two-wave pipeline + conditioned subcategory pass

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Ship the new fields on the `/v1/enrichments` wire + eval

**Files:**
- Modify: `internal/agent/publish/publish.go` (Enrichment + Build), `internal/agent/enrich/eval/eval.go`, `internal/agent/enrich/eval/gold.jsonl`
- Test: `internal/agent/publish/publish_test.go` (add), `internal/agent/enrich/eval/eval_test.go` (if present; else add)

**Interfaces:**
- Consumes: `Profile.Activity/Personal/FunctionGuess/Subcategory/SubcategoryAlt` (Tasks 4-5).
- Produces: additive JSON fields `activity_type`, `personal`, `function_guess`, `subcategory`, `subcategory_alt` on the `Enrichment` wire shape.

- [ ] **Step 1: Write the failing test**

```go
// internal/agent/publish/publish_test.go  (add)
func TestBuildCarriesJobCategoryFields(t *testing.T) {
	p := enrich.Run("write a python function", "claude_code", enrich.Meta{}, enrich.NewDeterministic())
	e := Build(queue.Job{Source: "claude_code"}, p, "a@b.test", false, time.Now())
	if e.Activity.Value == "" || e.FunctionGuess.Value == "" {
		t.Fatalf("expected activity+function_guess on the wire, got %+v", e)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/publish/ -run TestBuildCarriesJobCategoryFields -v`
Expected: FAIL — `e.Activity undefined`.

- [ ] **Step 3: Add fields to `Enrichment` and `Build`**

In `internal/agent/publish/publish.go`, add to the `Enrichment` struct (after `SensitivitySpans`):

```go
	Activity       enrich.Labeled   `json:"activity_type"`
	Personal       enrich.Labeled   `json:"personal"`
	FunctionGuess  enrich.Labeled   `json:"function_guess"`
	Subcategory    enrich.Labeled   `json:"subcategory"`
	SubcategoryAlt []enrich.Labeled `json:"subcategory_alt,omitempty"`
```

And in `Build(...)`, set them from the profile:

```go
		Activity:       p.Activity,
		Personal:       p.Personal,
		FunctionGuess:  p.FunctionGuess,
		Subcategory:    p.Subcategory,
		SubcategoryAlt: p.SubcategoryAlt,
```

- [ ] **Step 4: Run test to verify pass**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/publish/ -run TestBuildCarriesJobCategoryFields -v`
Expected: PASS.

- [ ] **Step 5: Extend the eval gold set for the new facets**

Add `activity_type` and `subcategory` fields to `GoldRow` in `internal/agent/enrich/eval/eval.go`:

```go
type GoldRow struct {
	Text        string `json:"text"`
	TaskType    string `json:"task_type"`
	Domain      string `json:"domain"`
	Sensitivity string `json:"sensitivity"`
	Activity    string `json:"activity_type"`
	Subcategory string `json:"subcategory"`
}
```

Extend `Pred` and `RunModel` similarly (add `Activity`, `Subcategory` from `p.Activity.Value`, `p.Subcategory.Value`), and add the two fields to `fieldOf`. Append at least 12 labeled rows to `internal/agent/enrich/eval/gold.jsonl` covering distinct functions, e.g.:

```json
{"text":"write pytest unit tests for this date parser","task_type":"codegen","domain":"software","sensitivity":"none","activity_type":"generate","subcategory":"eng.test"}
{"text":"summarize this 40-minute meeting transcript into decisions and action items","task_type":"summarization","domain":"business","sensitivity":"none","activity_type":"transform","subcategory":"gen.notes"}
```

(Add ~10 more spanning eng/data/legal/sales/support/fin to give the gate signal.)

- [ ] **Step 6: Run the eval + full suite**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/...`
Expected: PASS. Then record the eval baseline (the eval command/target used elsewhere in this repo, e.g. `go test ./internal/agent/enrich/eval/ -v`) so the schema-v2 accuracy is captured before merge.

- [ ] **Step 7: Commit**

```bash
cd ~/keld/keld-cli
git add internal/agent/publish/publish.go internal/agent/publish/publish_test.go internal/agent/enrich/eval/eval.go internal/agent/enrich/eval/gold.jsonl
git commit -m "feat(publish): emit job-category fields on the enrichment wire + eval

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage** (against `keld-atlas` spec §2):
- Declarative plug-and-play Pass mechanism → Task 3. ✅
- Metadata (repo/tool) preamble threaded in → Task 1. ✅
- Prompt-intrinsic passes activity_type/personal/function_guess → Task 4. ✅
- Conditioned subcategory (device guesses function; ships ranked scores) → Task 5 (+ `subcategory_alt` on the wire, Task 6). ✅
- Category NOT produced on-device → confirmed (only `function_guess` emitted). ✅
- Eval-gated schema bump → Tasks 2 (bump) + 6 (eval fields/rows). ✅
- Privacy: sensitivity/entities still run on raw text (unchanged extractors); preamble only on classification passes → `classifyPass` prepends preamble; `SensitivityExtractor`/`DomainEntitiesExtractor` untouched. ✅
- interaction_mode / deadline_profile / routable → intentionally **out of scope here** (derived deterministically in Atlas, Plan 2), per spec §2.2.

**Deferred to Plan 2 (Atlas):** `team→function` category, reconciliation of `function_guess`/`subcategory_alt`, deterministic facet derivation, confidence-gating method chosen from eval data, API endpoints.

**Placeholder scan:** no TBD/TODO; all code steps carry code. The gold-set expansion (Task 6 Step 5) names concrete rows and a minimum count; the executor extends by the stated pattern.

**Type consistency:** `LabelDef{ID,Text}`, `Pass`, `classifyPass` returning ids, `Labeled` (existing), `versioned()` (existing), `labeledFrom`/`altsNamed` (pipeline helpers) are used consistently across Tasks 3-6.
