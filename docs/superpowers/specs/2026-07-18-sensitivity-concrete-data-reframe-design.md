# Sensitivity = concrete leaked data, not content domain (reframe)

**Date:** 2026-07-18
**Status:** Approved (brainstorm) — ready for implementation
**Scope:** Eval gold relabel + documentation. NO code change. Redefine the
`sensitivity` facet's semantics and correct the gold set to match.

## The reframe

The `sensitivity` facet exists to **detect concrete leaked sensitive DATA** —
credentials/keys/passwords (the primary concern) and personal/financial
identifiers (SSN, card numbers, email, phone, person names, addresses). It is
**NOT** a classifier of the prompt's *subject matter*. "Medical discharge
summary" or "diagnosed with diabetes" are topics, not leaked data — flagging them
as sensitive would be classifying context, which is explicitly out of scope.

Consequence: the class label is just a rollup of **which concrete entity was
detected**, exactly as the code already implements via `SensitivityFromEntity`:
- `secrets` = credential/key/token/password present
- `pci` = credit card present
- `phi` = SSN present (the code's rule is literally `{"phi", ["ssn"]}` — it never
  detected medical context)
- `pii` = other personal identifier present (email, phone, person, address)
- `none` = no concrete sensitive identifier
- `proprietary` = **deprecated** (pure content-domain, no concrete token, no
  detector). Kept in the `Sensitivity` vocab for contract stability but never
  emitted; do not build a detector for it.

**Rejected direction:** adding medical entity labels / elevating medical context
to `phi`. That would classify subject matter, which the reframe explicitly
excludes.

## Why there is no code change

`SensitivityFromEntity` already maps detected entities → class. The prior
"PHI recall misses" were an artifact of GOLD LABELS that encoded medical-context
classification (labeling a medical topic as `phi` even with no SSN). The detection
was largely correct; the measurement target was wrong. Fixing the gold labels to
"which concrete entity is present" corrects the metric without touching code.

## Gold relabel (the work)

Relabel `sensitivity` on the medical-context + proprietary gold rows to reflect
concrete-identifier presence (review-gated):
- "Translate this Spanish medical discharge summary" phi → **none** (no identifier).
- "Patient John Smith, DOB…, diagnosed…" phi → **pii** (name + DOB is an identifier).
- "Pull the patient's diagnosis and medications…" phi → **none** (no concrete
  identifier; keeps the row so GLiNER's spurious `person` registers as an honest FP).
- "Summarize radiology report for patient MRN 4482910" phi → **pii** (MRN is a
  record identifier; relabeling avoids relying on GLiNER's MRN→SSN misread, and
  surfaces that entity confusion honestly).
- 3× `proprietary` rows → **none** (no concrete token; class deprecated).
Unchanged: SSN→phi, all card→pci, all email/phone/address→pii, all credentials→secrets.

## Measurement

Re-run `keld-agent eval --confound --context`: `sensitive_recall` should rise
(the medical-context "misses" become correct none/pii targets) and `sensitivity`
accuracy should improve or hold. Record any residual GLiNER precision issues
surfaced (the spurious `person` FP, the MRN→SSN class confusion) as findings for a
future entity-precision lever — not fixed here.

## Success criteria

- Gold `sensitivity` reflects concrete-identifier semantics; no content-domain labels.
- `sensitive_recall` up vs 0.684 baseline; sensitivity accuracy flat-or-up.
- Reframe documented (this spec + a code comment on `SensitivityFromEntity` noting
  class = which concrete entity, and `proprietary` deprecated). Zero code behavior change.
