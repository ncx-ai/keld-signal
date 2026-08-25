# The session prior — measurement against the four bars

**Measurement only. Nothing shipped**: no facet, no vocabulary change, no `SchemaVersion` bump
(it stays 12). This measures what
`docs/superpowers/specs/2026-08-24-session-prior-design.md` describes; whether it ships is the
reader's decision.

Harness `scripts/session_prior.py`, tests `scripts/test_session_prior.py` (11 tests, 10
mutations, every one confirmed to bite with an `AssertionError`). Durable output:
`~/keld/refseries-context/session-prior/` (`prior-frame.ndjson`, `session-prior-results.json`,
`frame-run.txt`, `measure-run.txt`).

    PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/session_prior.py frame
    PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/session_prior.py measure

## The frame, asserted

500 frozen-corpus transcripts, span 60 / stride 50 minutes, cut inside the per-file loop,
identity `(file, start)` with a per-file `fid` in the wid.

    ASSERTED windows=1022 files=500 distinct_wids=1022
      session-collision check: prefix-keyed would give 1017 (5 lost, 0.5%);
      445 of 500 files sit in a colliding prefix group
      session-first windows (empty `before` prior): 461 (45.1%) over 490 sessions

The 8-character `display_session` prefix collides for 445 of the 500 files, so the trap that
produced 550 windows against a true 1,022 is still live on this corpus and is checked at run
time rather than assumed. 490 of the 500 files yield windows (4 hold no parseable turn; 6 more span no time at all — every turn at one instant — so no window opens).

## The one place this departs from the spec, and why — READ THIS FIRST

The spec recommends option A, "prior over the session so far (causal)". Taken literally — the
daemon recomputes the prior each tick over everything ingested, and by then the window it is
characterising has itself been ingested — **the window's own evidence sits inside its own
prior**. Measured as `sofar`, that reading is degenerate in a way the spec does not anticipate:

- **`novel` cannot fire. Ever.** The window's dominant value is in the session by construction.
  Bar 2, "the signal's real product", is **structurally zero, not empirically low** — 0 novel
  windows on all seven dimensions, 0/1022, verified as `sofar_novelty_structurally_zero: true`.
- A session's **first window is its own prior**: agreement 100%, departure 0, contrast vacuous.
- Self-inclusion shrinks every departure toward zero, monotonically with how much of the session
  the window is.

So the primary variant here is `before`: the prior over `[session_start, window_start)`, the
session as it stood **before** this window. Still causal (a subset of what the daemon knew), and
the only reading under which all three contrast measures are non-degenerate. `sofar` is reported
in full beside it so the cost of the literal reading is visible rather than asserted.

**If this design ships, the spec's boundary section needs this correction**: the prior must be
cut at the window's start, not at "now".

`before` leaves 461 of 1,022 windows (45.1%) with an empty prior. Those are session-first
windows and they are reported as `absent`, never filled — which is the design's own rule, and
the single largest fact in bar 3 below.

## Bar 1 — AGREEMENT. Bar stated before computing: **at least one of `model` /
`output_type` / `language` / `workflow` / `tooling` at or below 0.90**

Justification, fixed in code before any result was read: a reader sees on the order of ten
windows in a working day. At 0.95 agreement a departure surfaces once per twenty windows — once
every two days — which is indistinguishable from a field that never fires, and this series has
already dropped three dynamics and four of six transcript signals on that reasoning. At 0.90 or
below roughly one window a day carries a contrast, which is where a reader learns to look at the
field. Denominator is windows where **both** sides are attributed: where either is not, `agrees`
is undefined and inventing a value for it is the fallback this design exists to refuse.

**No pooled number is reported anywhere in this study.** `project` and `branch` are scoped by the
transcript and `workspace` has zero transitions across the whole corpus; pooling them with the
dimensions that can move would describe neither.

variant `before` (primary):

