# Speech-act facet + adversarial eval expansion (foundation for the speech-act lever)

**Date:** 2026-07-17
**Status:** Approved (brainstorm) — ready for implementation planning
**Scope:** Go-only. Add a new emitted `speech_act` enrichment facet, expand the
eval harness with an adversarial `s1` class + speech-act metrics, and pick the
facet's label wording by bakeoff. **Conditioning** (using speech_act to resolve
task_type/activity) is explicitly a **follow-up spec**, designed against the
baseline this one produces.

## Background / motivation

The enrichment pipeline classifies each prompt into a `Profile` (task_type,
domain, sensitivity, activity_type, personal, function_guess, subcategory). Prior
work (A0/A4 in v0.4.0, A6 in v0.5.0) established the winning pattern here:
**structural, subject-independent signals beat letting the bi-encoder key on
subject nouns**, and for a bi-encoder the **label wording is the dominant lever**
("software engineering" beat "codegen" 10× on task_type leakage).

Speech act — whether the current prompt is a **command**, a **question**, a
**statement**, or a terse **fragment** — is another subject-independent,
structural axis. It is expected to help resolve facets that the current pipeline
conflates: e.g. *"How would you add pagination here?"* is a question to answer
(`rag_qa`/`reasoning`), not code to write (`codegen`); *"The migration keeps
deadlocking."* is a bug report whose ask is diagnosis, not a command.

**The blocker this spec removes:** the eval sets are ~all imperative (gold: 8/73
interrogative; confound c1/c2/c3 are commands/drafts). A speech-act signal is
currently **unmeasurable** — we cannot tell whether it helps. This spec builds
the measurable substrate (facet + adversarial rows + metrics + tuned wording) so
the conditioning lever can be designed against real numbers, the same way A6 was.

## Goals

1. Emit a new `speech_act` facet in `Profile` (contract change; Atlas sees it).
2. Make it **trustworthy**: label wording chosen by bakeoff, accuracy measured on
   an adversarial set, not assumed.
3. Produce `s1_downstream_baseline` — the headroom number the future conditioning
   lever must beat.
4. Zero regression on every existing facet.

## Non-goals (deferred to a follow-up spec)

- **The conditioning mechanism.** How speech_act feeds task_type/activity (soft
  preamble-tag vs hard candidate-restriction; which facets), designed against the
  `s1_downstream_baseline` this spec produces.
- Any change to task_type / activity_type derivation.

## Design

### A. The `speech_act` facet

- **Vocabulary** (`labels.go`): canonical IDs `command` / `question` /
  `statement` / `fragment` as `SpeechActDefs []LabelDef{ID, Text}`. IDs are stable
  (Atlas contract, human-readable — plain words, not the Latinate mood terms,
  which also read better to a bi-encoder). `Text` is the readable phrase the model
  classifies against, **chosen by the bakeoff (section C)** — not hardcoded here.
- **Extractor** (`speechact.go`, new): `SpeechActExtractor` added to **Wave1**. It
  is independent of every other facet, so it is safe in the order-independent,
  batch-committed Wave1. It classifies **the current prompt only** (`ctx.Text`),
  *not* `Meta.Preamble()+ctx.Text`: mood is a property of the actual ask, and the
  preamble's context metadata (repo/branch/recent-prompts) would only muddy it.
  Emits `speech_act` + `speech_act_alt`, same shape as task_type, routed through
  the `classifyPass` id-mapping so the emitted value is always a canonical id.
- **Profile / contract** (`types.go`, `labels.go`): add `SpeechAct Labeled` +
  `SpeechActAlt []Labeled` to `Profile`; **SchemaVersion 4 → 5** (a new emitted
  field is a real contract change, unlike the v3/v4 derivation bumps).

**Known ripples:** `pipeline_test.go` asserts `len(ExtractorVersions) == 7` →
becomes 8; the `enrichtest` fake needs `speech_act` keyword priors or it abstains
(add them, drift-proof if practical).

### B. Eval-set expansion

- **Schema** (`eval.go`): add `SpeechAct string \`json:"speech_act"\`` to
  `GoldRow`. Blank = "not scored" (existing optional-facet convention).
