"""`S(t)` — the STRUCTURED FEATURE VECTOR: one row of the training corpus, computed on device.

    a reference series  ->  a fixed-width vector of numbers  ->  (later) Atlas

Design: `docs/superpowers/specs/2026-08-26-signal-embeddings-design.md`, step 2 of four. Keld
trains models centrally to predict what a piece of work will do next; this module computes the
`X` those models are trained on. Nothing here learns, nothing here publishes, and nothing here
loads a model — it is `store.rollup_window` + `turn_magnitude` + `dynamics` + `prior`, composed
into a vector.

## WHAT MAKES THIS DIFFERENT FROM EVERY OTHER PAYLOAD IN THIS PACKAGE

⚠️ **`workstreams.payload` IS THE WRONG INPUT AND IS DELIBERATELY NOT CALLED.** It emits, per
ALLOCATION dimension, only the DOMINANT value's `value`/`share`/`evidence`/`status`. That is a
presentation decision for a human reader — one line per dimension — and it throws away the
distribution, which is the only thing a model can learn a shape from. A window that was 51% Go
and one that was 51% Go / 49% Python publish identically there and must not here. So this module
reads `store.rollup_window` directly and keeps the whole per-level histogram.

⚠️ **`window.MIN_EVIDENCE` IS A LABEL ON A PUBLISHED ATTRIBUTION, NEVER A FILTER ON FEATURES.** A
sub-floor rollup is perfectly good input to a model even where it is not honest to publish as an
attribution — the floor exists so a reader is not told "this hour was Python" off one
observation, and a model is not a reader. Nothing here abstains, and the same reasoning applies
to `latency`'s `MIN_GAPS`: `fast_share` and the gap percentiles are computed at `min_gaps=1` and
the companion `n_gaps` dimension carries the confidence. The floors are not deleted; they are
simply not the question this file asks.

## THE SHELL LADDER IS DISJOINT

    [0,5m)   [5,20m)   [20,60m)   [60,240m)   [240m, session start)

⚠️ **NOT nested windows.** `5m` / `20m` / `60m` / `240m` each measured back from `t` counts the
same first five minutes four times, and the four blocks come out near-collinear: a model has to
undo the overlap before it can use any of them. Shells are decorrelated, cost exactly the same
(one `rollup_window` per shell, ~2 ms each), and **any nested window is recoverable by summing
shells** — a consumer that wants "the last hour" adds three columns. The reverse is not true, so
the disjoint form is the one that must be stored.

The outermost shell is open on the left and is clamped to the session's own first active bin (and
to the retention serving floor, whichever is later). Its `coverage` dimension says how much of
its nominal 240 minutes actually existed, so an empty outer shell on a young session is
distinguishable from one on a session that simply went quiet.

## CLOSED VOCABULARIES ARE FROZEN FROM `vocab.py`, NEVER FROM THE STORE

⚠️ This is the invariant the whole corpus rests on. A machine that has never run `pandoc` **must
still emit a zero in the `document` toolchain slot**. Derive the histogram slots from what a
store happens to contain and two machines produce vectors whose index *i* means different things,
and the corpus is silently incoherent — no error, no warning, just a model trained on noise. So
every histogram's slot order comes from a table in `vocab.py` (or from `workspace.vcs_of`'s own
closed return set), is sorted, and is frozen into `MANIFEST` at import time. `manifest()` is
exported precisely so a consumer can assert the alignment rather than trust it.

Each histogram carries a trailing `other` bucket. For the genuinely closed levels (`action`,
`vcs`) that bucket is unreachable today; it is there because the sidecar ships SEPARATELY from
the daemon and from Atlas, so a producer that gains a value must land it somewhere visible
rather than silently rescaling every other slot.

## OPEN-VOCABULARY LEVELS CONTRIBUTE SHAPE, NEVER IDENTITY

`file`, `dir`, `component`, `exe`, `verb`, `term`, `skill`, `branch`, `workspace`, `service` have
unbounded vocabularies — measured on one live store: `term` 1,334 distinct, `verb` 490, `exe`
466, `file` 687. One-hot is impossible and **hashing is forbidden**: a hash of an identity is a
fingerprint of that identity, which is exactly the thing that must not cross the wire.

So all 25 levels — closed and open alike — contribute the same five SHAPE STATISTICS, and the
open ones contribute nothing else:

    log1p(evidence)   how much happened at this level
    log1p(n_distinct) how many different things
    top1_share        how concentrated
    top3_mass         how concentrated, less brittle to a near-tie at the top
    norm_entropy      how evenly spread, scale-free in the number of values

None of the five can hold a value string. `term` — the one level read from message TEXT, which
has held real person names — therefore reaches this vector as five numbers and nothing else.

## THE NORMALISATION TRANSFORM IS PART OF THE SPEC, NOT OF THE TRAINING SCRIPT

⚠️ `log1p` versus raw is **not recoverable after the fact**, so it is fixed here and documented
per group rather than left to a caller:

  * **Shares stay raw.** Everything already in `[0, 1]` — histogram slots, `top1_share`,
    `top3_mass`, `norm_entropy`, `cache_ratio`, `error_rate`, `fast_share`, `coverage`,
    `turnover`, `decay`, `agrees`, `novel`, the one-hots — is emitted unchanged. The spec's
    volume arithmetic assumes int8 quantisation, which is only cheap because nearly every
    dimension is a share.
  * **Counts and durations are `log1p`.** Evidence totals, distinct counts, token counts,
    character counts, byte counts, turn counts, gap seconds, session age. All are unbounded and
    heavy-tailed (a single `Write` is 22 KB; a cache read is 257k tokens), so a raw count would
    dominate a vector of shares and would quantise to a single saturated bucket.
    ⚠️ **`n_distinct` is `log1p` too, and that is a deliberate departure from the spec's own
    wording**, which lists it beside four shares as if it were one. It is not: `term` reaches
    1,334 distinct on a real store, and an unbounded integer sitting next to `[0,1]` values is
    the exact failure the previous bullet exists to prevent.
  * **`concentration_shift` is a DIFFERENCE of two shares** and lives in `[-1, 1]`. Left raw.
  * **Signed values are not rescaled to `[0,1]`.** `departure` and `concentration_shift` keep
    their sign, because the direction is the signal.

## ABSENT IS NOT ZERO, AND THE VECTOR SAYS SO

The capture rows (`magnitude.CAPTURE_KINDS` — the token split, the per-role character counts,
the thinking-block incidence, the tool outcomes) exist ONLY where the transcript was ingested
under `KELD_CAPTURE=1`. A store without them is not a store where nothing was said.

**The choice made here is a companion PRESENCE FLAG, not NaN and not a mask array.** NaN does not
survive int8 quantisation, does not survive JSON, and every consumer would have to agree on how
to impute it; a separate mask doubles the payload to carry two bits. Two flags do the whole job:

  * `row.meta.capture_recorded` — 1.0 when this TRANSCRIPT's parse state was written under
    capture. It is a per-transcript fact (`ingest.capture_mode` fingerprints it into
    `parse_state`), so it is a row-level dimension rather than five identical per-shell ones.
    Every capture-derived slot reads 0.0 when it is 0.0, and **a consumer must not read those
    slots at all in that case.**
  * `<shell>.effort.cost_recorded` — `store.has_magnitudes`, scoped to the COST kinds. Per shell,
    because a shell holding no assistant turn genuinely has no cost to record even on a captured
    transcript. It is the same gate `magnitude.authored` already uses to tell a truthful
    "authored 0 bytes" from an honest "no record".

⚠️ The capture fingerprint's guarantee is per TRANSCRIPT, not per STORE — a dormant session keeps
its uncaptured rows for as long as it stays dormant. That is why the flag is computed from the
transcript's own parse state and not from the process's current `KELD_CAPTURE`.

## CAUSALITY

Every input is a prefix of what the machine knew at `t`, with ONE declared exception, which the
spec names as a leakage trap: `workspace.scan_workspace` is a whole-file pre-pass, so the
`workspace` / `repo` / `root_dir` resolution behind the `workspace`, `repo`, `vcs`, `file`,
`dir`, `component` and `lang` levels can be revised retroactively by a `CLAUDE.md` read later in
the session. Measured bound: retroactive reparses hit 0.7% of appends (53/7,571). The affected
groups are named in `RETROACTIVE_GROUPS` and the row carries `causal: False` for them, so a
training run can drop those columns rather than a reader rediscovering this.

`reconcile` is re-scoped per shell (`_rollup_at`, the pattern `blockdigest._rollup_at` uses and
for the identical reason): the STORED reconcile slot is whole-file, so reading it directly would
attribute a path using a declaration the shell never saw. `prior` is already half-open on the
right and is reused unchanged.

## THE SPEC VERSION RIDES EVERY ROW

⚠️ `FEATURE_SPEC_VERSION` is a module constant and is published on every payload, for
`ingest.terms_mode`'s exact reason: two spec versions must never be pooled by accident, and a
vector is the one artefact where that mistake is invisible — the widths can even match while
index 700 means two different things. `spec_sha` beside it is a digest of the ordered manifest
itself, so a slot inserted without a version bump is still caught.

## WHAT THIS MODULE MAY NOT DO

No raw text, no spans, no offsets, no identity strings — counts and shares only. `analyze.py`
(v1's frozen entry point) and `levels.events_for_turns` (the oracle's producer) are neither
imported nor touched.
"""
import datetime as dt
import hashlib
import math
import time