| dimension | both attributed | agree | disagree | agreement | window attributed |
|---|---|---|---|---|---|
| project | 508 | 508 | 0 | **100.0%** | 993 |
| branch | 456 | 347 | 109 | **76.1%** | 986 |
| model | 497 | 497 | 0 | **100.0%** | 981 |
| output_type | 347 | 301 | 46 | **86.7%** | 661 |
| language | 272 | 192 | 80 | **70.6%** | 642 |
| workflow | 66 | 17 | 49 | **25.8%** | 191 |
| tooling | 67 | 66 | 1 | **98.5%** | 129 |

variant `sofar`: project 100.0%, branch 90.3%, model 100.0%, output_type 93.5%, language 89.9%,
workflow 83.8%, tooling 99.2%.

**PASS** — `output_type` (86.7%), `language` (70.6%) and `workflow` (25.8%) clear the bar. The
contrast is not near-total agreement and does carry information.

But the per-dimension split is the whole result, exactly as anticipated: **`project` and `model`
agree 100% of the time with zero exceptions**, and `tooling` disagrees once in 67. On those
three the contrast is dead weight — a published `agrees: true` that is true by construction. On
`workflow` the window agrees with its session only **one time in four**: an hour of agentic work
runs a different skill from the session's dominant one three times out of four, which is a
genuinely different fact from anything the window alone reports.

Note also the self-inclusion effect measured directly: `language` agreement moves 70.6% →
89.9% and `workflow` 25.8% → 83.8% purely from putting the window inside its own prior. Two
thirds of `sofar`'s apparent agreement on `workflow` is the window agreeing with itself.

## Bar 2 — NOVELTY YIELD. Bar: **at least one dimension at or above 0.05**

Denominator is windows with an attributed value **and** a non-empty prior — novelty against an
empty prior is not yield, it is a session with no history, and it is counted separately.

variant `before` (primary):

| dimension | window attributed | with prior evidence | prior empty | novel | novelty |
|---|---|---|---|---|---|
| project | 993 | 535 | 458 | 0 | **0.0%** |
| branch | 986 | 528 | 458 | 32 | **6.1%** |
| model | 981 | 497 | 484 | 0 | **0.0%** |
| output_type | 661 | 364 | 297 | 4 | **1.1%** |
| language | 642 | 348 | 294 | 8 | **2.3%** |
| workflow | 191 | 91 | 100 | 40 | **44.0%** |
| tooling | 129 | 72 | 57 | 0 | **0.0%** |

variant `sofar`: **0.0% on every dimension, 0 of 1,022 — structurally, not empirically.**

**PASS on `workflow` (44.0%) and `branch` (6.1%); everything else is at or below 2.3%.**

`workflow` is the finding. 40 of 91 windows run a skill the session had never run before — a
change of method that no per-window view can see, because a per-window view has nothing to
compare against. Named, with profiles:

    12c80ab4#t0002-20260721T1402  superpowers:executing-plans  0.853 n=252
        prior attributed n=38  [superpowers:brainstorming:38]
    6e2a58f9#t0106-20260724T2035  superpowers:systematic-debugging  0.984 n=192
        prior attributed n=269 [executing-plans:152, brainstorming:52, writing-plans:48]
    139be731#t0003-20260724T1738  superpowers:executing-plans  0.887 n=159
        prior attributed n=19  [brainstorming:11, artifact-design:8]
    f745121b#t0290-20260728T1955  superpowers:systematic-debugging  0.979 n=142
        prior attributed n=388 [writing-plans:210, requesting-code-review:91, brainstorming:71]
    9aaa379a#t0166-20260804T1645  superpowers:executing-plans  0.961 n=129
        prior attributed n=158 [test-driven-development:89, brainstorming:54, writing-plans:8]
    fff01ac3#t0298-20260720T1709  superpowers:writing-plans  1.000 n=113
        prior attributed n=17  [brainstorming:17]
    32b52295#t0019-20260806T1927  superpowers:test-driven-development  0.667 n=108
        prior attributed n=125 [systematic-debugging:83, brainstorming:42]

These read as the phase transitions of the workflow they actually are — brainstorming → writing
plans → executing → debugging. That is a real signal and it is only visible against a prior.

