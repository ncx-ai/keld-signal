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

---

## Scope: platform-written transcripts only

This system reads only what the agent platforms write for themselves — `~/.claude/projects`,
`~/.codex/sessions`, `~/.gemini/tmp/*/chats`, and the Cowork session trees. **A manual export is
not an input.** Nothing may be designed around a field that exists only because a person clicked
export, because the daemon will never see one.

That rule settles the thinking question. Across 43 sessions, all **9,148** thinking blocks in the
platform-written Claude Code transcripts carry a signature and an **empty** `thinking` string.
The only corpus with real thinking text (61 blocks, 100,665 chars) was a hand-made claude.ai
session export. So **thinking volume is unavailable by design**: `asst_think_chars` is still
recorded where a store happens to carry it, but nothing downstream may depend on it.
`asst_think_msgs` (incidence) and the `unsaid_tok_approx` upper bound are the designed-for
signals.

### What the colleague's Cowork export was legitimately good for

Run as a **schema test**, not as evidence. Its 754 lines use the same field names the extractor
already reads — `type`, `timestamp`, `cwd`, `gitBranch`, `message.content`, `requestId`,
`attributionSkill` — plus `attributionMcpServer` / `attributionMcpTool`, which the *platform*
writes and a real Cowork transcript would therefore also carry. The extractor needed no
per-platform special case, and the exercise produced three changes that are general:

* **`mcp_server` and `mcp_tool` as reference levels.** A session that reaches Notion touches
  that server as surely as it touches a repository, and MCP attribution is platform-written.
* **Duplicate-file dedupe by content hash.** The export ships one session twice
  (`transcript.jsonl` and `<uuid>.jsonl` are byte-identical); counted twice, every series
  doubles. The guard is general — it protects against any duplicated transcript.
* **Several repo roots.** Another machine's paths (`/Users/<name>/...`) resolve against a list of
  roots, with a fallback that derives the root from the recorded `cwd` when the path cannot be
  stat'd locally.

Nothing about the *frequencies* in that session is usable: 271 lines over **7.1 hours**, one
branch, 14 engineer messages. And per AGENTS.md, Cowork is VM-backed — its platform-written
transcripts are unreadable from the host today, so this is a preview of material we currently
cannot obtain at all.

### A reporting defect that short session exposed

`>4wk` was printed whenever the similarity curve never crossed 0.5 — including for a series whose
longest lag was 7 hours, where no pair of bins is even a day apart. "Very stable" and "the span
is too short to tell" were being reported as the same fact, which is what a silent cap does. Now
the ceiling is the longest lag the series actually **contains** (`>7h`, `>5h`), and a level with
too few observed bins to estimate anything at all reports **`n/a`** and is listed separately
rather than being ranked.

This corrected a number in the main corpus too: keld-signal's `agent` level read `>4wk` and is
really `>74h`.

---

## Corrections to the numbers reported earlier

Four defects were found while building the ladder on top of these series. Each changed
published numbers, so the earlier table should be read as superseded.

**1. The absolute-0.5 crossing was measuring the bin's sample size.** A bin holding few events
gives a noisy composition estimate, and noise decorrelates immediately. keld-atlas `verb` crossed
absolute 0.5 at **>4wk, 168h and 1h** for 1 h, 15 min and 5 min bins — three orders of magnitude
from the same data. Half-life is now the decay measured from a **fixed 1 h reference lag**, which
is the only baseline that means the same thing at every bin size (normalising by the *nearest*
lag fails too: a finer bin gets a higher baseline, hence a shorter apparent half-life). At 15 min
and 5 min bins the corrected estimates now agree closely.

**2. A quarter of keld-atlas `file` mass was folded into one `__other__` column** at the old
500-term cap. A single column holding 25% of the mass is a large stable component that flatters
the half-life. The cap is now above the largest real vocabulary (3,365) and `__other__` is
excluded from the similarity vector when it does bite — a fold bucket cannot turn over.

**3. `>4wk` meant two different things** — "never decorrelated" and "the series is too short to
tell". Now the ceiling is the longest lag the series actually contains, and a level with too few
observed bins reports `n/a` and is listed apart rather than ranked.

