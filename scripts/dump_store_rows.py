#!/usr/bin/env python3
"""The GOLDEN DUMP: every store row the committed fixture transcripts produce, as NDJSON.

    PYTHONPATH=sidecar python3.12 scripts/dump_store_rows.py            # rewrite the dump
    PYTHONPATH=sidecar python3.12 scripts/dump_store_rows.py --check    # compare, exit 1 on drift

WHY THIS EXISTS. The normalised turn record (`sidecar/app/analysis/readers/`) moves six modules
off Claude Code's own field names and onto one record. That refactor is meant to change WHERE the
extraction reads from and nothing else — so the only proof worth having is a row-for-row
comparison against the answer taken BEFORE a line of it was written. This script produces that
answer, and it is committed in its own commit, ahead of the refactor, so the comparison cannot
quietly be taken after the fact (spec AC-2,
`docs/superpowers/specs/2026-09-14-normalised-turn-record-discovery.html`).

It is deliberately NOT a rollup. `window.rollup` sums, and a difference that cancels across two
bins would survive it; these are the raw rows.

WHAT IS NORMALISED, AND WHY EACH IS SAFE

  * `session` — the store keys on `ingest.session_of`, a sha256 of the transcript's ABSOLUTE
    path, so the raw value differs between two checkouts of this repo and would swamp every real
    difference. It is replaced by the fixture's own `<projdir>/<file>` name, which is what the
    path means. Every store built here holds exactly ONE transcript and that is ASSERTED rather
    than assumed, so a row landing under a foreign key is a failure here, never a value that gets
    quietly renamed (the idiom is `app/test_ingest.py::_dump`'s, for the same reason).
  * `source_line` — the batch ordinal. Every ingest below is a single whole-file pass, so it is a
    constant; it is dropped rather than compared, exactly as the chunk-equivalence tests drop it.

WHAT IS HELD FIXED

  * `nlp=None` — the `term` level falls back to `terms.SHAPES`, which is pure regex and identical
    on every machine. Passing a spaCy pipeline would make the dump depend on which model version
    happens to be installed, which is the machine-dependence `levels._epoch` refuses elsewhere.
  * Retention, OFF. `ingest_file` runs the retention sweep, and the sweep is relative to NOW:
    the `term` level expires at 90 days and `turn_magnitude` is swept to the resulting serving
    floor. A fixture transcript carries fixed timestamps, so a dump taken today and a dump taken
    three months from now would differ by the whole magnitude table with nothing about the code
    having changed — measured here: the golden fixture, dated 2026-03-02, lost all 43 of its
    magnitude rows and 4 term rows to a sweep on the day it was written. The horizons are
    therefore pinned to a value no fixture can reach, so the dump is a function of the code and
    the fixtures alone. (The same exposure exists for `check-fixture-identity.sh`, whose corpus
    is dated 2026-08-10 and so is 54 days from silently losing its own `term` rows; that is its
    gate to fix, not this one's.)
  * Both capture modes. `KELD_CAPTURE` is fingerprinted into the parse state and gates the
    `capture.scan` pass, so it selects a genuinely different set of `turn_magnitude` rows; each
    row carries the `mode` it was produced under, so the dump covers both rather than whichever
    the author's shell happened to have set.
"""
import argparse
import json
import os
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "sidecar"))


