"""Per-window rollup: reference events -> counts and shares, by level.

Pure aggregation over rows shaped like `levels.events_for_turns` output (or any subset of them
sliced into a window by a caller) — no I/O, no pandas. A window is a few thousand rows;
`collections.Counter` is the entire performance argument pandas would have made here, and this
package stays pandas-free (see `app/analysis/__init__.py`).
"""
import collections


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
MIN_EVIDENCE = 5


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
    items = rl.get(level) or []
    if not items:
        return None, 0.0, 0
    total = sum(n for _, n in items)
    value, top = items[0]
    share = top / total
    tied = len(items) > 1 and items[1][1] == top
    attributed = share >= floor and total >= min_evidence and not tied
    return (value if attributed else None), share, int(total)
