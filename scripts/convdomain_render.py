"""Render conversations for blind labelling.

⚠️ PRINTS NO STRATUM AND NO KEYWORD HITS. A labeller who knows a conversation was selected by
a legal keyword will find legal in it. The frame carries both fields; this deliberately does
not read them.
"""
import json, sys

ROWS = [json.loads(l) for l in open("/tmp/claude-1000/convdomain/frame.ndjson")]
LIMIT = int(sys.argv[1]) if len(sys.argv) > 1 else 4000

for r in ROWS:
    print(f"===== {r['id']}  ({r['turns']} turns) =====")
    print(r["text"][:LIMIT])
    if len(r["text"]) > LIMIT:
        print(f"... [{len(r['text']) - LIMIT} more chars]")
    print()
