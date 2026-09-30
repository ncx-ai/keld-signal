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


def is_code_work_by_class(activity_classes):
    """Independent control: does tool-call evidence mark this as code work?

    Returns True only when 'author_code' is the SINGLE LARGEST value in the
    activity_classes distribution. Ties result in False.
    Blocks with empty activity_classes are excluded from this control.
    """
    if not activity_classes:
        return None  # Exclude from control

    # Find the maximum value
    max_val = max(activity_classes.values())
    # Count how many keys have that max value
    max_count = sum(1 for v in activity_classes.values() if v == max_val)

    # True only if author_code is sole maximum
    return activity_classes.get("author_code", 0) == max_val and max_count == 1


def main():
    rows = [json.loads(l) for l in open(FRAME)]

    # --- CIRCULAR CONTROL (file extensions) ---
    by = collections.defaultdict(lambda: [0, 0])
    for r in rows:
        k = kind(r["file_types"])
        by[k][0] += 1
        if tier_a(r["file_types"]):
            by[k][1] += 1

    print("="*70)
    print("CIRCULAR CONTROL (file extensions only)")
    print("="*70)
    print(f"{'artifact kind':14} {'blocks':>8} {'tier A fires':>13} {'rate':>7}")
    for k in ("code", "editorial", "neither"):
        n, f = by[k]
        print(f"  {k:12} {n:8,} {f:13,} {100*f/max(n,1):6.1f}%")

    code_rate = by["code"][1] / max(by["code"][0], 1)
    ed_rate = by["editorial"][1] / max(by["editorial"][0], 1)
    circular_ratio = code_rate/max(ed_rate,1e-9)
    print(f"\nCode {100*code_rate:.1f}% vs editorial {100*ed_rate:.1f}% — ratio {circular_ratio:.1f}x")
    print("\nBAR 1 (pre-registered, this file, before the numbers were read):")
    print("  tier A PASSES circular control if: >=80% of code blocks AND <=20% of editorial blocks.")
    circular_pass = code_rate >= 0.8 and ed_rate <= 0.2
    print("  RESULT:", "PASS" if circular_pass else "FAIL")

    # --- INDEPENDENT CONTROL (activity_classes from tool calls) ---
    print("\n" + "="*70)
    print("INDEPENDENT CONTROL (activity_classes — tool calls, not file extensions)")
    print("="*70)

    # Filter rows that have activity_classes
    rows_with_class = [r for r in rows if r.get("activity_classes")]
    empty_class_count = len(rows) - len(rows_with_class)
    print(f"Note: {empty_class_count} blocks have empty activity_classes and are excluded from this control.\n")

    by_class = collections.defaultdict(lambda: [0, 0])
    for r in rows_with_class:
        k = is_code_work_by_class(r["activity_classes"])
        if k is not None:  # Only count blocks with a valid classification
            k_label = "code" if k else "non-code"
            by_class[k_label][0] += 1
            if tier_a(r["file_types"]):
                by_class[k_label][1] += 1

    print(f"{'class label':14} {'blocks':>8} {'tier A fires':>13} {'rate':>7}")
    for k in ("code", "non-code"):
        n, f = by_class[k]
        print(f"  {k:12} {n:8,} {f:13,} {100*f/max(n,1):6.1f}%")

    class_code_rate = by_class["code"][1] / max(by_class["code"][0], 1)
    class_non_rate = by_class["non-code"][1] / max(by_class["non-code"][0], 1)
    class_ratio = class_code_rate/max(class_non_rate,1e-9)
    print(f"\nCode {100*class_code_rate:.1f}% vs non-code {100*class_non_rate:.1f}% — ratio {class_ratio:.1f}x")
    print("\nBAR 2 (pre-registered, this file, before the numbers were read):")
    print("  tier A PASSES independent control if: >=70% of class-code blocks AND <=30% of class-non-code blocks.")
    class_pass = class_code_rate >= 0.7 and class_non_rate <= 0.3
    print("  RESULT:", "PASS" if class_pass else "FAIL")

    # --- CONTROL AGREEMENT ---
    print("\n" + "="*70)
    print("CONTROL AGREEMENT")
    print("="*70)
    agreement = 0
    for r in rows_with_class:
        k_ext = kind(r["file_types"])
        k_class = is_code_work_by_class(r["activity_classes"])
        if k_class is not None:
            k_class_label = "code" if k_class else "editorial"
            if (k_ext == "code" and k_class_label == "code") or (k_ext != "code" and k_class_label == "editorial"):
                agreement += 1

    total_comparable = len(rows_with_class)
    agreement_rate = agreement / max(total_comparable, 1)
    print(f"Agreement on comparable rows: {agreement}/{total_comparable} = {100*agreement_rate:.1f}%")
    print(f"Disagreement: {total_comparable - agreement}/{total_comparable} = {100*(1-agreement_rate):.1f}%")


if __name__ == "__main__":
    main()
