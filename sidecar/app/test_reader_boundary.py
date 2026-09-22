#!/usr/bin/env python3
"""AC-1: Claude Code's own field names live in exactly ONE module, `readers/claude.py`.

    cd sidecar && PYTHONPATH=. python3.12 app/test_reader_boundary.py

WHY A GREP AND NOT A DESIGN REVIEW. Six modules read Claude Code's line shape today
(`ingest`, `levels`, `capture`, `workspace`, `textembed`, `analyze` — plus `devblocks`, which
the discovery's grep missed). Adding a Codex branch to each of them is alternative A in
`docs/superpowers/specs/2026-09-14-normalised-turn-record-discovery.html`, and it lost on one
fact: six modules times one tool forever, and the prompt-id index bug came from exactly two
copies of one rule drifting. The record makes that six edits ONCE — but only for as long as
nobody adds a seventh reader of `promptId`. A grep is the only thing that notices.

⚠️ COMMENTS AND DOCSTRINGS ARE EXEMPT, AND THAT IS NOT A LOOPHOLE. This package documents its
own history at length, and the uuid-vs-promptId incident is written into `ingest.py`,
`analyze.py`, `blocks.py` and `store.py` as prose precisely so it is not repeated. Deleting
those paragraphs to satisfy a grep would destroy the reason the rule exists in order to enforce
it. The criterion is about where a field is READ, so this strips comments and docstrings with
`tokenize` — exact, not a regex over lines — and greps what is left. A string literal that is
part of an EXPRESSION (`b.get("type") == "tool_use"`) is code and is NOT stripped, which is the
whole point: that is the one of these five that only ever appears as a literal.
"""
import io
import os
import sys
import tokenize

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

ANALYSIS = os.path.join(os.path.dirname(os.path.abspath(__file__)), "analysis")

# The five names the discovery names (AC-1), plus `attributionMcpServer`/`attributionMcpTool`,
# which are the same class and were simply not listed. `message.content` is deliberately NOT here:
# `message` and `content` are ordinary English and a grep for them reports the docstrings of every
# module in the package.
CLAUDE_NAMES = ("promptId", "gitBranch", "attributionSkill", "isSidechain", '"tool_use"',
                "attributionMcpServer", "attributionMcpTool")

# The one module allowed to name them.
ALLOWED = {os.path.join("readers", "claude.py")}

# `testdata/` is exempt because it WRITES the shape rather than reading it: a fixture builder
# that could not name `gitBranch` could not produce a Claude transcript at all, and the rule this
# file enforces is about where the extraction READS from. The exemption is a directory prefix and
# not a filename, so a reader smuggled in as a "fixture helper" would have to be put somewhere
# that says what it is.
EXEMPT_DIRS = ("testdata" + os.sep,)


def _code_only(path):
    """`path`'s source with COMMENTS and DOCSTRINGS removed, line numbers preserved.

    A docstring is a string that stands alone as a statement. `tokenize` gives that exactly: a
    STRING token whose logical line begins with it — i.e. the previous significant token is a
    NEWLINE/INDENT/DEDENT or the start of file. Everything else, including a string used as a
    dict key or compared against, survives.
    """
    with open(path, "rb") as fh:
        toks = list(tokenize.tokenize(fh.readline))
    out = {}
    prev = None
    for tok in toks:
        if tok.type == tokenize.COMMENT:
            continue
        if tok.type == tokenize.STRING and prev in (None, tokenize.NEWLINE, tokenize.INDENT,
                                                    tokenize.DEDENT, tokenize.ENCODING):
            prev = tok.type
            continue
        if tok.type not in (tokenize.NL, tokenize.NEWLINE, tokenize.INDENT, tokenize.DEDENT,
                            tokenize.ENCODING, tokenize.ENDMARKER):
            out.setdefault(tok.start[0], []).append(tok.string)
        prev = tok.type
    return out


def _modules():
    for dirpath, _dirs, files in os.walk(ANALYSIS):
        for f in sorted(files):
            if not f.endswith(".py"):
                continue
            p = os.path.join(dirpath, f)
            rel = os.path.relpath(p, ANALYSIS)
            if rel.startswith(EXEMPT_DIRS):
                continue
            yield rel, p


def test_claude_field_names_appear_only_in_the_claude_reader():
    hits = []
    for rel, path in _modules():
        if rel in ALLOWED:
            continue
        for lineno, toks in _code_only(path).items():
            line = " ".join(toks)
            for name in CLAUDE_NAMES:
                if name in line:
                    hits.append(f"{rel}:{lineno}: {name} in {line.strip()[:90]}")
    assert not hits, (
        "Claude field names read outside readers/claude.py — the record exists so that a second "
        "tool is a reader, not a branch in six modules:\n  " + "\n  ".join(hits))


def test_the_claude_reader_really_does_name_them():
    """The complement, so a green suite cannot be bought by DELETING the reader.

    A test that only ever says "nowhere else" passes trivially against a package that reads no
    transcript at all; this pins that the names moved rather than vanished.
    """
    reader = os.path.join(ANALYSIS, "readers", "claude.py")
    assert os.path.exists(reader), "readers/claude.py does not exist"
    src = " ".join(" ".join(t) for t in _code_only(reader).values())
    missing = [n for n in CLAUDE_NAMES if n not in src]
    assert not missing, f"readers/claude.py no longer reads: {missing}"


def test_every_analysis_module_is_reachable_by_the_walk():
    """The walk must actually see the modules, or the grep above is vacuous."""
    rels = {rel for rel, _ in _modules()}
    for expected in ("ingest.py", "levels.py", "capture.py", "workspace.py", "textembed.py",
                     "analyze.py", "devblocks.py", os.path.join("readers", "claude.py")):
        assert expected in rels, f"{expected} not found under analysis/ — the walk is wrong"


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_")]
    bad = 0
    for name, fn in fns:
        try:
            fn()
            print(f"PASS {name}")
        except AssertionError as e:
            bad += 1
            print(f"FAIL {name}: {e}")
    print(f"{len(fns) - bad}/{len(fns)} passed")
    sys.exit(1 if bad else 0)
