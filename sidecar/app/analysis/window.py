"""Per-window rollup: reference events -> counts and shares, by level.

Pure aggregation over rows shaped like `levels.events_for_turns` output (or any subset of them
sliced into a window by a caller) — no I/O, no pandas. A window is a few thousand rows;
`collections.Counter` is the entire performance argument pandas would have made here, and this
package stays pandas-free (see `app/analysis/__init__.py`).
"""
import collections
import math


def rollup(rows):
    """Per level, (ref, total) descending. Counter rather than pandas: the package must stay
    dependency-light, and a window is a few thousand rows.

    Ties are broken alphabetically on `ref` rather than left to `Counter.most_common`'s
    insertion-order default. This is a DELIBERATE choice, not a verbatim match: the pandas
    rollup this replaces sorted a `groupby("ref").n.sum()` Series with `.sort_values()`, whose
    underlying `argsort` is not guaranteed stable, so which of several equal-count refs came
    first there was an accident of array layout, not a decision anyone made (confirmed against
    the real corpus — e.g. two tools tied at count 6 came out in a NON-alphabetical order).
    Reproducing that exact accident pandas-free would mean re-implementing numpy's sort
    internals for no semantic gain; alphabetical is at least reproducible and independent of
    transcript encounter order. Dominance itself (`dominant`, below) does not depend on this
    order — a tie for the top spot is unattributed either way.
    """
    by = collections.defaultdict(collections.Counter)
    for r in rows:
        if r[5] != "ref":
            continue
        by[r[6]][r[7]] += r[8]
    return {lv: sorted(c.items(), key=lambda kv: (-kv[1], kv[0])) for lv, c in by.items()}


def min_evidence_for(floor=0.5, alpha=0.05):
    """The smallest n at which a UNANIMOUS window of n observations is distinguishable from
    `floor` being the whole truth, at significance `alpha`.

    This is `MIN_EVIDENCE`'s derivation, executable, so the constant below is the OUTPUT of the
    argument rather than a number typed beside it. `ceil(log(alpha) / log(floor))`: the first n
    for which `floor ** n <= alpha`.

    Its arguments are the share floor and the significance level. THERE IS NO DURATION
    PARAMETER, and that absence is the whole finding of the slice work — see the note under
    MIN_EVIDENCE.
    """
    if not 0.0 < floor < 1.0:
        raise ValueError(f"floor must be in (0, 1): {floor!r}")
    if not 0.0 < alpha < 1.0:
        raise ValueError(f"alpha must be in (0, 1): {alpha!r}")
    return max(1, math.ceil(math.log(alpha) / math.log(floor)))


# The smallest number of observations at which a window may be attributed at all.
#
# DERIVED, not chosen. The existing 0.50 share floor is a claim about a POPULATION ("more than
# half this hour's evidence points one way"), tested against a SAMPLE. Take the floor itself as
# the null hypothesis — the top value's true share is exactly 0.5 — and the probability of a
# window of n observations coming out unanimous by chance is 0.5**n: 0.50 at n=1, 0.25, 0.125,
# 0.0625, and 0.031 at n=5. Five is therefore the first count at which even a PERFECT window
# (share=1.0) is distinguishable from a coin flip at the conventional 5%; below it no share,
# however high, says anything about the hour. That makes 5 the smallest floor that is a
# consequence of the 0.50 already in the code rather than a second arbitrary number beside it.
#
# Measured cost on the 572-window sample in ~/keld/refseries-context/workstreams.ndjson (4004
# dimension slots, 2927 attributed today): 347 slots become unattributed — 11.9% of what is
# attributed, touching 207 of 572 windows. 330 of those 347 are currently published at
# share=1.0, and 129 of them rest on a SINGLE observation. That is the number this exists for:
# `evidence` is dropped on the way to the published enrichment (see
# internal/agent/enrich/sidecar/workstreams.go), so downstream cannot tell one observation from
# five hundred, and a fresh session's first prompt in /tmp would otherwise publish `project` at
# confidence 1.0.
#
# ---------------------------------------------------------------------------------------------
# DOES IT HAVE TO CHANGE FOR A SHORTER SLICE? No. It is the SAME CONSTANT, and it is a constant
# for a reason, not by omission.
#
# `/analyze` gained dynamics: a recent SLICE of minutes read against an hour-long baseline. The
# plan carried a warning that this constant "was DERIVED for a 60-minute window" and that a
# shorter slice would need a smaller floor or nearly every slice would report unattributed. Read
# the derivation above again: DURATION APPEARS NOWHERE IN IT. The claim being tested is "could
# this unanimity have come from a coin", and the answer depends only on how many times the coin
# was flipped. Five observations are five observations whether they arrived over five minutes or
# five hours. Time enters only through evidence DENSITY, which is a property of the machine's
# work, not of the test.
#
# So a duration-scaled floor would not be a generalisation of this argument, it would be a
# different and worse one: it would make the significance of a published attribution a function
# of the slice length while `value` and `share` look identical either way — and `evidence` is
# dropped before publish, so no reader could tell a 3%-confident claim from a 50%-confident one.
# That is precisely the defect this constant was introduced to fix, reintroduced through the
# back door. A duration-scaled floor is therefore not merely unnecessary; it is forbidden, and
# `min_evidence_for` above takes no duration so that it cannot be written by accident.
#
# The warning's premise was arithmetically right and materially wrong. A 5-minute slice does
# carry about a sixth of an hour's evidence — measured on the frozen corpus, the median
# `workspace` evidence per window falls 130 -> 20 from 60 to 5 minutes, almost exactly a sixth.
# But a sixth of 130 is 20, which is FOUR TIMES this floor. Agentic transcripts are dense: a
# five-minute slice of real work is dozens of tool calls, not a handful of turns.
#
# MEASURED over 20,000 windows (4000 seeded anchor prompts x 5 slice lengths) across 55
# transcripts / 542 MB of ~/keld/refseries-context/frozen-corpus, by scripts/evidence_floor.py;
# durable output in ~/keld/refseries-context/dynamics/. Attribution rate at THIS floor, per
# allocation dimension — never pooled, because pooling averages a dense dimension with an empty
# one and describes neither:
#
#                    5m     10m     15m     30m     60m
#     project     87.0%   92.8%   94.9%   96.8%   97.9%
#     branch      86.7%   92.5%   94.2%   95.7%   95.8%
#     model       84.1%   90.8%   93.2%   95.3%   96.8%
#     output_type 54.6%   65.9%   71.0%   78.3%   85.5%
#     language    51.9%   63.0%   68.0%   74.4%   80.8%
#     workflow    19.5%   23.4%   25.9%   32.9%   41.4%
#     tooling      2.7%    6.8%   11.6%   19.3%   26.4%
#
# "Nearly every slice unattributed" is refuted for the three dimensions that carry most of the
# spend: a 5-minute slice attributes `project` 87% of the time. What the floor actually costs at
# 5 minutes, against the counterfactual of no evidence floor at all, is 424 of the 3902
# `project` slots a floor of 1 would attribute (10.9%), against 70 of 3984 (1.8%) at 60 minutes. Buying those back by dropping the floor to 1 gains
# 13.5 pooled points and takes the probability that an attribution is a coin flip from 0.031 to
# 0.50. That trade is not close.
#
# And for the dimensions that ARE mostly unattributed at 5 minutes, the floor is not the
# obstacle: `tooling` is unattributed 97.3% of the time there, of which 77.8 points is `absent`
# — no evidence at that level whatsoever — versus 19.0 points `thin`. Its `thin` share is 19-23%
# at EVERY slice length, and its `absent` share is still 50.3% at 60 minutes. No floor, and no
# slice length, converts an empty level into an attributed one. That is why `attribution` below
# reports WHICH of the four it was: a dynamics comparison that read `absent` as "the dominant
# value changed" would report a context switch out of a level that never fired.
MIN_EVIDENCE = min_evidence_for(0.5, 0.05)   # == 5