- **The `s1` adversarial class** — rows where **mood is the trap**: the speech-act
  reading points to a different (correct) downstream label than the naive/keyword
  reading. Four trap families:
  - **Question-in-coding-context** → tempts `codegen`; correct `rag_qa`/`reasoning`,
    activity `retrieve`/`converse`. *"How would you add pagination to this
    endpoint?"*, *"Should we cache this query in Redis or Memcached?"*
  - **Declarative bug/observation** → the ask is diagnosis, not a command. *"The
    migration keeps deadlocking under load."*, *"Latency doubled after the last
    deploy."* → `reasoning`/`analyze`.
  - **Fragment follow-ups** (c3 shape, labeled for mood). *"and the other endpoint
    too"*, *"ok ship it"* → `fragment`.
  - **Genuine command** (control cases, so the metric isn't one-sided). *"Add a
    rate limiter."* → `command`.
- Each `s1` row carries `text`, `class:"s1"`, `speech_act` (gold), the trapped
  downstream gold (`task_type` and/or `activity_type`), and a realistic `source`
  (coding tool vs generic) so no subject confound is reintroduced.
- **Counts / balance:** ~20 `s1` rows, weighted toward the under-represented moods
  (`question`/`statement`/`fragment`), each crossed with eng and non-eng subjects.
- **Backfill** a `speech_act` label onto **every** existing `gold.jsonl` row (73
  rows — most `command`, the 8 "?" rows `question`, plus any statements/fragments
  present) — cheap broad coverage to score the facet's plain accuracy beyond the
  adversarial set. (Rows genuinely too ambiguous to label are left blank = unscored.)
- **File:** `s1` rows go into the existing `confound.jsonl` (new class beside
  c1/c2/c3) so `LoadConfound` + `--confound` pick them up with no new plumbing;
  metrics filter by class.
- **Labeling quality (the real risk):** a mislabeled "correct" downstream answer
  makes the metric lie. Mitigation: every `s1` trap is **clear-cut** (a defensible
  single correct answer), each row gets a one-line rationale, and the drafted rows
  are **reviewed before they are locked** (spec/plan review gate).

### C. Metrics, bakeoff, testing

- **`speech_act_accuracy`** — add `speech_act` to the scored `fields` list +
  `fieldOf()`; `Score` then computes per-facet accuracy over every row with a gold
  `speech_act`. Report per-mood as well (`fragment`/`statement` are the hard ones).
- **`s1_downstream_baseline`** — a targeted metric (sibling of `LeakageRate`): over
  `s1` rows, the *current, unconditioned* error rate on the trapped downstream
  facet(s). Precisely: for each `s1` row, the trapped facet is whichever of
  `task_type` / `activity_type` the row sets a gold value for; the metric is the
  fraction of those (row, facet) pairs where prediction ≠ gold. Not fixed here — it
  is the number the follow-up conditioning lever targets.
- Both print under `--confound`, beside the existing `leakage`/`false_eng` line.
- **Bakeoff** (reusable throwaway, A6-style): classify the labeled speech_act rows
  against candidate `SpeechActDefs` wordings directly against the live sidecar,
  score `speech_act_accuracy` per candidate (watch the confusable pairs
  `command`↔`fragment`, `question`↔`statement`), pick the winner, lock the defs,
  delete the diagnostic, and record the winning + runner-up wordings in the HANDOFF.
- **Testing:** `speechact_test.go` (extractor routes the labeled path, maps
  text→id); add `speech_act` keyword priors to the `enrichtest` fake; bump
  `pipeline_test.go` count 7→8 and the schema test 4→5. No-regression gate: full
  eval before/after must leave all *other* facet accuracies flat (speech_act is
  Wave1-independent — verify, not assume).

## Success criteria

- Bakeoff-tuned `speech_act_accuracy` reported (soft bar ~≥0.8 overall; a low
  number is a **finding** that informs the conditioning spec, not a silent pass).
- `s1_downstream_baseline` recorded for the follow-up.
- `speech_act` emitted in `Profile`; SchemaVersion 5; full `go test ./...` green.
- All other facet accuracies flat vs the pre-change eval.

## Rollout

Same measure-first loop as A6: implement, run the bakeoff to lock wording, run the
full eval (both gates), keep only on a clean no-regression result, then ship
(schema-v5 release). The emitted facet ships even if conditioning is never built;
conditioning is a separate, later, measured decision.
