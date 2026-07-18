# Credential/secret leak detection — deterministic + semantic defense-in-depth

**Date:** 2026-07-18
**Status:** Approved (brainstorm) — ready for implementation planning
**Scope:** Go-only (daemon/enrich) + one CI workflow. Harden recall of the
`secrets` sensitivity class — reliably detect leaked credentials/keys in prompt
text — by adding a deterministic (regex + entropy) detection layer alongside the
existing GLiNER NER, unioning their spans, and keeping the ruleset current via a
gated weekly sync. **PII** hardening (`phi`/`pii`) is a deliberate follow-up
phase, not this spec. `proprietary` is **dropped** (a keyword like "confidential"
signals *talk about* confidentiality, not a leak — low, subjective signal).

## Motivation

A leaked credential in a prompt (API key, token, password, private key, DB
connection string) is the highest-stakes sensitivity miss — the credential *is*
the payload. Today the pipeline detects `secrets` **only** via the GLiNER sidecar
guessing "this looks like an api_key," so novel/unstructured formats slip. Root
cause confirmed by per-row diagnostic: the pipeline has **no deterministic
detection layer at all** — no regex for known key formats, no entropy heuristic.

Deep research (2026-07-18, adversarially verified against gitleaks/detect-secrets
configs, TruffleHog, and peer-reviewed benchmarks; full notes:
`docs/superpowers/plans/2026-07-18-credential-detection-research.md`) established
the design:
- **Deterministic regexes** reliably catch ~hundreds of *known, fixed-format*
  credentials (AWS `(AKIA|ASIA|…)[A-Z0-9]{16}`, GitHub `ghp_/gho_/ghu_/ghs_/ghr_`,
  Slack `xox[baprs]-`, Google `AIza…`, Stripe `(sk|rk)_live_…`, JWT, PEM/OpenSSH,
  DB URIs). gitleaks ships these (MIT, **Go+RE2** — port near-verbatim, no ReDoS).
- **Entropy must never fire alone.** TruffleHog scored a real secret at 4.08 and
  the English phrase "ThisIsAReallyLongString" at 4.11 (the non-secret higher),
  and emitted git SHAs as secrets. Entropy is a *context-gated secondary floor*.
- **ML adds recall exactly where regex fails** — format-free contextual secrets
  ("the password is Hunter2"), novel formats, prose — **but only if it runs as an
  independent detector over raw text**, not as a re-ranker of regex hits (else
  regex recall becomes the ceiling — arXiv 2410.23657). Its second job is
  **precision-gating**: veto placeholders (`YOUR_API_KEY`), redacted (`sk_**`),
  dummies.
- **Union, not choose.** gitleaks and TruffleHog each surface >400–1500 unique
  true positives the other misses. No single layer suffices.

**Domain-transfer caveat (why the empirical probe is mandatory):** every cited
benchmark measures secrets in *source code / GitHub issues*, not user-typed LLM
prompts. The structural conclusions transfer (regex-for-known, entropy-needs-
context, ML-for-contextual); the exact recall/precision numbers will not. So we
measure our own configuration on our own corpus (Phase 0) rather than trusting
published figures — and specifically we must measure GLiNER's recall as an
*independent* detector on prompts, which no cited source evaluates.

## Goals

1. Maximize recall of the `secrets` class — no real credential goes undetected —
   while controlling false positives (decoys: UUIDs, git SHAs, placeholders).
2. Keep the deterministic ruleset current automatically and *safely*.
3. Zero regression on other facets and on non-secret sensitivity classes.

## Non-goals

- **PII (`phi`/`pii`) recall** — separate follow-up phase (the medical-PHI and
  person→pii gaps found in the diagnostic).
- **Blocking / intervention.** Recall of the *derived signal* only; raw text
  already never leaves the machine, and detected spans are already masked.
- **`proprietary`** — dropped as a low-signal class.
- **Live credential verification** (TruffleHog-style API calls) — out of scope for
  an on-device privacy daemon (would exfiltrate the very secrets we protect).

## Design

### The 3-layer detector (union of spans → `secrets`)

A new detection component runs over `ctx.Text` and returns credential spans, each
mapped to the `secrets` sensitivity class and masked by the existing span
machinery. Layers:

