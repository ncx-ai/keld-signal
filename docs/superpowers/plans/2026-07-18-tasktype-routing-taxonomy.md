# task_type routing-aligned taxonomy — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Replace the `task_type` vocabulary with the routing-aligned 10-entry taxonomy (drop `agentic_tool_use`, add `text_generation`+`rewriting`, rename to HF conventions, `other`→`general`), relabel the eval gold set to match, and bakeoff-select the label descriptions — measured to raise task_type accuracy with no regression.

**Architecture:** task_type keeps its A6 path (`classifyPass` over `TaskTypeDefs` + context preamble); only the vocabulary (`TaskTypes` in `labels.go`, `TaskTypeDefs` in `a6_tasktype.go`) and the label descriptions change. Schema bumps 5→6. The eval gold set is remapped to the new labels (review-gated). A bakeoff finalizes the descriptions, watching the research-predicted conflation pairs.

**Tech Stack:** Go 1.26 (`export PATH="/opt/homebrew/bin:$PATH"`, `gofmt -l` clean before commit); the warm dev sidecar (port in `~/.keld/agent.json`) for the bakeoff/measurement.

**Spec:** `docs/superpowers/specs/2026-07-18-tasktype-routing-taxonomy-design.md`. **Research:** `docs/superpowers/plans/2026-07-18-tasktype-taxonomy-research.md`.

## Global Constraints

- Go only; `gofmt -l .` empty before every commit (CI gate).
- Canonical task_type ids (stable, Atlas contract): `summarization, translation, code_generation, information_extraction, classification, reasoning, question_answering, text_generation, rewriting, general`. Never emit anything else.
- `TaskTypeDefs` label TEXT is provisional in Task 1 and bakeoff-finalized in Task 3; keep A6's winning `code_generation`="software engineering" as the starting point.
- Bi-encoder guardrails (A2/A6): descriptions SHORT, positive, discriminative, no negation; HF-aligned phrasings.
- Measure-first, strict no-regression: keep only if task_type gold accuracy ↑ vs 0.696, leakage flat-or-down, every OTHER facet flat.
- Gold relabel (Task 2) is REVIEW-GATED — a mislabel makes the bakeoff optimize against wrong targets.

---

### Task 1: New task_type vocabulary + plumbing (schema v6)

**Files:**
- Modify: `internal/agent/enrich/labels.go` (`TaskTypes`, `SchemaVersion` 5→6 + comment)
- Modify: `internal/agent/enrich/a6_tasktype.go` (`TaskTypeDefs`)
- Modify: `internal/agent/enrich/labels_test.go` (schema test 5→6)
- Modify: `internal/agent/enrich/a6_tasktype_test.go` (expected id `codegen`→`code_generation`)
- Modify: `internal/agent/enrich/extractors_test.go` (`TestTaskTypeExtractorTopLabel` expect `code_generation`)
- Modify: `internal/agent/enrich/pipeline_test.go` (task_type expects `code_generation`)
- Modify: `internal/agent/enrich/enrichtest/enrichtest.go` (task_type keyword priors for new ids)
- Modify: `internal/agent/enrich/enrichtest/enrichtest_test.go` (`TestFakeClassifyCodegen` → `code_generation`; `TestFakeClassifyFallsBackToOther` → `general`)

**Interfaces:**
- Produces: `TaskTypes []string` (10 ids) and `TaskTypeDefs []LabelDef` (10 entries, ids match) both updated in lockstep; `SchemaVersion == 6`.

- [ ] **Step 1: Replace `TaskTypes`** (`labels.go`)

```go
// TaskTypes is the canonical task_type vocabulary — routing keys for Keld
// Inference Exchange order books (real-world async inference job categories).
// Text jobs only; modality is a separate future axis. See the taxonomy spec.
var TaskTypes = []string{
	"summarization", "translation", "code_generation", "information_extraction",
	"classification", "reasoning", "question_answering", "text_generation",
	"rewriting", "general",
}
```

- [ ] **Step 2: Bump the schema** (`labels.go`)

```go
// ... and v6, which redesigned the task_type vocabulary into routing-aligned
// job categories (dropped agentic_tool_use, added text_generation + rewriting,
// renamed to HF conventions, other→general).
const SchemaVersion = 6
```
(Extend the existing SchemaVersion doc comment; keep prior sentences.)

- [ ] **Step 3: Replace `TaskTypeDefs`** (`a6_tasktype.go`) — provisional descriptions (Task 3 finalizes)

```go
var TaskTypeDefs = []LabelDef{
	{"summarization", "summarizing text into a shorter form"},
	{"translation", "translating text between languages"},
	{"code_generation", "software engineering"},
	{"information_extraction", "extracting structured data or entities from text"},
	{"classification", "categorizing or labeling an input"},
	{"reasoning", "reasoning, analysis, math, or planning"},
	{"question_answering", "answering a question from documents or knowledge"},
	{"text_generation", "writing new content from scratch"},
	{"rewriting", "editing or rewriting existing text"},
	{"general", "a general or unclear request"},
}
```
(Keep the existing comment explaining the "software engineering" framing; update it to note the vocab is now the routing taxonomy.)

