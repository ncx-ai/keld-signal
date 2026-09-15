#!/usr/bin/env python3
"""AC-2: the record refactor changes no store row.

    cd sidecar && PYTHONPATH=. python3.12 app/test_reader_golden.py

`scripts/dump_store_rows.py` was committed BEFORE a line of the record was written (see its
docstring, and the commit that carries it alone). This runs it in `--check` mode: it re-ingests
every committed fixture through `ingest.ingest_file` and compares `event`, `bin`, `prompt` and
`turn_magnitude` row for row against that dump. **Zero rows may differ.**

⚠️ THE DUMP IS THE ORACLE AND IT IS NOT REGENERATED FROM HERE. If this fails, the refactor moved
a row; the answer is to find out which and why, never to re-run the dump without `--check`.
Regenerating it is only ever correct for a deliberate change of what is computed, and then in its
own commit, with the diff as the evidence — the objection the discovery page answers under "Isn't
the golden dump just freezing today's bugs?": yes, and that is the point.

Run as a subprocess rather than imported, because the dump script sets `KELD_CAPTURE` and the
retention horizons in `os.environ` and is meant to own that process.
"""
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

SIDECAR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
REPO = os.path.dirname(SIDECAR)
SCRIPT = os.path.join(REPO, "scripts", "dump_store_rows.py")
GOLDEN = os.path.join(REPO, "docs", "superpowers", "specs", "golden")


def test_the_golden_dump_exists_and_is_not_empty():
    """A missing or empty dump would make the check below pass vacuously."""
    for table in ("event", "bin", "prompt", "turn_magnitude"):
        p = os.path.join(GOLDEN, f"turn-record-{table}.jsonl")
        assert os.path.exists(p), f"missing golden dump: {p}"
        n = sum(1 for _ in open(p))
        assert n > 0, f"golden dump is empty: {p} — it cannot prove anything"


def test_store_rows_equal_the_golden_dump():
    env = dict(os.environ, PYTHONPATH=SIDECAR)
    r = subprocess.run([sys.executable, SCRIPT, "--check"], cwd=REPO, env=env,
                       capture_output=True, text=True)
    assert r.returncode == 0, (
        "store rows differ from the pre-refactor golden dump (AC-2):\n"
        + r.stdout + r.stderr)


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
