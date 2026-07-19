# Agentic corpus — Implementation Plan

> Execute inline (TDD). Steps use `- [ ]`.

**Goal:** Measure classification accuracy on agentic-framework traffic — extend `Meta` with agentic fields, add an `agentic.jsonl` corpus (clean + raw shapes, multi-judge-labeled), and a `--agentic` eval reporting accuracy by shape and augmented-vs-bare.

**Spec:** `docs/superpowers/specs/2026-07-19-agentic-corpus-design.md`.

## Tasks

### Task 1: Extend `Meta` + `Preamble()` (additive, coding-safe)
- Add to `enrich.Meta`: `Framework, AgentRole, Workflow, Step string`, `RecentSteps []string`.
- `Preamble()`: append agentic context parts AFTER the existing coding parts; add a "Recent steps (newest first)" block for `RecentSteps`. Coding path byte-identical.
- TDD: test (a) a coding Meta preamble is byte-identical to the current output, (b) an agentic Meta renders framework/agent/workflow/step + recent steps.

### Task 2: Eval plumbing — GoldRow fields + LoadAgentic + metrics
- `eval.GoldRow`: add `Shape, Framework, AgentRole, Workflow, Step string`, `RecentSteps []string` (json tags). `Meta(source)` populates the agentic Meta fields.
- `//go:embed agentic.jsonl` + `LoadAgentic()`.
- `AccuracyByShape(gold, pred, facet) map[string][2]int` (shape → [correct,total]).
- TDD: metric test with a tiny fixture.

### Task 3: `--agentic` command
- `keld-agent eval --agentic`: load agentic rows; run augmented (`RunModelWithContext`) + bare (`RunModel`); print task_type + domain accuracy overall, by shape, and augmented−bare delta.

### Task 4 (controller): generate + label the corpus
- Subagent generators produce ~40 clean agentic sub-tasks across frameworks/roles/tasks; for ~half, also produce the matching FULL RAW LLM call (same underlying task) → ~60-80 rows total (clean + raw).
- 3 blind judges label task_type + domain + activity_type + speech_act on the CLEAN texts; consensus → gold. Build agentic.jsonl with metadata + labels + shape.

### Task 5 (controller): measure
- Rebuild, warm sidecar, `keld-agent eval --agentic`. Record: overall + clean-vs-raw + augmented-vs-bare in HANDOFF. No regression on the main gold set (coding preamble unchanged).
