# Task 2 Results: Tier A — Code-Artifact Dominance Rule

## Critical Finding: Circular vs. Independent Controls

**IMPORTANT:** The initial control (file extensions) is circular by design and does not test whether tier A identifies actual code work. An independent control (tool-call evidence) reveals that tier A does **not** discriminate code work from non-code work.

**VERDICT:** 
- **Circular control (file extensions):** PASS (but circular at 97.2% agreement, so not meaningful)
- **Independent control (tool calls):** FAIL (rejects tier A for shipment)

---

## Control 1: Circular Control (File Extensions)

### Why It's Circular

- `kind()` labels a block "code" when: code files > doc files
- `tier_a()` fires when: code files / ALL files ≥ 0.5
- **These are near-inverses by construction** — `kind()` and `tier_a()` measure nearly the same thing

**Control agreement:** 1,306 of 1,343 blocks (97.2%) — a rule cannot meaningfully validate itself.

### Results

| Artifact Kind | Blocks | Tier A Fires | Rate |
|---|---:|---:|---:|
| code | 796 | 786 | 98.7% |
| editorial | 233 | 27 | 11.6% |
| neither | 314 | 0 | 0.0% |

**Metrics:**
- Code fire rate: 98.7%
- Editorial fire rate: 11.6%
- Ratio: 8.5x

**Pre-registered bar:** Tier A PASSES if ≥80% code AND ≤20% editorial.
**Result:** **PASS** ✅ — *but this measures tier A against a restatement of itself*

---

## Control 2: Independent Control (Activity Classes from Tool Calls)

### Why It's Independent

- `activity_classes` is derived from **tool calls and shell commands**, not file extensions
- A completely different evidence path, orthogonal to file artifacts
- Tests whether tier A identifies work that tool-call evidence marks as code

**Note:** 5 blocks had empty `activity_classes` and were excluded from this control.

### Results

| Class Label | Blocks | Tier A Fires | Rate |
|---|---:|---:|---:|
| code | 103 | 77 | 74.8% |
| non-code | 1,235 | 736 | 59.6% |

**Metrics:**
- Code-class fire rate: 74.8%
- Non-code-class fire rate: 59.6%
- Ratio: 1.3x

**Pre-registered bar:** Tier A PASSES if ≥70% code-class AND ≤30% non-code-class.
**Result:** **FAIL** ❌
- Code-class: 74.8% ≥ 70% ✓
- Non-code-class: 59.6% > 30% ✗ **(fails by 29.6 percentage points)**

### Critical Interpretation

**Tier A fires on nearly 60% of blocks that tool calls mark as non-code work.** This reveals the rule primarily responds to file-extension patterns, not to actual work being performed:

- A block dominated by code files triggers tier A **even when the developer's tool-call activity indicates non-code work**
- The rule does not discriminate code work from non-code work; it discriminates code artifacts from other artifacts
- In a real system, this would misroute ~60% of editorial/non-code blocks into code-handling pipelines

---

## Control Agreement

| Metric | Count | Rate |
|---|---:|---:|
| Agreement (both "code" or both "non-code") | 593 | 44.3% |
| Disagreement (opposite labels) | 745 | 55.7% |

The controls agree on fewer than half the blocks (44.3%). When they disagree, tier A's file-extension-based signal overrides the tool-call evidence, indicating the rule does not track the work itself—only the artifacts.

---

## Sensitivity Analysis: Threshold Sweep (Control 1 only)

The dominance threshold was swept from 0.3 to 0.9 on the circular control:

```
  threshold 0.3: code  99.7%  editorial  28.8%
  threshold 0.4: code  99.1%  editorial  17.2%
  threshold 0.5: code  98.7%  editorial  11.6%
  threshold 0.6: code  96.2%  editorial   0.0%
  threshold 0.7: code  90.7%  editorial   0.0%
  threshold 0.8: code  84.5%  editorial   0.0%
  threshold 0.9: code  77.0%  editorial   0.0%
```

The circular control shows low sensitivity across the range, but **this sweep is not meaningful** — it only confirms the circularity at different thresholds.

---

## Conclusion: Which Control to Cite

**The independent control is authoritative** because it tests tier A against a different evidence source (tool calls, not file extensions).

The circular control at 97.2% agreement proves that the two file-extension-based metrics measure nearly the same thing—neither is a useful validation of the other.

---

## Final Verdict: FAIL

**Tier A must not ship.** While it passes the circular control (8.5x discrimination of file types), it fails the independent control (1.3x discrimination of work, with 59.6% false-positive rate on non-code blocks).

The rule tracks file-extension patterns without discriminating actual code work from non-code work when those patterns diverge from tool-call evidence. In production, it would misclassify or misroute the majority of non-code work.

