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


def dominant(rl, level, floor=0.5):
    """The value owning this window at `level`, or None — plus the share and total evidence seen
    either way, so an unattributed window is still visible rather than silently empty.

    `rl` is the ROLLUP (the output of `rollup(rows)`), not the raw rows: later production code
    computes the rollup once and asks it several questions, so that shape is load-bearing here.

    Two distinct ways a level fails to have a dominant value:
      - the top value's share is below `floor` — a bucket holding under half the evidence is not
        what the hour was about (0.5 is deliberate, same reasoning as `workstreams.ALLOCATION`'s
        floor);
      - the top two values are TIED. A tie is unattributed rather than an arbitrary pick: a
        multi-label event double-counts spend across both labels, and silently choosing a winner
        among near-equals is the plausible-wrong-number failure this work hit roughly twenty
        times. A tie at or above the floor is exactly the case the floor's reasoning already
        covers — two things are claiming the window, not one — so it is checked independently of
        the floor comparison rather than folded into it.
    """
    items = rl.get(level) or []
    if not items:
        return None, 0.0, 0
    total = sum(n for _, n in items)
    value, top = items[0]
    share = top / total
    tied = len(items) > 1 and items[1][1] == top
    return (value if share >= floor and not tied else None), share, int(total)
