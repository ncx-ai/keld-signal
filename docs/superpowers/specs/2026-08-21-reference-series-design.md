# Reference series — what is in focus, at what frequency

The pipeline already holds the facts that carry attribution (handoff: 36.9% of coverage, and
100% of what worked). Those facts are currently read **per job**. This measures them **over
time**, because a rolling window of messages needs context that changes at the rate the thing
itself changes — a repo per session, a branch per day, a file per few turns.

## The rule that shapes everything: state carries, rates don't

A gap in the series means time passed, not that focus moved. Someone slept. So the two metric
families are treated differently and never mixed:

* **Rate series** — events per bin. Genuinely zero across a gap, and idle bins stay visible as
  idle rather than being dropped, because "no work" is a fact about the period.
* **State series** — which repo, which branch, which component, which files are in focus. These
  are **forward-filled** (last observation carried forward) across gaps, annotated with
  `age_hours` since the last real observation. The consumer decides what staleness it will
  accept; the series never decides for it by silently blanking.

Forward-filling without an age would be a lie at long lags — "focus: financial-tab.tsx" carried
across three weeks. The age is what makes the fill honest, and each level's measured half-life
is what tells us how long its fill is worth trusting.

## The levels, slowest expected to fastest

Nine deterministic reference types, no model involved. All from tool-call **inputs** and
per-line metadata — never from tool output, which records what was *observed* rather than what
was *worked on*, and which is where a stray `ls` would flood the vocabulary.

| level | reference | source |
|---|---|---|
| `repo` | repository | per-line `cwd`, mapped under `--repo-root`; worktree paths folded back |
| `branch` | git branch | per-line `gitBranch` — a branch is a workstream, and one session had six |
| `component` | subtree | path prefix within the repo, depth-capped (`services/web/components`) |
| `dir` | directory | full dirname of a touched file |
| `file` | file | `file_path` from Read/Edit/Write, paths parsed out of Bash commands |
| `lang` | file type | extension of the above, via `qwen_windows.EXT_LANG` |
| `tool` | invoked tool | `tool_use.name` |
| `verb` | shell verb | first token of each Bash pipeline segment (`go test`, `git commit`) |
| `agent` | subagent | `Agent.subagent_type` |
| `skill` | skill | `Skill` input and `attributionSkill` |

## What is computed per level

Per repo timeline (stitched across sessions, since branch and component outlive a session) and
per session:

* **rate** — events per bin, with idle bins marked.
* **composition** — the share vector over that level's vocabulary; `top1`, top-k set.
* **breadth** — distinct references in the bin. **entropy** — bits over the shares: focus
  against scatter.
* **turnover** — `1 - cosine(share_now, share_prev_observed)`, and Jaccard distance of the top-k
  sets. Computed between *observed* bins only; a forward-filled bin has no turnover by
  definition.
* **rolling mean and median** of each of the above at three horizons, so a level's own
  timescale is visible rather than assumed.

## The natural frequency, measured two ways

1. **Persistence half-life.** Cosine similarity between the share vectors of two observed bins,
   averaged over all pairs at a given wall-clock lag, tabulated over lag buckets from 1 hour to
   4 weeks. The lag where similarity crosses 0.5 is the level's half-life. Wall-clock lag is the
   right unit precisely because of forward-fill: the question is how long a carried value stays
   true.
2. **Reference lifetime.** For each individual reference, last-seen minus first-seen. The median
   per level is a second, independent estimate of the same thing, and disagreement between the
   two is informative — a level can have long-lived members and a fast-rotating mix.

## Two phases, cached

`extract` walks the transcripts into one compact event stream; `series` aggregates it. The split
is not just for iteration speed: it is the production shape, where the daemon extracts per line
as it already does in `daemon/context.go`, and only the aggregation is new.

## What this does NOT do yet

No context ladder. The refresh interval per level is the output of the measurement, so emitting
a ladder now would mean assuming the numbers we are about to measure. The ladder is the next
step, designed from the half-life table.

Privacy: paths and repo names are local facts and stay local. Output goes to `/tmp`; nothing
here is publishable, and the eventual production path publishes only masked derivatives — same
invariant as the rest of the daemon.

