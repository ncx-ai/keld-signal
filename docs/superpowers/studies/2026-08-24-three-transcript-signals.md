# Three discarded transcript signals, measured

Harness `scripts/tool_signal.py`, tests `scripts/test_tool_signal.py` (21 tests, 10 mutations each
confirmed to bite). Runs: `frame-run.txt`, `stats-run.txt`, `cost-run.txt`. Frame
`signal-frame.ndjson` (1,022 windows), statistics `signal-stats.json`, corpus totals
`corpus-totals.json`, parse cost `parse-cost.json`.

**Measurement only.** Nothing was wired into the store or the pipeline, and nothing under
`sidecar/app/` was modified.

## Corpus and frame

500 transcripts / 697.4 MB from `~/keld/refseries-context/frozen-corpus/{projects,john-projects}`,
span 60 min / stride 50 min, cut inside a per-file loop. **1,022 windows, asserted**, joined
row-for-row on `(file, start)` to the published `observable-frame.ndjson` so `volume` and
`n_actions` are the published numbers rather than a re-derivation. 496 files produced windows; 4
are empty.

⚠️ **The corpus does not match the volumes in the brief, and the brief's own note says why:** it
quotes 180 transcripts. Over the 500-transcript frozen corpus the totals are

| | brief (180 transcripts) | measured (500, frozen) |
|---|---|---|
| `tool_result` blocks | 15,069 | **32,028** |
| with `is_error` | 327 (2.2%) | **755 (2.36%)** |
| payload | 21.6 MB | **52.4 MB** |
| median result | 252 B | **312 B** |
| inter-turn gaps | 45,764 | **65,970** |
| gap median / p90 | 2.2s / 16.4s | **4.2s / 27.5s** |

The *rate* agrees (2.2% vs 2.36%); the medians do not, and the gap median is 1.9x the quoted one.
Every number below is on the 500-file frozen corpus, because that is the corpus the 1,022-window
frame is defined on.

⚠️ **The collision assertion is weaker than the failure it names.** Keying the frame on
`(prefix, start)` instead of per-file identity loses only **5** windows here (1,017 vs 1,022), not
472. The 550-window merge was not a write-time key collision: `routing_class` merged *rows from
different files into one pseudo-transcript* and then windowed that, which moves every
pseudo-transcript's `t0`/`tN`. 445 of 500 files do sit in a colliding prefix group, so the trap is
live — but the assertion that fires on it is the per-file `(file, start)` count, not the prefix
count.

## Pre-registered thresholds (fixed in the file before any number existed)

`R_SIZE_MAX 0.50` — |r| against log1p(volume) or log1p(n_actions) at or above this **is** size.
`RESID_MIN 0.50` — share of variance outside the volume decile. `PREVALENCE_MIN 0.20` — a signal
present in fewer windows than this is not a facet.

## Results

| signal | prevalence | median | p90 | max | r log-vol | rho | r log-acts | eta2 vol | resid | eta2 file / chance | verdict |
|---|---|---|---|---|---|---|---|---|---|---|---|
| error/n_errors | 0.364 | 0 | 2 | 22 | **+0.471** | +0.612 | +0.472 | 0.295 | 0.705 | 0.228 / 0.095 | borderline size |
| error/error_rate | 0.364 | 0 | 0.059 | 0.500 | **+0.152** | +0.500 | +0.158 | 0.049 | 0.951 | 0.159 / 0.091 | **independent** |
| error/max_err_run | 0.364 | 0 | 1 | 9 | +0.464 | +0.589 | +0.464 | 0.243 | 0.757 | 0.101 / 0.071 | borderline size |
| error/n_thrash | **0.048** | 0 | 0 | 3 | +0.198 | +0.211 | +0.203 | 0.051 | 0.949 | 0.124 / 0.080 | **fails prevalence** |
| latency/gap_median | 0.993 | 4.18s | 12.24s | 920s | **-0.084** | +0.108 | -0.079 | 0.010 | 0.990 | 0.109 / 0.089 | **independent** |
| latency/gap_p90 | 0.993 | 25.34s | 131.4s | 2840s | **-0.135** | -0.030 | -0.156 | 0.026 | 0.974 | 0.165 / 0.084 | **independent** |
| latency/slow_share | 0.866 | 0.098 | 0.240 | 1.0 | **-0.185** | -0.111 | -0.214 | 0.051 | 0.949 | **0.286 / 0.092** | **independent** |
| latency/fast_share | 0.979 | 0.536 | 0.784 | 1.0 | **+0.012** | -0.075 | -0.001 | 0.022 | 0.978 | **0.401 / 0.082** | **independent** |
| output/out_bytes | 0.979 | 25.1 KB | 144.3 KB | 507 KB | **+0.552** | +0.671 | +0.561 | 0.390 | 0.610 | 0.369 / 0.082 | **SIZE** |
| output/log_out_bytes | 0.979 | 10.15 | 11.90 | 13.16 | **+0.654** | +0.671 | +0.664 | 0.380 | 0.620 | 0.193 / 0.087 | **SIZE** |
| output/bytes_per_result | 0.979 | 1383 B | 5088 B | 22.5 KB | **-0.339** | -0.284 | -0.334 | 0.150 | 0.850 | 0.106 / 0.084 | independent, but see below |
| output/out_bytes_median | 0.979 | 422 B | 4544 B | 14.4 KB | **-0.510** | -0.442 | -0.501 | 0.349 | 0.651 | **SIZE** (negative) |

