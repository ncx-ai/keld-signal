"""`activity_class` — the vocabulary, its coverage, and the two limits it must state.

Standalone script, no pytest: `PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_reqclass.py`
"""
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis import reqclass
from app.analysis.dimensions import INVENTORY
from app.analysis.store import PRECOMPUTED_LEVELS

VOCAB = {"retrieve", "operate", "verify", "author_code", "author_prose",
         "synthesize", "delegate", "acknowledge", "unclassified"}

def req(tools=(), text="", out=0, think=0):
    return {"tools": list(tools), "text": text, "think": think, "out": out}

def test_every_class_is_reachable():
    cases = {
        "verify":       req([("Bash", {"command": "cd /x && pytest -q"})]),
        "author_code":  req([("Edit", {"file_path": "a.py"})]),
        "author_prose": req([("Edit", {"file_path": "a.md"})]),
        "retrieve":     req([("Bash", {"command": "git log --oneline"})]),
        "operate":      req([("Bash", {"command": "git add . && git commit -m 'x'"})]),
        "delegate":     req([("Agent", {"prompt": "you are"})]),
        "synthesize":   req(text="## R\n- a\n- b\n- c", out=800),
        "acknowledge":  req(text="done", out=5),
        "unclassified": req([("Bash", {"command": "frobnicate --wibble"})]),
    }
    for want, r in cases.items():
        got = reqclass.route_class(r)
        assert got == want, f"{want}: got {got}"
    assert set(cases) == VOCAB, "a class exists that nothing reaches"

def test_no_value_outside_the_vocabulary():
    # Whatever it is handed, it may only ever answer inside the published set --
    # an unexpected value would reach Atlas as a dimension value nobody declared.
    for r in (req(), req([("Totally", {"unknown": 1})]), req([("Bash", {})]),
              req(text="x" * 5000, out=99999), req([("Bash", {"command": "\x00"})])):
        assert reqclass.route_class(r) in VOCAB

def test_it_is_an_inventory_dimension_capped_at_the_whole_vocabulary():
    entry = [e for e in INVENTORY if e[0] == "activity_classes"]
    assert entry, "activity_classes must be an INVENTORY dimension, never ALLOCATION: above " \
                  "~20 requests no unit is coherent enough for a single winner"
    name, level, cap = entry[0]
    assert level == "activity_class"
    assert cap == len(VOCAB), "the cap must be the WHOLE vocabulary — a cut distribution is a " \
                              "wrong one, not a shorter one"
    assert "activity_class" in PRECOMPUTED_LEVELS, "the level registry is DERIVED from " \
        "ALLOCATION+INVENTORY; if this fails the derivation was replaced by a typed list"

def test_unclassified_is_a_real_value_not_a_default():
    # ⚠️ The failure this pins has happened twice in this project: `atv1`'s `other`
    # at 38.8% and an `operate` fallthrough at 57.5%, both of which looked healthy
    # until someone measured the composition. An unmatched request must say so.
    assert reqclass.route_class(req([("Bash", {"command": "zzz --nope"})])) == "unclassified"

def test_a_tool_free_request_reaches_only_the_prose_branch():
    # The web-app / Cowork shape. Stated as a LIMIT, not a bug: with no tool calls
    # the vocabulary can only answer two of its nine values, measured at 74.8%
    # acknowledge / 25.2% synthesize over 11,575 real chat turns.
    for text, out in (("hi", 3), ("## Report\n- a\n- b\n- c", 900), ("x" * 4000, 1200)):
        assert reqclass.route_class(req(text=text, out=out)) in {"synthesize", "acknowledge"}

def test_non_developer_tools_degrade_sensibly():
    for tool, want in (("mcp__x__notion-update-page", "author_prose"),
                       ("mcp__x__notion-fetch", "retrieve"),
                       ("Artifact", "author_prose"),
                       ("Skill", "delegate"),
                       ("WebSearch", "retrieve")):
        assert reqclass.route_class(req([(tool, {})])) == want, tool

if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for f in fns:
        f(); print(f"  PASS {f.__name__}")
    print(f"{len(fns)} passed")
