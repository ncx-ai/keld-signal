#!/usr/bin/env python3
"""The declarative file-kind table: its shape, its one hard rule (no colon in an id), its
derivation from `vocab.EXT_LANG`, and that the committed JSON Atlas pins matches it."""
import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import SCHEMA, filekinds as F
from app.analysis.dimensions import INVENTORY
from app.analysis.vocab import ACTIONS, EXT_LANG

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
JSON_PATH = os.path.join(REPO, "docs", "signal-file-kinds.json")

MEASURED = (".tsx .py .md .go .ts .png .html .js .jpg .diff .json .css .mjs .yml .sh .xml .output "
            ".txt .yaml .rs .hurl .example .svg .toml .jsonl .command .conf .pdf .env .rels .csv "
            ".mod").split()


def test_every_kind_has_a_display_and_a_valid_group():
    for k, (display, group) in F.KINDS.items():
        assert display and isinstance(display, str), k
        assert group in F.GROUPS, (k, group)


def test_no_kind_id_contains_a_colon():
    """Atlas parses `<action>:<kind>:<ext>` with split(":", 2); a colon would corrupt it."""
    for k in F.KINDS:
        assert ":" not in k, k
    for k in F.EXT_KIND.values():
        assert ":" not in k, k


def test_unrecognized_is_a_real_kind():
    assert F.KINDS["unrecognized"] == ("Unrecognised", "other")
    assert F.kind_for("x.zzz") == "unrecognized" and F.kind_for("noext") == "unrecognized"


def test_kind_for_never_returns_none_and_always_a_known_id():
    for p in ("x", "a/b.c", ".", "..", "x.TSX", "dir/.hidden", "x.", "/"):
        assert F.kind_for(p) in F.KINDS, p


def test_every_ext_and_basename_target_is_a_declared_kind():
    for e, k in F.EXT_KIND.items():
        assert e.startswith(".") and e == e.lower() and k in F.KINDS, (e, k)
    for n, k in F.BASENAME_KIND.items():
        assert n == n.lower() and k in F.KINDS, (n, k)


def test_every_measured_extension_is_mapped():
    for e in MEASURED:
        assert e in F.EXT_KIND, e
    assert F.kind_for("a/X.TSX") == "typescript"
    assert F.kind_for("a.mod") == "config"


def test_code_kinds_are_derived_from_ext_lang_not_retyped():
    for ext, display in EXT_LANG.items():
        k = F.kind_for("x" + ext)
        assert k in F.KINDS and F.group_for(k) == "code", (ext, k)
    assert F.display_for("typescript") == "TypeScript"
    assert F.kind_for("a.c") == F.kind_for("a.cpp") == "c_cpp"


def test_names_decide_when_there_is_no_extension():
    for name, kind in (("Dockerfile", "docker"), ("Dockerfile.dev", "docker"), ("Makefile", "make"),
                       (".gitignore", "config"), (".env", "config"), (".env.local", "config"),
                       ("LICENSE", "docs"), ("README", "docs"), ("README.md", "markdown")):
        assert F.kind_for("d/" + name) == kind, (name, F.kind_for(name))
    assert F.kind_for("makefile.py") == "python"     # a real extension outranks a name prefix


def test_required_kinds_exist():
    for k in "java ruby php c_cpp csharp swift kotlin sql notebook archive binary office_doc image".split():
        assert k in F.KINDS, k


def test_file_kinds_cap_is_computed_from_the_tables():
    caps = {e[0]: e[2] for e in INVENTORY}
    assert caps["file_kinds"] == caps["file_kind_tokens"] == len(ACTIONS) * len(F.KINDS)
    assert caps["file_actions"] == 12


def test_the_committed_json_matches_the_live_tables():
    with open(JSON_PATH) as fh:
        committed = json.load(fh)
    sys.path.insert(0, os.path.join(REPO, "scripts"))
    import gen_file_kinds
    assert committed == gen_file_kinds.build(), "run: python3 scripts/gen_file_kinds.py"
    assert committed["schema"] == SCHEMA


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
