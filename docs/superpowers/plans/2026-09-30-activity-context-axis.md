# Activity `context` axis — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish `activity_contexts` on the block row — what the work was FOR — from deterministic evidence only (tiers A+B), after proving empirically that the evidence actually fires.

**Architecture:** Two deterministic tiers. Tier A reads code-artifact evidence and answers `none` (the axis does not apply). Tier B crosswalks `system_categories` to seven business contexts and artifact modality to `media`. No model, no text. **Phase 1 is a study in `scripts/` that touches no production code**; Phase 2 ships only what Phase 1's numbers justify.

**Tech Stack:** Python 3.12 via `~/.keld/sidecar-venv/bin/python` (never the host interpreter), Go 1.x host toolchain, SQLite reference series, standalone sidecar test scripts (no pytest).

**Spec:** `docs/superpowers/specs/2026-09-30-activity-context-axis-design.md`

## Global Constraints

- **ABSENT IS NOT `general`.** When no tier fires, publish NOTHING. `general` means the work had no particular domain; absent means we could not tell.
- **`operations` is never published.** Unreachable by A or B; carries no modality and no vendor signature.
- **`media` is not a system category.** It is a modality, carried by `file_types`/`artifact`. A `media` system category would collide with `marketing` on every design tool.
- **The 14 system categories with no context counterpart contribute nothing** — `knowledge_base`, `code_hosting`, `ci_cd`, `cloud_infra`, `data_platform`, `observability`, `security_iam`, `design`, `analytics_bi`, `scheduling`, `storage_files`, `ecommerce`, `ai_ml`, `issue_tracking`. Asserted by name so adding a category cannot silently start publishing a context.
- **`unrecognized` maps to no context.**
- **Vendor entries pass four gates:** products-not-general-technology; `AMBIGUOUS` for ordinary English words; product-not-company; no `-` or `_` (both lanes split on them).
- **`jupyterhub` is excluded** by the products-not-technology rule. Asserted absent with the reason.
- **Must run under `ml_backend:"deterministic"`** — that is what `keld-agent install` writes.
- **Tier C is not built.** No model, no text reading, no flag.
- **Sidecar code runs in the venv**: `PYTHONPATH=. ~/.keld/sidecar-venv/bin/python`.
- **Corpus paths are environment-configured** (`KELD_CORPUS_A`, `KELD_CORPUS_B`), never hardcoded — these read private transcripts.

## Review Focus

1. **A block with BOTH code evidence and a context-mapping system** (an engineer sending a DocuSign envelope). Tier A says `none`, tier B says `legal`. The spec never defined precedence — Task 5 resolves it: **both publish**, because this is a DISTRIBUTION, not a label, and suppressing either would be inventing a winner. Tested in Task 5.
2. **A block with two context-mapping systems** (DocuSign + NetSuite) must publish `legal` AND `financial`, not one. Tested in Task 5.
3. **A block with no evidence at all** must publish NOTHING — not `general`, not an empty list that renders as a value. Tested in Task 5 and again end-to-end in Task 8.
4. **A block whose only code evidence is one config file** (`package.json` edit inside a marketing site) must not fire tier A on that alone. Tier A needs dominance, not presence. Tested in Task 5.
5. **`unrecognized` system category** must contribute no context — it is the honest bucket, not a domain. Tested in Task 5.

---

# PHASE 1 — STUDY (no production code)

**Nothing in this phase modifies `sidecar/app/` or `internal/`.** Output is numbers and a decision.

### Task 1: Block frame from both corpora

**Files:**
- Create: `scripts/context_frame.py`
- Create (output, gitignored): `/tmp/claude-1000/ctx/frame.ndjson`

**Interfaces:**
- Produces: `frame.ndjson`, one JSON object per block: `{corpus, session, start, end, is_subagent, file_types: {ext: n}, system_categories: {cat: n}, activity_classes: {cls: n}, n_requests}`

- [ ] **Step 1: Write the frame builder**