def _require_optional_deps():
    """⚠️ **TWO OPTIONAL IMPORTS SILENTLY CHANGE WHAT IS STORED, AND THE FIRST DUMP WAS TAKEN
    WITHOUT THEM.**

    This file's header claims the dump is "a function of the code and the fixtures alone". That
    was not true, and CI is where it showed: the dump was generated with the HOST python3, which
    has neither package, while CI installs `sidecar/requirements.txt`, which pins both. Measured
    on the first CI run of this gate:

      * `bashlex` (app/analysis/shell.py) parses command strings. Absent, the parse is degraded
        and fewer programs are extracted — CI produced `exe:pip`, `exe:pytest`, `exe:sh` and
        `action:install` rows the dump did not have.
      * `wordfreq` (app/analysis/terms.py) is the SHOUTING filter, and its own comment says
        "without it, shouting is simply not filtered". Absent, `TOP` (zipf 5.6) survives as a
        named term — the dump carried a `term:TOP` row that CI correctly drops.

    So the oracle froze a DEGRADED environment, and a gate that pins the wrong baseline defends
    the wrong thing. Both are real dependencies of the SHIPPED sidecar, so the dump is taken with
    them present, and their absence is a hard refusal rather than a quieter answer — the same
    rule the header already applies to spaCy and the retention horizons, which it holds fixed
    precisely so the dump cannot depend on the machine.
    """
    missing = []
    for mod, why in (("bashlex", "command parsing (exe/action levels)"),
                     ("wordfreq", "the shouting filter (term level)")):
        try:
            __import__(mod)
        except ImportError:
            missing.append(f"{mod} — {why}")
    if missing:
        sys.exit(
            "REFUSING: this dump would not be reproducible.\n  missing: "
            + "\n           ".join(missing)
            + "\n\nBoth are pinned in sidecar/requirements.txt and both CHANGE WHICH ROWS ARE"
            "\nSTORED, so a dump taken without them freezes a degraded environment and the"
            "\ncheck then fails on every machine that has them (which is what CI found)."
            "\n\nRun it with the sidecar venv, not the host python3:"
            "\n  scripts/conformance/analysis-venv.sh <dir>   # prints a usable interpreter"
        )


_require_optional_deps()

# Two fixture trees, and the second exists because the first cannot answer the question.
#
#   fixture-corpus/ — the committed identity floor (`build_fixture_corpus.py`). Two invented
#     sessions. It carries NO `uuid` and NO `promptId` on any line, which is the very shape
#     AGENTS.md records as having hidden the uuid-only index bug ("every sidecar fixture built a
#     user turn with no promptId at all"), so over it alone the `prompt` table dumps EMPTY and a
#     golden comparison of it proves nothing about how a turn is named.
#
#   golden/ — added with this dump, for that reason. One invented session that exercises every
#     Claude field the record has to carry and the corpus above does not: `uuid`, `promptId`
#     (including a CONTINUATION line sharing one, so `upsert_prompts`' first-wins rule is
#     observable), `isSidechain`, `attributionSkill`, `attributionMcpServer`/`Tool`, thinking
#     blocks, a `tool_result` line (which only `capture.scan` ever reads), a bookkeeping record
#     with no top-level timestamp, a bare-string message content, a command echo, and a request
#     written across two assistant lines. Every name and path in it is invented and rooted at
#     /workspace/fixture-golden/, a prefix that cannot exist on a dev or CI machine — the same
#     rule `build_fixture_corpus.py` states for its own.
#
# It is deliberately NOT inside fixture-corpus/: `scripts/check-fixture-identity.sh` fingerprints
# that directory against a committed baseline, and a third session there would break that gate
# for a reason unrelated to identity.
FIXTURE_ROOTS = (
    os.path.join(ROOT, "sidecar", "app", "analysis", "testdata", "fixture-corpus", "projects"),
    os.path.join(ROOT, "sidecar", "app", "analysis", "testdata", "golden", "projects"),
)
GOLDEN = os.path.join(ROOT, "docs", "superpowers", "specs", "golden")

# The four tables the record refactor could possibly move a row in: what was extracted (`event`),
# what was precomputed from it (`bin`), how a turn is named (`prompt`) and what it cost
# (`turn_magnitude`). `ingest`/`parse_state` are checkpoints, not extraction, and carry absolute
# paths; `bin_offset` is a byte position whose columns say nothing about the reader.
TABLES = ("event", "bin", "prompt", "turn_magnitude")

