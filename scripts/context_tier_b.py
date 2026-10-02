"""Tier B: coverage. How often does a system category that MAPS to a context fire
on a real block?

⚠️ THE EXPECTED ANSWER IS "RARELY", AND THAT IS THE FINDING, NOT A FAILURE OF THE
STUDY. Both corpora are engineering work; the systems they touch are Notion,
GitHub and cloud providers, none of which maps to a context. A near-zero number
here does not mean tier B is wrong -- it is a lookup and cannot be wrong -- it
means its coverage is UNMEASURABLE on the data available, exactly as
`system_categories`' own coverage is. The decision that number informs is whether
tier B is worth shipping NOW or waits for a customer corpus.
"""
import json, collections

FRAME = "/tmp/claude-1000/ctx/frame.ndjson"

# The seven mapped categories. Everything else contributes NOTHING -- listed
# explicitly so adding a system category cannot silently start publishing a context.
CROSSWALK = {
    "legal_contracts": "legal",
    "finance_billing": "financial",
    "crm_sales": "sales",
    "support": "support",
    "marketing": "marketing",
    "medical": "medical",
    "scientific": "scientific",
}
UNMAPPED = ("knowledge_base", "code_hosting", "ci_cd", "cloud_infra", "data_platform",
            "observability", "security_iam", "design", "analytics_bi", "scheduling",
            "storage_files", "ecommerce", "ai_ml", "issue_tracking", "communication",
            "unrecognized")


def tier_b(system_categories):
    """The contexts this block's systems imply. Empty when none map."""
    return {CROSSWALK[c] for c in system_categories if c in CROSSWALK}


def main():
    rows = [json.loads(l) for l in open(FRAME)]
    fired = [r for r in rows if tier_b(r["system_categories"])]
    anysys = [r for r in rows if r["system_categories"]]

    print(f"blocks total                     : {len(rows):,}")
    print(f"blocks touching ANY system       : {len(anysys):,} "
          f"({100*len(anysys)/max(len(rows),1):.1f}%)")
    print(f"blocks where a MAPPED system fires: {len(fired):,} "
          f"({100*len(fired)/max(len(rows),1):.2f}%)")

    ctx = collections.Counter()
    for r in fired:
        ctx.update(tier_b(r["system_categories"]))
    print("\ncontexts reached:")
    for k, v in ctx.most_common() or [("(none)", 0)]:
        print(f"  {k:12} {v:6,}")

    seen = collections.Counter()
    for r in anysys:
        seen.update(r["system_categories"])
    print("\nsystem categories actually seen, and whether they map:")
    for k, v in seen.most_common(15):
        print(f"  {k:18} {v:6,}  {'-> ' + CROSSWALK[k] if k in CROSSWALK else '(contributes nothing)'}")

    print("\n⚠️ INTERPRETATION, fixed before the numbers were read:")
    print("  A low rate is a COVERAGE statement about these corpora, not a correctness")
    print("  statement about the crosswalk. It decides ship-now vs wait-for-a-corpus.")


if __name__ == "__main__":
    main()
