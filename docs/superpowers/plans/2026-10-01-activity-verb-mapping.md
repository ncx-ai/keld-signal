# Activity Verb Axis Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish an `activity_verbs` distribution per block, mapping `reqclass.py`'s nine measured activity classes onto atv1's verb vocabulary — six verbs shipping on existing validation, three more gated behind a measured study of two ambiguous splits.

**Architecture:** A pure lookup module (`sidecar/app/analysis/verbs.py`) translates one `reqclass` class plus the request's tool evidence into an atv1 verb or an abstention. It is emitted as an INVENTORY level beside `activity_classes` at the same site, travels the existing four-stop Go path, and is published with a token-weighted sibling. The two ambiguous classes (`synthesize`, `retrieve`) abstain until a study, whose discriminators are STRUCTURAL (tool identity, tool arguments, request order) and never textual, passes a pre-registered bar.

**Tech Stack:** Python 3.12 via `~/.keld/sidecar-venv/bin/python` (sidecar); Go host toolchain (daemon). Sidecar tests are standalone scripts, no pytest. Study scratch venv `/tmp/claude-1000/wcvenv` for pyarrow only.

**Spec:** `docs/superpowers/specs/2026-10-01-activity-verb-mapping-design.md`

## Global Constraints

- **The nine-value vocabulary is declared from the start; coverage grows, the vocabulary does not.** `VERBS` holds all nine (`code.write`, `code.edit`, `text.create`, `text.transform`, `text.summarize`, `review`, `extract`, `plan`, `research`) and the level's cap is **9** from Task 2 onward, even though Tasks 1-2 produce only six. This is deliberate: it means the study's outcome changes which values are *produced*, never the published vocabulary, so a passing study needs no second schema bump.
- **Cap 9 is the WHOLE closed vocabulary**, so the level can never be truncated and `inventory_omitted` can never name it. A cut distribution is a wrong one, not a shorter one.
- **`operate`, `acknowledge` and `unclassified` publish NO verb.** They are excluded as not-work, not as gaps. Never map them to atv1's `other`.
- **A failing split publishes NOTHING — there is no unsplit parent.** `synthesize` and `retrieve` are reqclass class names, not atv1 verbs.
- **Both split discriminators must be STRUCTURAL** — tool identity, tool arguments, request order. ⚠️ A discriminator that reads prose to decide is the seventh attempt at a thing refused six times and must be rejected in review.
- **No new model, no network, no inference.** This maps an existing output; it never revises `reqclass.route_class`'s nine-value result.
- **Sidecar code and tests run under `~/.keld/sidecar-venv/bin/python`**, never the host interpreter.
- **A new level may DESCRIBE existing evidence; it may never CREATE evidence where a turn produced none.** Emit inside the existing `if o.role != "user" and (o.tool_calls or (o.text or "").strip()):` guard — a level that fires outside it makes a 5-minute bin ACTIVE and silently shifts every block boundary.
- **Study blindness:** labels committed before any splitter exists, provable from git. ⚠️ **Never put aggregate label distributions in a commit message** — the conversation-domain study's blindness was compromised exactly that way, via `git show`.

## Review Focus

Five failure modes the spec implies that no task's own happy path exercises. Each has its test placed in the task that owns the code.

1. **A request whose class is excluded still emits a verb row** — `operate`/`acknowledge`/`unclassified` must produce no row at all, not a row with an empty or `other` value. Pinned in Task 1.
2. **`author_code` reached via a tool that is neither `Write` nor `Edit`** — `MultiEdit` and `NotebookEdit` are in `AUTHOR_TOOLS`, and `CODE_TOOLS`/`classify_bash` reach `author_code` with no authoring tool at all. The write/edit split must not raise or silently pick one. Pinned in Task 1.
3. **A request with zero output tokens** — `activity_verb_tokens` must emit nothing rather than a zero-weight row, matching `activity_class_tokens`'s existing `if _out:` guard. Pinned in Task 2.
4. **The fixture rebaseline hides a regression** — a baseline regenerated after a code change could absorb an unrelated level's drift. The rebaseline must be verified ADDITIVE. Pinned in Task 2.
5. **A split's discriminator reads text** — the structural constraint is the spec's load-bearing claim and nothing mechanical enforces it. Pinned in Task 5 as a test asserting the splitter's output is unchanged when the request's prose is replaced with unrelated prose of the same length.

---

## File Structure

| file | responsibility |
|---|---|
| `sidecar/app/analysis/verbs.py` | **create** — the nine-value vocabulary, the verb→family table, `verb_for()`. Pure; no I/O, no imports from `levels`. |
| `sidecar/app/test_analysis_verbs.py` | **create** — standalone test script for the above. |
| `sidecar/app/analysis/levels.py` | **modify** — emit `activity_verb` / `activity_verb_tokens` beside the existing activity_class pair. |
| `sidecar/app/analysis/dimensions.py` | **modify** — register both levels in `INVENTORY`, cap 9. |
| `internal/agent/enrich/sidecar/client.go` | **modify** — wire struct fields. |
| `internal/agent/enrich/sidecar/dimensions.go` | **modify** — convert. |
| `internal/agent/enrich/dynamics.go` | **modify** — `WindowAnalysis` fields. |
| `internal/agent/publish/facets.go` | **modify** — `AnalysisFacets` fields + `facetsOf` copy. |
| `internal/agent/enrich/labels.go` | **modify** — `SchemaVersion` 26 → 27. |
| `sidecar/app/analysis/testdata/fixture-identity-baseline.json` | **modify** — rebaseline, verified additive. |
| `scripts/verbsplit_frame.py` | **create** — study: extract requests, two corpora. |
| `scripts/verbsplit-labels.txt` | **create** — study: blind labels, committed FIRST. |
| `scripts/verbsplit_study.py` | **create** — study: the two splitters, the bar, the scorer. |
| `docs/notes/2026-10-01-verb-split-results.md` | **create** — study: the numbers. |

---

### Task 1: The verb lookup module

**Files:**
- Create: `sidecar/app/analysis/verbs.py`
- Test: `sidecar/app/test_analysis_verbs.py`

**Interfaces:**
- Consumes: nothing. Pure module.
- Produces: `VERBS` (tuple of 9 strings), `VERB_FAMILY` (dict verb→family), `EXCLUDED` (frozenset of 3 class names), `PENDING_SPLIT` (frozenset of 2 class names), and `verb_for(cls, tools) -> str | None` where `tools` is the list of `(name, input)` pairs `route_class` already receives.

- [ ] **Step 1: Write the failing test**

