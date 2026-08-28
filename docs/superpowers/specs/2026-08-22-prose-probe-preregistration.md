# Pre-registration — can GLiNER2 classify window-scale work?

Written and committed BEFORE any model call. Ground truth (`truth.csv`) is committed in a
separate commit, also before the first call. Results go in a separate document; this one is not
edited after the run.

## The question

Every number we have about window-scale classification comes from a prompted LLM. GLiNER2 —
which is what actually runs in production — has never been measured on a window at all. Two
things are being asked at once:

1. Can GLiNER2 classify a window into a closed extensive label set better than a deterministic
   rule over the frame?
2. Does the deterministic digest help or hurt **as input to a bi-encoder**? The digest's measured
   +36.7 / +26.7 lift was on a *generative* reader, and the stated mechanism — a conclusion beats
   a number because the reader cannot divide — cannot transfer to a model that scores label
   descriptions by semantic overlap. The prior does not carry.

## Why this gate, and what it cost

The original design gated on `artifact: spreadsheet`. That artifact appears **0 times in 552,423
events**. Corpus-wide, over 552 windows, `presentation` clears 5 evidence in exactly **1** window,
`document` in 2, `image` in 2, `data` in 0. The only non-code artifacts with mass are `prose` (85)
and `web` (38). The gate is therefore `prose`, chosen for N, and the substitution is recorded
because it changes what the result generalises to.

Worth carrying forward: in the one non-engineering session in the corpus (`a8f58d56`), the windows
with **no artifact evidence at all** are precisely the product-research hours that carry ACME,
UnityPredict, Bedrock, Together.ai, Vertex and the Magenta decision. On this corpus the `artifact`
level is empty exactly where the attribution value is.

## Sample

Gate: `artifact: prose >= 5` in a 60min span / 50min stride window → 85 windows, 24 sessions.
Seeded draw (`SEED = 20260822`), max 2 per session, N = 24. Negative control: `prose == 0 and
code >= 20`, max 1 per session, N = 12. Windows and per-window prose share are in `sample.csv`.

Frames are **session-scoped** (`/tmp/refseries-sess`, `series --entity session --bin 0.25`), not
repo-scoped, so the digest describes the same transcript the window text is drawn from. Repo
scope would blend concurrent sessions in the same repository and make digest and text disagree
about what the window was.

## Labels

Rhetorical function of the writing. Scored against readable descriptions, since for a bi-encoder
the label wording is load-bearing.

| id | label text | description |
|---|---|---|
| `plan` | planning what to do next | decides or sequences work that has not happened yet |
| `record` | recording what was found | captures results, findings or decisions already reached |
| `explain` | explaining how something works | durable reference for a later reader |

Chosen so it is **not** frame-determined: a file under `specs/` can be any of the three. That
keeps the frame rule a genuine competitor rather than a tautology — the flaw that made the
activity-type rule's 9/9 partly circular.

## Ground truth

One labeller reads each window's **text only** — never the digest, which is an experimental arm —
and assigns one label plus a confidence. Committed before the first model call; the commit hash
is quoted in the results. Low-confidence windows are reported separately and excluded from the
primary metric.

Stated limitation: a single labeller who also runs the arms. Committing first prevents post-hoc
adjustment. It does not prevent initial bias, and no claim here should be read as if it did.

## Arms

| arm | input |
|---|---|
| `majority` | most frequent true class; no model |
| `rule` | deterministic rule over the frame (`file`, `dir`, `skill`, `action`) |
| `G-digest` | the `executive()` digest |
| `G-text` | window text (tools stripped), head-truncated by gliner2 |
| `G-both` | digest + text |

Each GLiNER2 arm runs in both modes: `single_label` (softmax, forced choice) and `multi_label`
at `cls_threshold` 0.5 — the only mode in which it can decline.

## Budget, measured

`max_len` counts **word tokens** and truncates head-keep / tail-drop (`processor.py:408-411`);
the classification prefix is prepended *after* truncation, so label descriptions do not consume
the budget. Counted with a verbatim copy of gliner2's own splitter regex:

| input | median | max | over the 768 budget |
|---|---|---|---|
| digest | 438 | 495 | 0 / 36 |
| text | 1498 | 1655 | 33 / 36 |
| digest + text | 1938 | 2125 | 36 / 36 |

Two consequences, recorded in advance so they are not mistaken for findings later:

- The digest **always fits whole**. The text **never** does — it loses about half.
- Because the digest leads and truncation is head-keep, `G-both` is in practice *digest plus the
  first ~300 tokens of text*. That is what a naive implementation would do, so it is the honest
  arm, but it is not "digest and text together".
- `window_text` itself caps at 7000 chars keeping the NEWEST turns, so `G-text` is the earliest
  portion of the latest turns. Deterministic, and stated rather than silent.

## Run conditions

Study sidecar, 18 threads (`KELD_SIDECAR_MAX_THREADS`, `OMP/MKL/OPENBLAS/NUMEXPR_NUM_THREADS`),
`MALLOC_ARENA_MAX=2` retained — the arena cap is the memory control and raising threads without
it is what produced 6.4 GB RSS against a 2.6 GB working set. `max_len` 768. The production daemon
is stopped for the duration so no second model instance contends. Peak worker RSS read from
`/metrics` after the run.

The `lenstat.go` memory sweep was run at 2 threads and does **not** transfer to 18; its figures
are not quoted as if they did.

## Checks

- **Repeatability**: 5 windows re-run at the same thread count; labels must match.
- **Thread transfer**: 5 windows at 2 threads vs 18. Production runs at 2 and the study at 18;
  torch reduction order varies with thread count. If these diverge, the study number does not
  transfer to production, and that outranks everything else in this document.
- **Splitter drift**: the copied regex is asserted against gliner2's own on the real inputs.

## Decision rule

- **Dead** — no GLiNER2 arm beats `majority`. Prior: the LLM scored 33% against a 67% majority on
  the analogous extensive task. Route abandoned; go to text-derived entities.
- **Subordinate** — beats `majority`, not `rule`. The frame stays authoritative; reported as the
  seventh repetition of that pattern.
- **Alive** — a GLiNER2 arm beats `rule`.
- **Input design** — if `G-digest` is within 3 points of `G-both`, the digest is sufficient and
  window-text compression is retired. If `G-text` or `G-both` wins by more than 5 points,
  compression is worth building and the sentential / longest-entry-first policy becomes the next
  arm.
- **Gate value** — the gate is justified only if the negative control declines on >= 2/3 of
  `prose == 0` windows in `multi_label` mode.
- **Power escape** — N = 24 across 3 classes makes one window ~4 points; under ~8 points is
  noise. If the best GLiNER2 arm lands within +-8 points of `rule`, extend to 40 windows and
  re-label before concluding.

Stated plainly: this is a smoke test with a pre-registered kill condition. It can decisively kill
the route. It cannot decisively bless it.
