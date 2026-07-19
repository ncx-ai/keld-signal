# Agentic-framework test corpus + Meta augmentation

**Date:** 2026-07-19
**Status:** Approved (brainstorm) — ready for implementation
**Scope:** Go (enrich Meta + eval) + a new eval corpus. Measure enrichment
classification accuracy against realistic **agentic-framework** traffic (Mastra,
LangChain/LangGraph, CrewAI) — both the prompt shapes and the metadata/context we
would realistically use to augment. A lot of future inference traffic is agentic;
this makes that traffic measurable.

## Motivation

The gold set is human-typed prompts (coding assistant + general). Agentic-framework
LLM calls differ structurally: the "prompt" is often a system prompt + tool
definitions + message/observation history + a current sub-task, and the available
context is agentic metadata (framework, agent role, workflow step, prior steps),
not repo/branch. We need to measure how well the sidecar classifies agentic traffic
and whether agentic metadata augmentation helps — separately from the human baseline.

## Design

### 1. `Meta` + `Preamble()` extension (additive, coding-safe)

Extend `enrich.Meta` with agentic fields (empty for coding tools):
`Framework, AgentRole, Workflow, Step string` and `RecentSteps []string`.
`Preamble()` renders them **appended after** the existing coding fields, and a
"Recent steps (newest first)" block mirroring RecentPrompts. The existing coding
path (`repository: none` base + repo/branch/project/tool + RecentPrompts) renders
**byte-identical** — so all validated coding/agentic-neutral numbers are unchanged.
Agentic rows render e.g.:
`[Context — repository: none; framework: langgraph; agent: billing_assistant; workflow: research_pipeline; step: 4]` + a Recent steps block.

### 2. The corpus (`agentic.jsonl`, ~60–80 rows)

Two prompt **shapes**, ~half each (per the "both/configurable" decision):
- `shape: "clean"` — the extracted current sub-task an integration that parses the
  agent step would send (short, e.g. "Summarize the findings from the web_search
  results"). Metadata is the primary context here.
- `shape: "raw"` — a realistic FULL LLM call: system prompt (role + tool
  definitions + output-format rules) + message/observation history + current task,
  concatenated. Tests whether the sidecar finds the underlying task under the
  scaffolding.

Each row carries the agentic metadata (`framework, agent_role, workflow, step,
recent_steps`) and the labels. Frameworks/roles varied (research/support/billing/
data/coding agents across mastra/langchain/langgraph/crewai). Realistic tool
mentions inside the text, but the `tool` field is NOT modeled as augmentation
(excluded — near-circular routing signal). Model/params NOT modeled (routing
metadata, not a classification input).

### 3. Eval plumbing

- `eval.GoldRow` gains `Shape, Framework, AgentRole, Workflow, Step string` +
  `RecentSteps []string`; `GoldRow.Meta()` populates the agentic Meta fields.
- `LoadAgentic()` reads the embedded `agentic.jsonl`.
- `keld-agent eval --agentic`: runs the corpus **augmented** (RunModelWithContext,
  agentic Meta in the preamble) AND **bare** (RunModel, empty Meta), and reports
  task_type + domain accuracy: overall, split by **shape (clean vs raw)**, and the
  **augmented − bare** delta (does the agentic metadata help — the A0 analog).

### 4. Labeling

Same multi-judge consensus (3 blind judges) for task_type + domain + activity_type
+ speech_act. Judges see the CLEAN sub-task text (the underlying job) so labels
reflect the true task regardless of shape; the same label applies to a clean row
and its raw counterpart where paired. Consensus → gold; splits → blank (unscored).

## Non-goals

- **Production agentic integration** (the daemon actually receiving agentic
  requests) — this is the eval corpus + the Meta plumbing that enables it, not the
  framework SDK integration.
- **tool / model augmentation** — excluded by decision.
- Changing the taxonomy — agentic sub-tasks use the same task_type/domain vocab.

## Success criteria

- `agentic.jsonl` (~60–80 rows, clean+raw), multi-judge-labeled.
- `keld-agent eval --agentic` reports task_type/domain accuracy overall, by shape,
  and augmented-vs-bare.
- `Meta` extension leaves every existing (coding) preamble byte-identical — all
  prior facet numbers unchanged.
- We learn: (a) agentic classification accuracy, (b) the raw-scaffolding penalty,
  (c) whether agentic metadata recovers it.
