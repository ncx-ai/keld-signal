"""Can GLiNER2 tell what BUSINESS DOMAIN a user's prompt serves, from the prompt alone?

The question behind the deferred `context` axis: given "draft a campaign brief", can a model
say "this is marketing related"? Tier C was deferred because no route could VALIDATE it on
real sessions. This asks something narrower and answerable: can the model do it AT ALL, on
prompts whose domain is known by construction.

THREE ARMS, and the second is the one that decides anything:

  gliner2   GLiNER2-large (1.9 GB, already on disk) scored against readable label
            DESCRIPTIONS rather than bare ids -- the bi-encoder keys on token and semantic
            overlap, so label wording is load-bearing (AGENTS.md).
  keyword   A deterministic bag of domain words. ⚠️ THIS IS THE ARM THAT MATTERS. If the
            model cannot beat a word list, it is a word list with a 1.9 GB dependency and an
            ml_backend:"auto" requirement that the shipped fleet does not have.
  shuffled  Labels permuted. Confirms we are measuring signal and not structure.

⚠️ THE obvious/oblique SPLIT IS THE RESULT, NOT THE BREAKDOWN. Both arms should do well on
prompts containing their domain's giveaway words; that proves nothing about either. The
question is what survives when the vocabulary is removed and only the WORK is described.

PRE-REGISTERED BAR, written here before this file was first run and committed in that state:

  GLiNER2 earns a place in this pipeline only if, on OBLIQUE prompts, it beats the keyword
  baseline by >= 15 accuracy points. Beating it on obvious prompts, or by less than that on
  oblique ones, means the model is not buying what a word list cannot.

⚠️ And the ceiling caveat stands whatever the number: these prompts were written by the same
author who is scoring them, so every arm is measured against text carrying that author's
tells. A good score here is an UPPER BOUND for real prompts, never a prediction.
"""
import collections
import json
import os
import random
import re
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
PROMPTS = os.path.join(HERE, "domain-prompts.jsonl")

# Readable descriptions, not bare ids. AGENTS.md: the bi-encoder scores against the label's
# WORDING, so `code_generation` is given as "software engineering".
LABELS = {
    "marketing": "marketing, advertising, brand and campaign work",
    "financial": "finance, accounting, budgeting and financial reporting",
    "sales": "sales, prospecting, deals and customer acquisition",
    "legal": "legal, contracts, compliance and agreements",
    "medical": "medicine, clinical care and patient health",
    "scientific": "scientific research, experiments and academic study",
    "support": "customer support, help desk and service tickets",
    "operations": "business operations, process and supply chain",
    "engineering": "software engineering and programming",
}

# A HONEST word list, not a strawman: the terms a person would actually reach for if asked to
# build this without a model. Deliberately generous to the baseline.
KEYWORDS = {
    "marketing": """campaign brand ad ads advert advertising seo keyword newsletter
        subject line open rate landing page copy messaging persona audience channel social
        content calendar utm retargeting positioning headline announcement launch""",
    "financial": """ledger reconcile invoice budget forecast revenue opex capex burn runway
        audit auditor accrual deferred variance cost centre balance statement gaap
        depreciation payable receivable close books earned spend spending""",
    "sales": """prospect pipeline lead deal quota demo discovery objection outreach icp
        opportunity champion crm close won churn upsell renewal cold email sequence
        customer acquisition stage""",
    "legal": """contract clause nda msa sow indemnification liability termination renewal
        gdpr compliance counsel redline governing law jurisdiction agreement lease
        assignment warranty confidential signatory notice period""",
    "medical": """patient diagnosis clinical icd prescription dose contraindication therapy
        discharge chart ehr physician nurse symptom treatment infusion surgery
        postoperative insurer authorisation ward""",
    "scientific": """hypothesis experiment sample size effect size confidence interval
        p-value significance reviewer peer review protocol pre-registration control arm
        attrition validity regression model study participants abstract methods""",
    "support": """ticket queue agent escalation sla first response macro help centre
        status page outage customer issue triage resolution backlog complaint
        article reply session""",
    "operations": """process sop runbook vendor supplier procurement inventory warehouse
        logistics lead time headcount approval threshold onboarding workflow
        postmortem incident capacity reorder step owner""",
    "engineering": """code function bug refactor deploy commit merge branch test ci
        database migration index query api endpoint latency goroutine channel cache
        race condition handler webhook retry backoff profile server build""",
}
KW = {d: set(w.split()) for d, w in KEYWORDS.items()}
BAR_POINTS = 15