`branch`'s 32 are mostly the same event seen from a different level — a worktree or feature
branch opening inside a session that had been on `main`:

    12c80ab4#t0002-20260721T1402  worktree-atlas-auth-oauth 0.874 n=301 | prior attributed [main:156]
    1a3aa6d2#t0010-20260721T2301  design-sync-institutional-light 0.877 n=292 | prior [main:418]
    71d70b51#t0128-20260806T1118  HEAD 0.648 n=307 | prior attributed [main:309]

`language` novelty is 8 windows and `output_type` 4 (all four of them `web`), which is thin.
`project`, `model` and `tooling` produce **zero** novel windows in the whole corpus — for
`project` that is the expected consequence of `workspace` having no transitions.

## Bar 3 — PRIOR COVERAGE. Bar: **0.70 attributed**, and this is where the design is weakest

variant `before` (primary). Every reason its own column, including the zeroes; `absent` split
into the two different facts it pools:

| dimension | attributed | thin | no_majority | tie | absent | coverage | prior ev median | absent: session-first / level-never-fired | coverage given a prior exists (n=561) |
|---|---|---|---|---|---|---|---|---|---|
| project | 533 | 28 | 0 | 0 | 461 | **52.1%** | 86 | 461 / 0 | **95.0%** |
| branch | 482 | 28 | 51 | 0 | 461 | **47.2%** | 86 | 461 / 0 | **85.9%** |
| model | 533 | 0 | 0 | 0 | 489 | **52.1%** | 84 | 461 / 28 | **95.0%** |
| output_type | 510 | 6 | 15 | 0 | 491 | **49.9%** | 16 | 461 / 30 | **90.9%** |
| language | 407 | 7 | 108 | 3 | 497 | **39.8%** | 10 | 461 / 36 | **72.5%** |
| workflow | 370 | 7 | 98 | 1 | 546 | **36.2%** | 0 | 461 / 85 | **66.0%** |
| tooling | 248 | 45 | 0 | 0 | 729 | **24.3%** | 0 | 461 / 268 | **44.2%** |

variant `sofar`: project 99.6%, model 99.5%, branch 93.9%, output_type 79.3%, language 68.5%,
workflow 45.3%, tooling 30.9%.

**FAIL on all seven dimensions as the bar is literally stated** (best is 52.1%). But the reasons
breakdown reframes it exactly the way `action`'s did:

- **461 of the 1,022 windows (45.1%) are a session's first window**, and every one of those is
  `absent` on every dimension. That is not a floor problem, a slice-length problem, or a
  dimension problem — it is the boundary the spec's own "honest answer" section is about. No
  parameter fixes it, and filling it would be the fallback the design refuses.
- Restricted to the 561 windows that **have** a session behind them, five of seven clear 0.70:
  `project` and `model` 95.0%, `output_type` 90.9%, `branch` 85.9%, `language` 72.5%.
  `workflow` (66.0%) and `tooling` (44.2%) still fail.
- **`no_majority` is essentially absent from the three cheap dimensions** — `project` 0,
  `model` 0, `tooling` 0. When those dimensions have a session, they have an answer. The
  spec's "the window's ambiguity is the session's ambiguity" case is real but concentrated:
  `language` 108 windows (10.6% of the frame) and `workflow` 98.
- **`tooling` is a different shape entirely**: 729 absent of which 268 are windows that DO have
  a session in which the `toolchain` level simply never fired. That is the same
  77.8%-absent inventory shape `action` and `tooling` have shown at every slice length, and no
  prior converts an empty level into an attributed one.

Read honestly: **the prior is a usable frame of reference for `project`, `model`, `output_type`,
`branch` and (marginally) `language`, and only once a session has accumulated. It is not one for
`workflow` or `tooling`** — and `workflow` is precisely the dimension that carries bars 1 and 2.
That tension is the central finding of this measurement and is not resolvable by tuning.

## Bar 4 — INDEPENDENCE. Bar: **|r| with log(1 + window volume) < 0.50**

variant `before` (primary):