**4. Extraction was reading paths out of command prose.** `chars/msg` and `0/20` ranked among the
top directories and `r.h` among the top files, all quoted from our own shell text; and
`windows/window_01.txt` was classified as a *component*, because `.txt` is not in the language
map so a dotted file was taken for a directory. A bash token now needs a slash, a dotted
extension and no all-digit segment; a dotted basename is a file whatever its extension; tool
`file_path` inputs remain authoritative and bypass the test, which is the real distinction — an
input is a declaration, a command line is text that happens to contain slashes.

### The corrected ladder, 15-minute bins, decay from a 1 h reference lag

| level | keld-atlas | keld-signal | earlier (superseded) |
|---|---|---|---|
| `repo` | >4wk | >4wk | >4wk |
| `tool` | >4wk | >4wk | >4wk |
| `verb` | >4wk | 288h | >4wk / 11h |
| `lang` | >4wk | 116h | >4wk / 61h |
| `model` | 407h | >4wk | 391h |
| `component` | **380h** | **32h** | 2h / 4h |
| `branch` | **293h** | **201h** | 31h / 92h |
| `dir` | 7h | 24h | 1h / 2h |
| `file` | 4h | 8h | <1h |

The qualitative revision matters more than the numbers: **`component` moves from the fastest of
the path levels to among the slowest.** Which *file* is open changes hourly; which *subsystem* the
work is in barely moves for weeks. Relative decay sees that and the absolute crossing did not —
and a subsystem is exactly the kind of fact a context ladder wants on a slow rung.

`skill` is deliberately excluded from the ladder: 16–30 observed bins and estimates ranging from
10h to >4wk across repos and bin sizes. Thin support, unstable, not evidence.

## The context ladder

Four rungs, boundaries taken from the bands above, each roughly an order of magnitude apart:
~700h, ~250h, ~10h, ~1h.

| rung | levels | measured band | refresh | payload |
|---|---|---|---|---|
| **IDENTITY** | `repo`, `lang`, `model`, `tool` | 116h – >4wk | once per session | top 3 with shares |
| **WORKSTREAM** | `branch`, `component` | 32 – 380h | on branch/component change, clock backstop at 0.5x half-life | top 4 with shares |
| **WORKING SET** | `dir`, `file` | 4 – 24h | per window | top 5 with shares |
| **TEMPO** | speaker channels | ~1h | per window | one line, relative to this person's own baseline |

**Every rung's lookback and staleness budget is read from `levels.parquet`, not written in the
code.** That is the design, not an implementation detail: `component` is 380h in keld-atlas and
32h in keld-signal, so any single hard-coded interval is wrong for one of them. A repo with a
different rhythm self-tunes.

* **Lookback** = that level's own half-life, clamped to the series span. A rung summarises the
  window over which its composition is still half-true.
* **Staleness budget** = 0.5 x half-life. Inside it, the carried value is stated with its age
  (`[as of 6h ago]`). Past it, the value is still carried but explicitly labelled
  (`[CARRIED 40h — past 0.5x its 32h half-life, treat as aged]`). It is never silently kept and
  never silently dropped: the same rule as `omittedNotice`, applied to context instead of text.
* **Shares, not bare lists.** `scripts 57%, windows 28%` says what dominates; a list of names
  does not.
* **Identifiers are never truncated** (AGENTS.md). Whole terms are dropped and the count is
  stated — `(+5 more not shown)` — because a path cut short is a false path.
* **Every rung is a known fact** from `cwd`, `gitBranch` and tool inputs. Nothing on the ladder
  is inferred, which is the handoff's ask-the-facts-first rule extended along the time axis: the
  facts the pipeline already holds, held at the rate each one actually changes.

Why IDENTITY and WORKSTREAM are separate rungs despite being only 2–3x apart: their refresh is
different in kind, not just in period. Identity cannot change within a repo. Workstream changes
on an **event** — the engineer switches branch or subsystem — with the clock only as a backstop.

Rates and turnover stay OUT of the injected context. They are anomaly-detection signals about the
series, not facts about the work.

    scripts/refseries.py ladder --repo keld-signal            # render the block
    scripts/refseries.py ladder --repo keld-atlas --at 2026-08-04T14:00
