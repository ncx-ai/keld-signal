"""The allocation + inventory payload: ask a window's rollup a fixed set of questions.

Ported from `scripts/workstreams.py`, which discovered this shape against the real corpus before
any of it was production code — which workstreams earn a place, and how large the honest
unattributed row is. Nothing here infers: every value is a deterministic reference level, and a
window with no dominant value is reported as unattributed rather than given a plausible one.
"""
from app.analysis.window import dominant

# ALLOCATION workstreams: spend divides among them, so one value must own the window. The floor is
# what makes "unattributed" honest — below it there is no dominant value and we say so rather than
# picking the largest of several near-equals. 0.5 is deliberate: a bucket holding under half the
# evidence is not what the hour was about.
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
INVENTORY = [("harness_tools", "tool"), ("programs", "exe"),
             ("external_systems", "service"), ("integrations", "mcp_tool")]

# Loopback is not an external system. It is 85% of the raw service level and would otherwise be
# the top "system this org depends on".
LOOPBACK = {"127.0.0.1", "localhost", "0.0.0.0", "::1", "enrich-sidecar"}


def payload(rl):
    """rollup -> {"workstreams": {...}, "inventory": {...}}."""
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
        inv[name] = [{"value": str(k), "n": int(v)} for k, v in vals[:12]]
    return {"workstreams": ws, "inventory": inv}
