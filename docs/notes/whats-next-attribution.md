# What's next for project attribution

State + direction note, written 2026-09-02 at the end of the first end-to-end session.
Owner decisions recorded here so the next session starts from them, not from re-discussion.

## Where things stand tonight

- **keld-signal `feat/project-attribution`** (31 commits ahead of main): the full feature —
  pipeline, Gemma verifier in a recycled child, durable jobs, wire v22, the rank-and-margin
  decision rule, verifier A/B + customer-layer metrics in the eval, smoke runbook, design notes.
  Passed its final whole-branch review after fix waves; merge-ready.
- **keld-atlas `proto/projects`** (4 commits): the DIRTY prototype — `projects` on org settings
  (migration 0084), served on `/v1/enrichment-settings`, admin PATCH, `/v1/projects/report`,
  Projects tab in Workstreams. Disposable by design.
- **The loop ran end to end tonight**: daemon fetched the six demo projects FROM Atlas (no file
  override), real blocks flowed up (24 at last count, 16 attribution jobs still draining), the
  smoke agent (`/tmp/keld-agent-smoke`, `KELD_HOME=~/.keld-smoke`, isolated ports) was left
  running so the queue drains overnight. Refresh `/workstreams?tab=projects` in the morning.
- Real-session quality under the new rule: trust 53% → 74%, exact blocks 6/21 → 11/21,
  coverage 86% → 76%. Gemma E2B: 1-for-3 on real data — the A/B instrument now measures
  whether it earns its runtime on every eval run.

## Decisions taken (do not re-litigate)

1. **The Projects tab stays feature-toggled / internal for now.** What exists is the DEBUG
   surface — keep it, it is how we verify the machinery. The customer-facing projects
   experience gets designed and built properly as its own piece of work; it is not an
   iteration on the prototype. The toggle mechanics themselves are deferred — not this week's
   work — but nothing merged may ship the tab to customers by default.
2. **Next major piece: the attribution learning loop** — see
   `docs/notes/attribution-learning-loop.md` (tags with provenance, ambient pin/block voting,
   hard blocks as post-score masks, the capped categorical reinforcer as cascade tier 3, build
   order starting with tags + voting). That note is the spec seed for the next discovery.

## New threads to design (came out of tonight's data)

### 3. Project SUGGESTION from unattributed blocks

Most real blocks tonight attributed to none of the six projects — correct behaviour, but a
dead end for the customer. Turn the unattributed pile into value: cluster unattributed blocks
(we already embed them; cluster the vectors, summarise each cluster's shared evidence — repo,
named terms, kind-of-work) and SUGGEST "this looks like a project you haven't declared:
~40 blocks, mostly repo X, mostly person Y — name it?" One click creates the project and
back-fills the cluster as its first anchored blocks. This inverts the cold-start problem:
instead of demanding definitions up front, the system proposes them from observed work.
Fits the learning-loop flywheel (a confirmed suggestion = a batch of anchor labels).

### 4. Bigger categories above projects

Granular projects are hard to map into; the org also thinks in broad buckets — engineering
work, product work, marketing, launch. Design already exists for this shape:
`embedding-experiment/docs/notes/categories-and-secondary-dimensions.md` — **categories are
parents of projects, attribution stays at the concrete child level, the category rolls up for
free**. Do NOT embed broad categories directly (measured/argued there: abstract labels score
mushy against concrete text); give each parent an optional catch-all child ("Engineering —
general") for work that belongs to the bucket but no declared project. Suggestion (#3) and
categories compose: an unattributed cluster can be suggested INTO a category's catch-all
first, and promoted to a named project later. Schema cost: one `parent` field on the project
definition; report cost: a rollup view.

### 8. Validation of the centring + taxonomy work (2026-09-03), and the decision it forced

76 real blocks from this machine, labelled three ways — with full context, blind from text
plus the repo line, and blind from text with the repo line explicitly ignored — then every
arm scored against each. Nothing here is customer data: one person, three days, 96% one
label, LLM-labelled. It RANKS options; it validates none of them against ground truth.

**Held up.** Centring's gain is real: +0.219 F1 against the no-repo gold, 95% bootstrap
[+0.110, +0.356], and leave-one-block-out offsets score identically to in-sample (0.730), so
it is neither noise nor leakage. The four engineering projects' repo sets are disjoint and the
repo dimension alone scores 0.969 precision against a gold that never saw a repo — genuine
evidence the signal is right when present. The `project` (directory) dimension is the one to
match on; the `repo` (git remote) dimension disagrees with it on most blocks and is polluted
by remotes named in text.

**Did not hold up, and this is the finding that matters.** Remove the repo boost — the
condition a Jira/Notion-imported customer is in, since `repos` is never declared — and
centring falls from **0.725 to 0.373 F1 at 0.297 precision**; the shipped rule falls to 0.160.
Every good number rested on a hand-written repo mapping customers will not have. Without an
anchor, centring + MARGIN sprays labels (a generic design brief drew capital, demand, gtm
and trust at once), and the five non-dev projects hub on generic prose — the earlier hub test
used engineering messages only and missed it. 38% of blocks are undecidable from words; 30%
have none.

**Decision.** Ship centring ON for Keld's own org (already gated behind `KELD_ATTRIBUTION`,
tab internal-only): 50-message gate so cold machines behave as today, and a cap of two
assignments per block. Keep the nine projects declared; read the five non-dev ones as
data-gathering. Do NOT promise customer-facing attribution: the gap is a deterministic anchor
customers actually have — ticket keys and imported labels (implemented, untested for want of
data) — and later the learned repo tag via anchors, priced today at +0.22 F1. `repo-first`
as a shipped rule is DEAD: it presumes declared repos, and the design already says repos are a
learned Coding-tier tag with provenance, never customer-authored.