```python
"""Standalone test for analysis/verbs.py. Run:
    cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_verbs.py
"""
from app.analysis.verbs import VERBS, VERB_FAMILY, EXCLUDED, PENDING_SPLIT, verb_for


def test_vocabulary_is_nine_and_closed():
    assert len(VERBS) == 9, VERBS
    assert len(set(VERBS)) == 9
    # Every verb has exactly one family, and family is a pure function of verb.
    assert set(VERB_FAMILY) == set(VERBS)
    assert set(VERB_FAMILY.values()) == {"code", "language", "understanding", "agentic"}


def test_author_code_splits_on_write_vs_edit():
    assert verb_for("author_code", [("Write", {"file_path": "a.go"})]) == "code.write"
    assert verb_for("author_code", [("Edit", {"file_path": "a.go"})]) == "code.edit"


def test_author_prose_splits_on_the_same_tool_distinction():
    assert verb_for("author_prose", [("Write", {"file_path": "a.md"})]) == "text.create"
    assert verb_for("author_prose", [("Edit", {"file_path": "a.md"})]) == "text.transform"


def test_direct_mappings():
    assert verb_for("verify", [("Bash", {"command": "go test ./..."})]) == "review"
    assert verb_for("delegate", [("Agent", {})]) == "plan"


def test_excluded_classes_publish_no_verb():
    # REVIEW FOCUS 1: not `other`, not "", not a row at all -- None.
    for cls in ("operate", "acknowledge", "unclassified"):
        assert verb_for(cls, [("Bash", {"command": "git push"})]) is None, cls
    assert EXCLUDED == frozenset({"operate", "acknowledge", "unclassified"})


def test_pending_splits_abstain_until_the_study_lands():
    # There is NO unsplit parent to fall back to: `synthesize` and `retrieve` are
    # reqclass class names, not atv1 verbs.
    assert verb_for("synthesize", []) is None
    assert verb_for("retrieve", [("Read", {"file_path": "a.go"})]) is None
    assert PENDING_SPLIT == frozenset({"synthesize", "retrieve"})


def test_author_reached_without_an_authoring_tool_does_not_guess():
    # REVIEW FOCUS 2. classify_bash and CODE_TOOLS reach author_code with no
    # Write/Edit in evidence; MultiEdit/NotebookEdit are authoring but neither
    # literal. Guessing a side here would publish a false claim about the work.
    assert verb_for("author_code", [("Bash", {"command": "python3 -c 'x'"})]) is None
    assert verb_for("author_code", [("javascript_tool", {})]) is None
    # MultiEdit and NotebookEdit ARE edits of an existing thing -- they resolve.
    assert verb_for("author_code", [("MultiEdit", {"file_path": "a.go"})]) == "code.edit"
    assert verb_for("author_prose", [("NotebookEdit", {})]) == "text.transform"


def test_an_unknown_class_abstains_rather_than_raising():
    assert verb_for("not_a_class", []) is None


def test_every_producible_verb_is_in_the_vocabulary():
    produced = set()
    for cls, tools in [
        ("author_code", [("Write", {})]), ("author_code", [("Edit", {})]),
        ("author_prose", [("Write", {})]), ("author_prose", [("Edit", {})]),
        ("verify", []), ("delegate", []),
    ]:
        v = verb_for(cls, tools)
        if v:
            produced.add(v)
    assert produced <= set(VERBS), produced - set(VERBS)
    assert len(produced) == 6, produced   # six now; nine after the study


if __name__ == "__main__":
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            fn(); print(f"ok {name}")
    print("PASS")
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_verbs.py`
Expected: FAIL with `ModuleNotFoundError: No module named 'app.analysis.verbs'`

- [ ] **Step 3: Write the implementation**

```python
"""`activity_verb` -- the atv1 VERB an inference request's capability maps to.

⚠️ THIS ADDS NO CLASSIFIER. It maps `reqclass.route_class`'s existing nine-value
output -- measured at macro F1 0.920 against its author's blind labels and 0.815
against an independent labeller's -- onto the `verb` column of
`keld-activity-types-v1.csv` (atv1). Four of the six work-bearing classes map
DEFINITIONALLY: `verify -> review` is a statement about what two words mean, and
measuring it would measure a labeller's reading of a dictionary.

⚠️ THREE CLASSES PUBLISH NO VERB, AND THAT IS A SCOPE DECISION RATHER THAN A GAP
IN atv1. `operate` is state change -- `git push`, `mkdir`, `docker run`. atv1 is a
taxonomy of model CAPABILITIES (its own verb_notes: "Richest verb - context drives
model choice and price"), and composing `git push` stresses none that routing could
act on. Mapping it to atv1's `other` was considered and REJECTED: it would bucket
running a command with a genuine abstention and discard a separation reqclass
measures at F1 0.920. `acknowledge` is a bare "done"; `unclassified` is an honest
abstention and stays one.

⚠️ `synthesize` AND `retrieve` ABSTAIN, AND THERE IS NO UNSPLIT PARENT TO FALL
BACK TO -- those are reqclass CLASS names, not atv1 verbs, and atv1 has no value
meaning "retrieved something, unspecified". They resolve only once the split study
(docs/superpowers/specs/2026-10-01-activity-verb-mapping-design.md section 4)
passes its pre-registered bar. `retrieve` is 37.3% of engineering requests, so
this abstention is most of the axis's missing coverage, not a rounding error.

THE VOCABULARY IS DECLARED WHOLE AND DOES NOT GROW. All nine verbs are listed
below although only six are produced today, so the study's outcome changes which
values are PRODUCED and never the published vocabulary -- a passing study needs no
second schema bump.
"""

# All nine. Cap 9 at the emission site is this whole closed set, so the level can
# never be truncated and `inventory_omitted` can never name it.
VERBS = ("code.write", "code.edit", "text.create", "text.transform",
         "text.summarize", "review", "extract", "plan", "research")

# ⚠️ FAMILY IS A PURE FUNCTION OF VERB -- verified across all 68 atv1 rows, every
# verb maps to exactly one family. It is therefore NOT a published level: Atlas
# renders the family column from this table, which ships as part of the contract.
# `system_categories` earns its own level because it is strictly LESS identifying
# than its neighbour `external_systems`; family has no comparable justification,
# and a level costs a schema bump, a fixture rebaseline and an Atlas column.
VERB_FAMILY = {
    "code.write": "code",          "code.edit": "code",
    "text.create": "language",     "text.transform": "language",
    "text.summarize": "language",
    "review": "understanding",     "extract": "understanding",
    "plan": "agentic",             "research": "agentic",
}

EXCLUDED = frozenset({"operate", "acknowledge", "unclassified"})
PENDING_SPLIT = frozenset({"synthesize", "retrieve"})

# `Write` creates; everything else in AUTHOR_TOOLS modifies something that exists.
_CREATE_TOOLS = {"Write"}
_EDIT_TOOLS = {"Edit", "MultiEdit", "NotebookEdit"}

_DIRECT = {"verify": "review", "delegate": "plan"}
_AUTHOR = {"author_code": ("code.write", "code.edit"),
           "author_prose": ("text.create", "text.transform")}


def verb_for(cls, tools):
    """Map one request's activity class + tool evidence to an atv1 verb, or None.

    `tools` is the list of (name, input) pairs `reqclass.route_class` already
    receives. None means "publish no verb for this request" and is returned for
    an excluded class, a pending split, an unknown class, and an authoring class
    whose evidence does not say which side it is.
    """
    if cls in _DIRECT:
        return _DIRECT[cls]
    if cls in _AUTHOR:
        create, edit = _AUTHOR[cls]
        # ⚠️ DO NOT GUESS A SIDE. `classify_bash` and CODE_TOOLS both reach
        # `author_code` with no authoring tool in evidence at all (`python3 -c`,
        # `javascript_tool`), and PROSE_TOOLS reaches `author_prose` the same way.
        # Picking create-or-edit there would publish a false claim about someone's
        # work from evidence that does not contain the answer.
        for name, _ in tools:
            bare = name.split("__")[-1]
            if bare in _CREATE_TOOLS:
                return create
            if bare in _EDIT_TOOLS:
                return edit
        return None
    return None


def family_for(verb):
    """The atv1 family of a verb, or None. A 9->4 rollup, never an inference."""
    return VERB_FAMILY.get(verb)
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_verbs.py`
Expected: PASS, nine `ok test_*` lines.