```python
"""Block frame for the context-axis study: every closed block from both corpora
with the inventories tiers A and B would read.

⚠️ CORPUS PATHS ARE ENVIRONMENT-CONFIGURED (KELD_CORPUS_A / KELD_CORPUS_B). These
read private transcripts; a hardcoded path names whose.

This touches no production code. It calls the SHIPPED block cutter and digest so
the frame is the blocks that actually exist, not a re-implementation of them.
"""
import json, os, sys, tempfile

SIDECAR = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "sidecar")
sys.path.insert(0, SIDECAR)
from app.analysis.blockdigest import digest_blocks
from app.analysis.ingest import ingest_file
from app.analysis.store import open_store


def corpus(var, what):
    p = os.environ.get(var)
    if not p:
        sys.exit(f"set {var} to the {what} directory")
    return os.path.expanduser(p)


def counts(inv, key):
    """An inventory level as {value: n}; {} when the level is absent."""
    rows = (inv or {}).get(key) or []
    return {(r["value"] if isinstance(r, dict) else r[0]):
            (r.get("n", 1) if isinstance(r, dict) else r[1]) for r in rows}


def frame(root, tag, store, out):
    n = 0
    for dp, _, names in os.walk(root):
        for fn in names:
            if not fn.endswith(".jsonl"):
                continue
            path = os.path.join(dp, fn)
            try:
                ingest_file(store, path)
                ans = digest_blocks(store, path, current=False)
            except Exception as e:                      # a broken transcript is not a study failure
                print(f"  skip {fn}: {type(e).__name__}", file=sys.stderr)
                continue
            for b in ans.get("blocks", []):
                inv = (b.get("dimensions") or {}).get("inventory") or {}
                out.write(json.dumps({
                    "corpus": tag,
                    "session": b.get("session"),
                    "start": b.get("start"),
                    "end": b.get("end"),
                    "is_subagent": os.path.basename(path).startswith("agent-"),
                    "file_types": counts(inv, "file_types"),
                    "system_categories": counts(inv, "system_categories"),
                    "activity_classes": counts(inv, "activity_classes"),
                    "n_requests": sum(counts(inv, "activity_classes").values()),
                }, separators=(",", ":")) + "\n")
                n += 1
    return n


def main():
    outp = "/tmp/claude-1000/ctx/frame.ndjson"
    os.makedirs(os.path.dirname(outp), exist_ok=True)
    total = 0
    with tempfile.TemporaryDirectory() as tmp, open(outp, "w") as out:
        store = open_store(os.path.join(tmp, "frame.db"))
        for var, tag in (("KELD_CORPUS_A", "A"), ("KELD_CORPUS_B", "B")):
            got = frame(corpus(var, tag), tag, store, out)
            print(f"corpus {tag}: {got:,} blocks")
            total += got
    print(f"wrote {total:,} blocks -> {outp}")


if __name__ == "__main__":
    main()
```

- [ ] **Step 2: Run it**

```bash
cd /tmp/claude-1000/wt-combined
KELD_CORPUS_A=~/keld/john-projects/projects \
KELD_CORPUS_B=~/keld/refseries-context/frozen-corpus/projects \
~/.keld/sidecar-venv/bin/python scripts/context_frame.py
```

Expected: a few thousand blocks across both corpora. If it reports **0 blocks**, stop — `digest_blocks` is returning nothing and the rest of the study is meaningless.

- [ ] **Step 3: Sanity-check the frame before drawing anything from it**

```bash
~/.keld/sidecar-venv/bin/python -c "
import json,collections
rows=[json.loads(l) for l in open('/tmp/claude-1000/ctx/frame.ndjson')]
print('blocks:', len(rows))
print('with file_types :', sum(1 for r in rows if r['file_types']))
print('with system_cats:', sum(1 for r in rows if r['system_categories']))
print('subagent blocks :', sum(1 for r in rows if r['is_subagent']))
c=collections.Counter()
for r in rows: c.update(r['system_categories'])
print('system categories seen:', dict(c.most_common(10)))"
```

Expected: most blocks have `file_types`; **few have `system_categories`** — that is the result Task 3 quantifies, seen early.

- [ ] **Step 4: Commit**

```bash
git add scripts/context_frame.py
git commit -m "study: block frame for the context axis, from the shipped cutter and digest"
```

---

### Task 2: Tier A — does code evidence identify code work, and only code work?

**Files:**
- Create: `scripts/context_tier_a.py`

**Interfaces:**
- Consumes: `/tmp/claude-1000/ctx/frame.ndjson` from Task 1
- Produces: `tier_a(file_types: dict) -> bool` — the candidate rule, copied verbatim into Task 5 if it passes

- [ ] **Step 1: Write the measurement WITH its control**

```python
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
```

- [ ] **Step 2: Commit the bar BEFORE running it**

```bash
git add scripts/context_tier_a.py
git commit -m "study: tier A measurement + its pre-registered bar, committed before it runs"
```