| dimension | r_agrees | r_novel | r_departure | r_abs_departure | n |
|---|---|---|---|---|---|
| project | 0.000 | 0.000 | 0.000 | 0.000 | 535 |
| branch | -0.008 | 0.182 | 0.009 | 0.029 | 528 |
| model | 0.000 | 0.000 | -0.043 | -0.042 | 497 |
| output_type | 0.126 | -0.028 | -0.228 | -0.239 | 364 |
| language | -0.011 | 0.069 | -0.126 | -0.117 | 348 |
| workflow | 0.075 | 0.157 | -0.062 | -0.041 | 91 |
| tooling | -0.019 | 0.000 | 0.011 | 0.014 | 72 |

**PASS. Max |r| = 0.239** (`output_type`, `r_abs_departure`), no violations, and every measure on
every dimension well clear of 0.50 in both variants (`sofar` max 0.172). This is not a size
bucket wearing a label — the test that killed derived routing clusters (size x
did-anything-run), output volume (+0.552), `interactivity` (+0.497) and an authoring split
(+0.737). The mild negative on `output_type`/`language` departure is the expected direction: a
bigger window has a longer tail, so its top share is lower and departures compress.

## The motivating case — reproduced as a class, NOT as the specific window

The spec's example: "a window reported `language: Python 0.576` — barely over the floor — in a
session that was overwhelmingly TypeScript."

**Exactly one window on this frame has `language` share exactly 0.576**, and it is not that case:

    6e2a58f9#t0106-20260724T2035   Python 0.576 (n=125)
        before prior: attributed Python 0.537 (n=108) [Python:58, TypeScript:47, Markdown:3]
        departure +0.039   agrees TRUE   novel False

Its session is Python-led too. So the number 0.576 alone does not identify an excursion on this
frame, and here the contrast correctly reports **agreement** — the design saying "this is not an
excursion" is the design working, not failing. (The 0.576 in the spec came from a different
frame; it cannot be pinned to a window here.)

**The shape reproduces, and the contrast flags every instance.** Nine windows are Python below
0.62 in a TypeScript-led session, and all nine are flagged `agrees: false` with a large positive
departure:

    1d2b97b5#t0447-20260806T0204  Python 0.571 (n=7)
        prior attributed TypeScript 0.886 (n=271) [TypeScript:240, Python:15, Go:9]
        prior share of Python 0.055   departure +0.516
    5a36f1d7#t0102-20260722T2057  Python 0.567 (n=30)
        prior attributed TypeScript 0.834 (n=290) [TypeScript:242, Python:36, Markdown:11]
        departure +0.443
    5a36f1d7#t0102-20260723T1837  Python 0.556 (n=142)
        prior attributed TypeScript 0.821 (n=759) [TypeScript:623, Python:110, Markdown:16]
        departure +0.411
    agent-a1#t0034-20260810T2139  Python 0.516 (n=31)
        prior attributed TypeScript (n=103) [TypeScript:92, Python:11]   departure +0.409
    f745121b#t0290-20260729T1645  Python 0.583 (n=12)
        prior attributed TypeScript 0.596 (n=933) [TypeScript:556, Python:288, Go:46]
        departure +0.275
    36821116#t0021-20260727T1909  Python 0.560 (n=75)
        prior attributed TypeScript (n=154) [TypeScript:94, Python:46, Markdown:8]
        departure +0.261
    6e2a58f9#t0106-20260725T2135  Python 0.543 (n=46)  departure +0.136
    6e2a58f9#t0106-20260725T2225  Python 0.536 (n=28)  departure +0.127
    f745121b#t0290-20260729T1915  Python 0.605 (n=147) departure +0.335

Widened to the general shape — `language` attributed below 0.70 where the session does not agree
— there are **61** such windows under `before` (57 under `sofar`). The measure that does the
work is `departure` against `prior_share_of_window_value`, not `agrees`: on
`1d2b97b5#t0447-20260806T0204` the session gives Python a 5.5% share while the window gives it
57.1%, and that gap is the excursion stated as a number.

Note what `novel` does NOT do here: it is `false` in all nine, because a TypeScript session that
has touched Python at all is not one where Python is absent. **`novel` and `departure` catch
different things**, and the motivating case is a `departure` case.