- [ ] **Step 5: Verify the family table against the real atv1 CSV**

Run:
```bash
python3 - <<'PY'
import csv, sys
sys.path.insert(0, "sidecar")
from app.analysis.verbs import VERB_FAMILY
rows = list(csv.DictReader(open("/home/dg/Downloads/keld-activity-types-v1.csv")))
atv1 = {r["verb"]: r["family"] for r in rows}
bad = {v: (f, atv1.get(v)) for v, f in VERB_FAMILY.items() if atv1.get(v) != f}
print("MISMATCHES:", bad or "none")
assert not bad, bad
print(f"all {len(VERB_FAMILY)} verb->family pairs match atv1")
PY
```
Expected: `all 9 verb->family pairs match atv1`.
⚠️ If the CSV is not at that path, locate it rather than skipping — this step is the only thing tying the table to its source, and a hand-typed family is exactly the drift `DynamicStatuses` is pinned against elsewhere in this repo.

- [ ] **Step 6: Commit**

```bash
git add sidecar/app/analysis/verbs.py sidecar/app/test_analysis_verbs.py
git commit -m "verbs: map reqclass classes onto the atv1 verb vocabulary

Adds no classifier. Four of six work-bearing classes map definitionally;
operate/acknowledge/unclassified publish no verb as a scope decision, not a gap;
synthesize/retrieve abstain until their split study passes, with no unsplit
parent to fall back to."
```

---

### Task 2: Publish `activity_verbs`, end to end

**Files:**
- Modify: `sidecar/app/analysis/levels.py` (beside the `activity_class` emission, ~line 412)
- Modify: `sidecar/app/analysis/dimensions.py` (`INVENTORY`, ~line 249)
- Modify: `internal/agent/enrich/sidecar/client.go:495-496`
- Modify: `internal/agent/enrich/sidecar/dimensions.go:179-180`
- Modify: `internal/agent/enrich/dynamics.go:130-141`
- Modify: `internal/agent/publish/facets.go:79-80,121-122`
- Modify: `internal/agent/enrich/labels.go:383` (`SchemaVersion` 26 → 27)
- Modify: `sidecar/app/analysis/testdata/fixture-identity-baseline.json`
- Test: `sidecar/app/test_analysis_levels.py`

**Interfaces:**
- Consumes: `verbs.verb_for(cls, tools)` from Task 1.
- Produces: levels `activity_verb` and `activity_verb_tokens`; Go fields `ActivityVerbs` / `ActivityVerbTokens` of type `[]enrich.NameCount` on `WindowAnalysis` and `AnalysisFacets`, JSON keys `activity_verbs` / `activity_verb_tokens`.

- [ ] **Step 1: Write the failing test**

Append to `sidecar/app/test_analysis_levels.py`:

```python
def test_activity_verb_is_emitted_beside_activity_class():
    """A Write of a .go file is author_code, which is atv1 `code.write`."""
    rows = _levels_for_turn(role="assistant",
                            tools=[("Write", {"file_path": "x.go"})],
                            text="", out_tokens=120)
    assert ("activity_verb", "code.write") in {(l, r) for _, l, r, _ in rows}


def test_activity_verb_tokens_carries_the_output_weight():
    rows = _levels_for_turn(role="assistant",
                            tools=[("Write", {"file_path": "x.go"})],
                            text="", out_tokens=120)
    w = [n for _, l, r, n in rows if l == "activity_verb_tokens" and r == "code.write"]
    assert w == [120], w


def test_zero_output_tokens_emits_no_token_row():
    # REVIEW FOCUS 3: matches activity_class_tokens' existing `if _out:` guard.
    # A zero-weight row would make the token distribution claim a request
    # produced output when it produced none.
    rows = _levels_for_turn(role="assistant",
                            tools=[("Write", {"file_path": "x.go"})],
                            text="", out_tokens=0)
    assert not [1 for _, l, _, _ in rows if l == "activity_verb_tokens"]
    assert [1 for _, l, _, _ in rows if l == "activity_verb"]   # count row still emitted


def test_an_excluded_class_emits_no_verb_row_at_all():
    """`git push` is `operate` -- not work, so no verb and no token row."""
    rows = _levels_for_turn(role="assistant",
                            tools=[("Bash", {"command": "git push"})],
                            text="", out_tokens=40)
    assert ("activity_class", "operate") in {(l, r) for _, l, r, _ in rows}
    assert not [1 for _, l, _, _ in rows if l.startswith("activity_verb")]


def test_a_pending_split_emits_no_verb_row():
    """A plain Read is `retrieve`, which abstains until the split study lands."""
    rows = _levels_for_turn(role="assistant",
                            tools=[("Read", {"file_path": "x.go"})],
                            text="", out_tokens=40)
    assert ("activity_class", "retrieve") in {(l, r) for _, l, r, _ in rows}
    assert not [1 for _, l, _, _ in rows if l.startswith("activity_verb")]


def test_a_user_turn_emits_no_verb_row():
    """A new level may DESCRIBE existing evidence; it may never CREATE evidence
    where a turn produced none. A verb row on a user turn would make that turn's
    5-minute bin ACTIVE and silently shift every block boundary derived from it."""
    rows = _levels_for_turn(role="user", tools=[], text="do the thing", out_tokens=0)
    assert not [1 for _, l, _, _ in rows if l.startswith("activity_verb")]
```