from app.analysis import COMPONENT_DEPTH
from app.analysis import blocks as blocks_mod
from app.analysis import latency, magnitude, prior as prior_mod, window
from app.analysis.dynamics import (DEFAULT_SIZER, DYNAMIC_DIMENSIONS, READINGS, STATUSES,
                                   dynamics_for)
from app.analysis.ingest import RECONCILE_SLOT, pending_in, session_of
from app.analysis.levels import LEVELS, quantize
from app.analysis.prior import PRIOR_DIMENSIONS
from app.analysis.reconcile import reconcile
from app.analysis.store import BIN_SECONDS
from app.analysis.vocab import ACTIONS, ARTIFACT_EXT, ARTIFACT_SKILL, EXT_LANG, TOOLCHAIN_EXE, \
    TOOL_ACTION

# ⚠️ BUMP THIS WHENEVER A SLOT MOVES, IS ADDED, OR CHANGES TRANSFORM. It rides every payload so
# two spec versions cannot be pooled, and `spec_sha` below catches the case where someone edits a
# vocabulary table and forgets. Same argument as `ingest.terms_mode` fingerprinting the terms
# pipeline's identity into `parse_state`: an unversioned corpus of vectors is not repairable
# after the fact, because the rows carry no record of what they meant.
FEATURE_SPEC_VERSION = 1