# Column lists, written out rather than `SELECT *`, so that a column ADDED to one of these tables
# does not silently rewrite the dump on the next run.
COLUMNS = {
    "event": ("ts", "level", "ref", "n"),
    "bin": ("bin_ts", "level", "ref", "n"),
    "prompt": ("prompt_id", "ts"),
    "turn_magnitude": ("ts", "kind", "value"),
}


def fixtures():
    """`(name, path)` for every committed fixture transcript, in a stable order."""
    out = []
    for root in FIXTURE_ROOTS:
        for projdir in sorted(os.listdir(root)):
            d = os.path.join(root, projdir)
            if not os.path.isdir(d):
                continue
            for fname in sorted(x for x in os.listdir(d) if x.endswith(".jsonl")):
                out.append((f"{projdir}/{fname}", os.path.join(d, fname)))
    return out


def rows_for(path, name, capture):
    """Ingest `path` in ONE pass into a scratch store and return its rows as dicts."""
    from app.analysis.ingest import ingest_file, session_of
    from app.analysis.store import open_store

    os.environ["KELD_CAPTURE"] = "1" if capture else "0"
    # See the module docstring: a horizon no fixture timestamp can fall behind.
    os.environ["KELD_REFSERIES_RETAIN_DAYS"] = "1000000"
    os.environ["KELD_REFSERIES_TERM_RETAIN_DAYS"] = "1000000"
    mode = "capture" if capture else "plain"
    out = []
    with tempfile.TemporaryDirectory() as tmp:
        store = open_store(os.path.join(tmp, "golden.db"))
        ingest_file(store, path, nlp=None)
        conn = store._conn()
        key = session_of(path)
        for table in TABLES:
            seen = {r[0] for r in conn.execute(f"SELECT DISTINCT session FROM {table}")}
            assert seen <= {key}, f"{table} holds a session that is not {name}'s: {seen - {key}}"
            cols = COLUMNS[table]
            sql = (f"SELECT {', '.join(cols)} FROM {table} "
                   f"ORDER BY {', '.join(cols)}")
            for r in conn.execute(sql):
                row = {"session": name, "mode": mode, "table": table}
                row.update(dict(zip(cols, r)))
                out.append(row)
        store.close() if hasattr(store, "close") else None
    return out


def dump():
    """Every fixture, both capture modes, one file per table, sorted and stable."""
    per_table = {t: [] for t in TABLES}
    for name, path in fixtures():
        for capture in (False, True):
            for row in rows_for(path, name, capture):
                per_table[row["table"]].append(row)
    written = {}
    os.makedirs(GOLDEN, exist_ok=True)
    for table, rows in per_table.items():
        # `sort_keys=True` inside the row AND a sort over the serialised lines: the file is a SET
        # of rows, and a dump whose order depended on dict insertion would report a difference
        # that is not one.
        lines = sorted(json.dumps(r, sort_keys=True) for r in rows)
        written[table] = "\n".join(lines) + ("\n" if lines else "")
    return written


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--check", action="store_true",
                    help="compare against the committed dump instead of rewriting it")
    args = ap.parse_args()

    written = dump()
    drift = []
    for table, body in written.items():
        p = os.path.join(GOLDEN, f"turn-record-{table}.jsonl")
        if args.check:
            have = open(p).read() if os.path.exists(p) else ""
            if have != body:
                drift.append((p, have, body))
            continue
        with open(p, "w") as fh:
            fh.write(body)
        print(f"{p}: {body.count(chr(10))} rows")
    if drift:
        for p, have, body in drift:
            print(f"DRIFT {p}", file=sys.stderr)
            a, b = have.splitlines(), body.splitlines()
            for line in list(_diff(a, b))[:40]:
                print("  " + line, file=sys.stderr)
        return 1
    if args.check:
        print("golden dump matches")
    return 0


def _diff(a, b):
    import difflib
    return difflib.unified_diff(a, b, "committed", "current", lineterm="", n=0)


if __name__ == "__main__":
    sys.exit(main())
