"""Standalone test for analysis/verbs.py. Run:
    cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_verbs.py
"""
from app.analysis.verbs import VERBS, VERB_FAMILY, EXCLUDED, PENDING_SPLIT, verb_for


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
    # REVIEW FOCUS 1: not `other`, not "", not a row at all -- None.
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
    # REVIEW FOCUS 2. classify_bash and CODE_TOOLS reach author_code with no
    # Write/Edit in evidence; MultiEdit/NotebookEdit are authoring but neither
    # literal. Guessing a side here would publish a false claim about the work.
    assert verb_for("author_code", [("Bash", {"command": "python3 -c 'x'"})]) is None
    assert verb_for("author_code", [("javascript_tool", {})]) is None
    # MultiEdit and NotebookEdit ARE edits of an existing thing -- they resolve.
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


if __name__ == "__main__":
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            fn(); print(f"ok {name}")
    print("PASS")
