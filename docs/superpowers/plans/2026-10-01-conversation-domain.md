# Conversation-domain recognition study — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Measure whether a conversation's business domain is recoverable from its text, on real multi-domain user data with independent labels, and whether any method beats a keyword detector on data the keywords did not select.

**Architecture:** A study under `scripts/`, touching no production code. Extract English WildChat conversations in TWO STRATA — keyword-selected candidates and pure random — label 120 blind, then score five arms against a pre-registered bar, reporting each stratum separately.

**Tech Stack:** Python 3.12. `pyarrow` lives ONLY in a throwaway venv at `/tmp/claude-1000/wcvenv`. GLiNER2 1.3.2 and its 1.9 GB weights are already on disk; it runs on GPU.

**Spec:** `docs/superpowers/specs/2026-10-01-conversation-domain-design.md`

## Global Constraints

- **NO PRODUCTION CODE.** Nothing under `sidecar/app/` or `internal/` may be modified. If a task seems to need it, stop and report BLOCKED. Revert is `rm scripts/convdomain_* && rm -rf /tmp/claude-1000/wcvenv`.
- **`pyarrow` goes in `/tmp/claude-1000/wcvenv` ONLY**, never in `~/.keld/sidecar-venv`, which is production tooling. Use `/tmp/claude-1000/wcvenv/bin/python` for anything touching the parquet; use `~/.keld/sidecar-venv/bin/python` for anything touching GLiNER2.
- **⚠️ TWO STRATA, SCORED SEPARATELY, NEVER POOLED.** CANDIDATES were selected by keyword; scoring a keyword arm on them measures the SELECTOR. RANDOM is the only stratum where a false-positive rate is measurable and is the one that decides the bar.
- **⚠️ ORDER IS THE GUARD.** Labels (Task 2) are committed BEFORE any arm (Task 3) exists. The arm author must never read the labels file.
- **⚠️ THE STRATUM IS HIDDEN FROM THE LABELLER.** Knowing a conversation came from the candidate pool biases toward finding a domain in it.
- **English only** — 29,634 of 59,857 conversations.
- **Two axes per conversation:** `domain` ∈ {marketing, financial, sales, legal, medical, hr, engineering, none, other} and `mode` ∈ {doing, asking}.
- **Sample: 120** — 60 CANDIDATES (10 per domain) + 60 RANDOM.

## Review Focus

1. **A conversation whose first turn is domain-free but whose later turns are not** (chat drifts into a legal question at turn 5) must be scored on the whole conversation, not the opener. Tested in Task 1.
2. **A conversation selected into CANDIDATES that a human labels `none`** — the keyword hit a quoted word, not the work. This must be possible and must be scored, not quietly dropped: it is how candidate precision is measured. Tested in Task 2's verification.
3. **A RANDOM conversation that genuinely is domain work** must be labelled as such. The strata are sampling frames, not labels, and an arm must not be scored as wrong for finding one. Tested in Task 2's verification.
4. **A very short conversation** (one user turn of ten words) gives almost nothing to go on; an arm must be allowed to abstain rather than forced to guess. Tested in Task 3.
5. **A conversation containing a domain word inside code or a quoted document** (`"invoice_id"` in a SQL schema) must not count as financial work. Tested in Task 3.

---

### Task 1: The two-strata frame

**Files:**
- Create: `scripts/convdomain_frame.py`
- Create (output, gitignored): `/tmp/claude-1000/convdomain/frame.ndjson`

**Interfaces:**
- Produces: `frame.ndjson`, one record per sampled conversation: `{id, stratum, hit_domains, turns, chars, text}` where `text` is the full conversation rendered as `USER:`/`ASSISTANT:` blocks.

- [ ] **Step 1: Write the extractor**