# The DISJOINT lookback ladder, in minutes back from the anchor. `None` means "to the session's
# first active bin" — the only open shell, and the reason `coverage` exists. See the module
# docstring on why these are shells and not nested windows.
SHELLS = (("s0", 0.0, 5.0),
          ("s1", 5.0, 20.0),
          ("s2", 20.0, 60.0),
          ("s3", 60.0, 240.0),
          ("s4", 240.0, None))

# The nominal length the open shell's `coverage` is measured against. It has no natural one, so
# it saturates at another 240 minutes: coverage 1.0 means "at least four hours of session existed
# before the 240-minute mark", which is the question a reader of that dimension is actually
# asking. Stated here rather than inlined because it is a definition, not a constant.
OPEN_SHELL_NOMINAL_S = 240.0 * 60.0

# The look-back the ROW-LEVEL blocks are computed over. `dynamics` and `prior` are once-per-row,
# not per-shell: both are already comparisons between two spans, so computing them five times
# would produce twenty-odd near-duplicate columns rather than new information. 60 minutes is the
# span `DEFAULT_SIZER` was measured on (+74.6 precision points against `FixedSizer(15)` over 25
# sessions / 111 transitions / 1,966 windows) and using a different one here would ship an
# arithmetic nobody measured.
ROW_SPAN_MINUTES = 60.0

# One call's batch bound, `blockdigest.DEFAULT_MAX_BLOCKS`' sibling and set by the same argument:
# a bound on the RESPONSE, never a loss, because the caller owns its own cursor and the next call
# continues where this one stopped. It is LARGER than 24 because the units differ — the spec's
# densest anchor grain is one row per non-empty 5-minute bin, ~72 rows on an active day, so 96
# clears a full day in one call while still bounding a machine that was off for a week to a
# visible, steady drain rather than putting the week on the wire at once.
DEFAULT_MAX_FEATURE_ROWS = 96

# --- the CLOSED vocabularies, frozen from vocab.py -------------------------------------------
#
# ⚠️ Every one of these is derived from a table, sorted, and never from a store. See the module
# docstring: this is the invariant that makes index `i` mean the same thing on two machines.

OTHER = "other"

# `action` — genuinely closed at 22 values by construction (every return path in
# `vocab.action_for` is a literal or a table lookup), which is also why `workstreams.INVENTORY`
# publishes it with no top-N cut at all.
ACTION_SLOTS = tuple(sorted(ACTIONS)) + (OTHER,)

# `artifact` — the ARTIFACT_EXT kinds plus the two that only `artifacts_for` can produce: `code`
# (any extension in `CODE_EXT` that no kind claimed) and `chart` (from ARTIFACT_SKILL's dataviz).
ARTIFACT_SLOTS = tuple(sorted(set(ARTIFACT_EXT) | {"code"} | set(ARTIFACT_SKILL.values()))) \
    + (OTHER,)

# `lang` — the distinct VALUES of EXT_LANG, not its keys. Several extensions map to one language.
LANG_SLOTS = tuple(sorted(set(EXT_LANG.values()))) + (OTHER,)

# `toolchain` — what a program is FOR, from TOOLCHAIN_EXE's keys.
TOOLCHAIN_SLOTS = tuple(sorted(TOOLCHAIN_EXE)) + (OTHER,)

# `tool` — the harness tool names, plus ONE bucket for every MCP tool. An MCP tool name is
# `mcp__<server>__<tool>` and the server half is frequently a uuid, so the level is open in
# practice and one-hotting it would both explode the width and publish an org's integrations by
# name. One bucket says "this shell used MCP" and the `mcp_tool` / `mcp_server` shape statistics
# say how much and how varied, which is what a model can use.
MCP = "mcp"
TOOL_SLOTS = tuple(sorted(TOOL_ACTION)) + (MCP, OTHER)

# `vcs` — `workspace.vcs_of`'s complete return set. Closed by inspection, not by measurement:
# the function has exactly four return paths. Enumerated rather than imported because it is a
# vocabulary and not a table, and because the wording of two of them is load-bearing
# ("reported, unverifiable" is a claim about EVIDENCE, not about a different VCS).
VCS_SLOTS = ("git", "git (reported, unverifiable)", "none", "unknown", OTHER)

# `model` — the FAMILY, not the id. Model ids carry a date suffix that rolls forward every few
# months (`claude-opus-4-5-20251101`), so one-hotting the id would give every slot a lifetime of
# one release and make a corpus spanning two of them incomparable with itself. The family is the
# fact that is stable and the fact that routing cares about.
MODEL_SLOTS = ("opus", "sonnet", "haiku", "fable", "synthetic", OTHER)

# level -> the histogram it contributes. Levels absent from this map contribute SHAPE ONLY. That
# is the whole of the open-vocabulary rule, expressed as data rather than as an if-statement.
HISTOGRAMS = (("action", ACTION_SLOTS), ("artifact", ARTIFACT_SLOTS), ("lang", LANG_SLOTS),
              ("toolchain", TOOLCHAIN_SLOTS), ("tool", TOOL_SLOTS), ("vcs", VCS_SLOTS),
              ("model", MODEL_SLOTS))

