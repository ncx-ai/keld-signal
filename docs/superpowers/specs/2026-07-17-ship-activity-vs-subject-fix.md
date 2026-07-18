# Ship the activity-vs-subject enrichment fix (A0 + A4)

**Date:** 2026-07-17
**Status:** Approved — finalization of the validated experiment
**Scope:** Go-only. Promote the two validated wins to default, prune the rejected
experiments, bump the schema, ship v0.4.0.

## What the experiment established (validated on the source-attributed eval)

- **A0** — `task_type` gets the context preamble: `task_type` leakage 0.625→0.500,
  zero regression. **KEEP.**
- **A4** — compositional `function_guess` (coding tool ⇒ `eng`): `function_guess`
  leakage 0.375→0.000, `false_eng` 0→0, confound function accuracy 0.773→0.909,
  gold-only flat (0.800). **KEEP.**
- **A1** (tool prior) — inert (model scores `eng` ~0). **REJECT.**
- **A2 / A2.1** (label rewrites) — regressed vs v1 (0.824→0.500/0.059). **REJECT.**

## What ships

1. **A0 default-on, unconditional.** `task_type` classification always uses
   `Meta.Preamble()+text` (like the other classifiers). It is a strict win
   (helps confound rows, no-op on topic-aligned rows), so no flag — remove
   `taskTypeUsesContext` / `KELD_ENRICH_TASKTYPE_CONTEXT`.
2. **A4 default-on, with a disable escape-hatch.** For `claude_code`/`codex`,
   `function_guess = eng`; generic tools keep topical classification. On by
   default; `KELD_ENRICH_COMPOSITIONAL_FUNCTION=off` (or `0`) disables it — kept
   because it is a hard rule and operators may want an override.
3. **Prune the rejected experiments:** delete `a1_toolprior.go`(+test) and
   `a2_labels.go`(+test); remove the `applyToolPrior` call in `pass.go` and the
   `functionLabels()` indirection (funcGuessExtractor uses `Functions` directly).
4. **SchemaVersion 2 → 3** (`labels.go`) — signals the derivation change to
   Atlas even though the label vocabulary is unchanged; re-stamps extractor
   producer versions and the published `schema_version`.
5. **Keep the eval infra** — `keld-agent eval`, `confound.jsonl`, `LoadConfound`,
   `LeakageRate`/`FalseEngRate`, and the source-attributed `gold.jsonl`. This is
   now a durable regression harness, not experiment scaffolding.

## Validation gate (before release)

- `gofmt`/`go vet`/`go test ./...` clean.
- `keld-agent eval --confound --context` on the default build reproduces the
  validated numbers: `leakage(function_guess)=0.000`, `false_eng=0.000`,
  confound function accuracy ≈0.909; gold-only function accuracy ≈0.800.
- Disable escape-hatch verified: `KELD_ENRICH_COMPOSITIONAL_FUNCTION=off` restores
  topical function classification.

## Delivery

Squash-merge to `main`, tag **v0.4.0** (minor — deliberate enrichment behavior
change), release CI publishes the pkg. Release notes call out: engineering
sessions in coding tools now classify as `eng` regardless of subject matter;
`task_type` uses session context; schema v3.

## Out of scope (future)

- Expanding the confound set with real accumulated prompts (the harness is ready).
- `activity_type`-driven subcategory refinement, and any A5–A9 alternatives.
- An escape-hatch/override for A0 (deemed unnecessary — strict win).