This ordering is the point: the bar is provable from git to predate the number.

- [ ] **Step 3: Run it**

```bash
~/.keld/sidecar-venv/bin/python scripts/context_tier_a.py
```

Expected: code blocks fire high, editorial blocks fire low. **A ratio near 1.0x means tier A is not discriminating and must not ship.**

- [ ] **Step 4: Sweep the threshold and record the sensitivity**

```bash
~/.keld/sidecar-venv/bin/python -c "
import sys; sys.path.insert(0,'scripts')
import json
from context_tier_a import tier_a, kind
rows=[json.loads(l) for l in open('/tmp/claude-1000/ctx/frame.ndjson')]
for t in (0.3,0.4,0.5,0.6,0.7,0.8,0.9):
    c=[r for r in rows if kind(r['file_types'])=='code']
    e=[r for r in rows if kind(r['file_types'])=='editorial']
    cr=sum(1 for r in c if tier_a(r['file_types'],t))/max(len(c),1)
    er=sum(1 for r in e if tier_a(r['file_types'],t))/max(len(e),1)
    print(f'  threshold {t}: code {100*cr:5.1f}%  editorial {100*er:5.1f}%')"
```

Record which threshold to ship. If every threshold gives a ratio near 1.0x, tier A fails regardless of tuning — report that, do not tune toward the bar.

- [ ] **Step 5: Commit the results**

```bash
git add -A docs/notes/
git commit -m "study: tier A results — discrimination against the editorial control"
```

---

### Task 3: Tier B — how often does a context-mapping system actually fire?

**Files:**
- Create: `scripts/context_tier_b.py`

**Interfaces:**
- Consumes: `/tmp/claude-1000/ctx/frame.ndjson`
- Produces: `CROSSWALK: dict[str, str]` — the seven mapped categories, copied verbatim into Task 5

- [ ] **Step 1: Write the measurement**

```python
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
```

- [ ] **Step 2: Run it**

```bash
~/.keld/sidecar-venv/bin/python scripts/context_tier_b.py
```

- [ ] **Step 3: Commit**

```bash
git add scripts/context_tier_b.py
git commit -m "study: tier B coverage — how often a context-mapping system fires"
```

---

### GATE — decide before Phase 2

- [ ] **Write the results note and STOP for a human decision**

Create `docs/notes/2026-09-30-context-axis-study-results.md` recording, with the numbers:

1. Tier A: fire rate on code vs editorial blocks, the ratio, the threshold sweep, PASS/FAIL against the committed bar.
2. Tier B: mapped-system fire rate, which contexts were reached at all.
3. Combined coverage: what fraction of blocks would carry any `activity_contexts` value.

**The decision this gate exists for:**

- **Tier A PASSES and tier B fires ≥1%** → build both, Tasks 4-8.
- **Tier A PASSES and tier B is ~0%** → build tier A and the vendor table (Tasks 4-8), and record tier B as shipped-but-unexercised, the same honest status `system_categories` already carries. Do NOT tune the crosswalk to make a number appear.
- **Tier A FAILS** → stop. Publish nothing. The axis has no deterministic route and the spec's premise is wrong; say so and re-open the design.

Commit the note. **Do not start Task 4 without the human's decision.**

---

# PHASE 2 — PRODUCTION (only what the gate authorised)

### Task 4: Vendor table — `medical` and `scientific`

**Files:**
- Modify: `sidecar/app/analysis/systems.py`
- Modify: `sidecar/app/test_analysis_systems.py`

**Interfaces:**
- Produces: `CATEGORIES` grows to 24 with `medical`, `scientific`; `BRAND` gains ~23 tokens

- [ ] **Step 1: Write the failing test**

```python
def test_the_medical_and_scientific_vendors_resolve():
    """Tier B's two hardest contexts have vendor systems like every other domain;
    they were absent only because the table was built for generic enterprise."""
    for brand, cat in (("epic", "medical"), ("cerner", "medical"), ("veeva", "medical"),
                       ("benchling", "scientific"), ("overleaf", "scientific"),
                       ("arxiv", "scientific"), ("zenodo", "scientific")):
        assert category_for_brand(brand) == cat, brand
    assert category_for_host("arxiv.org") == "scientific"
    assert category_for_host("ncbi.nlm.nih.gov") == "scientific"


def test_jupyterhub_is_excluded_as_general_purpose_technology():
    """⚠️ The rule that removed `postgres`, `docker` and `terraform` applies here
    too: JupyterHub is run, not bought. Scientific work runs on it and that says
    only that computation happened."""
    assert "jupyterhub" not in BRAND
    assert category_for_brand("jupyterhub") == "unrecognized"


def test_dimensions_is_ambiguous_not_a_bare_brand():
    """Digital Science's `dimensions` is a real product AND this codebase's own
    core noun. Host-only, like `monday` and `heap`."""
    from app.analysis.systems import AMBIGUOUS
    assert "dimensions" in AMBIGUOUS
    assert category_for_brand("dimensions") == "unrecognized"
```

