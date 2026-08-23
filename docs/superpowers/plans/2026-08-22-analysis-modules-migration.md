# Analysis Modules Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract the measured analysis core out of `scripts/refseries.py` into an importable package under `sidecar/app/analysis/`, so the sidecar and the study run the same code.

**Architecture:** `refseries.py` becomes a thin CLI over the package. The package has one I/O module (`transcript.py`); everything else is pure functions over parsed turns. Vocabularies become loadable data. No pandas in the package — the study keeps pandas for its frame layer.

**Tech Stack:** Python 3.12 (sidecar venv), spaCy `en_core_web_sm`, bashlex, wordfreq. Study venv is Python 3.14 with pandas/pyarrow/yaml.

## Global Constraints

- **Sidecar tests are standalone scripts, never pytest.** Each ends with a `__main__` runner that calls every `test_*` function. Run with `~/.keld/sidecar-venv/bin/python`, never the host interpreter (AGENTS.md).
- **Study code runs under `~/.keld/study-venv/bin/python`.** Both interpreters must be able to import the package.
- **The package must not import anything from `scripts/`.** Dependencies point one way: `scripts/` → `sidecar/app/analysis/`.
- **No pandas, numpy or pyarrow in the package.** Extraction is counts and strings.
- **Never truncate an identifier.** A path or symbol cut short is a false identifier — drop the whole term instead (AGENTS.md).
- **Every task ends with a corpus-identity check**, not just unit tests. A refactor that moves the numbers is a bug.
- **Bump nothing in `enrich.SchemaVersion`.** This migration changes no published vocabulary.

## File Structure

```
sidecar/app/analysis/
  __init__.py        version constant only
  vocab.py           EXT_LANG, ARTIFACT_EXT, ARTIFACT_DIR, TOOLCHAIN_EXE, ARTIFACT_SKILL,
                     CODE_EXT, TOOL_ACTION, EXE_ACTION, EXE_TO_ACTION + action_for,
                     toolchain_for, artifacts_for, vcs_of, mcp_provider
  shell.py           SHELL_KEYWORD, TWO_WORD, HEREDOC, WRAPPERS, VALUE_FLAGS, SHELLS,
                     strip_heredocs, parsed_command_names, unwrap_command, in_path helpers
  text.py            clip, is_command_echo, text_of, think_blocks
  terms.py           moved verbatim from scripts/terms.py
  paths.py           PATH_TOKEN, plausible_path, rel_within, resolve_workspace, repo_of,
                     _git_root, scan_workspace, reconcile
  transcript.py      the only I/O: iter_turns(path), turns_between(path, start, end)
  levels.py          LEVELS + turn -> reference events
  window.py          events -> per-window rollup (counts, shares, dominance)
  workstreams.py     rollup -> allocation + inventory payload
  match.py           configured-vocabulary matching

sidecar/app/test_analysis_vocab.py      standalone, sidecar venv
sidecar/app/test_analysis_shell.py
sidecar/app/test_analysis_terms.py
sidecar/app/test_analysis_paths.py
sidecar/app/test_analysis_window.py

scripts/refseries.py       keeps frames, views, CLI; imports the package
scripts/check_identity.py  NEW: the corpus-identity harness every task runs
```

---

### Task 1: The identity harness and the package skeleton

Nothing can be moved safely until a refactor can be *proved* not to change the numbers. Unit tests
have not caught any of the ~20 plausible-wrong-numbers this work produced; a byte-identical corpus
rebuild has.

**Files:**
- Create: `sidecar/app/analysis/__init__.py`
- Create: `scripts/check_identity.py`

**Interfaces:**
- Produces: `scripts/check_identity.py` CLI — `check_identity.py snapshot <outdir>` and
  `check_identity.py verify <baseline.json> <outdir>`; exit 0 identical, 1 differs.

- [ ] **Step 1: Create the package skeleton**

