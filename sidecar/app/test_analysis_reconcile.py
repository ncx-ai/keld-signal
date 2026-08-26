"""What `reconcile` EMITS per resolved path — the only producer of `lang` and path-derived
`artifact` rows in the package, so the level vocabularies are decided here."""
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.reconcile import reconcile

BASE = (0.0, "sess", "repo", "branch", False)


def _rows(rel, from_input=True, depth=2):
    rows, _stats = reconcile([(BASE, rel, from_input, "/root")], depth)
    return [(lv, ref) for *_b, _kind, lv, ref, _n in rows]


def test_a_source_file_emits_a_language():
    got = _rows("internal/agent/enrich/mask.go")
    assert ("lang", "Go") in got
    assert ("artifact", "code") in got
    assert ("ext", ".go") in got


def test_a_markdown_file_emits_no_language_at_all():
    """Markdown was 7.4% of the whole `lang` level and is not a programming language. The file is
    still counted — `file`/`ext`/`dir` are unchanged — it simply no longer claims a language."""
    got = _rows("docs/AGENTS.md")
    assert not [r for lv, r in got if lv == "lang"], got
    assert ("ext", ".md") in got
    assert ("file", "docs/AGENTS.md") in got


def test_a_data_format_answers_the_artifact_level_instead():
    """The signal moves rather than disappearing: the question "what kind of thing is this" is
    what `artifact` answers, and every one of the four data formats already had a kind there."""
    assert ("artifact", "prose") in _rows("README.md")
    assert ("artifact", "data") in _rows("fixtures/events.json")
    assert ("artifact", "config") in _rows("deploy/values.yaml")
    assert ("artifact", "config") in _rows("ci/build.yml")
    for rel in ("README.md", "fixtures/events.json", "deploy/values.yaml", "ci/build.yml"):
        assert not [r for lv, r in _rows(rel) if lv == "lang"], rel


def test_a_mentioned_data_file_is_still_a_FILE_and_not_a_directory():
    """The regression guarded by PATH_EXT one level down. `looks_file` used to consult EXT_LANG,
    so a prose mention of `config.yaml` would have become a `dir` row the day YAML stopped being
    a language — a path-inventory defect with no relation to the language question."""
    got = _rows("services/api/config.yaml", from_input=False)
    assert ("file", "services/api/config.yaml") in got
    assert ("dir", "services/api") in got
    assert ("dir", "services/api/config.yaml") not in got


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