- [ ] **Step 2: Run to verify it fails**

```bash
cd /tmp/claude-1000/wt-combined/sidecar
PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_systems.py
```

Expected: FAIL on `category_for_brand("epic")` returning `unrecognized`.

- [ ] **Step 3: Add the two categories and their vendors**

In `systems.py`, add to `CATEGORIES` before `"unrecognized"`: `"medical", "scientific"`. Add to `_TABLE`:

```python
    # ⚠️ PRODUCTS, NOT THE SCIENCE. An EHR and an ELN are bought; `jupyterhub`,
    # `rstudio` and `matlab` are run, and naming them says only that computation
    # happened -- the same rule that removed `postgres` and `docker`.
    "medical": """epic cerner veeva medidata athenahealth doximity meditech
        allscripts nextgen eclinicalworks redox particlehealth""",
    "scientific": """benchling overleaf arxiv pubmed zenodo labarchives
        dataverse orcid figshare protocolsio scite""",
```

Add to `_HOST_SUFFIX`:

```python
    "arxiv.org": ("arxiv", "scientific"),
    "ncbi.nlm.nih.gov": ("pubmed", "scientific"),
    "zenodo.org": ("zenodo", "scientific"),
    "overleaf.com": ("overleaf", "scientific"),
    "benchling.com": ("benchling", "scientific"),
    "epic.com": ("epic", "medical"),
    "cerner.com": ("cerner", "medical"),
    "veeva.com": ("veeva", "medical"),
```

Add `dimensions` to the `AMBIGUOUS` frozenset.

- [ ] **Step 4: Run to verify it passes**

```bash
PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_systems.py
```

Expected: PASS, and the pre-existing gate tests (`test_general_purpose_technology_is_not_in_the_table`, `test_every_table_token_is_reachable`, `test_the_published_vocabulary_is_closed_and_every_entry_maps_into_it`) still pass — they now cover the new entries automatically.

- [ ] **Step 5: Commit**

```bash
git add sidecar/app/analysis/systems.py sidecar/app/test_analysis_systems.py
git commit -m "systems: medical and scientific categories, for tier B's two hardest contexts"
```

---

### Task 5: The context resolver

**Files:**
- Create: `sidecar/app/analysis/context.py`
- Create: `sidecar/app/test_analysis_context.py`

**Interfaces:**
- Consumes: `systems.CATEGORIES` (Task 4), `vocab.CODE_EXT`
- Produces: `CONTEXTS: tuple`, `contexts_for(file_types: dict, system_categories: dict) -> set[str]`

- [ ] **Step 1: Write the failing tests — every Review Focus item**