# The four ways a level can fail to have a dominant value, plus the one way it can succeed. They
# are FOUR DIFFERENT FACTS and only one of them is about the floor:
#   absent      — no evidence at this level at all. Not a small number; no number. A level can
#                 simply never have fired (`bin` is sparse by design and a missing bin is not a
#                 zero), and no slice length fixes it.
#   thin        — some evidence, fewer than `min_evidence` observations. The floor's own case.
#   tie         — the top two values are level. Two things claim the window, not one.
#   no_majority — enough evidence, top share below `floor`. The work was genuinely mixed.
REASONS = ("attributed", "absent", "thin", "tie", "no_majority")

Attribution = collections.namedtuple("Attribution", "value share evidence reason")


def attribution(rl, level, floor=0.5, min_evidence=MIN_EVIDENCE):
    """`dominant`, plus WHY — see `REASONS`. `dominant` is the three-field projection of this.

    Precedence when more than one condition holds: `absent`, then `thin`, then `tie`, then
    `no_majority`. Evidence outranks shape deliberately. One observation each for two values is
    not a genuinely divided window, it is two observations; calling that `tie` would tell a
    reader the work was split when the truth is that almost nothing was counted, and a dynamics
    metric built on that reading would score noise as change.
    """
    items = rl.get(level) or []
    if not items:
        return Attribution(None, 0.0, 0, "absent")
    total = sum(n for _, n in items)
    value, top = items[0]
    share = top / total
    if total < min_evidence:
        reason = "thin"
    elif len(items) > 1 and items[1][1] == top:
        reason = "tie"
    elif share < floor:
        reason = "no_majority"
    else:
        reason = "attributed"
    return Attribution(value if reason == "attributed" else None, share, int(total), reason)


def dominant(rl, level, floor=0.5, min_evidence=MIN_EVIDENCE):
    """The value owning this window at `level`, or None — plus the share and total evidence seen
    either way, so an unattributed window is still visible rather than silently empty.

    `rl` is the ROLLUP (the output of `rollup(rows)`), not the raw rows: later production code
    computes the rollup once and asks it several questions, so that shape is load-bearing here.

    Three distinct ways a level fails to have a dominant value:
      - the top value's share is below `floor` — a bucket holding under half the evidence is not
        what the hour was about (0.5 is deliberate, same reasoning as `workstreams.ALLOCATION`'s
        floor);
      - the window holds fewer than `min_evidence` observations at this level. A share is a
        ratio, and a ratio over one observation is 1.0 by construction; see MIN_EVIDENCE above
        for why 5 and what it costs. This is a SEPARATE condition from the share floor, not a
        stricter version of it: they answer "did one value win" and "was there anything to win",
        and a window can fail either alone;
      - the top two values are TIED. A tie is unattributed rather than an arbitrary pick: a
        multi-label event double-counts spend across both labels, and silently choosing a winner
        among near-equals is the plausible-wrong-number failure this work hit roughly twenty
        times. A tie at or above the floor is exactly the case the floor's reasoning already
        covers — two things are claiming the window, not one — so it is checked independently of
        the floor comparison rather than folded into it.

    In all three the share and total are still returned. Withholding the VALUE is the whole of
    the change; hiding the measurement would make an unattributed window indistinguishable from
    an empty one, which is the property this function's second and third return values exist to
    preserve.
    """
    a = attribution(rl, level, floor, min_evidence)
    return a.value, a.share, a.evidence