Script: `scripts/refseries.py`.

---

## Results — 53 transcripts, 359k observations

`extract` parses 53 transcripts in ~2.5 s (tool results are skipped, which is where the bytes
are). Two repos clear the row threshold: **keld-atlas** 38 sessions / 34.1 days / 270,778
observations / 26.2% active bins, and **keld-signal** 10 sessions / 28.0 days / 79,335
observations / 14.0% active bins. Forward-fill matters exactly as much as the active share
implies: three quarters of the calendar is gap.

### The ladder falls out of the data, and both repos agree on its shape

Composition half-life at 1 h bins, slowest first:

| tier | levels | half-life | reading |
|---|---|---|---|
| **A — session-constant** | `repo`, `tool`, `verb`, `agent`, `lang`*, `model`* | **> 4 weeks** | the *mix* never decorrelates even though the events are high-frequency. Tool turnover is 0.10. These cost nothing to carry and nothing to refresh. |
| **B — day scale** | `branch` 31 h (atlas) / 92 h (signal), `lang` 61 h (signal), `skill` 34 h (signal) | **1–4 days** | a branch is a workstream: 58 of them over 34 days in atlas. This is the rung that matches how a person would describe what they were working on. |
| **C — window scale** | `component` 2–4 h, `dir` 1–2 h, `file` < 1–1 h | **1–4 hours** | must be refreshed per window; carrying these forward is what goes stale first. |

\* `lang` and `model` sit in tier A for atlas (TypeScript-dominated, one model) and drop to tier
B for signal (Go/Markdown/Python mixed).

The two independent estimates agree: every tier-C level has a **median reference lifetime of
0 hours** — more than half of all files, dirs and components are touched in a single moment and
never again — while tier A sits at 431–819 h. A level can have a fast-rotating mix and long-lived
members; here the two measures point the same way, which is the stronger result.

### Speaker channels

At 1 h bins nearly every speaker channel reads `<1h`, which is the bin floor, not a finding. At
**15-minute bins they resolve to ~1 h** — so activity level is predictive within the hour and
not beyond it, and 1 h bins are simply too coarse for this family. The bin size sets the
resolvable frequency; each family needs its own.

Shape of the talking, keld-atlas: 1,320 engineer messages / 1.34 M chars, median **168 chars per
message**, **11% of turns under 40 chars** ("ok", "continue"), **12% arriving within 60 s** of the
previous one, median gap 376 s. The assistant writes **~10–14x** the engineer's characters.
Count and size are deliberately separate series: a burst of short turns and a burst of long ones
are both spikes in `user_msgs` and opposite in kind, and `user_chars_per_msg` is the only thing
that separates them.

`unsaid_share_approx` runs 0.86–0.95, but it is an **upper bound on reasoning, not a measure of
it**: the transcript keeps no thinking text (only a signature), so the channel is
`output_tokens` minus what was said, which also contains every tool-call argument — a `Write`
with a 400-line body is output too.

### What the measurement cannot see

* **Thinking volume.** Redacted in the transcript; incidence only (`asst_think_msgs`).
* **Vocabulary tails.** Capped at 500 per level for the composition matrix, and the cap is
  printed as `501/3365` whenever it bites rather than silently.
* **Tool output.** Never read, by design — it records what was observed, not what was worked on.

### Two defects this run caught, both of which had inverted a rung

* A path token from a shell command (`cd services/web`) was counted as a **file**, which made
  the file level look slower-moving than the directory level. A shell token is a file only if it
  carries a known extension; a tool's own `file_path` always is one.
* `if`, `then`, `fi` and the `1` from `2>&1` were being counted as shell **verbs**, outranking
  `go test`.

## Next: the ladder

The refresh intervals are now measured rather than assumed, so the ladder can be designed:
tier A carried for the session, tier B refreshed daily or on a branch change, tier C rebuilt per
window. `state` and `state_age_h` are already in `metrics.parquet` for every level, which is the
carry-forward mechanism itself — what remains is choosing each tier's payload and its staleness
budget from the half-life beside it.