# The five shape statistics, in slot order. Applied to ALL of `levels.LEVELS`, closed levels
# included — a closed level's concentration is a fact its histogram does not state.
SHAPE_STATS = ("evidence", "n_distinct", "top1_share", "top3_mass", "norm_entropy")

# The effort dimensions, in slot order, grouped by what gates them. Order is the published
# contract; the grouping is what a consumer reads the gate off.
EFFORT_COST = ("request_tokens", "edit_bytes", "authoring_turns", "cost_recorded")
EFFORT_CAPTURE = ("tok_out", "tok_in_fresh", "tok_in_cached", "cache_ratio",
                  "say_user_chars", "say_user_echo_chars", "say_asst_chars",
                  "say_asst_think_blocks", "think_turn_share",
                  "tool_errors", "tool_result_chars", "error_rate", "human_prompts")
EFFORT_ALWAYS = ("fast_share", "n_gaps", "gap_p50_s", "gap_p90_s", "turns", "tool_calls",
                 "coverage", "minutes")
EFFORT_SLOTS = EFFORT_COST + EFFORT_CAPTURE + EFFORT_ALWAYS

# The dynamics fields, in slot order. `changed` is TWO slots and not one: it is a THREE-state
# answer (`True` / `False` / `None`), and collapsing `None` onto `False` would publish "we
# checked, nothing moved" for a comparison that could not be made — the single misreading the
# whole evidence-floor line of work exists to prevent. So it is emitted as
# (`changed_known`, `changed_true`).
DYNAMIC_SCALARS = ("turnover", "decay", "concentration_shift", "changed_known", "changed_true")

# The prior contrasts. `agrees` and `novel` are tri-state for `contrast`'s own reasons (an
# unattributed prior has no value to agree with; an empty prior makes everything trivially
# novel), but unlike `changed` their None case is already fully described by the prior's own
# `status` shape statistics, so they are emitted as a single 0/1 with None reading 0.0.
PRIOR_SCALARS = ("agrees", "departure", "novel")

# Positional / global. `hour` and `weekday` are cyclical, so they enter as a sin/cos pair rather
# than as a number where 23:00 and 00:00 sit maximally far apart.
POSITION_SLOTS = ("hour_sin", "hour_cos", "weekday_sin", "weekday_cos",
                  "session_age", "gap_since_last_turn", "in_block", "block_position")

# ⚠️ THE GROUPS WHOSE INPUTS ARE NOT A STRICTLY CAUSAL PREFIX, named so a training run can drop
# them rather than rediscover the trap. `workspace.scan_workspace` is a WHOLE-FILE pre-pass: a
# `CLAUDE.md` read at 17:00 re-resolves the 09:00 turns, and with them the `root_dir` every path
# is relative to. Measured bound: retroactive reparses hit 0.7% of appends (53/7,571), so the
# effect is small — but "small" is not "absent", and a leakage this quiet is worth a column list.
# `lang` is in here because `lang` rows exist ONLY through `reconcile`, which resolves against
# the resolved workspace root.
RETROACTIVE_LEVELS = ("workspace", "workspace_evidence", "repo", "repo_from_text",
                      "repo_mentioned", "vcs", "branch", "component", "dir", "file", "lang")


def _num(x, default=0.0):
    """A float, or `default` for anything that is not one. Every input here comes out of a store
    another process wrote; a None must read as the default rather than take a row down."""
    if x is None or isinstance(x, bool):
        return default
    try:
        return float(x)
    except (TypeError, ValueError):
        return default


def _log1p(x):
    """The count/duration transform. Clamped at 0 because `log1p` of a negative is a domain
    error and a negative count is a producer bug, not a reason to fail a row."""
    return math.log1p(max(0.0, _num(x)))


def model_family(ref):
    """A model id -> its FAMILY slot. Substring, in the order families were introduced, because
    an id is `claude-<family>-<major>-<minor>-<date>` and only the family half is stable.

    `<synthetic>` is Claude Code's own marker for a locally-generated assistant line (no API call
    was made), so it is a family rather than an `other`: a shell full of synthetic turns did not
    spend anything, which is a real and different fact from an unrecognised vendor id.
    """
    r = str(ref or "").lower()
    for fam in ("opus", "sonnet", "haiku", "fable"):
        if fam in r:
            return fam
    if "synthetic" in r:
        return "synthetic"
    return OTHER


def _slot_for(level, ref):
    """Which histogram slot one rollup entry lands in. `other` when the vocabulary does not know
    it — never a new slot, and never silently dropped (dropping would rescale every sibling)."""
    if level == "model":
        return model_family(ref)
    if level == "tool":
        r = str(ref or "")
        if r.startswith("mcp__") or r.startswith("mcp:"):
            return MCP
        return r if r in TOOL_ACTION else OTHER
    return str(ref or "")


def histogram(items, level, slots):
    """A rollup level's `[(ref, n), ...]` -> one share per slot, in `slots` order.

    NORMALISED by the level's own total, so the group is scale-free and the magnitude is carried
    separately by the shape statistics' `log1p(evidence)`. An empty level is all zeros — which is
    correct and is NOT the same as a level of one value at share 1.0, because `evidence` beside
    it distinguishes them.
    """
    idx = {s: i for i, s in enumerate(slots)}
    out = [0.0] * len(slots)
    total = sum(_num(n) for _ref, n in items)
    if total <= 0:
        return out
    for ref, n in items:
        out[idx.get(_slot_for(level, ref), idx[OTHER])] += _num(n) / total
    return out