- [ ] **Step 4: Update the enrichtest fake keyword priors** (`enrichtest.go`)

Replace the `taskKW` map in `NewFake()` with the new ids (the description-alias loop over `enrich.TaskTypeDefs` stays unchanged):

```go
	taskKW := map[string][]string{
		"code_generation":        {"function", "code", "implement", "class", "refactor", "deploy", "migration"},
		"summarization":          {"summarize", "summary", "tldr"},
		"translation":            {"translate", "translation"},
		"information_extraction": {"extract", "parse", "pull out"},
		"classification":         {"classify", "categorize", "label"},
		"reasoning":              {"why", "reason", "prove", "analyze"},
		"question_answering":     {"according to", "based on the", "what does the doc"},
		"text_generation":        {"draft", "compose", "write a post", "write an email"},
		"rewriting":              {"rewrite", "edit", "revise", "paraphrase"},
		"general":                {},
	}
```
(`general` has no keywords → the fake's `fallbackLabel` returns it when present, preserving the "unmatched → general" behavior.)

- [ ] **Step 5: Update the schema + vocab-referencing tests**

`labels_test.go`:
```go
	if SchemaVersion != 6 {
		t.Fatalf("SchemaVersion = %d, want 6", SchemaVersion)
	}
```
`a6_tasktype_test.go` `TestTaskTypeDescriptions`: the `want: "software engineering"` case must expect id `code_generation` (was `codegen`).
`extractors_test.go` `TestTaskTypeExtractorTopLabel`: input "write a function in go" now expects `code_generation`.
`pipeline_test.go` `TestRunProducesEnrichedProfile`: task_type expects `code_generation` (input "write a go function…").
`enrichtest_test.go`: `TestFakeClassifyCodegen` expects `code_generation`; `TestFakeClassifyFallsBackToOther` (input "zzzzz") now expects `general` (rename the test to `…FallsBackToGeneral`).

- [ ] **Step 6: Run the enrich packages + build + gofmt**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/... && go build ./... && gofmt -l internal/agent/enrich/`
Expected: PASS, no build errors, gofmt empty. If a test still references an old id (`codegen`/`rag_qa`/`extraction`/`agentic_tool_use`/`other`), fix it to the new vocab.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/enrich/
git commit -m "feat(enrich): routing-aligned task_type taxonomy (schema v6)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: Relabel the eval gold set to the new taxonomy (REVIEW GATE)

**Files:**
- Modify: `internal/agent/enrich/eval/gold.jsonl`
- Modify: `internal/agent/enrich/eval/confound.jsonl`

**This task STOPS before commit for a human review gate** — the relabel is the crux.

**Interfaces:** every `task_type` value in both files becomes one of the 10 new ids; new rows add coverage for `text_generation`, `rewriting`, `general`.

- [ ] **Step 1: Mechanical renames** (both files) — via a script or careful edit, remap `task_type`:
  - `codegen` → `code_generation`
  - `extraction` → `information_extraction`
  - `rag_qa` → `question_answering`
  - (`summarization`, `translation`, `classification`, `reasoning` unchanged for now)

- [ ] **Step 2: Relabel `agentic_tool_use` rows** (11 across both files) — each to its underlying inference job, by reading the text:
  - deploy / run migration / push-with-token / configure deployment → `code_generation`
  - book meeting + email agenda / draft outreach / update profile (compose text) → `text_generation`
  - genuinely just tool-invocation with no inference job → `general`
  Record the per-row decision in the report.

- [ ] **Step 3: Split `other` rows** (15 across both files) into `text_generation` / `rewriting` / `general` by reading each:
  - draft/write new prose, email, story, joke → `text_generation`
  - edit/rewrite/improve/critique user-supplied text → `rewriting`
  - genuinely general/mixed/how-to → `general`

- [ ] **Step 4: Reclassify mislabeled `summarization` rows** — any `summarization` row whose task is to MODIFY user text (rewrite/condense-in-place/critique) → `rewriting`; keep true "summarize into shorter form" as `summarization`.

- [ ] **Step 5: Add coverage rows** — append to `gold.jsonl` enough rows that `text_generation`, `rewriting`, and `general` each have ≥4 labeled examples (they're currently absent/thin). Realistic prompts, correct `task_type`, plus `domain`/`sensitivity` where natural (leave optional facets blank if unclear). Examples:

```json
{"text":"Write a launch announcement blog post for our new analytics dashboard.","task_type":"text_generation","domain":"business"}
{"text":"Draft a friendly reminder email to customers with overdue invoices.","task_type":"text_generation","domain":"business"}
{"text":"Rewrite this paragraph to be more concise and professional.","task_type":"rewriting"}
{"text":"Edit my cover letter for grammar and tone.","task_type":"rewriting"}
{"text":"Paraphrase this product description so it doesn't duplicate the original.","task_type":"rewriting"}
{"text":"Help me plan my week and figure out what to prioritize.","task_type":"general"}
{"text":"What are some good icebreakers for a team offsite?","task_type":"general"}
```

- [ ] **Step 6: Validate parse + distribution**

Run:
```bash
export PATH="/opt/homebrew/bin:$PATH"; cd ~/keld-signal
python3 - <<'PY'
import json,collections
for f in ["internal/agent/enrich/eval/gold.jsonl","internal/agent/enrich/eval/confound.jsonl"]:
    rows=[json.loads(l) for l in open(f) if l.strip()]
    print(f, "rows:", len(rows), "task_type:", dict(collections.Counter(r.get("task_type","(blank)") for r in rows)))
    bad=[r["task_type"] for r in rows if r.get("task_type") and r["task_type"] not in {"summarization","translation","code_generation","information_extraction","classification","reasoning","question_answering","text_generation","rewriting","general"}]
    if bad: print("  !! non-vocab task_type:", set(bad))
PY
go test ./internal/agent/enrich/eval/
```
Expected: both parse; NO non-vocab task_type; every category (esp. text_generation/rewriting/general) covered; eval tests PASS.

- [ ] **Step 7: STOP — REVIEW GATE.** Do NOT commit. Report the full relabel (every `agentic_tool_use` and `other` row's new label + rationale, the summarization reclassifications, and the new rows) for controller/human review. Apply requested changes, re-run Step 6, THEN commit:

```bash
git add internal/agent/enrich/eval/gold.jsonl internal/agent/enrich/eval/confound.jsonl
git commit -m "test(eval): relabel gold to routing-aligned task_type taxonomy

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: Bakeoff the label descriptions + measure (controller-run, live sidecar)

**Files:**
- Create (throwaway): `internal/agentcli/tasktype2_bakeoff_test.go`
- Modify: `internal/agent/enrich/a6_tasktype.go` (lock bakeoff winner)
- Modify: `docs/superpowers/HANDOFF.md` (results)

- [ ] **Step 1: Ensure a warm 8-thread sidecar** (reuse the running dev daemon; re-warm per HANDOFF recipe if idle-unloaded). Build: `go build -o /tmp/exp-tt ./cmd/keld-agent`.

- [ ] **Step 2: Write the bakeoff diagnostic** (A6/speech-act pattern) — for each candidate `TaskTypeDefs` wording set, classify the relabeled gold rows (with context preamble) against the live sidecar; report overall task_type accuracy + per-category correct/total + the 4 conflation pairs (reasoning↔classification/question_answering, information_extraction↔classification, information_extraction↔summarization, text_generation↔summarization/translation). Candidates should vary the weakest members: `reasoning` (e.g. "reasoning, analysis, math, or planning" vs "step-by-step reasoning and problem solving" vs "analyzing and deciding"), `rewriting` (vs "improving or revising existing text"), `text_generation` (vs "drafting original writing"), `general`.

- [ ] **Step 3: Run the bakeoff; pick the winner** — highest overall accuracy with NO collapsed category and the conflation pairs minimized. Run:
`export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agentcli/ -run TestTaskType2Bakeoff -v -count=1`

- [ ] **Step 4: Lock the winning descriptions** into `TaskTypeDefs` (`a6_tasktype.go`); delete the throwaway bakeoff; `gofmt -l` clean.

- [ ] **Step 5: Full no-regression eval (both gates)**

```bash
go build -o /tmp/exp-tt ./cmd/keld-agent
/tmp/exp-tt eval --context                     # task_type gold accuracy (new vocab) — must beat 0.696
/tmp/exp-tt eval --confound --context          # leakage flat-or-down; other facets flat
/tmp/exp-tt eval --context --calibration       # did dropping agentic_tool_use/other cut high-confidence error mass?
```
Success: task_type gold accuracy ↑ vs 0.696; leakage(task_type) ≤ baseline; function_guess/speech_act/domain/etc. unchanged; calibration ECE for task_type improved or flat.

- [ ] **Step 6: Full unit suite** — `go test ./...` all PASS.

- [ ] **Step 7: Record in HANDOFF** — the winning wordings, task_type accuracy before/after, per-category breakdown, the conflation-pair outcomes, calibration delta, and confirmation of no regression. Commit:

```bash
git add internal/agent/enrich/a6_tasktype.go docs/superpowers/HANDOFF.md
git commit -m "feat(enrich): lock bakeoff-selected task_type descriptions; record results

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Notes for the implementer
- Do NOT add modality categories, `data_analysis`, or any order-book/routing logic — all out of scope (spec non-goals).
- task_type still routes through the A6 `classifyPass`(TaskTypeDefs) path; you are changing vocab + descriptions, not the mechanism.
- The gold relabel (Task 2) review gate is mandatory — a wrong "correct" label silently corrupts the bakeoff and the accuracy number.