⚠️ `_levels_for_turn` is a helper this test file may not have. If it does not exist, write it in the same file, following whatever construction the file's existing `activity_class` tests already use to build a turn and collect `add(...)` calls — do not invent a second idiom beside an existing one.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_levels.py`
Expected: FAIL — no `activity_verb` rows are produced.

- [ ] **Step 3: Emit the levels**

In `sidecar/app/analysis/levels.py`, inside the existing
`if o.role != "user" and (o.tool_calls or (o.text or "").strip()):` block, after the
`activity_class_tokens` emission, add:

```python
            # `activity_verb` -- the atv1 VERB this request's capability maps to.
            #
            # ⚠️ DERIVED FROM THE CLASS ABOVE, NOT INDEPENDENTLY CLASSIFIED. It is a
            # lookup over `route_class`'s existing output (analysis/verbs.py), which is
            # why it needs no validation of its own for the six verbs it produces and
            # why it must never drift from the class rows beside it.
            #
            # ⚠️ ABSENT ON PURPOSE for three classes. `operate`, `acknowledge` and
            # `unclassified` are not work -- running `git push` stresses no model
            # capability that routing could act on -- and `synthesize`/`retrieve`
            # abstain pending their split study. `verb_for` returns None for all five
            # and NO ROW IS EMITTED: the verb distribution is deliberately over less
            # than the whole block, and a consumer must not read its total as the
            # block's request count. The `activity_class` rows beside it remain the
            # complete denominator.
            _cls = reqclass.route_class({
                "tools": [(c.name, c.input) for c in o.tool_calls],
                "text":  o.text or "",
                "think": o.think_chars or 0,
                "out":   int((o.usage or {}).get("output_tokens") or 0),
            })
            _verb = verbs.verb_for(_cls, [(c.name, c.input) for c in o.tool_calls])
            if _verb:
                add("ref", "activity_verb", _verb, 1)
                # Same two-denominator argument as activity_class_tokens: on one real
                # block `author_prose` is 9.4% of calls and 25.8% of output tokens
                # while `retrieve` is 18.9% of calls and 5.5% of tokens. Publishing
                # one denominator misreports the block. ⚠️ NOT a cost figure --
                # output is 10-14% of modelled cost, the rest being cache reads.
                _vout = int((o.usage or {}).get("output_tokens") or 0)
                if _vout:
                    add("ref", "activity_verb_tokens", _verb, _vout)
```

Add `verbs` to the module's imports beside `reqclass`.

⚠️ `route_class` is now called three times per turn in this block. That is acceptable — it is pure and cheap — but if the implementer prefers, hoisting the existing two calls into one local is a safe, behaviour-identical simplification. Do not change what any of them is passed.

- [ ] **Step 4: Register both levels in `INVENTORY`**

In `sidecar/app/analysis/dimensions.py`, after the `activity_class_tokens` entry:

```python
             # `activity_verbs` -- the atv1 VERB of each request, as a DISTRIBUTION,
             # for the same reason activity_classes is one: above ~20 requests no unit
             # is coherent enough for a single label while the distribution stays
             # distinctive. Must not be moved to ALLOCATION -- that floor (winning
             # share >= 0.50 and >= MIN_EVIDENCE) would publish `no_majority` on most
             # units and discard the signal.
             #
             # Cap 9 is the WHOLE closed vocabulary (analysis/verbs.VERBS), so this
             # level can never be truncated and `inventory_omitted` can never name it.
             # ⚠️ IT IS 9 ALTHOUGH ONLY SIX VERBS ARE PRODUCED TODAY: the vocabulary is
             # declared whole so a passing split study changes which values are
             # produced and never the published vocabulary -- no second schema bump.
             ("activity_verbs", "activity_verb", 9),
             ("activity_verb_tokens", "activity_verb_tokens", 9),
```

- [ ] **Step 5: Run the sidecar tests**

Run:
```bash
cd sidecar && for f in app/test_analysis_levels.py app/test_analysis_verbs.py app/test_analysis_dimensions.py; do
  [ -f "$f" ] && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python "$f"; done
```
Expected: PASS on each that exists.

- [ ] **Step 6: Wire the Go path — four stops**

`internal/agent/enrich/sidecar/client.go`, beside lines 495-496:
```go
	ActivityVerbs        []InventoryItem `json:"activity_verbs"`
	ActivityVerbTokens   []InventoryItem `json:"activity_verb_tokens"`
```

`internal/agent/enrich/sidecar/dimensions.go`, beside lines 179-180:
```go
		ActivityVerbs:        convertIdentifierInventory(inv.ActivityVerbs),
		ActivityVerbTokens:   convertIdentifierInventory(inv.ActivityVerbTokens),
```

`internal/agent/enrich/dynamics.go`, beside the `ActivityClassTokens` field:
```go
	// ActivityVerbs is the atv1 VERB each inference request's capability maps to,
	// as a distribution. DERIVED from ActivityClasses by lookup (sidecar
	// analysis/verbs.py), never classified independently.
	//
	// ⚠️ ITS TOTAL IS NOT THE BLOCK'S REQUEST COUNT. Five of the nine activity
	// classes publish no verb -- operate/acknowledge/unclassified are excluded as
	// not-work, and synthesize/retrieve abstain pending their split study -- so
	// this distribution deliberately covers less than the whole block.
	// ActivityClasses is the complete denominator; a consumer that normalises
	// against this one is reporting shares of a subset as shares of the work.
	ActivityVerbs []NameCount
	// The same verbs weighted by OUTPUT TOKENS rather than counted. Two
	// denominators because they disagree by up to 3x on the same block.
	// ⚠️ Not a cost figure -- output is 10-14% of modelled cost.
	ActivityVerbTokens []NameCount
```

`internal/agent/publish/facets.go`, beside lines 79-80 and 121-122:
```go
	ActivityVerbs        []enrich.NameCount `json:"activity_verbs,omitempty"`
	ActivityVerbTokens   []enrich.NameCount `json:"activity_verb_tokens,omitempty"`
```
```go
		ActivityVerbs:        a.ActivityVerbs,
		ActivityVerbTokens:   a.ActivityVerbTokens,
```