def shape(items):
    """A rollup level's `[(ref, n), ...]` -> the five SHAPE STATISTICS, in `SHAPE_STATS` order.

    THE ONLY THING AN OPEN-VOCABULARY LEVEL CONTRIBUTES, and the reason a level with 1,334
    distinct values costs five dimensions rather than 1,334 or a hash. Not one of the five can
    hold a value string; the function never touches `ref` at all except to count how many there
    were.

    `norm_entropy` is Shannon entropy over the shares divided by `log(n_distinct)`, so it is
    0.0 for a single value (perfectly concentrated) and 1.0 for a perfectly flat level whatever
    its cardinality. Without the normalisation it would grow with the number of distinct values
    and would say "varied" about a level that is merely wide, which is the `programs`-vs-`branch`
    confusion `dynamics` already had to rule out by measurement.
    """
    counts = [_num(n) for _ref, n in items if _num(n) > 0]
    total = sum(counts)
    if not counts or total <= 0:
        return [0.0] * len(SHAPE_STATS)
    counts.sort(reverse=True)
    n = len(counts)
    ent = -sum((c / total) * math.log(c / total) for c in counts)
    return [_log1p(total),
            _log1p(n),
            counts[0] / total,
            sum(counts[:3]) / total,
            0.0 if n < 2 else ent / math.log(n)]


def _rollup_at(store, path, session, lo, hi):
    """`[lo, hi)` of one session -> a rollup. THE one way this module computes one.

    The stored reconcile rows are EXCLUDED and RECOMPUTED at this shell's own scope, which is
    `blockdigest._rollup_at`'s rule and is here for the identical reason: `reconcile` resolves a
    prose path against every path a tool DECLARED, so its answer depends on which declarations
    are in scope. The store holds the WHOLE FILE's reconciliation, and slicing that by timestamp
    would attribute a path using a declaration the shell never saw — a leak of the future into a
    causal feature, and a silent one.

    ⚠️ It is a five-line composition duplicated against `store.window_rows` / `reconcile` /
    `window.rollup`, never against `blockdigest`'s private helper. That is the same trade
    `blockdigest`'s own docstring records: what is duplicated is a composition, not a definition,
    so a change to any of those primitives still reaches both paths. Importing across would tie
    the feature path's lifetime to v2's block path, and the spec's rule is that each new thing
    lives in a module that can be deleted whole.

    Affordable because `pending` is tiny — 104-859 entries for 2-47 MB transcripts, and
    reconciling the whole of one costs 0-3 ms.
    """
    rows = store.window_rows(session, lo, hi, exclude_slots=(RECONCILE_SLOT,))
    recon_rows, _stats = reconcile(pending_in(store, path, lo, hi), COMPONENT_DEPTH)
    return window.rollup(rows + recon_rows)


def _mag_sum(store, session, lo, hi, kind):
    """`(total, n_turns)` for one magnitude kind. Two facts, never one: a 22 KB `Write` and fifty
    400 B fixes are the same sum and different work, which is precisely what `magnitude`'s own
    study measured (22x-87x p10->p90 in bytes at a FIXED edit count)."""
    rows = store.turn_magnitudes(session, lo, hi, kind=kind)
    return sum(_num(v) for _ts, v in rows), len(rows)


