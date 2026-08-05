# Enrichment Gating (Turn-Type Gate + Length Pre-filter) — Design

- **Date:** 2026-08-05
- **Status:** Approved (design), pending implementation plan
- **Repo:** keld-signal (keld-agent on-device daemon)
- **Related:** Atlas gallery "Turn Type" reporting dim (separate); the empirical study
  `turntype_study.py` / `speechact_gate_study.py`.

## Problem

`enrich.Run` runs **every** Wave1 pass (7 built-ins + custom) plus Wave2 for **every** prompt/turn,
one model inference per pass — 8-9 sidecar inferences per turn (`pipeline.go`). A large fraction of
Claude Code / Codex turns are content-free follow-ups ("ok", "ok, do that", "continue"). On those,
the semantic classifiers (task_type, domain, activity, function_guess, subcategory, personal) have
nothing to classify — they burn compute and, worse, a zero-shot model may emit a confidently-wrong
label instead of abstaining. Only the safety/governance pass (`sensitivity`) has value on such turns.

## Goal

Skip the semantic passes on content-free turns, while **always** running governance
(`sensitivity` + the deterministic credential/PII detector). Gate on a cheap, safe signal that
never drops a real request.

## Decisions (approved)

1. **Gate signal = the existing `speech_act` pass + a length/approval pre-filter** — reuse, no new
   classifier. (The Atlas gallery "Turn Type" is a separate reporting dimension; the on-device gate
   uses the built-in `speech_act`, whose doc already anticipated conditioning other passes.)
2. **Gated turns still publish** a minimal enrichment (`sensitivity` + `speech_act` filled, semantic
   fields empty) with `pipeline_status = "gated"` — preserves turn count, safety coverage, and the
   "this was an approval" signal, and is distinguishable from `partial` (timeout) and `ok`.
3. **Env flag, default-off first**: `KELD_ENRICH_GATE_ENABLED` (per-machine), following the
   `gatingEnabled()` env precedent in `a6_tasktype.go` / `a4_compositional.go`. Org-level remote
   policy is a deliberate follow-on, not v1.

## Empirical validation (why this is safe)

Ran the real GLiNER2 sidecar over 40 realistic turns (16 content-free, 24 substantive), classifying
`speech_act` with its actual described labels, plus the pre-filter:

- `speech_act == fragment` alone: **precision 100%, recall 56%, 0/24 dangerous** false gate-offs.
- pre-filter alone: precision 100%, recall 100% on-corpus, 0/24 dangerous.
- **Combined (pre-filter OR fragment): precision 100%, recall 100%, 0/24 dangerous.**