**Layer 1 — deterministic regex (always fires, regardless of entropy).**
- A **vendored gitleaks ruleset** (full set, pinned to a gitleaks *release tag*),
  loaded as data (rule = id + regex + keyword prequalifiers + per-rule entropy
  floor). Covers cloud/SaaS keys, source-host tokens, JWT, PEM/OpenSSH, DB URIs.
- A **keyword pre-filter (Aho-Corasick)** as the engine front door: a rule's
  regex only runs if one of its keywords is present in the text — this is how
  gitleaks stays fast, and it is required to keep +ruleset scanning within the
  daemon's on-device latency/CPU budget (NOT a naive N-regex sweep per prompt).
- The handful of intentionally-noisy generic rules (e.g. `generic-api-key`) are
  kept but gated harder and/or left for the Layer-3 precision gate to veto — never
  dropped.

**Layer 2 — context-gated entropy (never standalone).**
- Fires only with format/keyword context (keyword-near-assignment). Base64 floor
  ~4.5, hex ~3.0 with an integer penalty (detect-secrets defaults), excluding
  UUIDs and git-SHA-shaped strings.
- Purpose: generic high-entropy secrets not matched by a named rule.

**Layer 3 — GLiNER (independent recall + precision gate).**
- **Independent detection** over raw `ctx.Text` with labels like
  `password`/`api key`/`secret`/`token` — recovers contextual/novel/prose secrets
  (it already caught "the database password is Hunter2!Prod"). Runs over raw
  text, NOT over regex candidates.
- **Precision gate:** a span from Layer 1/2 that GLiNER (or a lightweight
  classifier) reads as a placeholder/redacted/dummy is suppressed.

**Combination:** union the spans from all layers; de-dup overlapping spans; map to
`secrets`; the existing `SensitivityFromEntity`/masking path emits the class and
masked spans. Any secret span ⇒ `sensitivity = secrets`.

### Measurement — the credential eval corpus

A **SecretBench-informed corpus** (taxonomy: private key, API/SaaS key, auth
token, DB URI, password, username — plus **decoys**: UUIDs, git SHAs,
placeholders, redacted values) added to the eval harness. New metrics:
- `secret_recall` (over rows containing a real credential) and
- `secret_fpr` (false-positive rate over decoy rows).
Reported by `keld-agent eval`. This corpus is the acceptance gate for the build
AND the safety gate for the sync (below).

### The gated weekly sync

A **scheduled GitHub Actions workflow (weekly)** keeps the vendored ruleset current
*safely* — an unvetted rule update could silently reduce recall, so the sync is
gated:
1. Check gitleaks for a newer **release tag** than the vendored pin.
2. If newer: fetch its `gitleaks.toml`, **compile every rule as RE2** (reject any
   that fail), then **run the credential eval corpus** — allow only if
   `secret_recall` does not drop and `secret_fpr` does not spike.
3. Open a **PR** with the diff (auto-merge if fully green; otherwise leave for
   review). Rules reach machines via the normal signed release.

**Rejected:** runtime on-device fetch/hot-reload of rules — supply-chain risk
(executing third-party config on the leak-detection component) and provenance
loss (non-reproducible signal). **Documented future option:** a *signed*,
CI-vetted rule bundle served from the Atlas control plane and verified on-device
via the existing settings poll — build only if release cadence proves too slow.

## Build order (measure-first)

0. Credential eval corpus + `secret_recall`/`secret_fpr` metrics → **baseline**
   (current GLiNER-only recall on the broad corpus — expected low).
1. Layer 1 (vendored ruleset + Aho-Corasick prefilter engine) → measure the jump.
2. Layer 2 (context-gated entropy) → measure.
3. Layer 3 (GLiNER independent labels + precision gate) → measure.
4. Weekly gated sync workflow.

Each step keeps only on `secret_recall`↑ / `secret_fpr` flat-or-down, other facets
flat. Schema bump if the emitted secret sub-types change the contract.

## Success criteria

- `secret_recall` materially up from the GLiNER-only baseline on the broad corpus;
  `secret_fpr` controlled (decoys not flagged).
- Deterministic layer runs within the daemon's per-prompt latency budget (prefilter
  verified to skip non-matching rules).
- Other facets and non-secret sensitivity classes flat.
- Weekly sync workflow green, gated on the corpus, PR-on-diff, pinned to a release tag.

## Licensing

gitleaks rules are MIT — vendor with attribution (LICENSE + source/version noted
in the vendored file). detect-secrets (Apache-2.0) entropy defaults are reused as
guidance, not code.