def effort(store, session, lo, hi, tool_calls, coverage):
    """The per-shell effort group, in `EFFORT_SLOTS` order.

    ⚠️ **It takes no `captured` argument and never drops a slot.** The width of a row is fixed by
    the manifest, so an uncaptured transcript emits the capture slots as 0.0 and the row's
    `capture_recorded` flag is what says they must not be read. Shortening the group instead
    would make a vector's width a function of the machine, which is the same incoherence rule 2
    forbids one level down.

    ⚠️ **No abstention anywhere.** `latency.tempo` and `latency.percentiles` withhold their
    numbers below `MIN_GAPS` (3) and `magnitude.authored` withholds below `has_magnitudes`,
    because both are PUBLISHED to a human who would otherwise read a 0/0 as a measurement. This
    is not that: `percentiles` is called at `min_gaps=1` and the companion `n_gaps` dimension
    carries the confidence, which is the same call the module docstring makes about
    `MIN_EVIDENCE`. The floors are not deleted — they are simply not this file's question.

    `cost_recorded` and the row's `capture_recorded` are what keep that honest. A zero in
    `tool_errors` means "no tool call failed" only where capture ran; otherwise it means the
    question was never asked, and the flag is the only thing that says which.
    """
    req, _ = _mag_sum(store, session, lo, hi, magnitude.REQUEST_TOKENS)
    edit_vals = store.turn_magnitudes(session, lo, hi, kind=magnitude.EDIT_BYTES)
    auth = magnitude.authored((v for _ts, v in edit_vals),
                              recorded=store.has_magnitudes(session, lo, hi))

    tok_out, _ = _mag_sum(store, session, lo, hi, magnitude.TOK_OUT)
    tok_fresh, _ = _mag_sum(store, session, lo, hi, magnitude.TOK_IN_FRESH)
    tok_cached, _ = _mag_sum(store, session, lo, hi, magnitude.TOK_IN_CACHED)
    ctx = tok_cached + tok_fresh
    say_user, n_user = _mag_sum(store, session, lo, hi, magnitude.SAY_USER)
    say_echo, _ = _mag_sum(store, session, lo, hi, magnitude.SAY_USER_ECHO)
    say_asst, _ = _mag_sum(store, session, lo, hi, magnitude.SAY_ASST)
    # ⚠️ INCIDENCE, NEVER LENGTH. `magnitude.SAY_THINK` (`say_asst_think`) exists and is NEVER
    # WRITTEN: every thinking block a platform writes carries a signature and an EMPTY string
    # (measured 10,741 blocks, 0 of nonzero length), and `_aggregate_mag` drops zeros. A feature
    # built on thinking LENGTH would read all-zero on every machine forever and would look like a
    # signal that had gone quiet. `SAY_THINK_BLOCKS` is the fact that IS in the data: did the
    # turn think, and how many times.
    think_blocks, n_think_turns = _mag_sum(store, session, lo, hi, magnitude.SAY_THINK_BLOCKS)
    errs, _ = _mag_sum(store, session, lo, hi, magnitude.TOOL_ERRORS)
    res_chars, _ = _mag_sum(store, session, lo, hi, magnitude.TOOL_RESULT_CHARS)

    times = store.turn_times(session, lo, hi, exclude_slots=(RECONCILE_SLOT,))
    tmp = latency.tempo(times, min_gaps=1)
    pcts = latency.percentiles(times, min_gaps=1)
    turns = len(times)

    return [
        # --- cost, gated by cost_recorded ---
        _log1p(req),
        _log1p(auth.nbytes),
        _log1p(auth.turns),
        1.0 if auth.status == magnitude.AUTHORED_STATUSES[0] else 0.0,
        # --- capture, gated by row.meta.capture_recorded ---
        _log1p(tok_out),
        _log1p(tok_fresh),
        _log1p(tok_cached),
        (tok_cached / ctx) if ctx > 0 else 0.0,
        _log1p(say_user),
        _log1p(say_echo),
        _log1p(say_asst),
        _log1p(think_blocks),
        (n_think_turns / turns) if turns else 0.0,
        _log1p(errs),
        _log1p(res_chars),
        (errs / tool_calls) if tool_calls > 0 else 0.0,
        _log1p(n_user),
        # --- always available ---
        _num(tmp.fast_share),
        _log1p(tmp.n_gaps),
        _log1p(pcts.p50),
        _log1p(pcts.p90),
        _log1p(turns),
        _log1p(tool_calls),
        float(coverage),
        _log1p(max(0.0, hi - lo) / 60.0),
    ]


def shell_bounds(at, session_start):
    """The five disjoint intervals, oldest-last, as `(name, lo, hi, coverage)`.

    Clamped to `session_start` on BOTH sides: a shell entirely before the session is empty with
    coverage 0.0, and one straddling the start is truncated with a coverage below 1.0. That is
    what stops a young session's outer shells from reading as "four hours of silence".
    """
    out = []
    for name, back_lo, back_hi in SHELLS:
        hi = at - back_lo * 60.0
        lo = session_start if back_hi is None else at - back_hi * 60.0
        lo, hi = max(lo, session_start), max(hi, session_start)
        nominal = OPEN_SHELL_NOMINAL_S if back_hi is None else (back_hi - back_lo) * 60.0
        cov = 0.0 if nominal <= 0 else min(1.0, max(0.0, hi - lo) / nominal)
        out.append((name, lo, hi, cov))
    return out


def _shell_values(store, path, session, lo, hi, cov):
    """One shell's ~267 numbers, in manifest order: histograms, then shape, then effort."""
    rl = _rollup_at(store, path, session, lo, hi) if hi > lo else {}
    vals = []
    for level, slots in HISTOGRAMS:
        vals.extend(histogram(rl.get(level) or [], level, slots))
    for level in LEVELS:
        vals.extend(shape(rl.get(level) or []))
    tool_calls = sum(_num(n) for _ref, n in (rl.get("tool") or []))
    vals.extend(effort(store, session, lo, hi, tool_calls, cov))
    return vals


def _one_hot(value, vocab):
    """A closed-vocabulary string -> its indicator. An unrecognised value is ALL ZEROS rather
    than an `other` slot, deliberately: `dynamics.STATUSES` and `READINGS` are frozen contracts
    mirrored Go-side, so a value outside them is version skew and must be visible as a row of
    zeros rather than absorbed into a bucket that says nothing."""
    return [1.0 if value == v else 0.0 for v in vocab]


def _dynamics_values(dims):
    """The dynamics group, in manifest order. `dims` is `dynamics.compare`'s output."""
    vals = []
    for name, _level, _floor in DYNAMIC_DIMENSIONS:
        d = dims.get(name) or {}
        changed = d.get("changed")
        vals.extend([_num(d.get("turnover")), _num(d.get("decay")),
                     _num(d.get("concentration_shift")),
                     0.0 if changed is None else 1.0,
                     1.0 if changed is True else 0.0])
        vals.extend(_one_hot(d.get("status"), STATUSES))
        vals.extend(_one_hot(d.get("reading"), READINGS))
    return vals


def _prior_values(dims):
    """The prior group, in manifest order. `dims` is `prior.compare`'s output.

    CONTRAST, NEVER FALLBACK — unchanged here. A None `agrees`/`novel` reads 0.0, which is
    "not asserted", and the prior's own `status` is not smuggled in as a value: the block never
    supplies a value the window lacked, and neither does this.
    """
    vals = []
    for name, _level, _floor in PRIOR_DIMENSIONS:
        p = dims.get(name) or {}
        vals.extend([1.0 if p.get("agrees") is True else 0.0,
                     _num(p.get("departure")),
                     1.0 if p.get("novel") is True else 0.0])
    return vals


