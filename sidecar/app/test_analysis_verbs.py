"""Standalone test for analysis/verbs.py. Run:
    cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_verbs.py
"""
from app.analysis.verbs import VERBS, VERB_FAMILY, EXCLUDED, PENDING_SPLIT, HANDLED, verb_for, family_for


def test_vocabulary_is_nine_and_closed():
    assert len(VERBS) == 9, VERBS
    assert len(set(VERBS)) == 9
    # Every verb has exactly one family, and family is a pure function of verb.
    assert set(VERB_FAMILY) == set(VERBS)
    assert set(VERB_FAMILY.values()) == {"code", "language", "understanding", "agentic"}


def test_author_code_splits_on_write_vs_edit():
    assert verb_for("author_code", [("Write", {"file_path": "a.go"})]) == "code.write"
    assert verb_for("author_code", [("Edit", {"file_path": "a.go"})]) == "code.edit"


def test_author_prose_splits_on_the_same_tool_distinction():
    assert verb_for("author_prose", [("Write", {"file_path": "a.md"})]) == "text.create"
    assert verb_for("author_prose", [("Edit", {"file_path": "a.md"})]) == "text.transform"


def test_direct_mappings():
    assert verb_for("verify", [("Bash", {"command": "go test ./..."})]) == "review"
    assert verb_for("delegate", [("Agent", {})]) == "plan"


def test_excluded_classes_publish_no_verb():
    # Excluded classes return None rather than atv1's `other`. operate/acknowledge/
    # unclassified are out-of-scope work (state change, bare acknowledgement, honest
    # unknown), not gaps in atv1's capability taxonomy.
    for cls in ("operate", "acknowledge", "unclassified"):
        assert verb_for(cls, [("Bash", {"command": "git push"})]) is None, cls
    assert EXCLUDED == frozenset({"operate", "acknowledge", "unclassified"})


def test_pending_splits_abstain_until_the_study_lands():
    # There is NO unsplit parent to fall back to: `synthesize` and `retrieve` are
    # reqclass class names, not atv1 verbs.
    assert verb_for("synthesize", []) is None
    assert verb_for("retrieve", [("Read", {"file_path": "a.go"})]) is None
    assert PENDING_SPLIT == frozenset({"synthesize", "retrieve"})


def test_author_reached_without_an_authoring_tool_does_not_guess():
    # Some tool lists reach author_code/author_prose with no Write/Edit evidence
    # (classify_bash, CODE_TOOLS, javascript_tool). Guessing a side would publish
    # a false claim about the work. Return None only when evidence is absent.
    assert verb_for("author_code", [("Bash", {"command": "python3 -c 'x'"})]) is None
    assert verb_for("author_code", [("javascript_tool", {})]) is None
    # MultiEdit and NotebookEdit are authoring tools that resolve to edit.
    assert verb_for("author_code", [("MultiEdit", {"file_path": "a.go"})]) == "code.edit"
    assert verb_for("author_prose", [("NotebookEdit", {})]) == "text.transform"


def test_an_unknown_class_abstains_rather_than_raising():
    assert verb_for("not_a_class", []) is None


def test_every_producible_verb_is_in_the_vocabulary():
    produced = set()
    for cls, tools in [
        ("author_code", [("Write", {})]), ("author_code", [("Edit", {})]),
        ("author_prose", [("Write", {})]), ("author_prose", [("Edit", {})]),
        ("verify", []), ("delegate", []),
    ]:
        v = verb_for(cls, tools)
        if v:
            produced.add(v)
    assert produced <= set(VERBS), produced - set(VERBS)
    assert len(produced) == 6, produced   # six now; nine after the study


def test_mcp_prefixed_tool_names_normalize():
    # Tool names may be prefixed with mcp__ for managed tool integration.
    # The normaliser splits on __ and takes the last component, so mcp__abc__Write
    # normalises to Write and resolves like any bare Write.
    assert verb_for("author_code", [("mcp__abc__Write", {"file_path": "a.go"})]) == "code.write"
    assert verb_for("author_code", [("mcp__xyz__Edit", {"file_path": "a.go"})]) == "code.edit"
    assert verb_for("author_prose", [("mcp__tool__Write", {"file_path": "a.md"})]) == "text.create"


def test_write_and_edit_both_present_prefers_create():
    # A request with both Write and Edit in the evidence prefers create: the new
    # thing is the larger claim about the work, and code.write / text.create are
    # more informative than their edit counterparts.
    assert verb_for("author_code", [("Write", {}), ("Edit", {})]) == "code.write"
    assert verb_for("author_code", [("Edit", {}), ("Write", {})]) == "code.write"
    assert verb_for("author_prose", [("Edit", {}), ("Write", {})]) == "text.create"


def test_family_for_returns_the_four_families():
    # family_for is a pure rollup from verb to one of four families.
    assert family_for("code.write") == "code"
    assert family_for("code.edit") == "code"
    assert family_for("text.create") == "language"
    assert family_for("text.transform") == "language"
    assert family_for("text.summarize") == "language"
    assert family_for("review") == "understanding"
    assert family_for("extract") == "understanding"
    assert family_for("plan") == "agentic"
    assert family_for("research") == "agentic"


def test_family_for_unknown_verb_returns_none():
    # An unknown verb returns None, not a guess or an error.
    assert family_for("not_a_verb") is None
    assert family_for("") is None


def test_every_reqclass_class_is_accounted_for():
    # A tenth class must not silently abstain. Excluded-on-purpose and
    # abstaining-pending-a-study are different decisions, and a new class is
    # NEITHER until someone decides which. This test imports reqclass.CLASSES
    # and ensures every class is explicitly handled (HANDLED is the union of
    # EXCLUDED, PENDING_SPLIT, _DIRECT, and _AUTHOR).
    from app.analysis.reqclass import CLASSES
    handled = HANDLED
    assert handled == set(CLASSES), set(CLASSES) ^ handled


if __name__ == "__main__":
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            fn(); print(f"ok {name}")
    print("PASS")
