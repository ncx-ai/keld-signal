# Document-type recognition study — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Measure whether externally-authored structural signatures can recognise what KIND of document an agent wrote, on real data, against blind labels committed beforehand.

**Architecture:** A study under `scripts/`, touching no production code. Extract documents the agent AUTHORED (`Write` calls — full content plus path) from both corpora; label a sample blind; write a signature table scoring distinct signal KINDS; score it against a pre-registered bar. Output is a number and a recommendation.

**Tech Stack:** Python 3.12 via `~/.keld/sidecar-venv/bin/python` (never the host interpreter). No model, no new dependencies, stdlib only.

**Spec:** `docs/superpowers/specs/2026-10-01-document-type-recognition-design.md`

## Global Constraints

- **PHASE 1 TOUCHES NO PRODUCTION CODE.** Nothing under `sidecar/app/` or `internal/` may be modified. If a task seems to need it, stop and report BLOCKED. Revert is `rm scripts/doctype_*`.
- **Corpus paths are environment-configured** (`KELD_CORPUS_A`, `KELD_CORPUS_B`), never hardcoded — these read private transcripts and a hardcoded path names whose.
- **Sidecar Python runs ONLY via `~/.keld/sidecar-venv/bin/python`** (3.12). The host interpreter is 3.14 and lacks the wheels.
- **⚠️ ORDER IS THE EXPERIMENT'S ONLY REAL GUARD.** Labels (Task 2) are committed BEFORE the signature table (Task 3) exists. The signature author must never see the labels. Provable from git.
- **Two signal KINDS minimum** or a document is unrecognised. One signal is a coincidence.
- **The score counts DISTINCT KINDS matched, not total matches.** Repeating one phrase five times is one signal.
- **Vocabulary is closed, 8 types:** `readme`, `design_spec`, `adr`, `runbook`, `postmortem`, `meeting_notes`, `changelog`, `plan`.
- **`Write` calls only.** `Edit` carries `old_string`/`new_string` — a FRAGMENT, which cannot show a document's structure.

## Review Focus

1. **A document matching two types equally** (a design spec containing a runbook section) must split its evidence rather than pick a winner — the output is a distribution. Tested in Task 3.
2. **A very short document** (a 3-line README stub) can match `filename` and little else; it must land under the two-kind floor and be reported unrecognised rather than scraping a win. Tested in Task 3.
3. **A document whose filename lies** (`notes.md` containing a full ADR) must still be recognised from its headings — filename is one signal, never sufficient alone. Tested in Task 3.
4. **Non-prose written files** (`.json`, `.go`, `.lock`) must never be offered a document type; they are out of vocabulary, not unrecognised-but-maybe. Tested in Task 1.
5. **A document written more than once in a session** (progressive drafts of the same path) must count once, not once per revision, or frequent editing inflates a type. Tested in Task 1.

---

### Task 1: The document frame

**Files:**
- Create: `scripts/doctype_frame.py`
- Create (output, gitignored): `/tmp/claude-1000/doctype/docs.ndjson`

**Interfaces:**
- Produces: `docs.ndjson`, one record per authored document: `{id, corpus, path, ext, nlines, head, content_chars}` where `head` is the first 40 lines verbatim.

- [ ] **Step 1: Write the extractor**