⚠️ `facetsOf` is the ONE function both row builders call, and a field added to `AnalysisFacets` but not copied here reaches Atlas on NO row — that defect already happened once in this branch with `activity_classes`. `internal/agent/publish/facets_reachability_test.go` is a reflection test that will fail if either field is missed; do not weaken it.

- [ ] **Step 7: Bump the schema**

`internal/agent/enrich/labels.go:383`: `const SchemaVersion = 27`

⚠️ A vocabulary change is contract-affecting. Note in the commit that the enrichment eval (`internal/agent/enrich/eval/`) is owed a re-run; this plan does not run it, because the verb levels are deterministic and model-free and the eval scores model-backed facets.

- [ ] **Step 8: Run the Go tests**

Run: `go test ./...`
Expected: PASS. If `facets_reachability_test.go` fails, a field was missed in Step 6 — fix it there rather than amending the test.

- [ ] **Step 9: Rebaseline the fixture, and VERIFY IT ADDITIVE**

Run:
```bash
OUT=$(mktemp -d)
KELD_TERMS=0 ~/.keld/study-venv/bin/python scripts/refseries.py extract \
  --roots sidecar/app/analysis/testdata/fixture-corpus/projects \
  --repo-root /nonexistent/fixture-repo-root --component-depth 3 --workers 1 --outdir "$OUT"
~/.keld/study-venv/bin/python scripts/check_identity.py verify \
  sidecar/app/analysis/testdata/fixture-identity-baseline.json "$OUT"
```

⚠️ **READ THE DIFF BEFORE REGENERATING.** Every line must be a `None -> {...}` for
`ref/activity_verb` or `ref/activity_verb_tokens`. **If ANY existing level's rows, total
or sha changes, STOP** — Task 2 has altered behaviour it was not supposed to touch, and
regenerating would hide that regression behind a legitimate-looking schema addition.
Report it rather than rebaselining.

Then, only if the diff is purely additive:
```bash
~/.keld/study-venv/bin/python scripts/check_identity.py snapshot "$OUT" \
  -o sidecar/app/analysis/testdata/fixture-identity-baseline.json
make fixture-identity-check STUDY_PYTHON=$HOME/.keld/study-venv/bin/python
```
Expected: `IDENTICAL`.

- [ ] **Step 10: Commit**

```bash
git add sidecar/app/analysis/levels.py sidecar/app/analysis/dimensions.py \
        sidecar/app/test_analysis_levels.py internal/ \
        sidecar/app/analysis/testdata/fixture-identity-baseline.json
git commit -m "publish activity_verbs: six atv1 verbs, declared vocabulary of nine

A lookup over route_class's existing output, emitted beside activity_class and
travelling the same four-stop Go path. The distribution deliberately covers less
than the block -- five classes publish no verb -- so activity_classes remains the
complete denominator. Schema 26 -> 27. Fixture rebaseline verified additive."
```

---

### Task 3: The split study's frame

**Files:**
- Create: `scripts/verbsplit_frame.py`

**Interfaces:**
- Consumes: nothing from earlier tasks (the study is independent of the published level).
- Produces: `/tmp/claude-1000/verbsplit/frame.ndjson`, one row per request:
  `{"id": "<12-hex>", "corpus": "ours"|"wildchat", "cls": "synthesize"|"retrieve", "tools": [[name, input], ...], "prior": ["<cls>", ...], "out": <int>, "text": "<str>"}`
  where `prior` is the activity classes of the preceding 5 requests in the same session, oldest first.

- [ ] **Step 1: Write the frame script**

```python
"""Extract `synthesize` and `retrieve` requests for the verb-split study.

⚠️ TWO CORPORA, AND THE SECOND IS THE POINT. `is_report()` -- which any sensible
`synthesize` splitter will reach for -- was TUNED on our own transcripts, so
validating a splitter that uses it against those same transcripts is partly
circular. WildChat is the holdout, already used in reqclass as a tool-free
negative control over 11,575 assistant turns.

⚠️ UNLIKE THE `context` AXIS, THIS NEEDS NO DOMAIN DIVERSITY. What the model DID
is visible whatever field the work served, which is exactly why engineering-only
corpora can answer this question when they could not answer that one.

Corpus paths come from the environment and are NEVER hardcoded:
    KELD_CORPUS_A, KELD_CORPUS_B   our transcript roots
    KELD_WILDCHAT                  the WildChat parquet shard
"""
import hashlib, json, os, random, sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "sidecar"))
from app.analysis import reqclass

OUT = "/tmp/claude-1000/verbsplit/frame.ndjson"
PRIOR_N = 5
TARGET_PER_CELL = 40          # 4 cells: {ours,wildchat} x {synthesize,retrieve}
SEED = 20261001


def _rid(corpus, session, idx):
    return hashlib.sha1(f"{corpus}:{session}:{idx}".encode()).hexdigest()[:12]


def _requests_from(turns):
    """Yield (idx, cls, tools, out, text) for each assistant-shaped turn."""
    for i, t in enumerate(turns):
        if t.get("role") == "user":
            continue
        tools = [(c.get("name", ""), c.get("input") or {}) for c in t.get("tool_calls") or []]
        text = t.get("text") or ""
        if not tools and not text.strip():
            continue
        rec = {"tools": tools, "text": text,
               "think": t.get("think_chars") or 0,
               "out": int((t.get("usage") or {}).get("output_tokens") or 0)}
        yield i, reqclass.route_class(rec), tools, rec["out"], text
```

⚠️ The two readers below depend on the exact on-disk shapes, which this plan does not
restate because they already have one definition each in this repo. Read
`sidecar/app/analysis/transcript.py` for our JSONL turn seams and reuse them; read
`scripts/convdomain_frame.py` for the WildChat parquet access pattern (`/tmp/claude-1000/wcvenv`,
pyarrow only, English rows). **Do not write a second parser for either.**