```python
"""Two sampling strata from WildChat, for the conversation-domain study.

⚠️ THE STRATA ARE THE WHOLE POINT. `candidate` conversations were selected BY KEYWORD, so a
keyword arm scored on them measures the SELECTOR, not the classifier -- it would score near
100% by construction. That is exactly how an earlier keyword baseline looked good at 52%
before collapsing to 17% on data it had not selected. `random` is drawn with no filter at all
and is the only stratum where a false-positive rate is measurable.

⚠️ `stratum` IS RECORDED BUT MUST BE HIDDEN FROM THE LABELLER. Knowing a conversation came
from the candidate pool biases a reader toward finding a domain in it.

⚠️ THE WHOLE CONVERSATION IS SCANNED, not the first turn. Chat drifts: a conversation can open
with small talk and reach a legal question at turn five.
"""
import json, os, random, re, sys

SHARD = "/tmp/claude-1000/wildchat/shard0.parquet"
OUT = "/tmp/claude-1000/convdomain/frame.ndjson"
N_PER_DOMAIN = 10
N_RANDOM = 60
SEED = 20261001

# The selection net. Deliberately generous: precision is established by hand-labelling, and a
# narrow net was exactly what made an earlier pass report 28 candidates where 1,137 exist.
SIGNALS = {
    "legal":     r"\b(nda|non-disclosure|terms (and|&) conditions|privacy policy|contract|clause|indemnif|liabilit|lease agreement|msa|terms of service)\b",
    "marketing": r"\b(landing page|campaign|ad copy|seo|newsletter|email blast|brand voice|press release|social media post|call to action)\b",
    "sales":     r"\b(cold email|outreach|prospect|proposal for|pitch deck|follow[- ]up email|quota|crm|lead gen)\b",
    "financial": r"\b(invoice|balance sheet|p&l|cash flow|journal entry|depreciation|reconcil|budget forecast|financial model|bookkeeping)\b",
    "medical":   r"\b(patient|diagnos|clinical|symptom|prescri|icd-?10|soap note|discharge|treatment plan|medical record)\b",
    "hr":        r"\b(job description|offer letter|performance review|onboarding plan|interview questions for|employee handbook|resume|cover letter)\b",
}
PAT = {k: re.compile(v, re.I) for k, v in SIGNALS.items()}


def render(conv):
    """The conversation as a labeller and an arm both see it."""
    out = []
    for m in conv:
        role = (m.get("role") or "?").upper()
        body = " ".join((m.get("content") or "").split())
        if body:
            out.append(f"{role}: {body}")
    return "\n\n".join(out)


def main():
    import pyarrow.parquet as pq
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    f = pq.ParquetFile(SHARD)

    pools = {k: [] for k in SIGNALS}
    allrows = []
    scanned = skipped_lang = 0
    for rg in range(f.num_row_groups):
        t = f.read_row_group(rg, columns=["conversation_hash", "language", "turn", "conversation"])
        hashes = t.column("conversation_hash").to_pylist()
        langs = t.column("language").to_pylist()
        turns = t.column("turn").to_pylist()
        convs = t.column("conversation").to_pylist()
        for h, lang, nturn, conv in zip(hashes, langs, turns, convs):
            scanned += 1
            if lang != "English":
                skipped_lang += 1
                continue
            text = render(conv)
            if not text.strip():
                continue
            # ⚠️ Scan the WHOLE conversation, both roles. Chat drifts.
            hits = sorted(k for k, p in PAT.items() if p.search(text))
            rec = {"id": h[:12], "stratum": None, "hit_domains": hits,
                   "turns": nturn, "chars": len(text), "text": text}
            allrows.append(rec)
            for k in hits:
                pools[k].append(rec)

    rnd = random.Random(SEED)
    chosen, taken = [], set()
    for dom in sorted(pools):
        pool = sorted(pools[dom], key=lambda r: r["id"])
        rnd.shuffle(pool)
        n = 0
        for r in pool:
            if r["id"] in taken:
                continue
            r2 = dict(r, stratum="candidate")
            chosen.append(r2)
            taken.add(r["id"])
            n += 1
            if n >= N_PER_DOMAIN:
                break
        print(f"  candidate {dom:10} pool {len(pools[dom]):5}  took {n}")

    rest = sorted((r for r in allrows if r["id"] not in taken), key=lambda r: r["id"])
    rnd.shuffle(rest)
    for r in rest[:N_RANDOM]:
        chosen.append(dict(r, stratum="random"))
        taken.add(r["id"])

    rnd.shuffle(chosen)                 # ⚠️ so file order leaks no stratum
    with open(OUT, "w") as out:
        for r in chosen:
            out.write(json.dumps(r, separators=(",", ":")) + "\n")
    print(f"\nscanned {scanned:,}   non-English skipped {skipped_lang:,}")
    print(f"sampled {len(chosen)}  ({sum(1 for r in chosen if r['stratum']=='candidate')} candidate, "
          f"{sum(1 for r in chosen if r['stratum']=='random')} random)")
    print(f"wrote {OUT}")


if __name__ == "__main__":
    main()
```