```python
"""Documents the agent AUTHORED, for the document-type study.

⚠️ CORPUS PATHS ARE ENVIRONMENT-CONFIGURED (KELD_CORPUS_A / KELD_CORPUS_B). These read
private transcripts; a hardcoded path names whose they are.

⚠️ `Write` CALLS ONLY. `Edit` carries `old_string`/`new_string` -- a FRAGMENT of a file,
which cannot show a document's structure. A study that mixed them would score the recogniser
on slices it could never classify, and report that as the recogniser failing.

⚠️ AND THE LAST WRITE WINS, ONCE. A path written several times in a session is one document
in progressive drafts; counting each revision would let frequent editing inflate whichever
type that file happens to be.
"""
import json, os, sys, hashlib

PROSE_EXT = {".md", ".txt", ".rst", ".adoc", ".org"}
OUT = "/tmp/claude-1000/doctype/docs.ndjson"


def corpus(var, what):
    p = os.environ.get(var)
    if not p:
        sys.exit(f"set {var} to the {what} directory")
    return os.path.expanduser(p)


def writes_in(path):
    """Every `Write` tool call in one transcript: (file_path, content)."""
    try:
        fh = open(path, encoding="utf-8", errors="replace")
    except OSError:
        return
    with fh:
        for line in fh:
            if '"tool_use"' not in line or '"Write"' not in line:
                continue
            try:
                rec = json.loads(line)
            except Exception:
                continue
            content = (rec.get("message") or {}).get("content")
            if not isinstance(content, list):
                continue
            for b in content:
                if not isinstance(b, dict) or b.get("type") != "tool_use":
                    continue
                if b.get("name") != "Write":
                    continue
                inp = b.get("input") or {}
                fp, body = inp.get("file_path"), inp.get("content")
                if isinstance(fp, str) and isinstance(body, str) and body.strip():
                    yield fp, body


def main():
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    latest = {}                      # (corpus, path) -> content; last write wins
    for var, tag in (("KELD_CORPUS_A", "A"), ("KELD_CORPUS_B", "B")):
        root = corpus(var, tag)
        for dp, _, names in os.walk(root):
            for fn in names:
                if not fn.endswith(".jsonl"):
                    continue
                for fp, body in writes_in(os.path.join(dp, fn)):
                    latest[(tag, fp)] = body

    kept = skipped = 0
    with open(OUT, "w") as out:
        for (tag, fp), body in sorted(latest.items()):
            ext = os.path.splitext(fp)[1].lower()
            if ext not in PROSE_EXT:     # a .go or .json file has no document TYPE
                skipped += 1
                continue
            lines = body.splitlines()
            out.write(json.dumps({
                "id": hashlib.sha1(f"{tag}:{fp}".encode()).hexdigest()[:10],
                "corpus": tag,
                "path": fp,
                "ext": ext,
                "nlines": len(lines),
                "content_chars": len(body),
                "head": "\n".join(lines[:40]),
            }, separators=(",", ":")) + "\n")
            kept += 1
    print(f"authored documents kept: {kept:,}   non-prose skipped: {skipped:,}")
    print(f"wrote {OUT}")


if __name__ == "__main__":
    main()
```

- [ ] **Step 2: Run it**

```bash
cd /tmp/claude-1000/wt-combined
KELD_CORPUS_A=~/keld/john-projects/projects \
KELD_CORPUS_B=~/keld/refseries-context/frozen-corpus/projects \
~/.keld/sidecar-venv/bin/python scripts/doctype_frame.py
```

Expected: several hundred prose documents kept, and a non-trivial number of non-prose skipped. **If it keeps 0, stop** — either `Write` is not being found or the extensions filter is wrong, and nothing downstream is meaningful.

- [ ] **Step 3: Verify the two Review Focus items this task owns**

```bash
~/.keld/sidecar-venv/bin/python -c "
import json, collections
rows=[json.loads(l) for l in open('/tmp/claude-1000/doctype/docs.ndjson')]
print('documents:', len(rows))
print('extensions:', dict(collections.Counter(r['ext'] for r in rows)))
# Review Focus 4: no non-prose survived
assert all(r['ext'] in {'.md','.txt','.rst','.adoc','.org'} for r in rows), 'non-prose leaked'
print('non-prose leaked: none')
# Review Focus 5: one record per path, not one per revision
k=[(r['corpus'],r['path']) for r in rows]
assert len(k)==len(set(k)), 'a path appears twice — revisions are not collapsing'
print('duplicate paths: none (last write wins)')
print('median lines:', sorted(r['nlines'] for r in rows)[len(rows)//2])
"
```

- [ ] **Step 4: Commit**

```bash
git add scripts/doctype_frame.py
git commit -m "study: extract authored documents (Write calls) for the doctype study"
```

---

### Task 2: Blind labels

**Files:**
- Create: `scripts/doctype_sample.py`
- Create: `scripts/doctype-labels.txt`

**Interfaces:**
- Consumes: `/tmp/claude-1000/doctype/docs.ndjson`
- Produces: `scripts/doctype-labels.txt`, lines of `<id> <type>` where type is one of the 8 or `other`

⚠️ **THE LABELLER MUST NOT SEE A SIGNATURE TABLE, BECAUSE NONE EXISTS YET.** Task 3 writes it. If you are implementing this task and a signature table is already present in the repo, STOP and report BLOCKED — the experiment's only guard has been broken and the labels are no longer independent.

- [ ] **Step 1: Write the sampler**

```python
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
```

- [ ] **Step 2: Render the sample and label it**

