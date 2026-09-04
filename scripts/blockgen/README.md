# blockgen

A synthetic Claude Code JSONL transcript generator for Keld Signal v3 (lane G, deliverable D1).
It writes transcripts shaped exactly like the ones `sidecar/app/analysis/transcript.py` parses,
so the **real** sidecar (`app.analysis.ingest` + `app.analysis.blocks`) can ingest them unchanged
and cut them into the focus blocks the generator intended — no mocking, no HTTP server, no
daemon required.

See `docs/v3/contracts.md`, section "The block generator (`scripts/blockgen/`)", for the binding
contract this implements.

## The two commands you actually run

This pairing is the whole product of this deliverable, so it gets said once, up front, in order:

```bash
# 1. Generate a corpus. By default it lands on TODAY (see "Time base" below), so opening the
#    Keld Signal page right after this shows real, current focus blocks — not an empty day.
python3 scripts/blockgen/blockgen.py --out ./out --sessions 20 --days 7 --seed 0

# 2. Point the daemon's transcript watcher at it. The output layout below is exactly what a
#    claude_code root looks like, so this env var is the whole of the wiring:
KELD_WATCH_ROOTS=claude_code:$(pwd)/out keld-agent run
```

That's it — the daemon ingests the generated transcripts the same way it ingests real ones, and
`/blocks` (or the Keld Signal page) starts showing the focus blocks blockgen intended.

## Running it — other shapes

```bash
# a custom profile
python3 scripts/blockgen/blockgen.py --out ./out --sessions 40 --days 14 --seed 1 \
    --profile my-profile.json

# pin a fixed calendar date instead of "today" (byte-identical for a given seed — see below)
python3 scripts/blockgen/blockgen.py --out ./out --sessions 20 --days 7 --seed 0 \
    --epoch 2026-01-05T13:00:00Z

# accelerated wall-clock, appending lines in real time so a daemon watching --out sees it grow
KELD_WATCH_ROOTS=claude_code:$(pwd)/out python3 scripts/blockgen/blockgen.py \
    --out ./out --sessions 3 --days 1 --seed 2 --live --speed 120
```

