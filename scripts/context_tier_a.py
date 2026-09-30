"""Tier A: does code-artifact dominance identify code work, and does it stay OFF
editorial work?

⚠️ THE CONTROL IS THE WHOLE POINT. Both corpora are ~100% engineering, so "tier A
fires a lot" proves nothing -- a rule that always fired would score perfectly.
What is measurable is DISCRIMINATION: corpus A contains real editorial work
(document/publishing sessions), and tier A must fire much less there. Measured
without that contrast, this task would report a number that cannot fail.
"""
import json, collections

FRAME = "/tmp/claude-1000/ctx/frame.ndjson"

# Code extensions, from the shipped vocabulary rather than invented here.
import os, sys
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "sidecar"))
from app.analysis.vocab import CODE_EXT

DOC_EXT = {".md", ".txt", ".rst", ".docx", ".pdf", ".org"}
DOMINANCE = 0.5     # the candidate threshold; Step 3 sweeps it


def tier_a(file_types, threshold=DOMINANCE):
    """True when this block's file evidence is DOMINATED by code.

    Dominance, not presence: a `package.json` edit inside a marketing site is one
    code file among many documents and must not make the block code work.
    """
    if not file_types:
        return False
    total = sum(file_types.values())
    code = sum(n for e, n in file_types.items() if e in CODE_EXT)
    return total > 0 and code / total >= threshold


def kind(file_types):
    """Ground-truth-ish label for the control: which KIND of artifact dominates."""
    code = sum(n for e, n in file_types.items() if e in CODE_EXT)
    doc = sum(n for e, n in file_types.items() if e in DOC_EXT)
    if code == 0 and doc == 0:
        return "neither"
    return "code" if code > doc else "editorial"


def main():
    rows = [json.loads(l) for l in open(FRAME)]
    by = collections.defaultdict(lambda: [0, 0])
    for r in rows:
        k = kind(r["file_types"])
        by[k][0] += 1
        if tier_a(r["file_types"]):
            by[k][1] += 1

    print(f"{'artifact kind':14} {'blocks':>8} {'tier A fires':>13} {'rate':>7}")
    for k in ("code", "editorial", "neither"):
        n, f = by[k]
        print(f"  {k:12} {n:8,} {f:13,} {100*f/max(n,1):6.1f}%")

    code_rate = by["code"][1] / max(by["code"][0], 1)
    ed_rate = by["editorial"][1] / max(by["editorial"][0], 1)
    print(f"\nDISCRIMINATION: code {100*code_rate:.1f}% vs editorial {100*ed_rate:.1f}%")
    print(f"  ratio {code_rate/max(ed_rate,1e-9):.1f}x")
    print("\nBAR (pre-registered, this file, before the numbers were read):")
    print("  tier A PASSES if it fires on >=80% of code blocks AND <=20% of editorial blocks.")
    print("  RESULT:", "PASS" if code_rate >= 0.8 and ed_rate <= 0.2 else "FAIL")


if __name__ == "__main__":
    main()