def _block_position(store, session, at):
    """`(in_block, position)` — where `at` sits inside the block of work containing it.

    A block is 20 minutes at most and its boundaries are arithmetic (a cap or a 15-minute
    silence), so the POSITION is what carries information: "four minutes into a block" and
    "nineteen minutes into a block" are different states of the same work, and the second is
    about to end whatever it is doing. `in_block` is separate because `at` can legitimately fall
    in dead air, where a position of 0.0 would be a claim rather than an absence.
    """
    bins = blocks_mod.active_bins(store, session)
    if not bins:
        return 0.0, 0.0
    lo = math.floor(float(bins[0]) / BIN_SECONDS) * BIN_SECONDS
    hi = math.ceil(float(bins[-1] + BIN_SECONDS) / BIN_SECONDS) * BIN_SECONDS
    for b in blocks_mod.cut(store, session, float(lo), float(hi)):
        if float(b.start) <= at < float(b.end):
            span = float(b.end) - float(b.start)
            return 1.0, 0.0 if span <= 0 else (at - float(b.start)) / span
    return 0.0, 0.0


def _position_values(store, session, at, session_start, last_turn):
    """The positional group, in `POSITION_SLOTS` order.

    ⚠️ HOUR AND WEEKDAY ARE LOCAL, not UTC, and that is the deliberate reading: the fact worth
    learning is "this is 09:00 for the person", which is the same fact in every timezone, whereas
    09:00 UTC is a different part of the working day on every machine. A corpus pooled across
    timezones on UTC hours would smear the one daily cycle it contains.
    """
    when = dt.datetime.fromtimestamp(at)
    h = 2.0 * math.pi * (when.hour + when.minute / 60.0) / 24.0
    w = 2.0 * math.pi * when.weekday() / 7.0
    in_block, pos = _block_position(store, session, at)
    return [math.sin(h), math.cos(h), math.sin(w), math.cos(w),
            _log1p(max(0.0, at - session_start)),
            _log1p(max(0.0, at - last_turn)) if last_turn is not None else 0.0,
            in_block, pos]


def _build_manifest():
    """The ordered slot names. Built ONCE at import and frozen; `manifest()` hands out the tuple.

    Names are dot-separated and stable: `<shell>.hist.<level>.<slot>`, `<shell>.shape.<level>.
    <stat>`, `<shell>.effort.<name>`, `row.dyn.<dimension>.<field>`, `row.prior.<dimension>.
    <field>`, `row.pos.<field>`, `row.meta.<field>`. A consumer that stores the manifest once and
    asserts it against `spec_sha` needs nothing else to align two machines' rows.
    """
    names = []
    for shell, _lo, _hi in SHELLS:
        for level, slots in HISTOGRAMS:
            names.extend(f"{shell}.hist.{level}.{s}" for s in slots)
        for level in LEVELS:
            names.extend(f"{shell}.shape.{level}.{s}" for s in SHAPE_STATS)
        names.extend(f"{shell}.effort.{s}" for s in EFFORT_SLOTS)
    for name, _level, _floor in DYNAMIC_DIMENSIONS:
        names.extend(f"row.dyn.{name}.{s}" for s in DYNAMIC_SCALARS)
        names.extend(f"row.dyn.{name}.status.{s}" for s in STATUSES)
        names.extend(f"row.dyn.{name}.reading.{s}" for s in READINGS)
    for name, _level, _floor in PRIOR_DIMENSIONS:
        names.extend(f"row.prior.{name}.{s}" for s in PRIOR_SCALARS)
    names.extend(f"row.pos.{s}" for s in POSITION_SLOTS)
    names.append("row.meta.capture_recorded")
    return tuple(names)


MANIFEST = _build_manifest()
DIMS = len(MANIFEST)

# A digest of the ORDERED manifest. Rides every payload beside `FEATURE_SPEC_VERSION` so a slot
# inserted, renamed or reordered WITHOUT a version bump is still caught — which is the failure
# mode a version constant alone cannot catch, because the person who edits `vocab.EXT_LANG` is
# not thinking about this file. Truncated to 16 hex characters: it is a change detector, not a
# security primitive, and a collision costs a missed warning rather than a wrong row.
SPEC_SHA = hashlib.sha256("\n".join(MANIFEST).encode("utf-8")).hexdigest()[:16]

# The groups whose columns are not a strictly causal prefix (see the module docstring). Computed
# from the manifest rather than restated, so a level joining `RETROACTIVE_LEVELS` needs no second
# edit and cannot go stale.
RETROACTIVE_GROUPS = tuple(
    n for n in MANIFEST
    if any(f".hist.{lv}." in n or f".shape.{lv}." in n for lv in RETROACTIVE_LEVELS))


def manifest():
    """The ordered slot names, one per dimension. EXPORTED because rule 2 of the spec is only
    enforceable if a consumer can check it: a corpus builder asserts this against the manifest it
    already holds instead of trusting that two sidecar builds agree."""
    return MANIFEST