def load():
    out = []
    with open(PROMPTS) as fh:
        for line in fh:
            r = json.loads(line)
            if "_comment" in r:
                continue
            out.append(r)
    return out


def keyword_predict(text):
    """Score each domain by how many of its words appear. Ties and zero-hits -> None, which
    counts as wrong rather than being quietly excluded."""
    low = " " + re.sub(r"[^a-z0-9 ]+", " ", text.lower()) + " "
    score = {d: sum(1 for w in words if f" {w} " in low) for d, words in KW.items()}
    best = max(score.values())
    if best == 0:
        return None
    top = [d for d, n in score.items() if n == best]
    return top[0] if len(top) == 1 else None


def report(name, rows, pred):
    """Accuracy overall and per tier, plus the per-domain breakdown."""
    def acc(subset):
        if not subset:
            return 0.0
        return sum(1 for r in subset if pred.get(r["id"]) == r["domain"]) / len(subset)

    ob = [r for r in rows if r["tier"] == "obvious"]
    ol = [r for r in rows if r["tier"] == "oblique"]
    print(f"\n{name}")
    print(f"   overall {100*acc(rows):5.1f}%   obvious {100*acc(ob):5.1f}%   oblique {100*acc(ol):5.1f}%")
    return acc(rows), acc(ob), acc(ol)


def main():
    rows = load()
    print(f"{len(rows)} prompts, {len(set(r['domain'] for r in rows))} domains")

    # --- arm 2: the baseline that decides whether the model is worth anything ----------
    kw_pred = {r["id"]: keyword_predict(r["text"]) for r in rows}
    kw = report("KEYWORD BASELINE (deterministic, no model)", rows, kw_pred)

    # --- arm 3: shuffled control -------------------------------------------------------
    rnd = random.Random(20260930)
    doms = [r["domain"] for r in rows]
    rnd.shuffle(doms)
    sh_pred = {r["id"]: d for r, d in zip(rows, doms)}
    sh = report("SHUFFLED CONTROL (labels permuted)", rows, sh_pred)

    # --- arm 1: GLiNER2 ----------------------------------------------------------------
    from gliner2 import GLiNER2
    path = os.path.expanduser("~/.keld/models/gliner2-large-v1")
    t0 = time.time()
    model = GLiNER2.from_pretrained(path)
    try:
        import torch
        if torch.cuda.is_available():
            model = model.cuda()
            print(f"\nGLiNER2 on GPU, loaded in {time.time()-t0:.1f}s")
    except Exception as e:
        print(f"\nGLiNER2 on CPU ({e}), loaded in {time.time()-t0:.1f}s")

    names = list(LABELS)
    descs = [LABELS[n] for n in names]
    g_pred, t1 = {}, time.time()
    for r in rows:
        try:
            out = model.classify_text(r["text"], {"domain": descs})
        except Exception as e:
            print("classify failed:", type(e).__name__, e)
            break
        got = out.get("domain") if isinstance(out, dict) else out
        if isinstance(got, list) and got:
            got = got[0]
        if isinstance(got, dict):
            got = got.get("label") or got.get("text")
        # map the DESCRIPTION the model returned back to its domain id
        g_pred[r["id"]] = names[descs.index(got)] if got in descs else got
    print(f"   {len(g_pred)} classified in {time.time()-t1:.1f}s")
    gl = report("GLINER2-large", rows, g_pred)

    # --- the verdict, against the bar committed before this ran ------------------------
    delta = 100 * (gl[2] - kw[2])
    print("\n" + "=" * 72)
    print("PRE-REGISTERED BAR: on OBLIQUE prompts, GLiNER2 must beat the keyword")
    print(f"baseline by >= {BAR_POINTS} accuracy points to earn a place in this pipeline.")
    print(f"   oblique: gliner2 {100*gl[2]:.1f}%  keyword {100*kw[2]:.1f}%  delta {delta:+.1f} points")
    print(f"   RESULT: {'PASS' if delta >= BAR_POINTS else 'FAIL'}")
    print(f"   (shuffled control {100*sh[0]:.1f}% — floor; anything near it is noise)")
    print("=" * 72)

    json.dump({"gliner2": g_pred, "keyword": kw_pred},
              open("/tmp/claude-1000/ctx/domain_preds.json", "w"))


if __name__ == "__main__":
    os.makedirs("/tmp/claude-1000/ctx", exist_ok=True)
    main()