`eta2 file` is the between-transcript share of variance against a permuted chance baseline: a
signal independent of size but at chance here is jitter, not a partition. It was added *after* the
first run and is a diagnostic, not a pre-registered gate.

### 1. error / retry — the flag survives, the thrash facet does not

- **36.4% of windows carry at least one error** (372/1,022). The 2.2% figure is the block-level
  rate; at window scale the signal is common, not rare.
- **Whether a window has an error is mostly its size.** Median volume 898 (error) vs 175 (clean),
  ratio **5.13**; `n_results` 5.44x; `n_turns` 4.55x. `n_errors` itself is borderline
  (r +0.471, rho +0.612).
- **The rate is not size.** `error_rate` r +0.152, eta2 0.049, resid 0.951, and it clears
  prevalence at 0.364. Among windows that have any error: median 0.036, p90 0.129, max 0.500.
- **Thrashing is real, concentrated, and too rare to be a facet.** Runs of consecutive failures on
  one resource: `0:650, 1:323, 2:34, 3:8, 4:3, 5:1, 8:2, 9:1`. Run>=2 in **49 windows (4.8%)**
  across 33 distinct files — **below `PREVALENCE_MIN 0.20`, refuted as a facet.** But those 49
  windows hold **29.1% of all errors** (240 of 825), so it is a concentrated rare event, not a flat
  one.
- **It is not the action mix restated.** Of the 54 windows with `error_rate >= 0.10`, 47 (87%) are
  `read`-dominated — against 72% of clean windows. `top_action` cannot separate them.

Named. Highest: **`f745121b#t0290-20260729T1915`** — vol 2596, acts 208, top `read`, 319 turns,
15 prompts, 135 results, **22 errors, longest single-resource run 9**, gapmed 4.20s, out 121 KB.
Next: **`72d3e8b1#t0129-20260810T1741`** (vol 1157, 66 results, 10 errors, **run 8**, gapmed 5.08s,
p90 58.5s, out 39 KB) and its near-twin **`51476fbe#t0026-20260810T1741`** (vol 1184, 68 results,
run 8) — a resumed/forked session pair, not a bug, but a reminder that the frame does not dedup by
content (one byte-identical group, 5 redundant windows).
Highest *rate*: **`agent-a7#t0373-20260812T0204`** — rate 0.500 on **2 results**, vol 48. Clean and
comparable: **`0ac739ad#t0299-20260805T2251`** — vol 1686, 161 acts, 150 turns, 59 results, zero
errors. Same size decile as several error windows; the flag separates them, volume does not.

### 2. turn latency — the one candidate that clears both tests

- **Not size, by a wide margin.** `gap_median` r **-0.084** against log volume (eta2 0.010, resid
  0.990); `gap_p90` -0.135; `slow_share` -0.185; `fast_share` **+0.012**. Against the published
  evidence count: -0.079 to -0.214. Against `n_prompts` -0.015 and `n_turns` -0.051 — so it is
  **not `interactivity` (+0.497) wearing a new label**, which was the specific worry.
- **Robust to eligibility.** A window with fewer than 5 gaps cannot support a median (7 windows
  have zero gaps, 35 have <5). Restricted to the 987 eligible windows the verdicts are unchanged:
  -0.047 / -0.115 / -0.211 / -0.107.
- **Session-structured, unlike everything else that passed.** `fast_share` eta2_file **0.401**
  against a 0.082 chance baseline — the strongest of all twelve measures; `slow_share` 0.286 vs
  0.092. `gap_median` is at chance (0.109 vs 0.089), so the *share* is the durable form and the
  *median* is not.
- **The "two regimes" hypothesis is wrong at window scale.** Only **6 of 1,022** windows have
  `gap_median >= 30s`; 592 are under 5s. The long tail lives *inside* windows (window `gap_p90`
  reaches 2,840s). So the regime split is per-gap; the usable window axis is the **share of slow
  gaps**, not a bimodal class. `slow_share` and `fast_share` correlate only -0.586, so they are not
  the same variable inverted.

Named. Fast: **`agent-af#t0441-20260813T2009`** — fast_share 1.000, vol 33, 6 turns, gapmed 1.04s.
Slow: **`16c69396#t0303-20260820T1352`** — fast_share 0.000, vol 59, 6 turns, 2 prompts, gapmed
28.9s, **p90 256.8s**, slow_share 0.40. Nearly the same size, nearly the same action mix, opposite
value: this is the separation `volume` and `evidence` cannot make. Contrast the defect this
surfaced: **`12c80ab4#t0002-20260721T1632`** holds **one turn** and therefore zero gaps, and was
sitting at `fast_share 0.0` next to the genuinely slow window above until the eligibility rule was
added.