- [ ] **Step 2: Run it**

```bash
cd /tmp/claude-1000/wt-combined
/tmp/claude-1000/wcvenv/bin/python scripts/convdomain_frame.py
```

Expected: ~29,634 English conversations scanned, each candidate pool in the hundreds (marketing ~350, medical ~281, legal ~179, hr ~179, sales ~145, financial ~62), and 120 sampled. **If any candidate pool yields fewer than 10, report the shortfall — do not widen the pattern to reach 10.**

- [ ] **Step 3: Verify the frame, including Review Focus 1**

```bash
/tmp/claude-1000/wcvenv/bin/python -c "
import json, collections
rows=[json.loads(l) for l in open('/tmp/claude-1000/convdomain/frame.ndjson')]
print('sampled:', len(rows))
print('strata:', dict(collections.Counter(r['stratum'] for r in rows)))
assert len(rows)==len(set(r['id'] for r in rows)), 'duplicate id across strata'
print('duplicate ids: none')
# every candidate carries at least one hit; no random was selected FOR a hit
assert all(r['hit_domains'] for r in rows if r['stratum']=='candidate')
print('every candidate has >=1 keyword hit: yes')
# RF1: the signal is genuinely not confined to the opening turn. Count candidates whose
# selecting keyword appears ONLY after the first user turn — those are conversations a
# first-turn-only scan would have missed entirely.
import re as _re
NET={'legal':r'nda|non-disclosure|terms (and|&) conditions|privacy policy|contract|clause|indemnif|liabilit|lease agreement|msa|terms of service',
     'marketing':r'landing page|campaign|ad copy|seo|newsletter|email blast|brand voice|press release|social media post|call to action',
     'sales':r'cold email|outreach|prospect|proposal for|pitch deck|follow[- ]up email|quota|crm|lead gen',
     'financial':r'invoice|balance sheet|p&l|cash flow|journal entry|depreciation|reconcil|budget forecast|financial model|bookkeeping',
     'medical':r'patient|diagnos|clinical|symptom|prescri|icd-?10|soap note|discharge|treatment plan|medical record',
     'hr':r'job description|offer letter|performance review|onboarding plan|interview questions for|employee handbook|resume|cover letter'}
drift=0
for r in rows:
    if r['stratum']!='candidate': continue
    first=r['text'].split('ASSISTANT:')[0]
    if not any(_re.search(NET[d], first, _re.I) for d in r['hit_domains']):
        drift+=1
print(f'RF1 candidates whose keyword appears ONLY after turn 1: {drift}/60')
print('    (a first-turn-only scan would have missed every one of these)')
print('median chars:', sorted(r['chars'] for r in rows)[len(rows)//2])
"
```

- [ ] **Step 4: Commit**

```bash
git add scripts/convdomain_frame.py
git commit -m "study: two-strata WildChat frame for the conversation-domain study"
```

---

### Task 2: Blind labels, stratum hidden

**Files:**
- Create: `scripts/convdomain_render.py`
- Create: `scripts/convdomain-labels.txt`

**Interfaces:**
- Consumes: `/tmp/claude-1000/convdomain/frame.ndjson`
- Produces: `scripts/convdomain-labels.txt`, lines of `<id> <domain> <mode>`

⚠️ **NO ARM EXISTS YET.** If you find `scripts/convdomain_study.py`, any keyword list beyond the frame's selection net, or any classifier for these domains, **STOP and report BLOCKED** — the study's integrity guard has been broken.

- [ ] **Step 1: Write the renderer**

```python
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
```

- [ ] **Step 2: Render and label**

```bash
/tmp/claude-1000/wcvenv/bin/python scripts/convdomain_render.py > /tmp/claude-1000/convdomain/sample.txt
wc -l /tmp/claude-1000/convdomain/sample.txt
```

Read all 120 and assign **two** labels each.

`domain` — one of: `marketing`, `financial`, `sales`, `legal`, `medical`, `hr`, `engineering`, `none`, `other`
`mode` — one of: `doing`, `asking`

