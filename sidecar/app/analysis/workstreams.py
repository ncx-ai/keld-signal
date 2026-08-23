"""The allocation + inventory payload: ask a window's rollup a fixed set of questions.

Ported from `scripts/workstreams.py`, which discovered this shape against the real corpus before
any of it was production code — which workstreams earn a place, and how large the honest
unattributed row is. Nothing here infers: every value is a deterministic reference level, and a
window with no dominant value is reported as unattributed rather than given a plausible one.
"""
from app.analysis import SCHEMA
from app.analysis.window import dominant

# ALLOCATION workstreams: spend divides among them, so one value must own the window. The floor is
# what makes "unattributed" honest — below it there is no dominant value and we say so rather than
# picking the largest of several near-equals. 0.5 is deliberate: a bucket holding under half the
# evidence is not what the hour was about.
#
# The share floor is only half of it. `dominant` also requires window.MIN_EVIDENCE observations,
# because a share is a ratio and a ratio over one observation is 1.0 by construction — see that
# constant for the derivation and the measured cost. The per-dimension number below is the SHARE
# floor only; the evidence floor is uniform, since it is a property of the arithmetic rather than
# of any one dimension.
ALLOCATION = [
    ("project",     "workspace", 0.50),
    ("branch",      "branch",    0.50),
    ("model",       "model",     0.50),
    ("output_type", "artifact",  0.50),
    ("language",    "lang",      0.50),
    ("workflow",    "skill",     0.50),
    ("tooling",     "toolchain", 0.50),
]

# INVENTORY dimensions: multi-valued by nature — "what was used", not "what owns this". No
# dominance requirement, because asking which single tool owns an hour is the wrong question.
#
# `named_terms` (level "term") belongs here, not in ALLOCATION, for a measured reason: assessed
# as an allocation dimension it had 97% coverage but only 19% dominance across 4256 distinct
# values — no window has one term that owns it, so it cannot bucket spend the way the other seven
# do. But it is the ONLY level in this whole package that reads message TEXT rather than tool-call
# inputs (see app/analysis/terms.py) — a customer, a supplier, a model under evaluation is only
# ever spoken, never a tool argument, so every other level is structurally blind to it. Dropping
# it because it doesn't fit ALLOCATION's shape would silently discard the one signal the rest of
# this package cannot see, and would also make analyze_window's spaCy pass — the most expensive
# part of the call — pure waste.
INVENTORY = [("harness_tools", "tool"), ("programs", "exe"),
             ("external_systems", "service"), ("integrations", "mcp_tool"),
             ("named_terms", "term")]

# Loopback is not an external system. It is 85% of the raw service level and would otherwise be
# the top "system this org depends on".
LOOPBACK = {"127.0.0.1", "localhost", "0.0.0.0", "::1", "enrich-sidecar"}


def payload(rl):
    """rollup -> {"workstreams": {...}, "inventory": {...}}.

    PRIVACY NOTE for `inventory.named_terms`: unlike every other value in this payload, a named
    term is drawn from message TEXT (see terms.py), not from tool-call inputs, and can legitimately
    be a person's name — confirmed on a real window ("Federico", "Daniel"). That is acceptable for
    what this payload is today: /analyze is sidecar -> daemon on one machine, and nothing here
    publishes. It is NOT acceptable to forward as-is to anything that syncs off-device — the
    masking rule for what crosses to Atlas is matched vocabulary IDs only, never raw named terms.
    Whoever wires publication next must filter this field through the org's configured vocabulary
    matcher (or drop it) before it leaves the device, the same way every other masked span already
    is upstream of publish (see AGENTS.md's privacy invariant).
    """
    ws = {}
    for name, level, floor in ALLOCATION:
        v, share, tot = dominant(rl, level, floor)
        ws[name] = None if v is None else {
            "value": v, "share": round(share, 3), "evidence": tot,
            "provenance": "known:tool_inputs"}
    inv = {}
    for name, level in INVENTORY:
        vals = rl.get(level) or []
        if name == "external_systems":
            vals = [(k, v) for k, v in vals if k not in LOOPBACK]
        # Fixed top-12, cut by POSITION, not by value: a tie straddling the boundary (item 12
        # and item 13 sharing the same count) is resolved by rollup()'s tie-break order alone,
        # so which one survives the cut is arbitrary — real for every level, but the one that
        # actually surfaces it is "programs" (exe): the largest inventory dimension by a wide
        # margin (~180 distinct values per window vs. tens for the others), so it is the one
        # with the most opportunities to have something sitting exactly on the boundary. Two
        # runs over different corpora (or a pandas run vs. this one) can disagree on which
        # value occupies slot 12 while agreeing on every count — that is not a bug in either
        # run, just an unrepresented tie at the cut. Confirmed on the frozen corpus: 114/572
        # windows differ in "programs"' published set (never its counts) against the pre-Task-7
        # pandas payload for exactly this reason — see task-7-report.md, Step 5.
        inv[name] = [{"value": str(k), "n": int(v)} for k, v in vals[:12]]
    return {"schema": SCHEMA, "workstreams": ws, "inventory": inv}