def session_start(store, session, floor=None):
    """The session's first active bin, clamped to the retention serving floor.

    The floor matters and is not decoration: `event` rows expire at 400 days and `term` at 90, so
    an old session's outer shell would otherwise be measured against a start whose rows no longer
    exist and would report a confident emptiness. Clamping makes `s4.effort.coverage` say the
    truth instead — the shell is short because the history was pruned.
    """
    bins = blocks_mod.active_bins(store, session)
    if not bins:
        return None
    start = math.floor(float(bins[0]) / BIN_SECONDS) * BIN_SECONDS
    return start if floor is None else max(start, float(floor))


def _captured(store, path):
    """Was this TRANSCRIPT ingested with capture on? Read off its own `parse_state`, never off
    the process's current `KELD_CAPTURE`.

    ⚠️ The two disagree routinely and the difference is the whole point: `ingest.capture_mode`
    fingerprints the setting into the parse state, so a change forces a reparse — but only for
    sessions that see another append. Flip `KELD_CAPTURE=1` and a dormant transcript keeps its
    uncaptured rows for as long as it stays dormant, while the environment says capture is on.
    Reading the environment here would stamp `capture_recorded: 1` on rows that hold none of it,
    which is precisely the incoherent-corpus failure the flag exists to prevent.
    """
    return (store.parse_state(path) or {}).get("capture") == "1"


def features_at(store, path, at, floor=None, sizer=None, captured=None, start=None):
    """ONE ROW: `S(t)` at instant `at`, as `{"at", "values", ...}`.

    `values` is `DIMS` floats in `manifest()` order and nothing else — no strings, no identity,
    no offsets. The row's own metadata (`session`, `at`, the spec identity) rides beside it, not
    inside the vector.

    `floor`, `sizer`, `captured` and `start` are injectable for the same reason `blockdigest`'s
    are: a study reproduces this exact arithmetic without a clock or an environment, and a batch
    caller resolves the per-transcript facts once instead of per row. Production passes none of
    them.

    Returns `None` for a session the store has never seen an active bin for — an honest "there is
    nothing to characterise", never a row of zeros, which would enter a training set as a real
    observation of nothing happening.
    """
    session = session_of(path)
    at = quantize(float(at))
    start = session_start(store, session, floor) if start is None else float(start)
    if start is None or at <= start:
        return None
    captured = _captured(store, path) if captured is None else bool(captured)

    vals = []
    for _name, lo, hi, cov in shell_bounds(at, start):
        vals.extend(_shell_values(store, path, session,
                                  quantize(lo), quantize(hi), cov))

    # Row-level blocks. `dynamics_for` is the same call `/analyze` and `/blocks` make, so the
    # measured `DEFAULT_SIZER` seam is exercised in production rather than only in tests.
    dyn = dynamics_for(store, session, at, ROW_SPAN_MINUTES,
                       sizer=sizer if sizer is not None else DEFAULT_SIZER, floor=floor)
    vals.extend(_dynamics_values(dyn.get("dimensions") or {}))

    # The prior is cut at the WINDOW's start, half-open on the right: `[session start, w_lo)`
    # against `[w_lo, at)`. Causal by construction and reused unchanged — see `prior.py`, whose
    # own correction established that any other cut puts the window inside its own prior.
    w_lo = quantize(max(start, at - ROW_SPAN_MINUTES * 60.0))
    win_rl = _rollup_at(store, path, session, w_lo, at) if at > w_lo else {}
    prior_rl = _rollup_at(store, path, session, start, w_lo) if w_lo > start else {}
    vals.extend(_prior_values(prior_mod.compare(win_rl, prior_rl)))

    last = store.turn_times(session, start, at, exclude_slots=(RECONCILE_SLOT,))
    vals.extend(_position_values(store, session, at, start, last[-1] if last else None))
    vals.append(1.0 if captured else 0.0)

    if len(vals) != DIMS:                       # a manifest/producer drift is never silent
        raise AssertionError(f"features produced {len(vals)} values, manifest has {DIMS}")
    return {"at": float(at), "session": session, "session_start": float(start),
            "capture_recorded": captured, "values": [float(v) for v in vals]}


def features(store, path, ats, floor=None, sizer=None, max_rows=None):
    """`POST /features`' whole answer: `{"rows": [...], "dims", "feature_spec", "spec_sha", ...}`.

    One row per anchor in `ats`, in the order given, skipping any the store cannot characterise.
    The per-transcript facts — the capture fingerprint, the session start, the serving floor —
    are resolved ONCE here rather than per row, because they are properties of the transcript and
    re-reading them per anchor would make a batch of 30 rows 30 times more expensive in the one
    part that does not scale with the anchor.

    The manifest is NOT returned by default: it is `DIMS` strings, an order of magnitude more
    bytes than the vectors it describes, and a caller needs it once per build rather than once
    per call. `spec_sha` is what makes that safe — a caller caches the manifest against the sha
    and re-fetches only when it moves.
    """
    session = session_of(path)
    floor = store.serving_floor() if floor is None else floor
    start = session_start(store, session, floor)
    captured = _captured(store, path)
    rows = []
    for at in ats:
        if max_rows is not None and len(rows) >= max_rows:
            break
        if start is None:
            break
        row = features_at(store, path, at, floor=floor, sizer=sizer,
                          captured=captured, start=start)
        if row is not None:
            rows.append(row)
    return {"feature_spec": FEATURE_SPEC_VERSION, "spec_sha": SPEC_SHA, "dims": DIMS,
            "session": session, "rows": rows,
            "generated_at": time.time()}
