"""Draw a labelling sample and render it BLIND.

Shows filename and the first 40 lines, and nothing else -- no scores, no candidate types, no
signature table. The labeller sees what a person would see opening the file.
"""
import json, random, sys

ROWS = [json.loads(l) for l in open("/tmp/claude-1000/doctype/docs.ndjson")]
N = int(sys.argv[1]) if len(sys.argv) > 1 else 60
random.Random(20261001).shuffle(ROWS)

for r in ROWS[:N]:
    print(f"===== {r['id']}  ({r['nlines']} lines, {r['ext']}) =====")
    print(f"path: {r['path']}")
    print(r["head"])
    print()