Complementary by construction: `speech_act` catches acknowledgement fragments ("ok", "thanks",
"hmm"); the pre-filter catches approval-imperatives the model calls *command*/*statement* ("do
that", "ok, do that", "lgtm", "ship it"). **Gate on `fragment` only — never `statement`** (real
corrections like "That's wrong — the import should be relative" are *statement* and substantive).
Directional (synthetic corpus, Atlas sidecar); the invariant that matters (0 dangerous) is
structural — substantive turns classify command/question/statement with high confidence, never
fragment, and don't match the narrow lexicon.

## Design

### The gate decision (per turn, before the semantic passes)

```
gateOff  :=  KELD_ENRICH_GATE_ENABLED
             AND ( prefilterContentFree(text)  OR  speechActTop == "fragment" )
```

- **Pre-filter** (`prefilterContentFree`, no model): normalize to lowercase alpha tokens; content-free
  iff `1 <= len(tokens) <= N` (N≈5) **and every token ∈ APPROVAL lexicon**
  (`ok, okay, yes, yep, yeah, sure, go, ahead, do, that, it, this, continue, proceed, lgtm, thanks,
  thank, you, perfect, sounds, good, ship, please, now, cool, great, nice, hmm, wait, sec, one,
  works, fine, done` — tunable). Deliberately narrow: a miss only wastes compute, never drops real
  work. Token cap N tunable via `KELD_ENRICH_GATE_MAX_TOKENS`.
- **`speech_act`** is computed early (it's already a pass) and its top label feeds the gate. Gate on
  `fragment` only.

### Pipeline restructure (`internal/agent/enrich/pipeline.go`)

Introduce an **always-run vs gated** distinction (none exists today — Wave1 is a flat slice):

- **Always-run passes:** `sensitivity` (governance) and `speech_act` (the gate signal). These run on
  every turn regardless of the gate.
- **Gated passes:** the remaining Wave1 semantic passes (`task_type`, `domain_entities`,
  `activity_type`, `personal`, `function_guess`) and **all** Wave2 (`subcategory`) and **all custom
  passes** (org classifiers are semantic; a custom governance pass is out of scope for v1).

Order within a gated run:
1. Compute `prefilterContentFree(text)` (no model).
2. Run the always-run passes: `speech_act` (unless pre-filtered — then set `speech_act=fragment`
   deterministically to save the call; see Open question O1), then `sensitivity`.
3. Compute `gateOff` from pre-filter + `speech_act`.
4. If `gateOff`: skip all gated passes; leave their `Profile` fields empty; set
   `PipelineStatus="gated"`. Else: run the gated Wave1 passes + Wave2 exactly as today.

Preserve the existing invariants: passes still run one-at-a-time (no goroutine fan-out; sidecar
memory safety), results buffered then committed, per-pass timeout isolation unchanged. The gate only
*removes* passes from the run; it never parallelizes.

Marking always-run vs gated: add an `AlwaysRun() bool` method to the `Extractor` interface
(default false via a small embed/helper), returning true for `SensitivityExtractor` and
`SpeechActExtractor`. This keeps the distinction in the type system rather than a name set in
`pipeline.go` (which would silently rot if a pass is renamed). Custom extractors return false.

### Gated `Profile` shape

- `sensitivity` (+ spans/creddetect) and `speech_act` populated normally.
- All other typed fields at their zero/empty value; `Custom` empty.
- `PipelineStatus = "gated"` (new value alongside `ok`/`partial`).
- Published exactly like any other profile (`publish.Build` → `POST /v1/enrichments`). Atlas stores
  `pipeline_status` verbatim; a gated turn's empty semantic fields already read as "uncategorized"
  in Atlas coverage — honest.

### Config

- `KELD_ENRICH_GATE_ENABLED` — `""`/`0`/`false` (default) ⇒ gate OFF (today's behavior, all passes
  run); `1`/`true` ⇒ gate ON. Resolved via a `gateEnabled()` helper mirroring `gatingEnabled()` in
  `a6_tasktype.go`.
- `KELD_ENRICH_GATE_MAX_TOKENS` — pre-filter token cap (default 5).
- No `settings.Remote` / Atlas contract change in v1 (org-level policy is a follow-on).

### Observability

Log (debug) per gated turn: the deciding signal (pre-filter vs fragment) and the count of passes
skipped, so the compute saving is measurable during rollout. No new metric export required for v1.

## Testing (Go, `go test ./...`; light — not the web suite)

Extend `internal/agent/enrich/pipeline_test.go` with a **call-counting fake `Model`** (pattern from
`passtimeout_test.go`'s `blockingModel`):
- **Gated turn skips semantic passes:** gate on, input a pre-filtered "ok" (and separately a
  `fragment`-classified input via the fake) → assert the fake's `Classify`/`Entities` was **not**
  called for `task_type`/`domain_entities`/`activity_type`/`function_guess`/`subcategory`, that
  `sensitivity` **was** run, `speech_act` is set, and `PipelineStatus=="gated"`.
- **Substantive turn unchanged:** gate on, input "Add a rate limiter…" → all passes run,
  `PipelineStatus` unchanged from today.
- **Gate off (flag disabled):** `KELD_ENRICH_GATE_ENABLED` unset → every pass runs even for "ok"
  (today's behavior preserved).
- **Pre-filter unit tests** (`prefilterContentFree`): table test over the study corpus — every
  content-free prompt true, every substantive prompt false; the narrowness invariant (a 6+ token
  prompt, or any non-approval token, ⇒ false).
- **`AlwaysRun()`**: sensitivity + speech_act true; others + custom false.

## Rollout

1. Ship with `KELD_ENRICH_GATE_ENABLED` unset (default off) — zero behavior change; the code path
   is exercised only by tests.
2. Enable on a dev/canary machine; confirm gated turns publish with `pipeline_status="gated"`,
   semantic fields empty, sensitivity present, and the inference-count drop in logs.
3. Once validated, flip the default to on (and/or graduate to an org-level remote policy).

## Open questions

- **O1 — speech_act on pre-filtered turns:** run it anyway (2 inferences on filler, most honest
  label) vs. set `speech_act=fragment` deterministically to save the call (1 inference). Default:
  run it (simple, honest); the determinism optimization can follow if inference budget matters.
- **O2 — Atlas rendering of `gated`:** Atlas stores `pipeline_status` opaquely, so no change is
  required for correctness; surfacing "gated" vs "uncategorized" in Atlas coverage reporting is a
  separate, optional Atlas follow-on.

## Out of scope

- Any new on-device "Turn Type" classifier (we reuse `speech_act`).
- Org-level remote gating policy (`settings.Remote`) and its Atlas contract.
- Atlas-side reporting changes for the `gated` status.
- Gating custom passes by a per-pass governance marker (all custom passes are gated in v1).