Harnesses: this session's scratchpad (`validate.py`, `arms_stable.py`, `score_vs_norepo.py`);
port beside `scripts/null_hubness_experiment.py` when the centring work starts.

### 9. The whole block, mean-pooled, centred per stream — measured and shipped (2026-09-03)

The owner asked the question the code never had: what if the embedding read the assistant's
replies too, and the block's metadata? 762 texts encoded (~9 s each on this thrashing machine;
assistant messages are twice the length of prompts), then every arm scored against both judges.

| arm (4 projects, no repos, centred) | with-input F1 ctx / no-repo | no-input F1 (23) |
|---|---|---|
| user text only, MAX (shipped until today) | 0.367 / 0.508 | 0.000 — unreachable |
| assistant only, MAX | 0.718 / 0.756 | 0.667 |
| whole block, MAX | 0.713 / 0.746 | 0.615 |
| whole block, MEAN, one mixed baseline | 0.772 / 0.781 | 0.606 |
| **whole block, MEAN, per-stream baselines** | **0.782 / 0.806** | **0.717** |
| metadata digest only (paths, terms, concepts) | 0.586 / 0.647 | 0.490 |

Per-block: 28% of 61 labelled blocks right on user text, 92% on the whole block. Whole block
WITHOUT centring: 59% — neither half is enough alone. The production objection ("whatever the
assistant happened to name") was real on 4 of 61 — a reply about a task queue put a block on
the wrong project — and is the minority mode; MEAN is what keeps one tangent from deciding.
The digest scoring 0.586 on its own says paths and terms carry the project too; unused today,
recorded for the anchoring work.

**Shipped:** `_span_texts` returns both streams; `score_block` pools by MEAN and centres each
message against its own stream's (document) baseline; `attribution.Offsets` persists two
floats per (stream, document), gated at 50; `concepts` and the verifier keep the user's words;
`centred`/`background_n` on the meta and `scoring` in `model_versions`. The eval now measures
this configuration with the baseline primed. This closes §6 (agent-only blocks) by a route that
needs no episode machinery, and §5's centring decision, in one change.

**Cost:** ~3x the encoding per block (12 messages instead of 4, twice the length); ~100 s on
this machine, likely 30-45 s on an idle laptop; still inside the 20-minute block cadence and
off the request path. Memory unchanged: messages are chunked before encoding.

**Still true:** one person, three days, four engineering projects, LLM-labelled. The non-dev
five were not in these arms and still need a gate before they score coding blocks.

## Smaller carried items

- Threshold/MARGIN/VERIFY_HALO calibration on real anchored labels once voting exists
  (never re-tune on synthetic; the fixtures only guard regressions).
- ~~Gemma skip-by-default decision: take it when two or three more real A/Bs agree with
  tonight's 1-for-3.~~ **TAKEN 2026-09-03, on deploy pressure rather than more A/Bs:**
  `KELD_ATTRIBUTION_VERIFIER` is now default OFF (`=1` opts in), both halves flipped
  together, GGUF no longer fetched unless opted in. The verifier was never in any of §8's
  measurements, so the default now matches what was measured. Re-enable per machine to
  A/B it; the eval's verifier arm still reports what it buys.
- Verifier freeze-check arm into `installers.yml` before any release (documented gap,
  AGENTS.md).
- Prototype warts if the proto branch lives longer than planned: GIN index on
  `raw->'projects'`, minutes double-count across multi-project blocks, no time-window filter.
- GLiNER sunset — still deferred, unchanged.

## Restarting the smoke setup

```
SNAP=$(ls -d ~/.cache/huggingface/hub/models--Qwen--Qwen3-Embedding-0.6B/snapshots/*/ | head -1)
KELD_HOME=$HOME/.keld-smoke KELD_TELEMETRY_PORT=24318 KELD_ATTRIBUTION=1 KELD_BLOCKS=1 \
  KELD_TEXTEMBED_DIR="$SNAP" \
  KELD_VERIFIER_GGUF=~/projects/keld/embedding-experiment/models/gemma-4-E2B-it-Q4_K_M.gguf \
  KELD_SETTINGS_POLL=30s KELD_ATTRIBUTION_INTERVAL=45s KELD_BLOCKS_INTERVAL=90s \
  /tmp/keld-agent-smoke run
```

Needs `/tmp/keld-agent-sidecar` (wrapper execing the repo's `sidecar/serve.py` via the venv)
beside the binary, or the daemon resolves the INSTALLED frozen sidecar, which predates the
`/projects`/`/attribute` routes and 404s them (the daemon then correctly holds jobs — observed
tonight). Local Atlas: `cd keld-atlas && make dev`; ingest token via
`ensure_ingest_token` in the api container.
