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
    assert F.KINDS["unrecognized"] == ("Unrecognized", "other")
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
    for k in "java ruby php c_cpp csharp swift kotlin sql notebook archive binary document spreadsheet presentation image".split():
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


def test_every_kind_has_a_phrase_that_reads_after_an_action_label():
    """⚠️ `phrase` is NOT `display`. Consumers render "<action label> <phrase>", so a standalone
    name produces "Reading Image" and "Editing Office document". A missing phrase is therefore a
    rendering bug in every consumer at once, which is why this asserts over the whole table."""
    for kind in F.KINDS:
        ph = F.phrase_for(kind)
        assert ph, kind
        assert ph.strip() == ph, kind


def test_proper_nouns_keep_their_case_and_common_nouns_do_not():
    """The two rules pull in opposite directions, so both sides are pinned. A future addition
    that plural-lower-cases a language name would read as "Editing typescripts"."""
    for kind in ("typescript", "sql", "markdown", "docker", "make"):
        assert F.phrase_for(kind)[0].isupper(), kind
    for kind in ("image", "log", "config", "archive", "binary", "docs"):
        assert F.phrase_for(kind)[0].islower(), kind


def test_unrecognized_is_spelled_the_american_way_like_its_own_id():
    """The id has always been `unrecognized`; the display briefly was not, which made the table
    disagree with itself. Atlas UI copy is American English throughout."""
    assert F.KINDS[F.UNRECOGNIZED][0] == "Unrecognized"
    assert "Unrecognised" not in open(F.__file__).read()


def test_the_three_office_kinds_are_distinct_and_each_maps_its_own_extensions():
    want = {"document": ".docx .doc .odt .rtf .pages", "spreadsheet": ".xlsx .xls .xlsm .ods .numbers",
            "presentation": ".pptx .ppt .odp .key"}
    assert len(set(want)) == 3 and "office_doc" not in F.KINDS
    for kind, exts in want.items():
        for e in exts.split():
            assert F.EXT_KIND[e] == kind, (e, kind)


def test_dot_key_is_keynote_not_a_certificate():
    assert F.kind_for("deck.key") == "presentation"
    assert ".key" not in {e for e, k in F.EXT_KIND.items() if k == "certificate"}
    assert F.kind_for("a.pem") == "certificate"


def test_every_business_domain_resolves_to_its_kind():
    for path, kind in (("a.qfx", "financial_data"), ("a.xbrl", "financial_data"),
                       ("a.fig", "design"), ("a.psd", "design"), ("a.eps", "design"),
                       ("a.mkv", "video"), ("a.flac", "audio"), ("a.eml", "email"),
                       ("a.ics", "calendar"), ("a.epub", "ebook"), ("a.tex", "typesetting"),
                       ("a.sav", "stats_data"), ("a.rds", "stats_data"), ("a.orc", "database"),
                       ("a.parquet", "database"), ("a.geojson", "geo"), ("a.dcm", "medical_image"), ("a.hl7", "health_message"),
                       ("a.dwg", "cad"), ("a.stl", "model_3d"), ("a.woff2", "font"),
                       ("a.pfx", "certificate"), ("a.dmg", "archive"), ("a.iso", "archive")):
        assert F.kind_for(path) == kind, (path, F.kind_for(path))


def test_every_group_is_used_and_every_kind_has_one():
    used = {g for _, g in F.KINDS.values()}
    assert used == set(F.GROUPS), set(F.GROUPS) ^ used
    assert F.group_for("audio") == F.group_for("video") == "media" and F.group_for("design") == "design"


def test_non_code_kinds_outnumber_code_kinds():
    code = sum(1 for _, g in F.KINDS.values() if g == "code")
    assert len(F.KINDS) - code > code, (code, len(F.KINDS))


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