```bash
~/.keld/sidecar-venv/bin/python scripts/doctype_sample.py 60 > /tmp/claude-1000/doctype/sample.txt
wc -l /tmp/claude-1000/doctype/sample.txt
```

Read every one of the 60 and assign exactly one label from: `readme`, `design_spec`, `adr`, `runbook`, `postmortem`, `meeting_notes`, `changelog`, `plan`, `other`.

Rules, fixed here before any labelling:
- Judge by **what the document IS**, not what it is about. A design spec about a runbook is a `design_spec`.
- `other` is honest abstention for a document that is none of the eight. Use it freely — a forced label is worse than an abstention and will be scored as such.
- Do not consult the filename alone. A file named `plan.md` holding meeting notes is `meeting_notes`.

- [ ] **Step 3: Write the labels file**

Format, one per line, with a header recording the ordering guarantee:

```
# BLIND LABELS for the document-type study.
# Assigned from filename + first 40 lines ONLY. No signature table existed when these were
# written — Task 3 creates it, and this file is committed first, which git proves.
# Labeller and signature author are the same party, so this ORDER is the only thing
# preventing the signatures from being fitted to the answers.
#
# <id> <type>    type in: readme design_spec adr runbook postmortem meeting_notes changelog plan other
a1b2c3d4e5 design_spec
...
```

- [ ] **Step 4: Verify every sampled id is labelled exactly once**

```bash
~/.keld/sidecar-venv/bin/python -c "
import json,collections
V={'readme','design_spec','adr','runbook','postmortem','meeting_notes','changelog','plan','other'}
lab={}
for l in open('scripts/doctype-labels.txt'):
    l=l.strip()
    if not l or l.startswith('#'): continue
    i,t=l.split()
    assert t in V, f'unknown label {t}'
    assert i not in lab, f'duplicate id {i}'
    lab[i]=t
known={json.loads(l)['id'] for l in open('/tmp/claude-1000/doctype/docs.ndjson')}
unknown=set(lab)-known
assert not unknown, f'labelled ids not in the frame: {sorted(unknown)[:3]}'
print('labels:', len(lab), '— every id resolves to a frame document')
print('distribution:', dict(collections.Counter(lab.values()).most_common()))
maj=collections.Counter(lab.values()).most_common(1)[0]
print(f'MAJORITY-CLASS BASELINE: {maj[0]} at {100*maj[1]/len(lab):.1f}% — this is the floor to beat')
"
```

- [ ] **Step 5: Hand 15 labels to the repo owner for spot-check**

⚠️ **SPEC §6 REQUIRES THIS AND IT IS THE ONLY EXTERNAL CHECK IN THE WHOLE STUDY.** The
labeller and the signature author are the same party; commit order stops the signatures being
fitted to the labels, but nothing stops the LABELS themselves being systematically wrong, and
everything downstream would inherit that silently.

Print 15 of the labelled documents with the label assigned, and ask the repo owner to confirm
or correct each:

```bash
~/.keld/sidecar-venv/bin/python -c "
import json,random
lab=dict(l.split() for l in open('scripts/doctype-labels.txt') if l.strip() and not l.startswith('#'))
docs={json.loads(l)['id']:json.loads(l) for l in open('/tmp/claude-1000/doctype/docs.ndjson')}
ids=sorted(lab); random.Random(7).shuffle(ids)
for i in ids[:15]:
    d=docs[i]
    print(f\"--- {i}  LABELLED: {lab[i]}\")
    print(f\"    path: {d['path']}\")
    print('    ' + '\\n    '.join(d['head'].splitlines()[:12]))
    print()
"
```

Apply any corrections they give, then re-run Step 4's verification. **If they correct more
than 3 of 15, stop and re-label the whole sample** — a 20% label error rate makes every
downstream number meaningless, and continuing would produce a precise measurement of nothing.

- [ ] **Step 6: Commit the labels BEFORE any signature work**

```bash
git add scripts/doctype_sample.py scripts/doctype-labels.txt
git commit -m "study: 60 blind document-type labels, committed before the signature table exists"
```

⚠️ **Do not start Task 3 in the same commit, the same message, or without this commit landing first.** The ordering is the experiment.

---

### Task 3: The signature table and the recogniser

**Files:**
- Create: `scripts/doctype_study.py`

**Interfaces:**
- Produces: `SIGNATURES: dict[str, dict[str, list]]`, `recognise(path, head) -> dict[str, int]` (type → distinct kinds matched), `BAR` constants.