Rules, fixed here before labelling:
- **`domain` is what the work SERVES**, not what words appear. A SQL schema containing `invoice_id` is engineering, not financial.
- **`mode` is `doing` when the person is performing the work** — drafting, analysing, fixing, writing for a real purpose. `asking` is homework, curiosity, explanation, "what is X".
- **`none` for no professional domain at all** — creative writing, roleplay, general trivia, personal chat. Expect a lot of it in this corpus and do not avoid it.
- **`other` for a real domain outside the nine** — e.g. agriculture, theology, geology.
- **Both labels always**, even when `domain` is `none` (a person asking a trivia question is `none asking`).
- ⚠️ **Do not try to infer which were keyword-selected.** If you catch yourself reasoning about selection, stop and judge the text.

- [ ] **Step 3: Write the labels file**

```
# BLIND LABELS for the conversation-domain study.
# Assigned from conversation text ONLY. No stratum, no keyword hits, and no arm were visible —
# no arm existed when these were written, which git proves by commit order.
#
# <id> <domain> <mode>
c9ec5b440fbd none asking
...
```

- [ ] **Step 4: Verify, including Review Focus 2 and 3**

```bash
/tmp/claude-1000/wcvenv/bin/python -c "
import json, collections
D={'marketing','financial','sales','legal','medical','hr','engineering','none','other'}
M={'doing','asking'}
lab={}
for l in open('scripts/convdomain-labels.txt'):
    l=l.strip()
    if not l or l.startswith('#'): continue
    i,d,m=l.split()
    assert d in D, f'bad domain {d}'; assert m in M, f'bad mode {m}'
    assert i not in lab, f'duplicate {i}'
    lab[i]=(d,m)
rows={json.loads(l)['id']:json.loads(l) for l in open('/tmp/claude-1000/convdomain/frame.ndjson')}
assert set(lab)==set(rows), 'labels and frame disagree on which ids exist'
print('labels:', len(lab))
print('domains:', dict(collections.Counter(d for d,_ in lab.values()).most_common()))
print('modes  :', dict(collections.Counter(m for _,m in lab.values())))
# RF2: a candidate labelled none — the keyword hit a word, not the work
c_none=[i for i in lab if rows[i]['stratum']=='candidate' and lab[i][0]=='none']
print(f'RF2 candidates labelled none : {len(c_none)}  <- this is candidate FALSE-POSITIVE rate')
# RF3: a random conversation that is real domain work
r_dom=[i for i in lab if rows[i]['stratum']=='random' and lab[i][0] not in ('none','other')]
print(f'RF3 randoms with a real domain: {len(r_dom)}  <- strata are frames, not labels')
maj=collections.Counter(d for d,_ in lab.values()).most_common(1)[0]
print(f'MAJORITY-CLASS BASELINE: {maj[0]} at {100*maj[1]/len(lab):.1f}%')
for s in ('candidate','random'):
    sub=[lab[i][0] for i in lab if rows[i]['stratum']==s]
    mj=collections.Counter(sub).most_common(1)[0]
    print(f'   {s:10} baseline: {mj[0]} at {100*mj[1]/len(sub):.1f}%')
"
```

- [ ] **Step 5: Hand 15 to the repo owner for spot-check**

⚠️ **The only external check in the study.** Commit order stops the arms being fitted to the labels; nothing stops the labels being systematically wrong, and everything downstream inherits that silently.

```bash
/tmp/claude-1000/wcvenv/bin/python -c "
import json, random
lab=dict((l.split()[0],' '.join(l.split()[1:])) for l in open('scripts/convdomain-labels.txt') if l.strip() and not l.startswith('#'))
rows={json.loads(l)['id']:json.loads(l) for l in open('/tmp/claude-1000/convdomain/frame.ndjson')}
ids=sorted(lab); random.Random(7).shuffle(ids)
for i in ids[:15]:
    print(f'--- {i}  LABELLED: {lab[i]}')
    print('    ' + rows[i]['text'][:400].replace(chr(10),' ')); print()
"
```

Apply corrections, re-run Step 4. **More than 3 corrections in 15 and re-label the whole sample** — a 20% label error rate makes every downstream number a precise measurement of nothing.

- [ ] **Step 6: Commit the labels BEFORE any arm work**