```python
def collect(corpus, sessions):
    """sessions: iterable of (session_id, [turn, ...])."""
    rows = []
    for sid, turns in sessions:
        seq = list(_requests_from(turns))
        classes = [c for _, c, _, _, _ in seq]
        for pos, (idx, cls, tools, out, text) in enumerate(seq):
            if cls not in ("synthesize", "retrieve"):
                continue
            rows.append({
                "id": _rid(corpus, sid, idx),
                "corpus": corpus,
                "cls": cls,
                "tools": [[n, i] for n, i in tools],
                # ⚠️ THE SEQUENCE IS EVIDENCE, and for `synthesize` it is the ONLY
                # evidence: route_class reaches that class only when the request has
                # NO tool calls, so its own request carries no tool signal at all.
                "prior": classes[max(0, pos - PRIOR_N):pos],
                "out": out,
                "text": text,
            })
    return rows


def main():
    rng = random.Random(SEED)
    allrows = []
    # Fill in the two readers per the note above.
    allrows += collect("ours", _read_our_sessions())
    allrows += collect("wildchat", _read_wildchat_sessions())

    picked = []
    for corpus in ("ours", "wildchat"):
        for cls in ("synthesize", "retrieve"):
            cell = [r for r in allrows if r["corpus"] == corpus and r["cls"] == cls]
            rng.shuffle(cell)
            if len(cell) < TARGET_PER_CELL:
                # ⚠️ LOUD, NEVER SILENT. A short cell changes what the study can
                # conclude, and a frame that quietly returns fewer rows is the
                # defect this repo has hit twice (19 of 372 items dropped by
                # walk-order selection; silent input drops in the doctype frame).
                print(f"WARNING cell {corpus}/{cls}: {len(cell)} rows, "
                      f"wanted {TARGET_PER_CELL}", file=sys.stderr)
            picked += cell[:TARGET_PER_CELL]

    rng.shuffle(picked)               # hide corpus and class from the labeller's eye
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    with open(OUT, "w") as fh:
        for r in picked:
            fh.write(json.dumps(r) + "\n")
    print(f"wrote {len(picked)} rows -> {OUT}")
    import collections
    print(collections.Counter((r["corpus"], r["cls"]) for r in picked))


if __name__ == "__main__":
    main()
```

- [ ] **Step 2: Run it and verify the cells filled**

Run: `KELD_CORPUS_A=... KELD_CORPUS_B=... KELD_WILDCHAT=... python3 scripts/verbsplit_frame.py`
Expected: `wrote 160 rows` and a Counter showing 40 in each of the four cells. Any `WARNING` line means a cell is short — report the real counts rather than lowering `TARGET_PER_CELL` to make the warning go away.

- [ ] **Step 3: Verify no row leaks its own answer**

Run:
```bash
python3 - <<'PY'
import json
rows=[json.loads(l) for l in open("/tmp/claude-1000/verbsplit/frame.ndjson")]
assert len({r["id"] for r in rows})==len(rows), "duplicate ids"
assert all(r["cls"] in ("synthesize","retrieve") for r in rows)
assert all(r["cls"]!="synthesize" or not r["tools"] for r in rows), \
    "a synthesize row carries tool calls -- route_class should make that impossible"
print(f"{len(rows)} rows, ids unique, synthesize rows are tool-free as expected")
PY
```
Expected: the assertion about tool-free `synthesize` rows holds. ⚠️ If it fails, `route_class` has a path to `synthesize` this plan did not account for — report it, because it changes what evidence the splitter can use.

- [ ] **Step 4: Commit**

```bash
git add scripts/verbsplit_frame.py
git commit -m "study: frame synthesize/retrieve requests from two corpora

WildChat is the holdout because is_report() was tuned on our own transcripts.
Sequence context is carried because a synthesize request has no tool calls at
all -- prior classes are its only structural evidence."
```

---

### Task 4: Blind labels, committed before any splitter exists

**Files:**
- Create: `scripts/verbsplit-labels.txt`

**Interfaces:**
- Consumes: `/tmp/claude-1000/verbsplit/frame.ndjson` from Task 3.
- Produces: `scripts/verbsplit-labels.txt`, lines `<id> <verb>` where verb ∈ {`text.summarize`, `research`, `extract`, `unclear`}.

- [ ] **Step 1: Render the rows for labelling**

Run:
```bash
python3 - <<'PY' > /tmp/claude-1000/verbsplit/sample.txt
import json
for r in (json.loads(l) for l in open("/tmp/claude-1000/verbsplit/frame.ndjson")):
    print(f"===== {r['id']} =====")
    print(f"tools: {[t[0] for t in r['tools']] or '(none)'}")
    print(f"tool args: {json.dumps([t[1] for t in r['tools']])[:400]}")
    print(f"output tokens: {r['out']}")
    print(f"--- text ---\n{r['text'][:3000]}")
    if len(r['text'])>3000: print(f"... [{len(r['text'])-3000} more chars]")
    print()
PY
wc -l /tmp/claude-1000/verbsplit/sample.txt
```

⚠️ The renderer prints **`id`, tools, tool args, output tokens and text** and **NOT** `corpus`, `cls` or `prior`. A labeller who knows a row came from WildChat, or which parent class it had, is anchored before reading it. (The frame already shuffles, so position leaks nothing.)

- [ ] **Step 2: Label every row blind**

Read `/tmp/claude-1000/verbsplit/sample.txt` and assign each id one of:

| label | meaning |
|---|---|
| `text.summarize` | condensed something already in the conversation — no new gathering |
| `research` | gathered across sources, or read broadly to understand, then reported |
| `extract` | pulled a specific, known value or field out of something |
| `unclear` | genuinely cannot tell from what is shown |

⚠️ Judge the REQUEST, not the session around it. ⚠️ `unclear` is a real answer and must not be minimised — a forced label is a wrong label, and the scorer counts `unclear` rows as unscoreable rather than as either side.

Write `scripts/verbsplit-labels.txt`:
```
# Blind labels for the verb-split study. Assigned from text+tools only, with
# corpus and parent class hidden. Committed BEFORE any splitter exists.
<id> <verb>
...
```

- [ ] **Step 3: Verify coverage before committing**

Run:
```bash
python3 - <<'PY'
import json
ids={json.loads(l)["id"] for l in open("/tmp/claude-1000/verbsplit/frame.ndjson")}
lab={}
for l in open("scripts/verbsplit-labels.txt"):
    l=l.strip()
    if l and not l.startswith("#"):
        i,v=l.split(); lab[i]=v
assert set(lab)==ids, f"missing {len(ids-set(lab))}, extra {len(set(lab)-ids)}"
assert set(lab.values())<={"text.summarize","research","extract","unclear"}, set(lab.values())
import collections; print(len(lab), "labels;", collections.Counter(lab.values()))
PY
```
Expected: every id labelled exactly once, values in-vocabulary.

- [ ] **Step 4: Commit — and keep the distribution OUT of the message**

```bash
git add scripts/verbsplit-labels.txt
git commit -m "study: blind verb-split labels, committed before any splitter exists

Assigned from text and tool evidence only, with corpus and parent class hidden.
Aggregate counts are deliberately omitted here: the conversation-domain study's
blindness was compromised when its arm author read the distribution out of
git show. They belong in the results note, written after scoring."
```