```python
def test_code_dominance_yields_none():
    assert contexts_for({".go": 8, ".md": 1}, {}) == {"none"}


def test_one_config_file_among_documents_does_NOT_yield_none():
    """⚠️ DOMINANCE, NOT PRESENCE. A package.json edit inside a marketing site is
    one code file among many documents; firing `none` there would declare that the
    axis does not apply to work it plainly applies to."""
    assert "none" not in contexts_for({".json": 1, ".md": 9}, {})


def test_a_mapped_system_yields_its_context():
    assert contexts_for({}, {"legal_contracts": 3}) == {"legal"}
    assert contexts_for({}, {"medical": 2}) == {"medical"}


def test_two_mapped_systems_yield_BOTH():
    """⚠️ This is a DISTRIBUTION, not a label. Picking a winner between two systems
    the block genuinely used would invent a fact."""
    assert contexts_for({}, {"legal_contracts": 1, "finance_billing": 1}) == {"legal", "financial"}


def test_code_evidence_and_a_mapped_system_BOTH_publish():
    """⚠️ THE PRECEDENCE CASE THE SPEC LEFT OPEN. An engineer who sends a DocuSign
    envelope did code work AND legal work in that block. Suppressing either side
    would be choosing a winner the evidence does not support."""
    assert contexts_for({".go": 9}, {"legal_contracts": 1}) == {"none", "legal"}


def test_an_unmapped_system_contributes_nothing():
    for cat in ("knowledge_base", "code_hosting", "cloud_infra", "issue_tracking",
                "ai_ml", "design", "communication"):
        assert contexts_for({}, {cat: 5}) == set(), cat


def test_unrecognized_contributes_nothing():
    assert contexts_for({}, {"unrecognized": 9}) == set()


def test_media_comes_from_modality():
    assert contexts_for({".mp4": 2}, {}) == {"media"}
    assert contexts_for({".wav": 1, ".png": 1}, {}) == {"media"}


def test_no_evidence_publishes_NOTHING_not_general():
    """⚠️ THE RULE MOST LIKELY TO BE BROKEN BY SOMEONE BEING HELPFUL. `general` is
    a real context meaning the work had no particular domain. Absent means we could
    not tell. Defaulting one to the other manufactures the single value that looks
    like an answer, and no consumer can tell it apart from a real one."""
    assert contexts_for({}, {}) == set()
    assert "general" not in contexts_for({}, {})


def test_operations_is_never_published():
    """Unreachable by either tier: no modality, no vendor signature. It must not
    appear from any input rather than appear rarely and wrongly."""
    import itertools
    from app.analysis.systems import CATEGORIES
    for cat in CATEGORIES:
        assert "operations" not in contexts_for({".go": 1}, {cat: 1}), cat


def test_the_published_vocabulary_is_closed():
    from app.analysis.systems import CATEGORIES
    for cat in CATEGORIES:
        for ext in (".go", ".md", ".mp4", ""):
            for v in contexts_for({ext: 1} if ext else {}, {cat: 1}):
                assert v in CONTEXTS, (cat, ext, v)
```

- [ ] **Step 2: Run to verify they fail**

```bash
cd /tmp/claude-1000/wt-combined/sidecar
PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_context.py
```

Expected: `ModuleNotFoundError: No module named 'app.analysis.context'`.

- [ ] **Step 3: Write the module**

```python
"""`activity_contexts` — what the work was FOR.

The domain of purpose of a block's work: who the output was for. NOT the topic
(a block can discuss a legal matter while producing an engineering change), NOT
the tool (marketing copy can land in Notion), and NOT the capability, which
`activity_class` already reports.

⚠️ TWO DETERMINISTIC TIERS ONLY. Tier C -- deciding from message text whether
prose is FOR marketing or FOR finance -- is DEFERRED, not missing. See
docs/superpowers/specs/2026-09-30-activity-context-axis-design.md §6: every route
to validating it failed (synthesis disagreed across generators on 2 of 3 sessions;
public chat held ~1 genuine instance in 59,857 conversations), and the one arm
ever measured OVER-FIRED. Do not add a text pass here without a corpus that can
score it.

⚠️ ABSENT IS NOT `general`. `general` is a real context meaning the work had no
particular domain; absent means we could not tell. This module returns an EMPTY
SET for "could not tell" and never synthesises `general`.
"""
from app.analysis.systems import CATEGORIES
from app.analysis.vocab import CODE_EXT

# The published vocabulary, from atv1's `context` column. `operations` is
# deliberately absent: it is carried only by verbs with no modality and no vendor
# signature, so neither tier can reach it, and a value that can never be produced
# reads as coverage. `general` is present because atv1 defines it, but NOTHING
# HERE EVER PRODUCES IT -- see the module docstring.
CONTEXTS = ("none", "media", "legal", "financial", "sales", "support",
            "marketing", "medical", "scientific", "general")

# Tier B: the system categories that imply a context. Everything else contributes
# NOTHING and that is enumerated by absence, not by an exclusion list -- a new
# system category is not a context until someone adds it here deliberately.
CROSSWALK = {
    "legal_contracts": "legal",
    "finance_billing": "financial",
    "crm_sales": "sales",
    "support": "support",
    "marketing": "marketing",
    "medical": "medical",
    "scientific": "scientific",
}

# Tier B, modality half: an artifact that IS media.
MEDIA_EXT = frozenset((".mp4", ".mov", ".avi", ".mkv", ".webm",
                       ".wav", ".mp3", ".m4a", ".flac", ".aac",
                       ".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp"))

# Tier A: how much of a block's file evidence must be code before the axis is
# declared inapplicable. DOMINANCE, NOT PRESENCE -- a package.json edit inside a
# marketing site is one code file among many documents. Value set by the threshold
# sweep in scripts/context_tier_a.py; changing it re-opens that measurement.
CODE_DOMINANCE = 0.5


def contexts_for(file_types, system_categories):
    """The contexts a block's deterministic evidence supports. Empty means we could
    not tell -- NEVER `general`.

    Both tiers contribute independently and neither suppresses the other: a block
    where an engineer sent a DocuSign envelope did code work AND legal work, and
    choosing a winner between them would invent a fact the evidence does not carry.
    This is a distribution, like `activity_classes`.
    """
    out = set()
    ft = file_types or {}

    total = sum(ft.values())
    if total:
        if sum(n for e, n in ft.items() if e in CODE_EXT) / total >= CODE_DOMINANCE:
            out.add("none")
        if any(e in MEDIA_EXT for e in ft):
            out.add("media")

    for cat in (system_categories or {}):
        ctx = CROSSWALK.get(cat)
        if ctx:
            out.add(ctx)
    return out
```

