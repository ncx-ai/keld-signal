#!/usr/bin/env python3
"""Write docs/signal-file-kinds.json from `sidecar/app/analysis/filekinds.py`.

    PYTHONPATH=sidecar ~/.keld/sidecar-venv/bin/python scripts/gen_file_kinds.py

Atlas pins a golden test against the output. `test_filekinds.py` asserts the committed file
equals `build()`, so it cannot drift from the live tables. Not hand-edited.
"""
import json
import os
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(REPO, "sidecar"))

from app.analysis import SCHEMA, filekinds as F  # noqa: E402

OUT = os.path.join(REPO, "docs", "signal-file-kinds.json")


def build():
    return {
        "schema": SCHEMA,
        "groups": list(F.GROUPS),
        "action_labels": dict(F.ACTION_LABELS),
        "kinds": [{"id": k, "display": d, "phrase": F.phrase_for(k), "group": g}
                  for k, (d, g) in F.KINDS.items()],
    }


if __name__ == "__main__":
    with open(OUT, "w") as fh:
        json.dump(build(), fh, indent=2, ensure_ascii=False)
        fh.write("\n")
    print(f"wrote {OUT} ({len(build()['kinds'])} kinds)")