```bash
git add scripts/convdomain_render.py scripts/convdomain-labels.txt
git commit -m "study: 120 blind conversation-domain labels, committed before any arm exists"
```

---

### Task 3: The arms and the bar

**Files:**
- Create: `scripts/convdomain_study.py`

**Interfaces:**
- Produces: `keyword_arm(text) -> str|None`, `gliner_arm(texts) -> list[str|None]`, `union_arm(...)`, `BAR_PRECISION`, `BAR_ACCURACY_MARGIN`

⚠️ **DO NOT OPEN `scripts/convdomain-labels.txt`.** The arms must be written from the task, not the answers. Reading the labels first makes the study measure nothing and cannot be repaired by committing afterwards.

- [ ] **Step 1: Write the arms**

```python
"""Five arms for the conversation-domain study, scored on two strata.

⚠️ THE KEYWORD ARM IS EXPECTED TO LOOK STRONG ON CANDIDATES AND WEAK ON RANDOM, because
candidates were SELECTED by a keyword net. That contrast is the finding, not a defect, and it
is why the two strata are never pooled.

⚠️ GLiNER2 LABEL WORDING IS LOAD-BEARING. Measured on real paths: description-framed labels
("legal, contracts, compliance and agreements") scored 12.7% while job-framed ("a software
developer's work") scored 48.3% on identical inputs -- a 4x swing. The job-framed set is used
here because it is the one that survived.
"""
import re

DOMAINS = ["marketing", "financial", "sales", "legal", "medical", "hr", "engineering"]

JOB_LABELS = {
    "marketing":   "a marketing team's work",
    "financial":   "a finance team's work",
    "sales":       "a sales team's work",
    "legal":       "a legal team's work",
    "medical":     "a doctor's clinical work",
    "hr":          "an HR team's work",
    "engineering": "a software developer's work",
}

KW = {
    "marketing":   "campaign brand ad ads advert advertising seo keyword newsletter subject line open rate landing page copy messaging persona audience channel content calendar utm retargeting positioning headline",
    "financial":   "ledger reconcile invoice budget forecast revenue opex burn runway audit accrual deferred variance balance statement depreciation payable receivable bookkeeping",
    "sales":       "prospect pipeline lead deal quota demo discovery objection outreach opportunity crm churn upsell renewal cold email pitch",
    "legal":       "contract clause nda msa sow indemnification liability termination renewal gdpr compliance counsel redline agreement lease warranty confidential",
    "medical":     "patient diagnosis clinical icd prescription dose contraindication therapy discharge chart physician nurse symptom treatment surgery",
    "hr":          "candidate interview hiring onboarding offer letter performance review headcount payroll benefits employee handbook resume cover letter recruiter",
    "engineering": "code function bug refactor deploy commit merge branch test database migration api endpoint server script python javascript",
}
KWSET = {d: set(w.split()) for d, w in KW.items()}

# ⚠️ Domain words inside code or quoted documents are not domain WORK. `invoice_id` in a SQL
# schema is engineering. Fenced code and inline code are removed before the keyword scan.
CODE = re.compile(r"```.*?```|`[^`]+`", re.S)


def strip_code(text):
    return CODE.sub(" ", text)


def keyword_arm(text):
    """Highest-scoring domain by distinct word hits, or None on a tie or no hit.

    None is an ABSTENTION and is scored as wrong in the accuracy number and excluded from
    precision -- the two denominators differ on purpose.
    """
    low = " " + re.sub(r"[^a-z0-9 ]+", " ", strip_code(text).lower()) + " "
    score = {d: sum(1 for w in ws if f" {w} " in low) for d, ws in KWSET.items()}
    best = max(score.values())
    if best == 0:
        return None
    top = [d for d, n in score.items() if n == best]
    return top[0] if len(top) == 1 else None


def gliner_arm(texts, max_chars=4000):
    """GLiNER2 over job-framed labels. Returns one domain (or None) per text.

    ⚠️ Runs in the SIDECAR venv, not the pyarrow one -- gliner2 and torch live there.
    """
    import os
    from gliner2 import GLiNER2
    model = GLiNER2.from_pretrained(os.path.expanduser("~/.keld/models/gliner2-large-v1"))
    try:
        import torch
        if torch.cuda.is_available():
            model = model.cuda()
    except Exception:
        pass
    names = list(JOB_LABELS)
    descs = [JOB_LABELS[n] for n in names]
    out = []
    for t in texts:
        o = model.classify_text(t[:max_chars], {"domain": descs})
        g = o.get("domain") if isinstance(o, dict) else o
        if isinstance(g, list) and g:
            g = g[0]
        if isinstance(g, dict):
            g = g.get("label") or g.get("text")
        out.append(names[descs.index(g)] if g in descs else None)
    return out


def union_arm(kw, gl):
    """Keyword where it fires, GLiNER2 otherwise. Best arm on both prior spikes."""
    return [k if k else g for k, g in zip(kw, gl)]


# PRE-REGISTERED BAR, committed before any scoring.
# ⚠️ TWO DENOMINATORS ON PURPOSE: precision over ANSWERED conversations (abstentions excluded,
# because an abstention is not a wrong answer); accuracy over ALL, abstentions counted WRONG,
# because the majority-class baseline never abstains and comparing against precision-on-answered
# would flatter an arm by exactly its abstention rate.
BAR_PRECISION = 0.80
BAR_ACCURACY_MARGIN = 0.20
```

- [ ] **Step 2: Write the self-tests for Review Focus 4 and 5**

Append:

```python
def _selftest():
    # RF5: a domain word inside code is not domain work.
    sql = "USER: fix this\n```sql\nSELECT invoice_id, balance FROM ledger;\n```"
    assert keyword_arm(sql) != "financial", f"code leaked into the keyword arm: {keyword_arm(sql)}"

    # RF4: a very short, contentless conversation must abstain, not guess.
    assert keyword_arm("USER: hi\n\nASSISTANT: hello") is None

    # A clear case still fires.
    assert keyword_arm("USER: draft an NDA with an indemnification clause for a vendor") == "legal"

    # union prefers the keyword answer and falls back.
    assert union_arm(["legal", None], ["medical", "sales"]) == ["legal", "sales"]
    print("selftest: RF5 code-strip, RF4 abstain, clear case, union — all pass")


if __name__ == "__main__":
    _selftest()
```

- [ ] **Step 3: Run the self-tests**

```bash
cd /tmp/claude-1000/wt-combined
~/.keld/sidecar-venv/bin/python scripts/convdomain_study.py
```

Expected: `selftest: ... all pass`. **Tuning against these synthetic cases is the ONLY tuning permitted — never against the labels.**

- [ ] **Step 4: Commit the arms and the bar BEFORE scoring**

```bash
git add scripts/convdomain_study.py
git commit -m "study: conversation-domain arms + pre-registered bar, committed before scoring"
```

---

### Task 4: Score, by stratum

**Files:**
- Modify: `scripts/convdomain_study.py` (append the scorer)

**Interfaces:**
- Consumes: `keyword_arm`, `gliner_arm`, `union_arm`, `BAR_PRECISION`, `BAR_ACCURACY_MARGIN` from Task 3; `scripts/convdomain-labels.txt` from Task 2.

- [ ] **Step 1: Append the scorer**

```python
def _score():
    import collections, json, random, sys
    lab = {}
    for line in open("scripts/convdomain-labels.txt"):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        i, d, m = line.split()
        lab[i] = (d, m)
    rows = {json.loads(l)["id"]: json.loads(l)
            for l in open("/tmp/claude-1000/convdomain/frame.ndjson")}
    ids = sorted(lab)
    texts = [rows[i]["text"] for i in ids]

    kw = [keyword_arm(t) for t in texts]
    gl = gliner_arm(texts)
    un = union_arm(kw, gl)
    rnd = random.Random(20261001)
    shuf = [lab[i][0] for i in ids]
    rnd.shuffle(shuf)

    def report(name, preds):
        print(f"\n{name}")
        for stratum in ("candidate", "random", "ALL"):
            idx = [j for j, i in enumerate(ids)
                   if stratum == "ALL" or rows[i]["stratum"] == stratum]
            if not idx:
                continue
            truth = [lab[ids[j]][0] for j in idx]
            pred = [preds[j] for j in idx]
            # RULING (controller, 2026-10-01): an arm declining to name a domain
            # IS predicting "no professional domain". The arms can emit 7 values;
            # the truth has 9. Scoring abstention wrong caps every arm at 40%
            # accuracy against a 41.7% baseline -- every arm fails by construction.
            said = [p if p is not None else "none" for p in pred]
            # PRECISION still excludes abstentions: it is over domains actually NAMED.
            answered = [(p, t) for p, t in zip(pred, truth) if p is not None]
            prec = (sum(1 for p, t in answered if p == t) / len(answered)
                    if answered else None)
            acc = sum(1 for p, t in zip(said, truth) if p == t) / len(truth)
            # `other` stays inexpressible by any arm and is scored WRONG for all of
            # them equally. The second accuracy says what the vocabulary cost.
            expr = [(p, t) for p, t in zip(said, truth) if t != "other"]
            acc_e = (sum(1 for p, t in expr if p == t) / len(expr)) if expr else None
            base = collections.Counter(truth).most_common(1)[0][1] / len(truth)
            print(f"   {stratum:10} n={len(idx):3}  answered {len(answered):3}  "
                  f"precision {('  n/a' if prec is None else f'{100*prec:5.1f}%')}  "
                  f"accuracy {100*acc:5.1f}%  "
                  f"acc(expressible) {('  n/a' if acc_e is None else f'{100*acc_e:5.1f}%')}  "
                  f"baseline {100*base:5.1f}%  margin {100*(acc-base):+6.1f}")
        return None

    print("=" * 78)
    print("⚠️ CANDIDATES WERE KEYWORD-SELECTED. An arm strong there and weak on RANDOM has")
    print("   learned the selector, not the domains. RANDOM decides the bar.")
    print("=" * 78)
    for name, preds in (("shuffled control", shuf), ("keyword", kw),
                        ("GLiNER2 (job-framed)", gl), ("union: keyword else GLiNER2", un)):
        report(name, preds)

    print(f"\nBAR: precision >= {100*BAR_PRECISION:.0f}% AND accuracy >= baseline + "
          f"{100*BAR_ACCURACY_MARGIN:.0f} points, ON THE RANDOM STRATUM.")
    print("   Abstention counts as a prediction of `none` (see RULING in report()).")
    print("   `other` is inexpressible by every arm and is scored wrong for all of them;")
    print("   acc(expressible) is the same number with those conversations removed.")
    print("   precision `n/a` means the arm named a domain zero times -- NOT 100%.")
    json.dump({"ids": ids, "keyword": kw, "gliner": gl, "union": un},
              open("/tmp/claude-1000/convdomain/preds.json", "w"))
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
~/.keld/sidecar-venv/bin/python scripts/convdomain_study.py --score
```

⚠️ **Report whatever it says, and do NOT edit the arms after seeing it.** A FAIL is the sixth measured negative on this question and is worth more than a sixth untested idea. Changing an arm now is a new experiment needing new labels, not a correction to this one.

- [ ] **Step 3: Commit**

```bash
git add scripts/convdomain_study.py
git commit -m "study: score the conversation-domain arms, by stratum"
```

---

### Task 5: Results note

**Files:**
- Create: `docs/notes/2026-10-01-conversation-domain-results.md`

- [ ] **Step 1: Write the note**

Record, with the numbers as they came out:

1. Frame: conversations scanned, candidate pool per domain, the 120 sampled.
2. Label distribution for `domain` and `mode`, and the majority-class baseline **per stratum**.
3. Each arm's precision, accuracy and margin, **candidate and random separately**.
4. **The candidate-vs-random gap for the keyword arm**, which is the study's structural finding whichever way the bar goes: it quantifies how much an apparently-good keyword result is the selector.
5. How many CANDIDATES a human labelled `none` (the selector's false-positive rate) and how many RANDOMS carried a real domain (what the selector misses).
6. `mode` accuracy, reported separately — telling doing from asking is a second claim.
7. ⚠️ The limits: WildChat is ChatGPT chat, not agentic transcripts with tool calls and heavy assistant text, so a pass is evidence the method works on conversations and NOT that it works on Keld's data; English only; and a 120-conversation sample gives roughly 13 per domain, so per-domain numbers are indicative, not measured.

- [ ] **Step 2: Commit**

```bash
git add docs/notes/2026-10-01-conversation-domain-results.md
git commit -m "study: conversation-domain results"
```

- [ ] **Step 3: STOP at the gate**

Present the numbers and wait for a human decision. Nothing in production is authorised by this plan.
