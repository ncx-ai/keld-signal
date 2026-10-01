"""Arms and the pre-registered bar for the conversation-domain study.

Arms: keyword, GLiNER2 (job-framed labels), keyword-union-GLiNER2, shuffled control.
The majority-class floor needs the truth, so it lives in the scorer, not here.

⚠️ THE KEYWORD ARM IS EXPECTED TO LOOK STRONG ON CANDIDATES AND WEAK ON RANDOM, because
candidates were SELECTED by a keyword net. That contrast is the finding, not a defect, and it
is why the two strata are never pooled. The word lists below were written once, generously,
from the task and were never iterated against any score.

⚠️ GLiNER2 LABEL WORDING IS LOAD-BEARING. Measured on real paths: description-framed labels
("legal, contracts, compliance and agreements") scored 12.7% while job-framed ("a software
developer's work") scored 48.3% on identical inputs. The job-framed set is used here.

⚠️ ABSTENTION. An arm that declines to name a domain returns None. The SCORER maps None to the
prediction "none" (no professional domain) -- abstention IS a prediction. `other` is not
expressible by any arm and is scored wrong for every arm equally. PRECISION excludes
abstentions (over conversations where the arm named a domain; `n/a` when there are none).
The scorer reports accuracy over all 120 AND over truth != "other", always both.

⚠️ THE ARMS READ ONLY `text` FROM THE FRAME -- not `stratum`, not `hit_domains` (the output of the
keyword selector that chose the candidates: reading it scores ~100% on candidates by
construction), not `id`. Predictions are emitted in frame order and joined by position.

Run (sidecar venv, NOT the host python):
    ~/.keld/sidecar-venv/bin/python scripts/convdomain_study.py            # self-test
    ~/.keld/sidecar-venv/bin/python scripts/convdomain_study.py predict    # write predictions
`predict` infers GLiNER2 once and caches raw output; re-runs reuse the cache.
"""
import json
import os
import random
import re
import sys

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

    None is an ABSTENTION: the scorer reads it as the prediction "none". It is excluded from
    precision, and counted as a prediction (right when truth is "none") in accuracy.
    """
    low = " " + re.sub(r"[^a-z0-9 ]+", " ", strip_code(window(text)).lower()) + " "
    score = {d: sum(1 for w in ws if f" {w} " in low) for d, ws in KWSET.items()}
    best = max(score.values())
    if best == 0:
        return None
    top = [d for d, n in score.items() if n == best]
    return top[0] if len(top) == 1 else None


# ⚠️ EVERY ARM SCORES EXACTLY text[:LABELLER_WINDOW] -- the string the labeller read.
# This mirrors convdomain_render.py's default LIMIT (4000); keep the two in step. The truth
# labels describe the HEAD of a conversation (79 of 120 exceed 4000 chars; the labeller saw a
# median 36% of those), so an arm reading more would be scored wrong for finding signal the
# labeller never saw, and the penalty would land hardest on candidates. The cut is deliberately
# a raw character cut with no turn-boundary adjustment: here the fragment IS what the truth
# describes, which outranks the usual never-cut-mid-sentence rule. The study therefore measures
# HEAD-OF-CONVERSATION recognition, a narrower claim than whole-conversation recognition.
LABELLER_WINDOW = 4000


def window(text):
    return text[:LABELLER_WINDOW]


def gliner_raw(texts):
    """Raw GLiNER2 output per text: {label, confidence}.

    ⚠️ Runs in the SIDECAR venv -- gliner2 and torch live there.
    """
    from gliner2 import GLiNER2
    model = GLiNER2.from_pretrained(os.path.expanduser("~/.keld/models/gliner2-large-v1"))
    try:
        import torch
        if torch.cuda.is_available():
            model = model.cuda()
    except Exception:
        pass
    descs = [JOB_LABELS[n] for n in DOMAINS]
    out = []
    for t in texts:
        o = model.classify_text(window(t), {"domain": descs}, include_confidence=True)
        g = o.get("domain") if isinstance(o, dict) else o
        label = conf = None
        if isinstance(g, dict):
            label, conf = g.get("label"), g.get("confidence")
        elif isinstance(g, str):
            label = g
        out.append({"label": label, "confidence": conf})
    return out


def gliner_arm(texts):
    """One domain (or None if the output is not one of the 7 labels) per text."""
    descs = [JOB_LABELS[n] for n in DOMAINS]
    return [DOMAINS[descs.index(r["label"])] if r["label"] in descs else None
            for r in gliner_raw(texts)]


def union_arm(kw, gl):
    """Keyword where it fires, GLiNER2 otherwise. Best arm on both prior spikes."""
    return [k if k else g for k, g in zip(kw, gl)]


SHUFFLE_SEED = 20261001


def shuffled_arm(preds, seed=SHUFFLE_SEED):
    """Noise floor: another arm's predictions permuted across conversations.

    Keeps that arm's marginal distribution and abstention rate, destroys the alignment with
    the text. Seeded from the constant above so a re-run reproduces.
    """
    p = list(preds)
    random.Random(seed).shuffle(p)
    return p


# PRE-REGISTERED BAR, committed before any scoring.
# ⚠️ Precision is over ANSWERED conversations (abstentions excluded). Accuracy is over all
# conversations with abstention scored as the prediction "none"; the bar is majority-class
# accuracy + BAR_ACCURACY_MARGIN. Both are read on the RANDOM stratum, which decides it.
BAR_PRECISION = 0.80
BAR_ACCURACY_MARGIN = 0.20

FRAME = "/tmp/claude-1000/convdomain/frame.ndjson"
CACHE = "/tmp/claude-1000/convdomain/gliner_raw.json"
PREDS = "/tmp/claude-1000/convdomain/arm_predictions.json"


def load_texts(path=FRAME):
    """The `text` field ONLY, in frame order. ⚠️ `hit_domains` is the selector's output and
    `stratum` is the scorer's business; neither is read here, and neither is `id`. Rows are
    therefore identified by POSITION (line index in the frame)."""
    return [json.loads(line)["text"] for line in open(path)]


def predict():
    import hashlib
    texts = load_texts()
    digest = [hashlib.sha1(t.encode()).hexdigest()[:12] for t in texts]
    if os.path.exists(CACHE):
        cached = json.load(open(CACHE))
        assert [r["sha"] for r in cached] == digest, "cache does not match frame"
        raw = [r["raw"] for r in cached]
    else:
        raw = gliner_raw(texts)
        json.dump([{"sha": d, "raw": r} for d, r in zip(digest, raw)], open(CACHE, "w"))
    descs = [JOB_LABELS[n] for n in DOMAINS]
    gl = [DOMAINS[descs.index(r["label"])] if r["label"] in descs else None for r in raw]
    kw = [keyword_arm(t) for t in texts]
    un = union_arm(kw, gl)
    sh = shuffled_arm(un)
    json.dump({"order": "frame line index", "keyword": kw, "gliner": gl, "union": un,
               "shuffled": sh, "shuffle_seed": SHUFFLE_SEED}, open(PREDS, "w"))
    print(f"wrote {PREDS} ({len(texts)} conversations); gliner cache {CACHE}")


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

    # the window is exactly the labeller's: text past it must not influence the keyword arm.
    assert keyword_arm("USER: hi " + "x " * LABELLER_WINDOW + " indemnification clause nda") is None

    # the shuffle reproduces and permutes.
    x = ["a", None, "b", "c", "d", None]
    assert shuffled_arm(x) == shuffled_arm(x) and sorted(map(str, shuffled_arm(x))) == sorted(map(str, x))
    print("selftest: RF5 code-strip, RF4 abstain, clear case, union, window, shuffle — all pass")


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "predict":
        predict()
    else:
        _selftest()
