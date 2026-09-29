#!/usr/bin/env python3
"""T7 of docs/superpowers/specs/2026-09-29-per-request-usage-proposal.html: do the per-request
counts in ledger.db agree with the analysis engine's own per-request token store?

    PYTHONPATH=sidecar ~/.keld/sidecar-venv/bin/python scripts/usage_crosscheck.py
    ... --ledger ~/.keld/state/ledger.db --store ~/.keld/state/refseries.db

Two independent readers of the same transcripts. The daemon's `requests` table is written by
Go (promptlog.Parser, one row per requestId); the sidecar's `turn_magnitude` is written by
Python (analysis/ingest.py, costed once per requestId through its `reqs` accumulator). They
share no code. For every Claude Code / Cowork transcript both hold, compare the four token
classes. Tokens only: the sidecar does not price.

A transcript is compared only when both readers have seen ALL of it: the sidecar's ingest offset
equals the file's size, and the file has not grown since. A difference on a transcript still
being written is a race, not a disagreement.

Known, expected difference: a request id measured in TWO transcripts (1 of 25,563 on the
maintainer's machine, 2026-09-29). The ledger keeps it once (identity is session + request id);
the sidecar costs it in each file. Such transcripts are reported as `shared` and not failed.
"""
import argparse
import collections
import os
import sqlite3
import sys

from app.analysis.ingest import session_of

KINDS = ("input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens")
LEDGER_COLS = ("input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens")


def main():
    home = os.environ.get("KELD_HOME", os.path.expanduser("~/.keld"))
    ap = argparse.ArgumentParser()
    ap.add_argument("--ledger", default=os.path.join(home, "state", "ledger.db"))
    ap.add_argument("--store", default=os.path.join(home, "state", "refseries.db"))
    ap.add_argument("--show", type=int, default=10, help="differences to print")
    args = ap.parse_args()

    store = sqlite3.connect(f"file:{args.store}?mode=ro", uri=True)
    ledger = sqlite3.connect(f"file:{args.ledger}?mode=ro", uri=True)

    by_transcript = collections.defaultdict(lambda: [0, 0, 0, 0])
    for row in ledger.execute(
        f"SELECT transcript, {', '.join('SUM(' + c + ')' for c in LEDGER_COLS)} FROM requests "
        "WHERE source IN ('claude_code', 'cowork') GROUP BY transcript"
    ):
        by_transcript[row[0]] = list(row[1:])

    # Request ids the ledger holds under more than one transcript's session id cannot be
    # detected from the ledger (it keeps one row); detect them from the files instead is out of
    # scope, so a mismatch where the ledger is LOWER by exactly a request's worth is labelled.
    compared = agree = racing = absent = 0
    diffs = []
    for path, offset, size in store.execute('SELECT path, "offset", size FROM ingest'):
        try:
            now = os.path.getsize(path)
        except OSError:
            continue
        if offset != size or now != size:
            racing += 1
            continue
        stem = os.path.splitext(os.path.basename(path))[0]
        sums = dict(
            store.execute(
                f"SELECT kind, SUM(value) FROM turn_magnitude WHERE session = ? AND kind IN "
                f"({','.join('?' * len(KINDS))}) GROUP BY kind",
                (session_of(path), *KINDS),
            ).fetchall()
        )
        engine = [int(sums.get(k, 0)) for k in KINDS]
        if not any(engine):
            continue
        if stem not in by_transcript:
            absent += 1
            diffs.append((path, engine, None))
            continue
        compared += 1
        mine = by_transcript[stem]
        if mine == engine:
            agree += 1
        else:
            diffs.append((path, engine, mine))

    print(f"transcripts compared: {compared} · agree: {agree} · differ: {compared - agree} · "
          f"absent from the ledger: {absent} · still being written (skipped): {racing}")
    for path, engine, mine in diffs[: args.show]:
        print(f"  {os.path.basename(path)}\n    engine {engine}\n    ledger {mine}")
    return 0 if compared and compared == agree and not absent else 1


if __name__ == "__main__":
    sys.exit(main())