## Named windows — largest departures, with profiles

    branch      2927f65b#t0011-20260724T1731  +1.000  feat/agents-teams-design 1.000 (n=64)
                    prior no_majority 0.419 (n=465) [main:195, design-sync-...:139, filter-...:91]
    branch      16c69396#t0303-20260812T1322  +1.000  feat/llm-classify-study 1.000 (n=20)
                    prior attributed main 1.000 (n=1142)
    language    2927f65b#t0011-20260723T1631  +1.000  Go 1.000 (n=7)
                    prior attributed Python 0.671 (n=79) [Python:53, TypeScript:21, Markdown:5]
    language    9aaa379a#t0166-20260801T1935  +0.946  Python 1.000 (n=45)
                    prior attributed TypeScript 0.946 (n=37) [TypeScript:35, Python:2]
    output_type 0ac739ad#t0299-20260805T2341  +0.967  prose 1.000 (n=6)
                    prior attributed code 0.946 (n=92) [code:87, prose:3, data:1]
    output_type 139be731#t0003-20260724T1738  +0.842  code 0.925 (n=107)
                    prior attributed web 0.750 (n=12) [web:9, prose:2, code:1]
    workflow    1a3aa6d2#t0010-20260721T2301  +1.000  superpowers:brainstorming 1.000 (n=25)
                    prior attributed superpowers:systematic-debugging 1.000 (n=166)
    workflow    32b52295#t0019-20260807T1437  +1.000  artifact-design 1.000 (n=6)
                    prior no_majority 0.356 (n=233) [systematic-debugging:83, brainstorming:78, tdd:72]
    tooling     fff01ac3#t0298-20260720T1619  +0.878  infrastructure 1.000 (n=5)
                    prior attributed image 0.775 (n=49) [image:38, infrastructure:6, database:5]
    model       16c69396#t0303-20260813T1922  -0.103  claude-opus-5 0.894 (n=113)
                    prior attributed claude-opus-5 0.997 (n=1259) [claude-opus-5:1255, <synthetic>:4]
    project     a8f58d56#t0000-20260819T1432  +0.000  keld 1.000 (n=18)
                    prior attributed keld 1.000 (n=67)

`project`'s **largest** departure across all 1,022 windows is +0.000 and `model`'s is -0.103.
Those two dimensions produce no contrast at all, on any of the three measures.

## Verdict

| bar | result |
|---|---|
| 1. Agreement ≤ 0.90 on some can-differ dimension | **PASS** — output_type 86.7%, language 70.6%, workflow 25.8% |
| 2. Novelty ≥ 0.05 on some dimension | **PASS** on `before` (workflow 44.0%, branch 6.1%); **structurally 0** on the spec's literal `sofar` |
| 3. Prior coverage ≥ 0.70 | **FAIL on all seven** as stated (best 52.1%); 5 of 7 pass once restricted to the 561 windows that have a session (project/model 95.0%, output_type 90.9%, branch 85.9%, language 72.5%); workflow 66.0% and tooling 44.2% fail either way |
| 4. \|r\| with log volume < 0.50 | **PASS** — max 0.239 |

What the numbers say, if the decision were being made from them:

1. **The prior must be cut at the window's start.** The spec's literal reading makes bar 2
   structurally unmeasurable. This is a spec correction, not a tuning choice.
2. **The contrast belongs on a subset of the dimensions, not all seven.** `project` and `model`
   agree 100% with zero novel windows and a maximum departure of 0.000 / -0.103; publishing a
   contrast for them is publishing a constant. `workflow`, `language`, `output_type` and
   `branch` are where the signal is.
3. **`workflow` carries bars 1 and 2 and fails bar 3.** 44% novelty and 25.8% agreement against
   a prior that is attributed only 66% of the time even when a session exists. Whether that is a
   frame of reference or an interesting-but-unreliable one is a judgement the numbers do not
   make.
4. **45.1% of windows have no session behind them at all.** Any payload carrying this field will
   report `absent` for nearly half of all windows, and the design's own rule says that must stay
   `absent`. A reader has to be told that up front or they will read the blank as a defect.