```bash
mkdir -p sidecar/app/analysis
cat > sidecar/app/analysis/__init__.py <<'EOF'
"""On-device transcript analysis, shared by the sidecar and the study.

Imported by BOTH `sidecar/app/*` and `scripts/refseries.py`. The study's value is that its
behaviour is measured; if production reimplemented this, the measurements would stop describing
what ships. One implementation, two front ends — see
docs/superpowers/specs/2026-08-22-analysis-modules-migration-design.md.

Nothing here may import from `scripts/`, and nothing here may import pandas.
"""

SCHEMA = 1
EOF
```

- [ ] **Step 2: Write the identity harness**

```bash
cat > scripts/check_identity.py <<'EOF'
#!/usr/bin/env python3
"""Prove a refactor did not change the numbers.

`snapshot` records a fingerprint of an events frame; `verify` rebuilds and compares. Row counts
are not enough — a reordering or a single changed ref is exactly the class of defect that has
slipped past unit tests here, so the fingerprint is a hash of the sorted full content plus
per-level vocabulary and totals, which localises any difference to a level.
"""
import argparse, hashlib, json, sys
import pandas as pd


def fingerprint(outdir):
    ev = pd.read_parquet(f"{outdir}/events.parquet")
    cols = list(ev.columns)
    flat = ev.astype(str).sort_values(cols).to_csv(index=False)
    per_level = {}
    for lv, g in ev.groupby("level", observed=True):
        per_level[str(lv)] = {"rows": int(len(g)), "total": float(g.n.sum()),
                              "distinct": int(g.ref.nunique())}
    return {"rows": int(len(ev)),
            "sha256": hashlib.sha256(flat.encode()).hexdigest(),
            "levels": per_level}


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("snapshot"); s.add_argument("outdir"); s.add_argument("-o", default="/tmp/identity.json")
    v = sub.add_parser("verify"); v.add_argument("baseline"); v.add_argument("outdir")
    a = ap.parse_args()
    if a.cmd == "snapshot":
        fp = fingerprint(a.outdir)
        json.dump(fp, open(a.o, "w"), indent=1)
        print(f"snapshot {fp['rows']} rows sha={fp['sha256'][:12]} -> {a.o}")
        return 0
    base = json.load(open(a.baseline))
    now = fingerprint(a.outdir)
    if base["sha256"] == now["sha256"]:
        print(f"IDENTICAL  {now['rows']} rows  sha={now['sha256'][:12]}")
        return 0
    print(f"DIFFERS  rows {base['rows']} -> {now['rows']}  "
          f"sha {base['sha256'][:12]} -> {now['sha256'][:12]}")
    for lv in sorted(set(base["levels"]) | set(now["levels"])):
        b, n = base["levels"].get(lv), now["levels"].get(lv)
        if b != n:
            print(f"  {lv:18} {b} -> {n}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
EOF
chmod +x scripts/check_identity.py
```

- [ ] **Step 3: Record the pre-migration baseline**

```bash
rm -rf /tmp/rs-base && mkdir -p /tmp/rs-base
~/.keld/study-venv/bin/python scripts/refseries.py extract --outdir /tmp/rs-base \
  --roots ~/.claude/projects /tmp/john-projects
~/.keld/study-venv/bin/python scripts/check_identity.py snapshot /tmp/rs-base -o /tmp/identity-base.json
```

Expected: `snapshot NNNNNN rows sha=... -> /tmp/identity-base.json`

- [ ] **Step 4: Prove the harness detects a change**

Temporarily break something, confirm `verify` fails, then revert:

```bash
sed -i 's/add("ref", "vcs", vcs_of/add("ref", "vcs_BROKEN", vcs_of/' scripts/refseries.py
rm -rf /tmp/rs-chk && mkdir -p /tmp/rs-chk
~/.keld/study-venv/bin/python scripts/refseries.py extract --outdir /tmp/rs-chk \
  --roots ~/.claude/projects /tmp/john-projects >/dev/null
~/.keld/study-venv/bin/python scripts/check_identity.py verify /tmp/identity-base.json /tmp/rs-chk
git checkout scripts/refseries.py
```

Expected: `DIFFERS` and a `vcs` line. A harness nobody has seen fail is not a harness.

- [ ] **Step 5: Commit**

```bash
git add sidecar/app/analysis/__init__.py scripts/check_identity.py
git commit -m "analysis: package skeleton and the corpus-identity harness

Every migration step is verified by rebuilding the corpus and comparing a hash of
the sorted events frame, not by unit tests alone: none of the ~20
plausible-wrong-numbers this work produced was caught by a unit test."
```

---

### Task 2: `vocab.py` — the closed vocabularies

Lowest risk: pure data and lookups with no dependencies. Proves the import path works from both
interpreters before anything harder moves.

**Files:**
- Create: `sidecar/app/analysis/vocab.py`
- Create: `sidecar/app/test_analysis_vocab.py`
- Modify: `scripts/refseries.py` — delete the moved definitions, import them instead
- Modify: `scripts/qwen_windows.py:84` — `EXT_LANG` moves; re-export for back-compat

**Interfaces:**
- Produces:
  - `EXT_LANG: dict[str, str]`, `CODE_EXT: tuple[str, ...]`
  - `ARTIFACT_EXT: dict[str, tuple[str, ...]]`, `ARTIFACT_DIR: dict[str, tuple[str, ...]]`,
    `ARTIFACT_SKILL: dict[str, str]`, `TOOLCHAIN_EXE: dict[str, tuple[str, ...]]`
  - `TOOL_ACTION: dict[str, str | None]`, `EXE_ACTION: dict[str, tuple[str, ...]]`,
    `EXE_TO_ACTION: dict[str, str]`
  - `action_for(tool=None, exe=None, verb=None) -> str | None`
  - `toolchain_for(exe: str) -> list[str]`
  - `artifacts_for(ext=None, rel=None, skill=None) -> list[str]`
  - `vcs_of(cwd, git_branch) -> str`
  - `mcp_provider(server: str, tool: str | None) -> str`

- [ ] **Step 1: Write the failing test**

```bash
cat > sidecar/app/test_analysis_vocab.py <<'EOF'
"""Closed-vocabulary lookups. Every case is a distinction that was measured to matter."""
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.vocab import (action_for, toolchain_for, artifacts_for, vcs_of, mcp_provider,
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


def test_vcs_of_reports_none_without_a_branch():
    assert vcs_of("/tmp/x", None) == "none"


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
EOF
```

- [ ] **Step 2: Run it to verify it fails**

Run: `~/.keld/sidecar-venv/bin/python sidecar/app/test_analysis_vocab.py`
Expected: `ModuleNotFoundError: No module named 'app.analysis.vocab'`

- [ ] **Step 3: Move the definitions**

Copy verbatim from `scripts/refseries.py` into `sidecar/app/analysis/vocab.py`, keeping every
comment — the comments record measurements and are the reason the tables look as they do:

- `EXT_LANG` from `scripts/qwen_windows.py:84`
- `LEVELS` stays in `refseries.py` for now (moves with `levels.py` in Task 6)
- `TOOL_ACTION`, `EXE_ACTION`, `EXE_TO_ACTION`, `action_for` — `refseries.py:120-166`
- `ARTIFACT_EXT`, `ARTIFACT_DIR`, `TOOLCHAIN_EXE`, `ARTIFACT_SKILL`, `CODE_EXT`,
  `toolchain_for`, `artifacts_for` — `refseries.py:173-235`
- `vcs_of` — `refseries.py:396`
- `mcp_provider` and `UUIDISH` — `refseries.py:532`

Header the module with:

```python
"""Closed vocabularies: what a file IS, what a program is FOR, what an act physically is.

Deterministic mappings, never a guess. Each table's comments record the measurement that shaped
it; do not condense them.
"""
import re
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `~/.keld/sidecar-venv/bin/python sidecar/app/test_analysis_vocab.py`
Expected: `7 passed`

- [ ] **Step 5: Rewire the study**

In `scripts/refseries.py`, delete the moved definitions and add near the other imports:

```python
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
from app.analysis.vocab import (  # noqa: E402
    EXT_LANG, CODE_EXT, ARTIFACT_EXT, ARTIFACT_DIR, ARTIFACT_SKILL, TOOLCHAIN_EXE,
    TOOL_ACTION, EXE_ACTION, EXE_TO_ACTION,
    action_for, toolchain_for, artifacts_for, vcs_of, mcp_provider)
```

In `scripts/qwen_windows.py`, replace the `EXT_LANG` literal with the same import and a comment:

```python
# EXT_LANG moved to app.analysis.vocab; re-exported so existing importers keep working.
from app.analysis.vocab import EXT_LANG  # noqa: F401
```

- [ ] **Step 6: Verify the study still parses identically**

```bash
~/.keld/study-venv/bin/python scripts/test_refseries.py
~/.keld/study-venv/bin/python scripts/test_bashrefs.py
~/.keld/study-venv/bin/python scripts/test_terms.py
rm -rf /tmp/rs-t2 && mkdir -p /tmp/rs-t2
~/.keld/study-venv/bin/python scripts/refseries.py extract --outdir /tmp/rs-t2 \
  --roots ~/.claude/projects /tmp/john-projects >/dev/null
~/.keld/study-venv/bin/python scripts/check_identity.py verify /tmp/identity-base.json /tmp/rs-t2
```

Expected: all suites pass, then `IDENTICAL`.

> If `verify` reports `DIFFERS`, the corpus grew between baseline and now (this machine's own
> transcripts are live). Re-snapshot from a git stash of the pre-task code and compare again;
> never accept a difference without identifying it.

- [ ] **Step 7: Commit**

```bash
git add sidecar/app/analysis/vocab.py sidecar/app/test_analysis_vocab.py \
        scripts/refseries.py scripts/qwen_windows.py
git commit -m "analysis: extract closed vocabularies into app.analysis.vocab

Pure data and lookups, no dependencies — the lowest-risk move, and it proves both
interpreters can import the package. Corpus rebuild is byte-identical."
```

---

### Task 3: `shell.py` — command extraction

Self-contained, and it already has 9 tests. The highest-value module: it took `exe` from 6053
distinct "programs" to 620.

**Files:**
- Create: `sidecar/app/analysis/shell.py`
- Create: `sidecar/app/test_analysis_shell.py` (port of `scripts/test_bashrefs.py`)
- Modify: `scripts/refseries.py` — delete the moved code, import it

**Interfaces:**
- Consumes: nothing from Task 2.
- Produces:
  - `strip_heredocs(command: str) -> str`
  - `parsed_command_names(command: str) -> list[str] | None` (None = unparseable, caller falls back)
  - `unwrap_command(words: list[str]) -> str | None`
  - `SHELL_KEYWORD: set[str]`, `TWO_WORD: set[str]`, `VALUE_FLAGS: set[str]`, `SHELLS: set[str]`,
    `WRAPPERS: dict[tuple[str, str | None], int]`

Note `bash_refs` does **not** move in this task: it also extracts paths, which belong to
`paths.py`. It stays in `refseries.py` and calls into `shell.py` until Task 5.

- [ ] **Step 1: Write the failing test**

```bash
sed -e 's|^from refseries import.*|import sys, os\nsys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))\nfrom app.analysis.shell import strip_heredocs, unwrap_command, parsed_command_names|' \
    scripts/test_bashrefs.py > sidecar/app/test_analysis_shell.py
```

Then delete from the copy the tests that call `bash_refs` or `mcp_provider` (they belong to Tasks
5 and 2), keeping `test_heredoc_terminator_line_is_dropped`,
`test_wrapper_yields_the_tool_it_runs`, `test_non_wrappers_are_left_alone`, and add:

```python
def test_command_substitution_is_visited_once():
    """The AST is reachable by two routes; git was counted twice for one invocation."""
    assert parsed_command_names("echo $(git rev-parse HEAD)") == ["echo", "git"]


def test_unparseable_input_returns_none_so_the_caller_can_fall_back():
    """bashlex parses 91.7% of real commands. The other 8.3% must degrade, not raise."""
    assert parsed_command_names("if [ ; then") is None
```

- [ ] **Step 2: Run it to verify it fails**

Run: `~/.keld/sidecar-venv/bin/python sidecar/app/test_analysis_shell.py`
Expected: `ModuleNotFoundError: No module named 'app.analysis.shell'`

- [ ] **Step 3: Move the code**

Copy verbatim from `scripts/refseries.py` into `sidecar/app/analysis/shell.py`:
`SHELL_KEYWORD` (78), `TWO_WORD` (81), `HEREDOC` + `strip_heredocs` (439), `parsed_command_names`
(472), `ENV_ASSIGN`/`VALUE_FLAGS`/`SHELLS`/`WRAPPERS`/`unwrap_command` (585). Keep all comments.

- [ ] **Step 4: Add bashlex to the sidecar requirements**

```bash
printf 'bashlex==0.18\n' >> sidecar/requirements.txt
~/.keld/sidecar-venv/bin/pip install -q bashlex==0.18
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `~/.keld/sidecar-venv/bin/python sidecar/app/test_analysis_shell.py`
Expected: all tests PASS

- [ ] **Step 6: Rewire and verify identity**

Delete the moved definitions from `refseries.py`, add
`from app.analysis.shell import (SHELL_KEYWORD, TWO_WORD, strip_heredocs, parsed_command_names, unwrap_command)`,
then:

```bash
~/.keld/study-venv/bin/python scripts/test_bashrefs.py
rm -rf /tmp/rs-t3 && mkdir -p /tmp/rs-t3
~/.keld/study-venv/bin/python scripts/refseries.py extract --outdir /tmp/rs-t3 \
  --roots ~/.claude/projects /tmp/john-projects >/dev/null
~/.keld/study-venv/bin/python scripts/check_identity.py verify /tmp/identity-base.json /tmp/rs-t3
```

Expected: `9 passed`, then `IDENTICAL`.

- [ ] **Step 7: Commit**

```bash
git add sidecar/app/analysis/shell.py sidecar/app/test_analysis_shell.py \
        sidecar/requirements.txt scripts/refseries.py
git commit -m "analysis: extract shell command parsing into app.analysis.shell

Heredoc stripping, bashlex extraction, wrapper unwrapping and -c recursion — the
module that took exe from 6053 distinct 'programs' to 620. bash_refs stays behind
until paths.py, because it also extracts paths."
```

---

### Task 4: `text.py` and `terms.py`

**Files:**
- Create: `sidecar/app/analysis/text.py`
- Create: `sidecar/app/analysis/terms.py` (moved from `scripts/terms.py`)
- Create: `sidecar/app/test_analysis_terms.py` (port of `scripts/test_terms.py`)
- Delete: `scripts/terms.py`
- Modify: `scripts/refseries.py`, `scripts/qwen_windows.py`

**Interfaces:**
- Produces:
  - `text.clip(text: str, cap: int) -> str` — bounds one turn at a sentence end, never mid-clause
  - `text.is_command_echo(text: str) -> bool`
  - `text.text_of(content) -> str`, `text.think_blocks(content) -> list[int]`
  - `terms.tally(messages: list[str], nlp=None) -> list[dict]` — `{"term", "n", "messages"}`
  - `terms.candidates(text: str, nlp=None) -> list[str]`

- [ ] **Step 1: Move `clip` and `is_command_echo` into `text.py`**

Copy from `scripts/qwen_windows.py`: `COMMAND_ECHO`, `SKILL_INJECTION`, `TASK_NOTIFICATION`,
`is_command_echo`, `clip`. Copy `text_of` and `think_blocks` from `refseries.py`. Keep the
comments — `TASK_NOTIFICATION`'s records that 15% of surviving user messages were machine text.

- [ ] **Step 2: Move `terms.py` unchanged**

```bash
git mv scripts/terms.py sidecar/app/analysis/terms.py
```

- [ ] **Step 3: Port the tests**

```bash
sed -e 's|^from terms import|from app.analysis.terms import|' \
    -e '2i import sys, os\nsys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))' \
    scripts/test_terms.py > sidecar/app/test_analysis_terms.py
```

- [ ] **Step 4: Add the dependencies**

```bash
printf 'spacy==3.8.*\nwordfreq==3.1.*\n' >> sidecar/requirements.txt
~/.keld/sidecar-venv/bin/pip install -q "spacy==3.8.*" "wordfreq==3.1.*"
~/.keld/sidecar-venv/bin/python -m spacy download en_core_web_sm
```

- [ ] **Step 5: Run the tests**

Run: `~/.keld/sidecar-venv/bin/python sidecar/app/test_analysis_terms.py`
Expected: `10 passed`

- [ ] **Step 6: Rewire, verify, commit**

`refseries.py` imports `from app.analysis import terms` and
`from app.analysis.text import clip, is_command_echo, text_of, think_blocks`; `qwen_windows.py`
re-exports `clip` and `is_command_echo`.

```bash
~/.keld/study-venv/bin/python scripts/test_terms.py
rm -rf /tmp/rs-t4 && mkdir -p /tmp/rs-t4
~/.keld/study-venv/bin/python scripts/refseries.py extract --outdir /tmp/rs-t4 \
  --roots ~/.claude/projects /tmp/john-projects >/dev/null
~/.keld/study-venv/bin/python scripts/check_identity.py verify /tmp/identity-base.json /tmp/rs-t4
git add -A && git commit -m "analysis: move text filters and named-term extraction into the package

terms.py moves unchanged. text.py takes clip and is_command_echo, whose
TASK_NOTIFICATION filter removed the 15% of 'engineer' messages that were machine
text — which also inflated the assistant-per-engineer ratio in every digest."
```

---

### Task 5: `paths.py` — the riskiest extraction

`resolve_workspace` carries the fix that took branch resolution from **56.3% to 87.7%**, and
`reconcile` is cross-file. Unit tests are not sufficient here; the identity check is the gate.

**Files:**
- Create: `sidecar/app/analysis/paths.py`
- Create: `sidecar/app/test_analysis_paths.py`
- Modify: `scripts/refseries.py` — `bash_refs` moves here too, now that both halves have a home

**Interfaces:**
- Consumes: `shell.strip_heredocs`, `shell.parsed_command_names`, `shell.SHELL_KEYWORD`,
  `shell.TWO_WORD`
- Produces:
  - `PATH_TOKEN: re.Pattern`, `plausible_path(tok: str) -> bool`
  - `rel_within(p, root_dir, cwd) -> str | None`
  - `resolve_workspace(cwd_raw, projdir, marker_dirs, cd_targets, repo_root) -> tuple[str, str, str, str]`
  - `scan_workspace(path) -> tuple[dict, set, collections.Counter]`
  - `reconcile(pending, component_depth) -> list[tuple]`
  - `bash_refs(command) -> tuple[list[str], list[str], list[str]]` — `(verbs, exes, paths)`

- [ ] **Step 1: Write the failing test**

```bash
cat > sidecar/app/test_analysis_paths.py <<'EOF'
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.paths import bash_refs, rel_within


def test_paths_read_the_full_command_and_commands_do_not():
    """A path inside a heredoc is a file the embedded script really touches; a COMMAND inside one
    is source code. Stripping heredocs for both emptied the artifact and subsystem slots for an
    hour of pptx editing."""
    cmd = ("python3 - <<PY\nimport os\nPY\nls unpacked-user/ppt/slides/slide2.xml")
    verbs, exes, paths = bash_refs(cmd)
    assert exes == ["python3", "ls"], exes
    assert "unpacked-user/ppt/slides/slide2.xml" in paths, paths


def test_a_quoted_path_is_not_torn_at_its_spaces():
    """Splitting on whitespace made `.../Application Support/.../soffice.py` arrive as
    `Support/Claude/.../soffice.py`, resolve under the repo root, and take 60% of a working set —
    the harness's own skill scripts presented as the work."""
    _, _, paths = bash_refs('python3 "/home/x/Application Support/Claude/skills/s.py"')
    assert not any(p.startswith("Support/") for p in paths), paths


def test_rel_within_rejects_a_path_outside_the_root():
    assert rel_within("/etc/passwd", "/home/dg/repo", "/home/dg/repo") is None


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
EOF
```

- [ ] **Step 2: Run it to verify it fails**

Run: `~/.keld/sidecar-venv/bin/python sidecar/app/test_analysis_paths.py`
Expected: `ModuleNotFoundError`

- [ ] **Step 3: Move the code**

Move verbatim into `paths.py`: `PATH_TOKEN` (66), `plausible_path`, `launch_dir`, `contains`,
`resolve_workspace`, `repo_of`, `_git_root`, `rel_within`, `scan_workspace`, `reconcile`,
`bash_refs` (630). Keep every comment — `resolve_workspace`'s explain the 87.7% fix and
`reconcile`'s explain why the scope is a machine and not a checkout.

- [ ] **Step 4: Run the test**

Run: `~/.keld/sidecar-venv/bin/python sidecar/app/test_analysis_paths.py`
Expected: `3 passed`

- [ ] **Step 5: The identity gate**

```bash
~/.keld/study-venv/bin/python scripts/test_refseries.py
~/.keld/study-venv/bin/python scripts/test_bashrefs.py
rm -rf /tmp/rs-t5 && mkdir -p /tmp/rs-t5
~/.keld/study-venv/bin/python scripts/refseries.py extract --outdir /tmp/rs-t5 \
  --roots ~/.claude/projects /tmp/john-projects >/dev/null
~/.keld/study-venv/bin/python scripts/check_identity.py verify /tmp/identity-base.json /tmp/rs-t5
```

Expected: `IDENTICAL`. **Any difference in `file`, `dir`, `component`, `workspace` or `branch`
means `resolve_workspace` or `reconcile` changed behaviour — stop and diff, do not proceed.**

- [ ] **Step 6: Commit**

```bash
git add sidecar/app/analysis/paths.py sidecar/app/test_analysis_paths.py scripts/refseries.py
git commit -m "analysis: extract path and workspace resolution into app.analysis.paths

The riskiest move: resolve_workspace carries the fix that took branch resolution
from 56.3% to 87.7%, and reconcile is cross-file. bash_refs comes along now that
both its halves have a home. Corpus rebuild byte-identical."
```

---

### Task 6: `transcript.py` and `levels.py` — splitting the 150-line body

`_process_transcript` currently opens a file, parses JSON, resolves paths, classifies artifacts
and emits events in one body. This is the split that makes the core testable without fixtures.

**Files:**
- Create: `sidecar/app/analysis/transcript.py`
- Create: `sidecar/app/analysis/levels.py`
- Modify: `scripts/refseries.py` — `_process_transcript` becomes a thin wrapper

**Interfaces:**
- Consumes: `vocab.*`, `paths.*`, `shell.*`, `text.*`, `terms.tally`
- Produces:
  - `transcript.iter_turns(path: str) -> Iterator[dict]` — parsed user/assistant lines, tool_result
    lines skipped (that is what keeps this a seconds-long parse)
  - `transcript.turns_between(path, start, end) -> list[dict]`
  - `levels.LEVELS: list[str]`
  - `levels.events_for_turns(turns, path, root, repo_root, nlp=None) -> tuple[list, list, int]`
    returning `(rows, pending, n_lines)` with the same row tuple shape as today:
    `(t, session, repo, branch, side, kind, level, ref, n)`

- [ ] **Step 1: Write the failing test**

```bash
cat > sidecar/app/test_analysis_levels.py <<'EOF'
import sys, os, json, tempfile
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.transcript import iter_turns
from app.analysis.levels import events_for_turns


def _write(tmp, lines):
    p = os.path.join(tmp, "abcd1234-0000.jsonl")
    with open(p, "w") as fh:
        for o in lines:
            fh.write(json.dumps(o) + "\n")
    return p


def test_tool_result_lines_are_skipped():
    """A tool_result carries no speech and no reference, and is where the huge lines are.
    Skipping it is what keeps this a seconds-long parse."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [
            {"type": "user", "timestamp": "2026-08-01T00:00:00Z", "cwd": "/x",
             "message": {"content": [{"type": "tool_result", "content": "huge"}]}},
            {"type": "user", "timestamp": "2026-08-01T00:00:01Z", "cwd": "/x",
             "message": {"content": [{"type": "text", "text": "hello"}]}},
        ])
        assert len(list(iter_turns(p))) == 1


def test_events_carry_the_expected_row_shape():
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [{"type": "assistant", "timestamp": "2026-08-01T00:00:00Z", "cwd": "/x",
                          "gitBranch": "main", "message": {"model": "claude-opus-5",
                          "content": [{"type": "text", "text": "hi"}]}}])
        rows, pending, n = events_for_turns(list(iter_turns(p)), p, tmp, None)
        assert n == 1
        assert all(len(r) == 9 for r in rows), rows[:2]
        assert any(r[6] == "model" and r[7] == "claude-opus-5" for r in rows), rows


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
EOF
```

- [ ] **Step 2: Run it to verify it fails**

Run: `~/.keld/sidecar-venv/bin/python sidecar/app/test_analysis_levels.py`
Expected: `ModuleNotFoundError`

- [ ] **Step 3: Split the body**

`transcript.py` takes the file open, the two `continue` guards and the `json.loads`. `levels.py`
takes everything from `t = pd.Timestamp(ts).timestamp()` onward — replacing that one pandas call
with stdlib, which is what makes the package pandas-free:

```python
from datetime import datetime

def _epoch(ts: str) -> float:
    """The one pandas call in the extraction path, replaced. `fromisoformat` handles the trailing
    Z from Python 3.11 on; the sidecar venv is 3.12."""
    return datetime.fromisoformat(ts.replace("Z", "+00:00")).timestamp()
```

- [ ] **Step 4: Run the test**

Run: `~/.keld/sidecar-venv/bin/python sidecar/app/test_analysis_levels.py`
Expected: `2 passed`

- [ ] **Step 5: The identity gate**

The timestamp change is the risk: `pd.Timestamp(...).timestamp()` and `datetime.fromisoformat`
must agree to the rounding used (`round(t, 1)`).

```bash
rm -rf /tmp/rs-t6 && mkdir -p /tmp/rs-t6
~/.keld/study-venv/bin/python scripts/refseries.py extract --outdir /tmp/rs-t6 \
  --roots ~/.claude/projects /tmp/john-projects >/dev/null
~/.keld/study-venv/bin/python scripts/check_identity.py verify /tmp/identity-base.json /tmp/rs-t6
```

Expected: `IDENTICAL`. A difference in row count with unchanged levels means timestamp parsing
diverged — compare `_epoch` against `pd.Timestamp` on the first 1000 timestamps before proceeding.

- [ ] **Step 6: Commit**

```bash
git add sidecar/app/analysis/transcript.py sidecar/app/analysis/levels.py \
        sidecar/app/test_analysis_levels.py scripts/refseries.py
git commit -m "analysis: split transcript I/O from level extraction

_process_transcript opened a file, parsed JSON, resolved paths, classified
artifacts and emitted events in one 150-line body. transcript.py is now the only
module in the package that does I/O. The single pandas call is replaced with
datetime.fromisoformat, which makes the package pandas-free."
```

---

### Task 7: `window.py` and `workstreams.py` — the rollup

**Files:**
- Create: `sidecar/app/analysis/window.py`
- Create: `sidecar/app/analysis/workstreams.py`
- Create: `sidecar/app/test_analysis_window.py`
- Modify: `scripts/workstreams.py` — becomes a CLI over the package

**Interfaces:**
- Consumes: `levels.events_for_turns`
- Produces:
  - `window.rollup(rows) -> dict[str, list[tuple[str, float]]]` — per level, `(ref, total)`
    descending
  - `window.dominant(rollup, level, floor=0.5) -> tuple[str | None, float, int]`
  - `workstreams.ALLOCATION: list[tuple[str, str, float]]`, `workstreams.INVENTORY`,
    `workstreams.LOOPBACK`
  - `workstreams.payload(rollup) -> dict` — `{"workstreams": {...}, "inventory": {...}}`

- [ ] **Step 1: Write the failing test**

```bash
cat > sidecar/app/test_analysis_window.py <<'EOF'
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.window import rollup, dominant
from app.analysis.workstreams import payload

R = [(0, "s", "r", "b", False, "ref", "artifact", "code", 9.0),
     (0, "s", "r", "b", False, "ref", "artifact", "prose", 1.0),
     (0, "s", "r", "b", False, "ref", "lang", "Go", 5.0),
     (0, "s", "r", "b", False, "ref", "lang", "Python", 5.0),
     (0, "s", "r", "b", False, "ref", "service", "127.0.0.1", 20.0),
     (0, "s", "r", "b", False, "ref", "service", "github.com", 2.0)]


def test_a_dominant_value_is_reported_with_its_share():
    assert dominant(rollup(R), "artifact")[:2] == ("code", 0.9)


def test_a_tie_is_unattributed_rather_than_an_arbitrary_pick():
    """Multi-label double-counts spend, and a silently chosen winner is the
    plausible-wrong-number failure this work hit roughly twenty times."""
    v, share, _ = dominant(rollup(R), "lang")
    assert v is None and share == 0.5


def test_an_absent_level_produces_no_key_rather_than_an_empty_one():
    assert payload(rollup(R))["workstreams"]["workflow"] is None


def test_loopback_is_not_an_external_system():
    """127.0.0.1 and localhost are 85% of the raw service level and would otherwise be the top
    'system this org depends on'."""
    ext = payload(rollup(R))["inventory"]["external_systems"]
    assert [e["value"] for e in ext] == ["github.com"], ext


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
EOF
```

- [ ] **Step 2: Run it to verify it fails**

Run: `~/.keld/sidecar-venv/bin/python sidecar/app/test_analysis_window.py`
Expected: `ModuleNotFoundError`

- [ ] **Step 3: Implement with `collections.Counter`, not pandas**

```python
import collections

def rollup(rows):
    """Per level, (ref, total) descending. Counter rather than pandas: the package must stay
    dependency-light, and a window is a few thousand rows."""
    by = collections.defaultdict(collections.Counter)
    for r in rows:
        if r[5] != "ref":
            continue
        by[r[6]][r[7]] += r[8]
    return {lv: c.most_common() for lv, c in by.items()}


def dominant(rl, level, floor=0.5):
    items = rl.get(level) or []
    if not items:
        return None, 0.0, 0
    total = sum(n for _, n in items)
    value, top = items[0]
    share = top / total
    return (value if share >= floor else None), share, int(total)
```

Port `ALLOCATION`, `INVENTORY`, `LOOPBACK` and the payload assembly from `scripts/workstreams.py`
verbatim, keeping the comments that record why the floor is 0.5 and why loopback is excluded.

- [ ] **Step 4: Run the test**

Run: `~/.keld/sidecar-venv/bin/python sidecar/app/test_analysis_window.py`
Expected: `4 passed`

- [ ] **Step 5: Verify the payload is unchanged against the committed sample**

```bash
~/.keld/study-venv/bin/python scripts/workstreams.py --outdir /tmp/rs-base \
  --out /tmp/workstreams-new.ndjson
diff <(sort /tmp/workstreams-new.ndjson) \
     <(sort ~/keld/refseries-context/workstreams.ndjson) && echo "PAYLOAD IDENTICAL"
```

Expected: `PAYLOAD IDENTICAL`

- [ ] **Step 6: Commit**

```bash
git add sidecar/app/analysis/window.py sidecar/app/analysis/workstreams.py \
        sidecar/app/test_analysis_window.py scripts/workstreams.py
git commit -m "analysis: window rollup and the workstream payload

Counter, not pandas — the package stays dependency-light. A tie is unattributed
rather than an arbitrary pick, and loopback is excluded from external systems
(127.0.0.1 and localhost are 85% of the raw service level). Payload verified
byte-identical to the committed sample."
```

---

### Task 8: Packaging — make the subpackage survive the freeze

`keld-agent-sidecar.spec` globs `app/*.py` for PyArmor hiddenimports and will **not** see
`app/analysis/*.py`. This fails only in the obfuscated build — invisible to every unit test, the
same class of defect `freeze_support()` was.

**Files:**
- Modify: `sidecar/keld-agent-sidecar.spec:26-31`

**Interfaces:**
- Consumes: the package from Tasks 2-7.

- [ ] **Step 1: Make the hiddenimports glob recursive**

Replace the `KELD_OBFUSCATE` block's inner loop:

```python
if os.environ.get("KELD_OBFUSCATE") == "1":
    import glob
    hiddenimports.append("app")
    for f in glob.glob(os.path.join(_here, "app", "**", "*.py"), recursive=True):
        rel = os.path.relpath(f, _here)
        mod = os.path.splitext(rel)[0].replace(os.sep, ".")
        base = os.path.basename(f)
        if base == "__init__.py":
            mod = mod.rsplit(".", 1)[0]
        if not base.startswith("test_"):
            hiddenimports.append(mod)
```

- [ ] **Step 2: Collect spaCy and its model**

Add `en_core_web_sm` and `spacy` to the `collect_all` loop:

```python
for pkg in ("torch", "gliner2", "transformers", "tokenizers", "safetensors",
            "huggingface_hub", "spacy", "en_core_web_sm", "wordfreq", "bashlex"):
```

- [ ] **Step 3: Verify the plain freeze**

Run: `make freeze-check`
Expected: PASS, including a real `/classify` worker spawn.

- [ ] **Step 4: Verify the obfuscated freeze**

Run: `make obfuscate-check`
Expected: PASS. This is the run that would catch a missing `app.analysis.*` hiddenimport.

- [ ] **Step 5: Prove the analysis package works inside the frozen binary**

`wordfreq` ships data files and was **not** covered by the earlier spaCy freeze test, so assert it
explicitly rather than assuming `collect_all` caught it:

```bash
dist/keld-agent-sidecar/keld-agent-sidecar --port 39777 &
sleep 20
curl -s -X POST http://127.0.0.1:39777/analyze -H 'content-type: application/json' \
  -d '{"session":"test","path":"/dev/null","from":"2026-01-01T00:00:00Z","to":"2026-01-01T01:00:00Z","schema":1}'
kill %1
```

Expected: a JSON payload with `workstreams` and `inventory` keys, not a traceback naming
`en_core_web_sm` or `wordfreq`.

- [ ] **Step 6: Commit**

```bash
git add sidecar/keld-agent-sidecar.spec
git commit -m "packaging: collect app.analysis, spacy, wordfreq and bashlex into the freeze

The hiddenimports glob was app/*.py and would not see app/analysis/*.py — a
failure that appears only in the obfuscated build, invisible to unit tests, the
same class as freeze_support(). wordfreq ships data files and was not covered by
the earlier spaCy freeze verification, so the check asserts it explicitly."
```

---

## Not in this plan

- **`match.py`** — new code, not a migration. Spec'd in
  `2026-08-22-configured-vocabulary-matching-design.md`; its own plan.
- **The `/analyze` endpoint and the Go client** — the analysis tier's wiring, phase 2 of
  `2026-08-22-sidecar-analysis-tier-design.md`. Task 8 Step 5 assumes it exists; if it does not
  yet, substitute a direct `python -c "from app.analysis import workstreams"` import check inside
  the frozen binary.
- **Baselines and lift** — needs persisted per-machine history; phase 3.
- **Digest prose** — no production consumer; phase 4.
- **Episode detection** — deliberately dropped. It reached only parity with a fixed 60min/50min
  grid at much higher complexity.
