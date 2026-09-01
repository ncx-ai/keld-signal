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

## Smaller carried items

- Threshold/MARGIN/VERIFY_HALO calibration on real anchored labels once voting exists
  (never re-tune on synthetic; the fixtures only guard regressions).
- Gemma skip-by-default decision: take it when two or three more real A/Bs agree with
  tonight's 1-for-3.
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