- [ ] **Step 4: Run to verify they pass**

```bash
PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_context.py
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add sidecar/app/analysis/context.py sidecar/app/test_analysis_context.py
git commit -m "context: the two deterministic tiers, absent never general"
```

---

### Task 6: Emit the level and publish the dimension

**Files:**
- Modify: `sidecar/app/analysis/levels.py`
- Modify: `sidecar/app/analysis/dimensions.py`
- Modify: `sidecar/app/analysis/__init__.py` (SCHEMA bump)
- Modify: `sidecar/app/test_analysis_window.py` (SCHEMA assertion)

**Interfaces:**
- Consumes: `context.contexts_for` (Task 5)
- Produces: level `activity_context`; INVENTORY entry `("activity_contexts", "activity_context", 10)`

- [ ] **Step 1: Write the failing test**

Add to `sidecar/app/test_analysis_context.py`:

```python
def test_the_dimension_is_registered_with_the_whole_vocabulary_as_its_cap():
    """Cap 10 is the entire vocabulary, like activity_classes: a truncated
    distribution is a wrong one, not a shorter one."""
    from app.analysis.dimensions import INVENTORY
    from app.analysis.context import CONTEXTS
    entry = [e for e in INVENTORY if e[0] == "activity_contexts"]
    assert entry, "activity_contexts is not a published dimension"
    assert entry[0][1] == "activity_context"
    assert entry[0][2] >= len(CONTEXTS)
```

- [ ] **Step 2: Run to verify it fails**

```bash
PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_context.py
```

Expected: FAIL — `activity_contexts is not a published dimension`.

- [ ] **Step 3: Emit the level**

⚠️ The context is a property of the BLOCK's accumulated evidence, not of one turn, so unlike `activity_class` it cannot be emitted per turn. Emit it in `blockdigest.digest()` where the block's rollup is already in hand, reading `file_types` and `system_categories` from that rollup.

In `sidecar/app/analysis/blockdigest.py`, inside `digest()`, immediately after
`out = dimensions.payload(rl)` — the local is `out`, and `rl` is the block's rollup:

```python
    # `activity_contexts` -- what the block's work was FOR, from deterministic
    # evidence only. ⚠️ Computed HERE rather than in levels.py because it is a
    # property of the block's ACCUMULATED evidence: a single turn's file does not
    # establish dominance, and per-turn emission would make one `.go` edit in a
    # documentation block publish `none`.
    # ⚠️ Read from `rl` (the ROLLUP, keyed by LEVEL name) rather than from `out`
    # (the PAYLOAD, keyed by DIMENSION name). The two differ -- `ext` against
    # `file_types`, `system_category` against `system_categories` -- and reading the
    # payload would couple this to the published naming rather than to the evidence.
    from app.analysis.context import contexts_for
    _ctx = contexts_for(dict(rl.get("ext", [])), dict(rl.get("system_category", [])))
    if _ctx:            # ⚠️ empty means we could not tell -- publish NO key at all
        out.setdefault("inventory", {})["activity_contexts"] = [
            {"value": v, "n": 1} for v in sorted(_ctx)]
```

- [ ] **Step 4: Register the dimension**

In `dimensions.py`, append to `INVENTORY` with its argument:

