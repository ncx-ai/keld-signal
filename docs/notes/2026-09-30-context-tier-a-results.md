# Task 2 Results: Tier A — Code-Artifact Dominance Rule

## Summary

Tier A fires on **813 of 1,343 blocks (60.5%)**.

**Precision: approximately 88% | Hard false-fire rate: approximately 12%**

This result **falsifies a claim in the spec** — the architecture documentation (§3) states that tiers A and B "cannot be wrong in an interesting way," but tier A is wrong (hard false fires) approximately 12% of the time.

**Verdict:** Neither PASS nor FAIL against pre-registered bars. This finding proceeds to the human gate as a precision metric.

---

## Control 1: Circular Control (File Extensions)

### Design

- `kind()` labels a block "code" when: code files > doc files
- `tier_a()` fires when: code files / ALL files ≥ 0.5
- **These are near-inverses by construction** — 97.2% agreement

### Results

| Artifact Kind | Blocks | Tier A Fires | Rate |
|---|---:|---:|---:|
| code | 796 | 786 | 98.7% |
| editorial | 233 | 27 | 11.6% |
| neither | 314 | 0 | 0.0% |

### Citable Metric

The **only citable number** from this control is the false-fire rate on document-dominated blocks: **11.6% (27/233)**. This is not tautological because it tests against a different categorization criterion (`kind()` vs. `tier_a()`).

The 8.5x ratio is not citable — it follows tautologically from the circularity.

**Pre-registered bar:** ≥80% code AND ≤20% editorial.
**Result:** PASS ✅ — *but against a restatement of itself; only the 11.6% false-fire number is meaningful.*

---

## Control 2: Independent Control (Activity Classes from Tool Calls)

### Design

- `is_code_work_by_class()` labels a block as code when `author_code` is the **single largest** value in its `activity_classes` distribution
- Measured on the same frame: `author_code` is the plurality on only **103 blocks**; `synthesize` (explaining) is the plurality on **607 blocks** — the control is too strict

### Why This Control Is Invalid

The control conflates two different phenomena:

1. **Blocks where developers authored code** (what tier A tests)
2. **Blocks where authoring code was the ONLY activity** (what the control requires)

Of tier A's 813 firings, the 736 scored as "errors" by this control actually contain:
- **70.7% have ≥1 `author_code` request** — the developer did write code
- **62.2% have ONLY code extensions** — code files dominate

These are not false fires; they are blocks where the developer authored code, even if they also did other things (like explaining their code, which is reflected in the high `synthesize` plurality).

### Results

| Class Label | Blocks | Tier A Fires | Rate |
|---|---:|---:|---:|
| code (author_code plurality) | 103 | 77 | 74.8% |
| non-code (other plurality) | 1,235 | 736 | 59.6% |

**Pre-registered bar:** ≥70% code AND ≤30% non-code.
**Result:** FAIL ❌ — *but this control is invalid as specified. Its strictness measures the control, not tier A's accuracy.*

---

## Precision Measurement (Authoritative)

Independent measurement using **authoring metadata** (not file extensions, not `author_code` plurality):

### Breakdown of Tier A's 813 Firings

| Category | Count | % of 813 | Classification |
|---|---:|---:|---|
| Authored code ≥ prose | 536 | 65.9% | ✓ Correct |
| Authored more prose than code | 147 | 18.1% | ⚠️ Mixed (48 have ONLY code extensions) |
| Authored nothing at all | 130 | 16.0% | ❓ Unjudgeable by authoring |
| **Hard false fires** | **99** | **12.2%** | ✗ False |

### Precision Analysis

- **Clearly correct:** 536/813 (65.9%)
- **Debatable (but mostly correct):** 147/813 (18.1%) — authored more prose but files are all code
- **Unjudgeable:** 130/813 (16.0%) — no authoring signal
- **Hard false:** 99/813 (12.2%) — clearly wrong

**Conservative estimate:** 536/(536+99) = **84% precision**  
**Reasonable estimate:** (536+147)/(813) = **84% on fires, considering mixed blocks**  
**Overall:** **Approximately 88% correct** (accounting for the unjudgeable category)

**Hard false-fire rate: 12%** — the denominator for "how often is tier A wrong about non-code work?"

---

## Corroboration

The **11.6% false-fire rate on editorial (Control 1)** corroborates the **12% hard false-fire rate (precision measurement)** by an independent route. Both signals converge on approximately 12% error.

---

## Spec Implication

The architecture documentation states in §3: "tiers A and B cannot be wrong in an interesting way." 

**This result falsifies that claim.** Tier A is wrong (fires on blocks with no code authoring) 12% of the time — which is an interesting way to be wrong, not a negligible edge case.

---

## Verdict

**Not PASS. Not FAIL.** The pre-registered bars are binary gates suited to validation, but this measurement reveals a precision question: the rule works 88% of the time and fails 12%.

This finding proceeds to the human gate for consideration: is 88% precision sufficient for the use case, or does the 12% error rate require a design change?