Output layout is exactly what the watcher expects for a `claude_code` root:
`<out>/<sanitised-cwd>/<session-uuid>.jsonl`, where the sanitised directory name is the
synthetic `cwd` with every `/` replaced by `-` (Claude Code's own convention — see
`workspace.py`'s `launch_dir`). A `manifest.json` sits beside the transcripts: the ground truth
— per session, which repository/branch/model it used and the exact intended active-run and
idle-break spans, plus which time-base mode produced it (`manifest["anchor"]`) — that both test
files check the real sidecar's output against.

`--live --speed N` appends lines in real, accelerated wall-clock (N simulated seconds per real
second) instead of writing everything at once, so a daemon pointed at `--out` via
`KELD_WATCH_ROOTS=claude_code:<out>` sees the files grow the way a real session would. It rides
whichever time base you choose (`--end`/`--epoch`) for the embedded timestamps; only its own
pacing is real-time-dependent, so it is not part of the byte-identity guarantee below — use it
for manual daemon/watcher testing, not automated checks.

## Time base: `--end` (default) vs. `--epoch`

**`--end` is the default, and it did not used to be — this was a real defect, not a style
choice.** Every block this generator writes used to sit relative to a fixed synthetic date
(2026-01-05), so months after that date a fresh run still produced a corpus that ended eight
months in the past: run blockgen, open the Keld Signal page (whose main view is *today*), see an
empty day, and reasonably conclude the app is broken. `--end` (an ISO instant, or the literal
`now`, which is what an omitted flag means) anchors the **last generated event** to that instant,
and `--days` is the span backward from it — so the default behaviour is "run this, then look at
today's blocks", which is the entire point of the tool.

**`--epoch <iso>` pins the OLD fixed-base behaviour**, for whenever you need byte-for-byte
reproducible output rather than a corpus that lands on today: generation starts at `epoch` and
moves forward across `--days` days, with no realignment afterward. The two flags are mutually
exclusive.

"Deterministic" means two different things between them, and that difference is real, not a
technicality:

| | same seed, same everything else | across two different real runs |
|---|---|---|
| `--epoch <iso>` | byte-identical output, always | byte-identical (the instant is a literal you typed, not "now") |
| `--end <iso>` | byte-identical output, always | byte-identical (same reason — an explicit instant is not real-time-dependent) |
| `--end` / `--end now` (the default) | **same STRUCTURE** — sessions, repos, branches, models, run/break shape, every relative offset — shifted to a **different absolute placement** each time, because "now" is different each time | not byte-identical, and not supposed to be |

In every mode the deterministic STRUCTURE is identical for a given seed: `--end`'s alignment is a
single additive shift applied to a corpus built exactly the way `--epoch` mode builds one,
computed once the whole corpus exists (see `generate`'s `epoch=`/`end=` docstring in
`blockgen.py`). The test suite pins `--epoch` throughout, specifically so its byte-identity
checks stay independent of the calendar date the suite happens to run on.

## Determinism

Every random decision is drawn from one `random.Random(seed)` instance in a fixed call order,
and every id (session/prompt/message uuid) is derived from that same stream rather than
`uuid.uuid4()`, so two different seeds practically never share a session id or a prompt id (128
random bits each). See "Time base" above for the one place real wall-clock time enters by
default.

Metadata is **generated, not random**: which repository a session belongs to, its branch category
(main / ticket-carrying feature / plain feature) and its model are each decided by a deterministic
smooth weighted round-robin (`WRR` in `blockgen.py`), not a weighted coin flip. A `WRR` never
touches the rng stream at all, so these three assignments are a pure function of session index and
the profile's weights — an exact realisation of the stated mix over N sessions, identical for
every seed, never a sampling outcome that merely converges to the weights in expectation.

⚠️ **A per-repository `WRR` was tried first and was wrong.** Each repository's own fresh `WRR`
starts at an all-zero `current`, and a fresh smooth-WRR's very first call breaks a tie toward the
lowest index — `main`, which ties `ticket` at equal weight 0.40. At a small session count, where
most repositories are visited only once, that meant **every** repository's first (and often only)
branch came out `main`: measured, `--sessions 4` gave 4/4 `main`, zero tickets. One `WRR` shared
across every session regardless of which repository it lands on fixed it — the mix is realised
over the *session* count, which is the population the mix is actually about.

⚠️ **Ticket numbers are drawn from ONE counter shared across every repository, not one per
prefix — a second, related defect.** With `repo_wrr` and `branch_wrr` both seed-independent (see
above), a small, realistic session count visits every ticket-carrying repository at most once
before any of them repeats. A per-prefix counter left every ticket numbered `-100` forever
(`ATLAS-100`, `KELD-100`, `SDK-100` — three distinct ticket *keys*, but a number that never once
advanced, which is indistinguishable from a hardcoded value and gives the deterministic
attribution pass's ticket-key rule nothing to actually exercise). One counter, incremented on
every ticket issued regardless of repository, gives two tickets different numbers well within a
handful of sessions (`ATLAS-100`, `KELD-101`, `TELPY-102`, ...) — pinned by
`test_blockgen.test_ticket_branches_get_distinct_numbers_at_realistic_session_count` at
`--sessions 8`.

## The profile

`--profile FILE.json` is deep-merged over the built-in `DEFAULT_PROFILE` (see `blockgen.py`);
give only the keys you want to change. Shape:

```json
{
  "repositories": [
    {"remote": "github.com/acme/widgets", "workspace": "widgets",
     "language": "go", "ticket_prefix": "WID", "weight": 2}
  ],
  "workspace_root": "/home/keldsynth/workspaces",
  "models": [["claude-opus-4-8", 0.65], ["claude-sonnet-4-6", 0.35]],
  "branch_mix": [["main", 0.40], ["ticket", 0.40], ["feature", 0.20]],
  "tool_mix": [["Bash", 0.35], ["Read", 0.25], ["Edit", 0.20], ["Write", 0.10], ["Grep", 0.10]],
  "active_run_blocks": [1, 6],
  "idle_bins_range": [3, 12],
  "session_seconds": [300, 16200],
  "sessions_per_day": [1, 4],
  "turns_per_prompt": [2, 4],
  "tokens_per_active_minute": [50000, 80000]
}
```

- `repositories` — the FIXED profile. `--profile` giving this key **replaces it wholesale**
  (the default five: `keld-signal`, `keld-atlas`, `sdk-testbench`,
  `atlas-telemetry-typescript`, `atlas-telemetry-python`, under `github.com/ncx-ai/`). Every
  session is bound to exactly one repository end to end: its `cwd`, its `gitBranch`, and the
  remote evidence planted in its tool calls all agree, and `weight` is the WRR weight driving
  how often that repository is picked.
- `idle_bins_range` — a break is this many fully-empty 5-minute bins, `[3, 12]` == 15-60 minutes.
  The floor of 3 is not arbitrary: it is `blocks.IDLE_BINS`, the sidecar's own idle threshold —
  anything below it would not read as a break to the real cutter at all.
- `active_run_blocks` — an active run is this many consecutive `blocks.MAX_BLOCK_MINUTES` (20)
  cap blocks, so `[1, 6]` means a continuous run of work 20-120 minutes long.
- `--sessions`/`--days` on the CLI drive the total session count and how they're spread across
  days directly; `sessions_per_day` in the profile is informational only in the current CLI (see
  `blockgen.py`'s `generate()`).

## The measured numbers behind the defaults

From `AGENTS.md`'s Signal v3 section and this machine's own real blocks (`sidecar/app/analysis/
blocks.py`, `docs/v3/contracts.md`):

| shape | value | source |
|---|---|---|
| block budget cut | 20 minutes | `blocks.MAX_BLOCK_MINUTES` (pre-registered 4-arm study, 496 sessions) |
| idle threshold | 3 consecutive empty 5-min bins (15 min) | `blocks.IDLE_BINS` |
| active run length | 1-6 consecutive 20-minute cap blocks (20-120 min) | contract, this machine's real blocks |
| idle gap length | 15-60 minutes | contract, this machine's real blocks |
| session length | 5 minutes - 4.5 hours | contract |
| sessions/day | 1-4 | contract |
| raw tokens / active minute | 50,000-80,000 | contract |
| request tokens vs. raw tokens | request ≈ half of raw | contract; a request's `usage` is repeated on every assistant line of that request (median 2 lines/request — `magnitude.py`), so the raw per-minute sum is roughly `2 x` the sum of distinct requests' own token counts |
| assistant turns / human prompt | 2-4 | contract |
| model mix | mostly `claude-opus-4-8`, some `claude-sonnet-4-6` | contract |

A real assistant line from this repo's own transcript
(`/Users/gabrielionescu/.claude/projects/.../96e3fce8-....jsonl`) shows the usage shape blockgen
reproduces: `input_tokens: 2, cache_creation_input_tokens: 86736 (1h TTL), cache_read_input_
tokens: 26915, output_tokens: 277` — i.e. a cache-read/cache-creation-dominated request with
input and output each under 1%. `blockgen.split_usage` proportions each synthetic request's
token budget the same way (roughly 0.3-2% input, 45-70% cache read, 20-45% cache creation,
2-8% output) rather than splitting it evenly.

## Why every scheduled event is clamped to its own 5-minute bin

`build_session` schedules one human prompt per 5-minute bin of an active run, with 2-4 requests
following it spaced tens of seconds apart. Early versions let that spacing drift past the bin's
own boundary on a busy bin (several requests, each 15-60s apart): a single stray event landing in
what was meant to be the *first* empty bin of the following idle break is enough for the sidecar's
own `blocks.active_bins` to see that bin as non-empty, silently fusing two intended active runs
into one and **dropping the break between them** — caught by
`test_blockgen_sidecar.test_every_intended_break_appears_as_a_gap_between_blocks`. Every event's
timestamp is now hard-clamped below `bin_start + BIN_SECONDS`, and `build_session_runs` was fixed
the same day for the sibling bug: a break used to sometimes get committed with no run scheduled
after it (when the session's target duration was reached right after adding the break rather
than a following run), which inflated the manifest's own intended break count past anything a
real cutter could ever show — pinned by `test_blockgen.test_breaks_never_dangle_after_the_last_
run`, swept across 40 seeds because the bug only reproduced for specific remaining-budget values
at loop exit.

## Tests

Two files, deliberately split by which Python they need:

```bash
# pure-python: no sidecar import, host python3
python3 scripts/blockgen/test_blockgen.py

# sidecar-backed: ingests through the REAL app.analysis code, sidecar venv only
KELD_HOME=$(mktemp -d) PYTHONPATH=sidecar \
    ~/.keld/sidecar-venv/bin/python scripts/blockgen/test_blockgen_sidecar.py
```

`test_blockgen.py` checks generator-level properties without touching the sidecar at all: same
seed is byte-identical under `--epoch` (and under a fixed `--end`), different seeds share no
session/prompt id, every line round-trips through `json`, both assistant lines of a request share
`requestId`/`usage`, repository counts match the profile's weights exactly (not approximately),
the output layout matches the sanitised-cwd convention, the run/break shapes stay inside the
contract's bounds, the default anchor lands within seconds of real "now" (both via the library
call and via the CLI), `--epoch`/`--end` refuse to be combined, and `--sessions 8` produces at
least two ticket-carrying branches with distinct numbers.

`test_blockgen_sidecar.py` ingests generated transcripts through `app.analysis.ingest.
ingest_file` and asks `app.analysis.blocks.cut` for the resulting blocks — in-process, the same
way the daemon's `/ingest` and `/blocks` handlers do internally, with no HTTP server needed. It
proves the four acceptance properties from `docs/v3/contracts.md`:

1. every human prompt resolves in the store's prompt index under its `promptId`, and a `uuid`
   that never appears as a `promptId` does **not** resolve (the regression AGENTS.md documents:
   indexing only `uuid` once made every real workstreams pass fail silently);
2. the sidecar's own block spans match blockgen's intended active runs to within one 5-minute
   bin, with the right number of 20-minute cap blocks per run;
3. every intended idle break of 15+ minutes shows up as an actual gap between blocks, ended/
   started with reason `"idle"` (never `"budget"`);
4. the intended repository resolves in the store's `repo` level as the exact full remote string,
   and never leaks into a different session.

A fifth test, `test_default_now_anchored_corpus_cuts_real_blocks_near_today`, generates a corpus
with blockgen's un-pinned DEFAULT time base (no `--epoch`, no `--end`) and confirms the real
sidecar cuts a block ending within the hour of actual wall-clock "now" — the end-to-end version
of the "run it, open the app, see today's blocks" requirement this file's default now satisfies.

It sets `KELD_HOME` to a fresh temp directory **and** passes an explicit store path to
`open_store()` — belt and suspenders, because this repo has been burned once already by a test
that silently mutated the developer's real `~/.keld` (see `AGENTS.md`'s note on `teleproxy`'s
`TestMain`). Confirmed on this machine: the real `~/.keld/state/refseries.db` is untouched by
running the sidecar test (its own daemon keeps updating it independently in the background,
which is expected and unrelated).

## What I had to change in the contract

Nothing. `docs/v3/contracts.md`'s block-generator bullets are implemented as written.