```python
             # `activity_contexts` -- what the block's work was FOR (the domain of
             # purpose), from deterministic evidence only: code-artifact dominance
             # for `none`, artifact modality for `media`, and a crosswalk from
             # `system_categories` for the business contexts.
             #
             # ⚠️ IT IS NOT `system_categories` RENAMED. That says which vendor
             # product was touched; this says who the output was FOR. A block can
             # write marketing copy into Notion -- knowledge_base as a system,
             # marketing as a context -- which is why the crosswalk covers seven
             # categories and the other fourteen contribute nothing.
             #
             # ⚠️ ABSENT IS NOT `general`. No key at all means we could not tell;
             # `general` would be a claim that the work had no particular domain.
             # Nothing in this pipeline ever produces `general`.
             #
             # ⚠️ `operations` is unreachable by either tier and is deliberately
             # absent from the vocabulary rather than carried and never emitted.
             ("activity_contexts", "activity_context", 10)]
```

- [ ] **Step 5: Bump the schema**

`sidecar/app/analysis/__init__.py`: `SCHEMA = 21` → `SCHEMA = 22`.
`sidecar/app/test_analysis_window.py`: `SCHEMA == 21` → `SCHEMA == 22`.

- [ ] **Step 6: Run the full sidecar suite**

```bash
cd /tmp/claude-1000/wt-combined/sidecar
for f in app/test_*.py; do PYTHONPATH=. ~/.keld/sidecar-venv/bin/python "$f" >/dev/null 2>&1 || echo "FAIL: $f"; done
```

Expected: only `app/test_reader_golden.py` fails — the golden gains rows. That is the next step, not a defect.

- [ ] **Step 7: Regenerate the golden in its OWN commit**

```bash
git add sidecar/ && git commit -m "context: emit activity_contexts on the block digest"
cd /tmp/claude-1000/wt-combined
PYTHONPATH=sidecar ~/.keld/sidecar-venv/bin/python scripts/dump_store_rows.py
git diff --stat docs/superpowers/specs/golden/
git diff docs/superpowers/specs/golden/ | grep '^[+-]' | grep -v '^[+-][+-]' | grep -cv activity_context
```

The last command must print `0` — every changed row is the new level and no existing row moved. Then:

```bash
git add docs/superpowers/specs/golden/
git commit -m "golden: regenerate for activity_context — <N> rows added, 0 changed"
# fill <N> from the --stat above; the point is the SECOND number being zero
```

---

### Task 7: Go decode and publish on the block

**Files:**
- Modify: `internal/agent/enrich/sidecar/client.go`
- Modify: `internal/agent/enrich/sidecar/dimensions.go`
- Modify: `internal/agent/enrich/dynamics.go`
- Modify: `internal/agent/publish/facets.go`
- Modify: `internal/agent/enrich/sidecar/acts_test.go`
- Modify: `internal/agent/enrich/labels.go` (SchemaVersion)

**Interfaces:**
- Consumes: wire key `activity_contexts` from Task 6
- Produces: `enrich.WindowAnalysis.ActivityContexts`, `publish.AnalysisFacets.ActivityContexts`

- [ ] **Step 1: Write the failing test — the guard demands an argument**

The existing `TestAllInventoryKeysAreDecodableFromTheInventoryBlock` fails on a field count mismatch as soon as the struct grows. Add the key with its written argument to `wantTags` in `acts_test.go`:

```go
		// THE TWENTY-FIRST, AND ITS ARGUMENT.
		//
		// `activity_contexts` says what the block's work was FOR -- the domain of
		// purpose. It earns a key because nothing else answers it:
		// `activity_classes` says what capability was stressed, `system_categories`
		// says which vendor product was touched, and a block can write marketing
		// copy into Notion, where those two are `author_prose` and
		// `knowledge_base` and neither is `marketing`.
		//
		// ⚠️ DETERMINISTIC TIERS ONLY. The text-reading tier that would decide
		// "is this prose FOR marketing or FOR finance" is DEFERRED, because every
		// route to validating it failed and the one arm ever measured OVER-FIRED.
		// A future text pass is a decision with its own evidence, not an extension
		// of this key.
		//
		// ⚠️ ABSENT IS NOT `general`. No key means we could not tell.
		"activity_contexts": false,
```

- [ ] **Step 2: Run to verify it fails**

```bash
cd /tmp/claude-1000/wt-combined
go test ./internal/agent/enrich/sidecar/ -run TestAllInventoryKeys
```