### 3. output volume — refuted, and what survives is half the latency signal

- **The prior was right.** `out_bytes` r **+0.552**, `log_out_bytes` **+0.654**, eta2 0.39 —
  over threshold, SIZE. Weaker than `context_volume`'s +0.914 but the same verdict.
- **It is mostly a count of tool calls.** log `out_bytes` vs log `n_results` r **+0.731**.
- **The median payload runs the *other* way.** `out_bytes_median` r **-0.510** — bigger windows
  return *smaller* individual results. Size-confounded, negatively, which is still size.
- **`bytes_per_result` clears the size test (-0.339, resid 0.850) but is not a third axis.** It is
  at chance on session structure (0.106 vs 0.084), and on the 770 windows with >=5 results it
  correlates **+0.536 with `fast_share`** — ~29% shared variance with signal 2. The surviving
  fragment of "output volume" is largely a restatement of "how fast were the turns".

Named. Largest: **`agent-a6#t0260-20260803T0009`** — 507 KB out, vol 3430, 452 turns, 231 results,
1 error, gapmed 3.24s. **`agent-ac#t0478-20260811T1909`** and **`agent-ac#t0302-20260811T1909`**
are the byte-identical duplicate pair (432 KB, vol 2582, 133 results each). Zero-output windows
with real work: **`129e9a80#t0301-20260812T0002`** (vol 37, 7 turns, 0 results, gapmed 29.1s,
p90 226.9s) — pure conversation, no tool output at all, and its latency profile is the opposite of
`agent-a6`'s despite both being unremarkable on volume.

## Parse cost — the brief's figure does not hold, and its diagnosis is wrong

500 files / 697.4 MB, 5 repeats, best-of reported. **73.8% of the corpus (514.1 MB) sits on lines
`turns_in` never parses.**

| arm | best | ms/MB | what it is |
|---|---|---|---|
| read | 0.321s | **0.46** | iterate the lines, no `json` at all |
| turns | 1.023s | **1.47** | today's filter |
| min | 1.493s | **2.14** | + `is_error` and byte length, no thrash key |
| full | 2.261s | **3.24** | + tool/target identity for run detection |

- **Full projection: +121.0%, 1.78 ms/MB** — not +36% / 0.5 ms/MB. On a 90 MB whole-file ingest
  that is **~160 ms**, not ~45 ms.
- **Minimal projection: +46.0%, 0.67 ms/MB.** The brief's figure is roughly *this* arm — the error
  flag and the byte length, without the identity the thrash signal needs.
- **The cost is `json.loads`, not re-reading bytes.** The bare line read is 0.46 ms/MB, only
  **31%** of today's pass, and it is already paid. `json.loads` on today's kept lines costs
  1.01 ms/MB; on the 514 MB currently skipped it costs 1.78 ms/MB. "Bytes we already read" is not
  where the time goes.
- The incremental-tail argument is untouched: a tail parse touches kilobytes either way.

**This changes the recommendation.** The thrash key costs **1.1 ms/MB more than the rest of the
signal combined** and buys a measure present in 4.8% of windows. The error flag and the byte
length cost 0.67 ms/MB together — and of those two, the byte length is refuted as a facet.

## Verdicts

| signal | size test | prevalence | session structure | verdict |
|---|---|---|---|---|
| error flag / `error_rate` | pass (+0.152) | pass (0.364) | at chance | **carry as a window statistic**, not a classified facet |
| error runs / thrashing | pass (+0.198) | **fail (0.048)** | at chance | **refuted as a facet**; a concentrated rare event (29% of errors in 4.8% of windows) — an alert, not an axis |
| turn latency (`fast_share`/`slow_share`) | **pass (+0.012 / -0.185)** | pass | **strongest (0.401 / 0.286 vs 0.08)** | **survives** — the only candidate clearing every test |
| output volume (total) | **fail (+0.552 / +0.654)** | pass | structured | **refuted** — a restatement of tool-call count |
| output volume (per call) | pass (-0.339) | pass | at chance | not an independent axis: +0.536 with `fast_share` |

One of three survives, which is the rate this series has been running at. The long-context routing
axis is **not** in `tool_result` sizes.

## Privacy

`project_result` is the only function that reads a `tool_result`, and it returns a `Result`
namedtuple with no field a string can occupy: four ints, a bool, a closed-vocabulary tool index,
and two BLAKE2b-64 digests. Target identity is a digest, never the string. Tool names outside a
closed 20-entry vocabulary collapse to one `<other>` bucket, so an arbitrary MCP or skill name
cannot escape. `assert_numeric` walks every field of every projected result and every window record
**on every run**, not only in the tests. Nothing in `signal-frame.ndjson`, `signal-stats.json`,
this file or the run logs is a payload: every rendered example is a window id, a count, a byte
length or a duration.
