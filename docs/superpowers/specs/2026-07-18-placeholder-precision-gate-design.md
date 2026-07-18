# Placeholder precision-gate for sensitivity (credential L3, part 1)

**Date:** 2026-07-18
**Status:** Approved (brainstorm) — ready for implementation planning
**Scope:** Go (enrich) + eval. Suppress placeholder/redacted values from triggering
the `secrets` sensitivity class, cutting the credential-detection false-positive
rate without losing recall. Part of the credential L3 (precision-gate) work; the
GLiNER-independent-recall half is separate/future.

## Motivation

The credential-detection Phase-1 measurement found `secret_fpr = 0.167` (3/18
decoys), and the per-row diagnostic showed **all 3 false positives are
placeholders** — `YOUR_API_KEY`, `<API_KEY>`, `<YOUR_SECRET_HERE>`. Root cause
(confirmed live): **GLiNER's entity `Extract` detects the placeholder text itself
as an `api_key`/`secret` entity span**, which the hard rule
(`sensitivityFromEntities`) then elevates to `secrets`@1.0. The deterministic
`creddetect` layer correctly stays silent (its entropy floors + gitleaks keyword
gating reject placeholders). The research predicted exactly this: the ML layer's
second job is precision-gating placeholders/redacted/dummy values.

## Goals

1. Placeholder/redacted values do NOT trigger `secrets` (or emit a masked span).
2. Zero recall loss — real credentials/secrets still detected (a real secret must
   never be mistaken for a placeholder).
3. Measured: `secret_fpr` ↓ (target ~0), `secret_recall` flat at 0.917, gold
   `secrets` rows unaffected.

## Non-goals

- GLiNER as an *independent* recall detector over raw text (the other L3 half).
- The weekly gitleaks sync; PII (`phi`/`pii`) recall — separate work.

## Design

- **A `placeholder` predicate** (`creddetect` or a small `enrich` helper):
  `IsPlaceholder(text string) bool`. Conservative — matches only clear placeholder
  shapes so it can NEVER match a real secret:
  - Angle/brace templates: `<...>`, `${...}`, `{{...}}`, `%...%`.
  - Prefixes: `YOUR_`, `MY_`, `THE_` (case-insensitive) on an all-caps/underscore token.
  - All-caps-underscore-only tokens with NO lowercase and NO digit-entropy
    (e.g. `API_KEY`, `SECRET_HERE`, `ACCESS_TOKEN`).
  - Runs of mask chars: `****`, `xxxx`, `XXXX`, `……`, `••••` (≥3).
  - Literal placeholder words: `PLACEHOLDER`, `EXAMPLE`, `REDACTED`, `CHANGEME`,
    `CHANGE_ME`, `TODO`, `DUMMY`, `FAKE`, `SAMPLE`.
  - **Guardrail:** the predicate keys on the ABSENCE of secret-like entropy (mixed
    case + digits + length). `Hunter2!Prod`, `sk-live-9f8a7b6c`,
    `ghp_ABC123DEF456GHI789JKL0mnop` must return false. This is verified by tests
    asserting real corpus secrets are NOT placeholders.
- **Gate location** (`SensitivityExtractor.Run`): when building `spans`/`found` from
  GLiNER's `res.Entities`, SKIP any sensitive-entity span whose text
  `IsPlaceholder`. Apply the same filter to `creddetect` spans (defense-in-depth;
  cheap, and a placeholder that somehow matched a regex is still a placeholder). A
  filtered span is neither added to `found` (so it cannot elevate to `secrets`) nor
  emitted as a `sensitivity_span`.
- **Span-level, not prompt-level:** a prompt containing BOTH a real secret and a
  placeholder keeps the real span and drops only the placeholder — recall preserved.
- Note: GLiNER's Extract gives us the entity's raw `text` (it is already used for
  `Mask(ent.Label, ent.Text)`), so the predicate has the text it needs; the raw
  value is still never emitted (only the masked hint, and only for surviving spans).

## Measurement

`keld-agent eval --creds` before/after: `secret_fpr` must drop (target ~0, since the
3 FPs are all placeholders) and `secret_recall` must stay 0.917. Full
`--confound --context` + gold: `sensitivity` accuracy and `sensitive_recall`
flat-or-up; no other facet changes. Add decoy corpus rows if needed so the
placeholder classes are well-covered. Add unit tests: each corpus placeholder →
IsPlaceholder true; each real corpus secret → false.

## Success criteria

- `secret_fpr` materially down (ideally 0) with `secret_recall` = 0.917 unchanged.
- Gold `secrets` rows still classified `secrets`; sensitivity accuracy not reduced.
- `IsPlaceholder` unit-tested on real-secret negatives and placeholder positives.
