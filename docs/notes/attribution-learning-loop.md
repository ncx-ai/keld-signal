# Design note: the attribution learning loop — tags, two passes, and user votes

Direction agreed 2026-09-02, after the rank-and-margin rule landed and the first real-session
evaluations ran. This is the next piece of work after the current iteration ships. Companion to
`embedding-experiment/docs/notes/categories-and-secondary-dimensions.md`.

## The problem this solves

Attribution today decides from the block's text plus declared project metadata, block by block,
with no memory. Real-session evaluation (23 blocks, 2026-09-02) showed the rank-and-margin rule
fixes over-assignment (trust 53% → 74%, exact blocks 6/21 → 11/21), but the remaining misses are
ambiguity the text alone cannot resolve — and the org's own history can.

## Anchor labels vs evidence (the distinction everything rests on)

- **Evidence** = what workstreams/enrichment derive per block (repo, dirs, actor, task-type,
  named terms). Automatic, plentiful, fallible. Inputs at prediction time.
- **Anchor labels** = verified verdicts: a human confirmed/corrected an attribution, or the
  evidence was unambiguous (ticket key; repo owned by exactly one project). Scarce, trustworthy.
  Used to CALIBRATE and to feed learned profiles — never invented, never taken from the model's
  own guesses.

⚠️ **The echo-chamber rule:** a project profile learned from the system's own unverified
attributions trains the model on its own errors (wrong repo→project call strengthens itself).
Profiles learn ONLY from anchored labels. Model-guessed attributions may be DISPLAYED as tags,
never fed back at full weight.

## Smart tags: the project's learned profile

Auto-populated per project; no customer authoring. Stratified so non-coding sources
(ChatGPT-style conversations, Bedrock/SDK logs) still work:

| Tier | Tags | Sources |
|---|---|---|
| Universal | people/teams; kind-of-work mix (task_type/activity/function); named terms (customers, systems, products); systems/integrations touched | every surface |
| Coding | repos, directories, languages, file types, tooling, skills | coding agents only |
| Reporting | models used, spend, temporal rhythm | dashboard value, weak evidence |

Every tag carries **provenance**: `declared` (imported from Jira/Notion/Monday — assignees,
components, labels seed the profile on day one, zero hand-labeling), `learned` (observed from
anchored blocks, **decayed** — rolling ~90d window; profiles encode the present, not history),
`confirmed` (a human touched it).

**User voting = ambient labeling.** Tags render with pin/block affordances. A pin or block is an
anchor label. Nobody labels 100 imported projects; they occasionally veto a wrong tag on a
project they were already looking at. This is the correction flywheel's UI.

**Hard blocks** ("never this repo → this project"): applied as a **post-scoring mask**, not a
pre-vectorization filter — scoring all projects is ~free (project vectors cached), and masking
after preserves auditability ("would have matched at 0.72, suppressed by your rule") plus a
suppression counter that exposes stale rules. Verifier calls ARE skipped for masked pairs (that
is where the cost lives). Each hard block is also a high-quality negative label. Rule-fired must
be visible in the block's status.

**Do not ship yet:** "related projects" from co-attribution. Co-occurrence in predictions
measures OUR confusion (attribution↔enrichment pair), not org structure. Internal metric until
it can be built from anchored labels.

## The two-pass decision (cascade with a capped reinforcer)

Tiers, each seeing only what the previous could not settle:

1. **Deterministic**: hard blocks (mask), exact repo/ticket evidence (boost — unchanged).
2. **Embedding + rank-and-margin**: `cut = max(null, top - MARGIN)` (shipped 2026-09-02).
3. **Profile reinforcer (NEW)** — the "second pass using past data". NOT a second embedding:
   identities are matched, not embedded. Per candidate in the VERIFY_HALO only, a categorical
   log-likelihood score: how much more often has this block's repo/dir/actor historically
   appeared in this project than in projects at large. Summed over present features,
   **hard-capped below MARGIN** so it can only tip ties, never override text evidence — the
   structural echo-chamber guard. Silent profile (cold start, new repo) = neutral, never
   negative. Weights per provenance: confirmed > declared > learned.
4. **Gemma E2B verifier**: only pairs still ambiguous after tier 3 — text unclear AND history
   silent/conflicted.

Expected effect: verifier volume collapses on mature orgs (most close calls have repo/actor
history), reserving the ~4s/pair model for genuinely novel work. Verify cost measured at ~80%
of eval runtime, so tier 3 is where the two-minute blocks die.

## Measurement already in place

- `test_attribution_quality.py` prints a **verifier A/B** from one pass (embedding-only vs
  after-verdicts: f1/p/r per arm, seconds, verdicts split corrected-vs-broke) and the
  **customer layer** (coverage, trust, clean-blocks). Extend to three arms when tier 3 lands.
- Real-session evidence so far: Gemma 1-for-3 on 2026-09-02's session under the new rule
  (old rule: net negative). If A/Bs keep it near break-even after tier 3, skip it by default.
- Later, with a few hundred anchors per org: calibrate confidence (isotonic/Platt) and consider
  conformal sets for guaranteed-coverage claims. Until then, per-block confidence is not a
  probability — customer claims stay aggregate ("when we attribute, measured X% correct").

## Build order

1. Tags display (all three tiers) + pin/block voting + hard-block mask → starts collecting
   anchors immediately, zero model risk.
2. Import bootstrap (Jira/Notion/Monday fields → declared tags).
3. Tier-3 profile reinforcer, fed by anchors only, decayed, capped; three-arm A/B.
4. Calibration + conformal, once anchors accumulate.