Expected: FAIL — `InventoryBlock models [...]` with a count mismatch.

- [ ] **Step 3: Add the field through the four Go seams**

`client.go` (decode struct): `ActivityContexts []InventoryItem \`json:"activity_contexts"\``
`dimensions.go` (convert): `ActivityContexts: convertIdentifierInventory(inv.ActivityContexts),`
`dynamics.go` (`WindowAnalysis`): `ActivityContexts []NameCount` with the doc comment from Step 1's argument.
`facets.go`: the struct field **and** the `facetsOf` copy — `TestFacetsOfCarriesEveryFieldItCanReach` fails if the copy is missed, which is why it exists.

- [ ] **Step 4: Bump SchemaVersion**

`internal/agent/enrich/labels.go`: `26` → `27`. Update `labels_test.go` and `block_projects_test.go` to match.

- [ ] **Step 5: Run the full Go suite**

```bash
go test ./... 2>&1 | grep -vE '^ok|no test files'
```

Expected: only `TestGoldenBlockIsTheCurrentWire` fails on the schema number.

- [ ] **Step 6: Regenerate the Atlas contract golden and commit**

```bash
KELD_UPDATE_GOLDEN=1 go test ./internal/agent/publish/ -run Golden
go test ./... 2>&1 | grep -vE '^ok|no test files'   # expect silence
git add internal/ scripts/testdata/
git commit -m "Go: decode and publish activity_contexts on the block"
```

---

### Task 8: End-to-end, through the real pipeline

**Files:**
- Modify: `sidecar/app/test_systems_business_domains.py`

- [ ] **Step 1: Write the failing tests**

```python
def test_a_medical_session_reaches_the_medical_context_end_to_end():
    """Not a call to the lookup -- through ingest, the store and the block digest,
    so a context that stops surviving the rollup fails here."""
    with tempfile.TemporaryDirectory() as tmp:
        tools = [mcp("epic-get-patient-chart", id="p1"),
                 mcp("veeva-query-records", obj="Study")]
        got = _inventory(tmp, "med", tools, "activity_contexts")
        assert "medical" in got, got


def test_a_scientific_session_reaches_the_scientific_context():
    with tempfile.TemporaryDirectory() as tmp:
        tools = [mcp("benchling-get-entry", id="e1"),
                 fetch("https://arxiv.org/abs/2401.00001")]
        got = _inventory(tmp, "sci", tools, "activity_contexts")
        assert "scientific" in got, got


def test_a_block_with_no_context_evidence_publishes_NO_context_key():
    """⚠️ THE ABSENT RULE, END TO END. A block that used a knowledge base and
    wrote code has no DOMAIN OF PURPOSE we can establish. It must publish no
    context key at all -- not `general`, which would claim the work had no
    particular domain."""
    with tempfile.TemporaryDirectory() as tmp:
        tools = [mcp("notion-fetch", page="x"), ("Read", {"file_path": "/x/a.md"})]
        got = _inventory(tmp, "plain", tools, "activity_contexts")
        assert "general" not in got, got
```

- [ ] **Step 2: Run to verify they fail, then pass after Tasks 4-7 are in**

```bash
cd /tmp/claude-1000/wt-combined/sidecar
PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_systems_business_domains.py
```

- [ ] **Step 3: Full verification, both sides**

```bash
cd /tmp/claude-1000/wt-combined/sidecar
for f in app/test_*.py; do PYTHONPATH=. ~/.keld/sidecar-venv/bin/python "$f" >/dev/null 2>&1 || echo "FAIL: $f"; done
cd .. && go test ./... 2>&1 | grep -vE '^ok|no test files'
```

Expected: no output from either.

- [ ] **Step 4: Commit**

```bash
git add sidecar/app/test_systems_business_domains.py
git commit -m "context: medical and scientific sessions end to end, and the absent rule"
```

---

### Task 9: Tell the Atlas side

- [ ] **Step 1: Message the Atlas session**

`activity_contexts` is an eighth measure-type level, and `dimensions.py`'s own note says a new one is **NOT auto-excluded** from Atlas's workstream readers — it will appear there until someone adds it. Send: the key, the 10-value vocabulary, that `general` never appears, that absent means unknown, and the two new system categories (`medical`, `scientific`) needing display names.

- [ ] **Step 2: Update the vendor-importer follow-on note**

Record in `docs/notes/` that the importer (spec §7) must reproduce the Task 4 entries as its acceptance test.