⚠️ Do not paste counts into the commit message, the branch name, or any file the Task 5 implementer can read.

---

### Task 5: The two splitters and the pre-registered bar

**Files:**
- Create: `scripts/verbsplit_study.py`

**Interfaces:**
- Consumes: `/tmp/claude-1000/verbsplit/frame.ndjson`.
- Produces: `split_synthesize(row) -> str | None`, `split_retrieve(row) -> str | None`, `BAR_PRECISION`, `BAR_MARGIN`, and `/tmp/claude-1000/verbsplit/preds.json` keyed by id.

⚠️ **The implementer of this task MUST NOT read `scripts/verbsplit-labels.txt`**, its git history, or any commit message naming label counts.

- [ ] **Step 1: Write the splitters**

```python
"""The two verb splitters, and the bar they are judged against.

⚠️ BOTH DISCRIMINATORS ARE STRUCTURAL -- tool identity, tool arguments, request
order. NEVER TEXTUAL. Six routes to inferring meaning from prose have been
measured and refused on this question (GLiNER2 on prompts 12.7%; GLiNER2 on paths
one domain only; keyword lists 17%; agent self-report, which never reaches the
model; document-type recognition, parked; conversation-domain on WildChat, every
arm below its bar). A splitter that resolves to "read the text and decide" is the
seventh attempt at a refuted thing. `test_text_is_not_read` pins this.

Committed BEFORE scoring. The author has not seen the labels.
"""
import json, sys

BAR_PRECISION = 0.80
BAR_MARGIN = 0.20          # over the majority-class baseline, per parent class

RETRIEVE_BROAD = {"LS", "Glob", "find", "WebSearch", "ListAgents", "CronList",
                  "notion-search", "notion-ai-search", "ToolSearch"}
RETRIEVE_POINTED = {"Grep", "notion-query-data-sources", "list_network_requests"}


def split_synthesize(row):
    """`text.summarize` vs `research`, from SEQUENCE alone.

    A synthesize request has NO tool calls by construction -- route_class reaches
    it only when `names` is empty -- so its own request carries no tool evidence.
    What it has is position: a synthesis that FOLLOWS retrieval is reporting on
    what was gathered (`research`); one that follows none condensed what was
    already in context (`text.summarize`).
    """
    prior = row.get("prior") or []
    if not prior:
        return None                       # nothing to reason from; abstain
    gathered = sum(1 for c in prior if c == "retrieve")
    if gathered >= 2:
        return "research"
    if gathered == 0:
        return "text.summarize"
    return None                           # exactly one: genuinely ambiguous


def split_retrieve(row):
    """`extract` vs `research`, from TOOL IDENTITY AND ARGUMENTS.

    Pulling a known thing out is `extract`: a Grep for a pattern, or a Read
    bounded by offset/limit. Reading to understand is `research`: a whole-file
    Read, an LS, a find.
    """
    for name, inp in (row.get("tools") or []):
        bare = name.split("__")[-1]
        if bare in RETRIEVE_POINTED:
            return "extract"
        if bare in RETRIEVE_BROAD:
            return "research"
        if bare in ("Read", "NotebookRead"):
            inp = inp or {}
            return "extract" if (inp.get("offset") or inp.get("limit")) else "research"
        if bare in ("WebFetch", "notion-fetch", "get_page_text", "read_page"):
            return "research"
    return None


def predict(row):
    return (split_synthesize(row) if row["cls"] == "synthesize"
            else split_retrieve(row))


def _selftest():
    assert split_retrieve({"tools": [["Grep", {"pattern": "x"}]]}) == "extract"
    assert split_retrieve({"tools": [["Read", {"file_path": "a.go"}]]}) == "research"
    assert split_retrieve({"tools": [["Read", {"file_path": "a.go", "offset": 10,
                                               "limit": 20}]]}) == "extract"
    assert split_retrieve({"tools": [["LS", {}]]}) == "research"
    assert split_retrieve({"tools": []}) is None
    assert split_synthesize({"prior": ["retrieve", "retrieve"]}) == "research"
    assert split_synthesize({"prior": ["author_code", "verify"]}) == "text.summarize"
    assert split_synthesize({"prior": ["retrieve", "verify"]}) is None
    assert split_synthesize({"prior": []}) is None
    test_text_is_not_read()
    print("ok selftest")


def test_text_is_not_read():
    """REVIEW FOCUS 5: the structural constraint, enforced rather than asserted.

    Replacing a row's prose with unrelated prose of the same length must not
    change any prediction. If it does, a splitter is reading text and the study
    has become the seventh attempt at a refuted approach.
    """
    rows = [json.loads(l) for l in open("/tmp/claude-1000/verbsplit/frame.ndjson")]
    for r in rows:
        before = predict(r)
        scrambled = dict(r, text="lorem ipsum dolor sit amet " * (len(r["text"]) // 27 + 1))
        assert predict(scrambled) == before, f"{r['id']}: prediction moved with text"
    print(f"ok text_is_not_read ({len(rows)} rows)")


if __name__ == "__main__":
    _selftest()
    if len(sys.argv) > 1 and sys.argv[1] == "predict":
        rows = [json.loads(l) for l in open("/tmp/claude-1000/verbsplit/frame.ndjson")]
        preds = {r["id"]: predict(r) for r in rows}
        json.dump(preds, open("/tmp/claude-1000/verbsplit/preds.json", "w"))
        import collections
        print(collections.Counter(preds.values()))
```

- [ ] **Step 2: Run the self-test**

Run: `~/.keld/sidecar-venv/bin/python scripts/verbsplit_study.py`
Expected: `ok selftest` and `ok text_is_not_read (160 rows)`.

- [ ] **Step 3: Write the predictions**

Run: `~/.keld/sidecar-venv/bin/python scripts/verbsplit_study.py predict`
Expected: a Counter over `research` / `extract` / `text.summarize` / `None`. Report the counts; they are not accuracies.

- [ ] **Step 4: Commit**

```bash
git add scripts/verbsplit_study.py
git commit -m "study: the two verb splitters and the pre-registered bar

Both discriminators are structural -- sequence for synthesize, tool identity and
arguments for retrieve -- and a test asserts predictions do not move when the
prose is replaced. Committed before scoring; the author has not seen the labels."
```

---

### Task 6: Score, per parent class, and write the results

**Files:**
- Modify: `scripts/verbsplit_study.py` (append the scorer)
- Create: `docs/notes/2026-10-01-verb-split-results.md`