⚠️ **DO NOT OPEN `scripts/doctype-labels.txt` WHILE WRITING THIS TASK.** The signatures must come from the document FORMATS, not from the answers. If you read the labels first, the study measures nothing and cannot be repaired by committing afterwards.

- [ ] **Step 1: Write the signature table and recogniser**

```python
"""Recognise what KIND of document was written, from externally-authored structural signatures.

⚠️ THE SIGNATURES ARE TRANSCRIBED CONVENTIONS, NOT INVENTED KEYWORDS, and that distinction is
the whole reason to expect this to transfer. An ADR's Context/Decision/Consequences headings
come from the ADR convention; MUST/SHOULD from RFC 2119; `### Added`/`### Fixed` from Keep a
Changelog. Hand-written KEYWORD lists measured 52% on text their author wrote and 17% on real
paths -- they encoded the author's tells. A format somebody else published does not.

⚠️ THE SCORE IS THE COUNT OF DISTINCT SIGNAL KINDS, NOT TOTAL MATCHES. Repeating one phrase
five times is one signal. Two kinds minimum or the document is UNRECOGNISED: one signal is a
coincidence, and a file named `plan.md` is not a plan.
"""
import re

MIN_KINDS = 2

# kind -> how it is tested:
#   filename : regex over the full path, case-insensitive
#   headings : markdown headings that must appear (>= `need` of them)
#   phrases  : canonical phrases the format mandates (>= `need` of them)
SIGNATURES = {
    "readme": {
        "filename": [r"(^|/)readme(\.|$)"],
        "headings": {"need": 2, "any": [r"^#+\s*(installation|install|usage|getting started|quick ?start|requirements|license|contributing)"]},
        "phrases": {"need": 1, "any": [r"\bgit clone\b", r"\bnpm install\b", r"\bpip install\b"]},
    },
    "adr": {
        "filename": [r"(^|/)adr/", r"(^|/)\d{4}-.*\.md$", r"decision[-_]record"],
        "headings": {"need": 2, "any": [r"^#+\s*context\b", r"^#+\s*decision\b", r"^#+\s*consequences\b", r"^#+\s*status\b", r"^#+\s*alternatives\b"]},
        "phrases": {"need": 1, "any": [r"\bwe will\b", r"\bsuperseded by\b", r"\baccepted\b", r"\bdeprecated\b"]},
    },
    "changelog": {
        "filename": [r"(^|/)changelog(\.|$)", r"(^|/)history(\.|$)", r"(^|/)news(\.|$)"],
        "headings": {"need": 2, "any": [r"^#+\s*\[?v?\d+\.\d+", r"^#+\s*(added|changed|fixed|removed|deprecated|security)\b", r"^#+\s*unreleased\b"]},
        "phrases": {"need": 1, "any": [r"\bkeepachangelog\b", r"\bsemantic versioning\b", r"\bunreleased\b"]},
    },
    "runbook": {
        "filename": [r"runbook", r"playbook", r"(^|/)ops?/"],
        "headings": {"need": 1, "any": [r"^#+\s*(procedure|steps|rollback|escalation|on-?call|prerequisites|verification)\b"]},
        "phrases": {"need": 2, "any": [r"^\s*\d+\.\s", r"\bif .* then\b", r"\brollback\b", r"\bverify that\b", r"\bon-?call\b"]},
    },
    "postmortem": {
        "filename": [r"post-?mortem", r"incident", r"retro"],
        "headings": {"need": 2, "any": [r"^#+\s*(timeline|impact|root cause|contributing factors|what went well|action items|detection)\b"]},
        "phrases": {"need": 1, "any": [r"\broot cause\b", r"\bcontributing factor", r"\btime to (detect|mitigate)\b", r"\bblameless\b"]},
    },
    "meeting_notes": {
        "filename": [r"(meeting|notes|standup|sync|1-?1|weekly)"],
        "headings": {"need": 1, "any": [r"^#+\s*(attendees|agenda|action items|notes|decisions|next steps)\b"]},
        "phrases": {"need": 2, "any": [r"\battendees?\b", r"\baction items?\b", r"\bagenda\b", r"^\s*[-*]\s*\w+:\s", r"\bnext steps\b"]},
    },
    "design_spec": {
        "filename": [r"(spec|design|rfc|proposal)"],
        "headings": {"need": 2, "any": [r"^#+\s*(goals?|non-?goals?|motivation|background|proposal|alternatives considered|open questions|design)\b"]},
        "phrases": {"need": 1, "any": [r"\bMUST\b", r"\bSHOULD\b", r"\bnon-?goals?\b", r"\balternatives considered\b", r"\bopen questions?\b"]},
    },
    "plan": {
        "filename": [r"(^|/)plans?/", r"implementation-?plan", r"(^|/)roadmap"],
        "headings": {"need": 2, "any": [r"^#+\s*(phase|task|milestone|step)\s*\d", r"^#+\s*(scope|deliverables?|timeline|sequence)\b"]},
        "phrases": {"need": 1, "any": [r"^\s*-\s*\[[ x]\]", r"\bphase \d\b", r"\btask \d\b", r"\bmilestone\b"]},
    },
}


def _hits(patterns, text, flags=re.I | re.M):
    return sum(1 for p in patterns if re.search(p, text, flags))


def recognise(path, head):
    """path + the document's first lines -> {type: distinct signal kinds matched}.

    Only types reaching MIN_KINDS appear. An empty dict means UNRECOGNISED, which is a
    result and not a failure -- see the module docstring on why one signal is a coincidence.
    """
    out = {}
    for name, sig in SIGNATURES.items():
        kinds = 0
        if "filename" in sig and _hits(sig["filename"], path):
            kinds += 1
        for key in ("headings", "phrases"):
            spec = sig.get(key)
            if spec and _hits(spec["any"], head) >= spec["need"]:
                kinds += 1
        if kinds >= MIN_KINDS:
            out[name] = kinds
    return out


def distribution(scores):
    """Normalised scores. A document matching two types equally splits its evidence rather
    than picking a winner -- forcing one would invent a fact the signals do not carry."""
    total = sum(scores.values())
    return {k: v / total for k, v in scores.items()} if total else {}


# PRE-REGISTERED BAR, written here before this file was first run and committed in that state.
# ⚠️ THE TWO HALVES USE DIFFERENT DENOMINATORS ON PURPOSE:
#   PRECISION   over documents the recogniser ANSWERS on; abstentions excluded, because an
#               abstention is not a wrong answer.
#   ACCURACY    over ALL labelled documents, abstentions counted WRONG, because the
#               majority-class baseline never abstains. Comparing its accuracy against
#               precision-on-answered would flatter the recogniser by exactly its abstention
#               rate.
BAR_PRECISION = 0.85
BAR_ACCURACY_MARGIN = 0.20
```

- [ ] **Step 2: Write the self-tests for the Review Focus items**

Append to the same file:

```python
def _selftest():
    """The three failure modes a reviewer would reach for, exercised on synthetic inputs so
    they do not consume labelled documents."""
    # RF1: a document matching two types splits evidence rather than picking a winner.
    both = recognise("docs/design/thing.md",
                     "# Goals\n# Non-Goals\nMUST do X\n# Procedure\n1. step\n2. step\nrollback\nverify that")
    assert len(both) >= 2, f"expected a split, got {both}"
    d = distribution(both)
    assert abs(sum(d.values()) - 1.0) < 1e-9

    # RF2: a short stub matches filename and little else -> under the floor, unrecognised.
    assert recognise("README.md", "# Thing\n\nTODO\n") == {}, "a stub scraped a win"

    # RF3: a lying filename is still recognised from its headings.
    adr = recognise("notes.md",
                    "# Status\nAccepted\n# Context\nx\n# Decision\nWe will y\n# Consequences\nz")
    assert "adr" in adr, f"headings did not rescue a lying filename: {adr}"

    print("selftest: RF1 split, RF2 floor, RF3 lying filename — all pass")


if __name__ == "__main__":
    _selftest()
```

- [ ] **Step 3: Run the self-tests**

```bash
cd /tmp/claude-1000/wt-combined
~/.keld/sidecar-venv/bin/python scripts/doctype_study.py
```

Expected: `selftest: RF1 split, RF2 floor, RF3 lying filename — all pass`. Fix the signatures until it does — **this is the only tuning permitted, and it is against synthetic inputs, never against the labels.**

- [ ] **Step 4: Commit the signatures and the bar BEFORE scoring**

```bash
git add scripts/doctype_study.py
git commit -m "study: doctype signature table + pre-registered bar, committed before scoring"
```

---

### Task 4: Score it

**Files:**
- Modify: `scripts/doctype_study.py` (append the scorer)

**Interfaces:**
- Consumes: `recognise`, `distribution`, `BAR_PRECISION`, `BAR_ACCURACY_MARGIN` from Task 3; `scripts/doctype-labels.txt` from Task 2.

- [ ] **Step 1: Append the scorer**

```python
def _score():
    import collections, json, os, sys
    labels = {}
    for line in open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "doctype-labels.txt")):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        i, t = line.split()
        labels[i] = t
    docs = {json.loads(l)["id"]: json.loads(l)
            for l in open("/tmp/claude-1000/doctype/docs.ndjson")}

    answered = correct = 0
    conf = collections.Counter()
    for i, truth in labels.items():
        d = docs.get(i)
        if d is None:
            sys.exit(f"labelled id {i} is not in the frame — regenerate the frame or the labels")
        scores = recognise(d["path"], d["head"])
        if not scores:
            conf[(truth, "(abstained)")] += 1
            continue
        top = max(scores.items(), key=lambda kv: (kv[1], kv[0]))[0]
        answered += 1
        if top == truth:
            correct += 1
        else:
            conf[(truth, top)] += 1

    n = len(labels)
    maj_type, maj_n = collections.Counter(labels.values()).most_common(1)[0]
    precision = correct / answered if answered else 0.0
    accuracy = correct / n
    baseline = maj_n / n

    print(f"labelled documents       {n}")
    print(f"answered                 {answered}  ({100*answered/n:.1f}%)   abstained {n-answered}")
    print(f"PRECISION (answered)     {100*precision:.1f}%   bar >= {100*BAR_PRECISION:.0f}%")
    print(f"ACCURACY  (all, abstain=wrong) {100*accuracy:.1f}%")
    print(f"majority-class baseline  {100*baseline:.1f}%  ({maj_type})")
    print(f"margin over baseline     {100*(accuracy-baseline):+.1f} points   bar >= +{100*BAR_ACCURACY_MARGIN:.0f}")
    ok = precision >= BAR_PRECISION and (accuracy - baseline) >= BAR_ACCURACY_MARGIN
    print(f"\nRESULT: {'PASS' if ok else 'FAIL'}")
    if conf:
        print("\nerrors (truth -> predicted):")
        for (t, p), c in conf.most_common(12):
            print(f"   {t:15} -> {p:15} x{c}")
```

and change the entrypoint to:

```python
if __name__ == "__main__":
    import sys
    _selftest()
    if "--score" in sys.argv:
        _score()
```

- [ ] **Step 2: Run the scorer**

```bash
~/.keld/sidecar-venv/bin/python scripts/doctype_study.py --score
```

⚠️ **Report whatever it says.** A FAIL is a legitimate and useful outcome — it is the fifth measured negative on this question and worth more than a fifth untested idea. **Do not edit the signature table after seeing this output.** If the signatures need changing, that is a new experiment with new labels, not a correction to this one.

- [ ] **Step 3: Commit the scorer and the output**

```bash
git add scripts/doctype_study.py
git commit -m "study: score the doctype signatures against the blind labels"
```

---

### Task 5: Results note

**Files:**
- Create: `docs/notes/2026-10-01-doctype-study-results.md`

- [ ] **Step 1: Write the note**

Record, with the numbers as they came out:

1. Frame size, how many authored prose documents, extension distribution.
2. Label distribution and the majority-class baseline.
3. Precision on answered, accuracy on all, margin over baseline, PASS/FAIL against the committed bar.
4. The abstention rate, described as a result rather than a shortfall — a recogniser answering on 30% at 95% precision is a different and possibly better outcome than one answering on all at 70%.
5. The confusion table, and whether errors cluster (one type absorbing others) or are diffuse. Clustered errors are a fixable signature problem; diffuse errors are a weak signal.
6. ⚠️ The honest limits: the labeller and the signature author were the same party, with only commit ORDER preventing fitting; the documents are all engineering-adjacent prose, so nothing here speaks to business document types; and the spec's claim that externally-authored formats transfer better than invented keywords is **tested by this study for the first time** — say which way it came out.

- [ ] **Step 2: Commit**

```bash
git add docs/notes/2026-10-01-doctype-study-results.md
git commit -m "study: document-type recognition results"
```

- [ ] **Step 3: STOP at the gate**

Present the numbers and the three outcomes from spec §8 — PASS, FAIL, or partial — and **wait for a human decision**. Phase 2 is not authorised by this plan.
