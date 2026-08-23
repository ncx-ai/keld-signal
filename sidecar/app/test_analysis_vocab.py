"""Closed-vocabulary lookups. Every case is a distinction that was measured to matter."""
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.vocab import (action_for, toolchain_for, artifacts_for, mcp_provider,
                                EXT_LANG, CODE_EXT)


def test_a_directory_shape_beats_an_extension():
    """An unpacked deck is a tree of bare slide XML. Counting both made an hour read half
    `markup` and half `presentation`, and `markup` — a fact about the file format rather than the
    work — took the headline."""
    assert artifacts_for(ext=".xml", rel="unpacked/ppt/slides/slide1.xml") == ["presentation"]
    assert artifacts_for(ext=".xml") == ["markup"]


def test_the_program_class_is_separate_from_the_artifact():
    """Ten pdftoppm calls rendering a deck for a visual check made an hour of slide editing
    report `pdf 54%`. What a program is FOR and what is being worked ON are different levels."""
    assert toolchain_for("pdftoppm") == ["pdf"]
    assert artifacts_for(ext=".pptx") == ["presentation"]


def test_a_two_word_verb_is_more_specific_than_its_program():
    assert action_for(verb="git commit -m x") == "commit"
    assert action_for(exe="git") == "version control"


def test_bash_defers_to_the_program_it_ran():
    assert action_for(tool="Bash") is None
    assert action_for(tool="Bash", exe="pytest") == "test"


def test_mcp_provider_never_returns_a_uuid():
    u = "c78d9895-d0ef-43c2-b7c3-db6cfc34856e"
    assert mcp_provider(u, "notion-fetch") == "notion"
    assert mcp_provider("github", "create_issue") == "github"
    assert mcp_provider(u, None).startswith("mcp:")


def test_code_ext_excludes_prose_and_config():
    for e in (".md", ".json", ".yaml", ".yml"):
        assert e not in CODE_EXT
    assert ".go" in CODE_EXT and ".go" in EXT_LANG


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