**Interfaces:**
- Consumes: `preds.json`, `scripts/verbsplit-labels.txt`, `frame.ndjson`.
- Produces: the numbers and the gate recommendation. ⚠️ Must not edit the splitters.

- [ ] **Step 1: Append the scorer**

```python
def _score():
    import collections
    lab = {}
    for line in open("scripts/verbsplit-labels.txt"):
        line = line.strip()
        if line and not line.startswith("#"):
            i, v = line.split(); lab[i] = v
    rows = {json.loads(l)["id"]: json.loads(l)
            for l in open("/tmp/claude-1000/verbsplit/frame.ndjson")}
    preds = json.load(open("/tmp/claude-1000/verbsplit/preds.json"))

    print("=" * 78)
    print("⚠️ `unclear` rows are UNSCOREABLE and are reported separately -- they are")
    print("   neither right nor wrong, and folding them into either would invent a")
    print("   judgement the labeller declined to make.")
    print("⚠️ WILDCHAT IS THE HOLDOUT. is_report() was tuned on `ours`.")
    print("=" * 78)
    for cls in ("synthesize", "retrieve"):
        for corpus in ("ours", "wildchat", "BOTH"):
            ids = [i for i in lab
                   if rows[i]["cls"] == cls
                   and (corpus == "BOTH" or rows[i]["corpus"] == corpus)]
            scoreable = [i for i in ids if lab[i] != "unclear"]
            if not scoreable:
                print(f"  {cls:11} {corpus:9} no scoreable rows"); continue
            answered = [i for i in scoreable if preds.get(i) is not None]
            prec = (sum(1 for i in answered if preds[i] == lab[i]) / len(answered)
                    if answered else None)
            acc = sum(1 for i in scoreable if preds.get(i) == lab[i]) / len(scoreable)
            base = (collections.Counter(lab[i] for i in scoreable)
                    .most_common(1)[0][1] / len(scoreable))
            print(f"  {cls:11} {corpus:9} n={len(ids):3} unclear={len(ids)-len(scoreable):2} "
                  f"answered={len(answered):3} "
                  f"prec={'n/a' if prec is None else f'{100*prec:5.1f}%'} "
                  f"acc={100*acc:5.1f}% base={100*base:5.1f}% "
                  f"margin={100*(acc-base):+6.1f}")
    print(f"\nBAR: precision >= {100*BAR_PRECISION:.0f}% AND accuracy >= baseline + "
          f"{100*BAR_MARGIN:.0f} points. ⚠️ WILDCHAT decides it, not `ours`.")
    print("⚠️ A FAILING SPLIT PUBLISHES NOTHING -- there is no unsplit parent verb.")
```

Change the entrypoint so `score` runs it.

- [ ] **Step 2: Run the scorer**

Run: `~/.keld/sidecar-venv/bin/python scripts/verbsplit_study.py score`
Expected: a table for both classes across both corpora. Report it faithfully, including bad numbers.

- [ ] **Step 3: Independently recompute one headline number**

Write a throwaway snippet that shares no code with `_score` and recomputes the WildChat precision for whichever split looks strongest. Expected: it reproduces the scorer exactly.
⚠️ The scorer is the one component whose error would silently flatter or damn everything, so check it rather than trusting it.

- [ ] **Step 4: Write the results note**

Create `docs/notes/2026-10-01-verb-split-results.md` covering, in this order:
1. **The verdict per split, against the bar, stated plainly** — pass or fail, on WildChat.
2. **The numbers**, per class per corpus, with `unclear` counts beside them.
3. **The `ours` vs `wildchat` contrast.** A split strong on `ours` and weak on WildChat has learned our transcripts, which is exactly what the holdout exists to reveal.
4. **What a pass would and would not mean.** It would mean the verb is recoverable from structural evidence on real requests. It would NOT mean `context` is any closer — restate that this axis does not advance the business-understanding goal.
5. **Disclosures**: the `unclear` rate; any short cell from Task 3's warning; that the labeller and the splitter author were different contexts and the labels were committed first, provable from git.
6. **The gate, as options for the repo owner, deciding nothing** — per spec §7: both pass → nine verbs; one passes → that split plus abstention for the other; neither → six verbs, recorded beside the six context negatives.

- [ ] **Step 5: Commit**

```bash
git add scripts/verbsplit_study.py docs/notes/2026-10-01-verb-split-results.md
git commit -m "study: verb-split results, scored per parent class on the holdout"
```

- [ ] **Step 6: STOP at the gate**

Do not implement the gate's outcome. Report the numbers and the options to the repo owner. If both splits pass, wiring the three extra verbs is a one-line change in `verbs.py` (`PENDING_SPLIT` shrinks and the splitters move into the module) needing **no schema bump**, because the vocabulary was declared whole in Task 1.

---

## Self-Review

**1. Spec coverage.** §1 two axes → Global Constraints + Task 6 step 4. §2 reqclass validation → Task 1 docstring. §3a lookup → Task 1. §3b exclusions → Task 1 test + Task 2 emission comment. §3c splits → Tasks 3-6. §3d nine verbs/four families → Task 1 Steps 1,5. §4 structural discriminators → Task 5 + `test_text_is_not_read`. §4 bar → Task 5 constants, Task 6 scorer. §4 WildChat holdout → Task 3. §4 blindness → Task 4 Step 4. §5 INVENTORY cap 9 → Task 2 Step 4. §5 token sibling → Task 2 Steps 3,4. §5 no family level → Task 1 (table only). §5 schema + fixture → Task 2 Steps 7,9. §7 gate → Task 6 Step 6. **No gaps.**

**2. Placeholder scan.** Two deliberate deferrals, both named with the file to read rather than left vague: Task 3's two corpus readers (point to `transcript.py` and `convdomain_frame.py`, with "do not write a second parser") and Task 2's `_levels_for_turn` helper (point to the file's existing idiom). Everything else carries runnable code.

**3. Type consistency.** `verb_for(cls, tools)` — `tools` is `[(name, input)]` in Task 1 and Task 2's call site; the frame stores `[[name, input]]` (JSON) and Task 5 unpacks it the same way. `VERBS`/`VERB_FAMILY`/`EXCLUDED`/`PENDING_SPLIT` named identically in Tasks 1 and 2. Go fields `ActivityVerbs`/`ActivityVerbTokens` consistent across all four stops.

**4. Review Focus.** All five placed: (1) Task 1 `test_excluded_classes_publish_no_verb`; (2) Task 1 `test_author_reached_without_an_authoring_tool_does_not_guess`; (3) Task 2 `test_zero_output_tokens_emits_no_token_row`; (4) Task 2 Step 9's STOP condition; (5) Task 5 `test_text_is_not_read`.
