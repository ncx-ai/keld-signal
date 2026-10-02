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

# EIGHTH LABEL: "no professional domain", competing in the SAME ranking (the NULL_DOC idiom of
# sidecar/app/analysis/attribution.py: a domain is named only by beating "nothing"). Worded in
# the same job-framed register as the seven. gliner_7 omits it and cannot say "none".
NULL_LABEL = "a hobby, school or everyday personal conversation, not anyone's professional work"

KW = {
    "marketing":   "campaign brand ad ads advert advertising seo keyword newsletter subject_line open_rate landing_page copy messaging persona audience channel content_calendar utm retargeting positioning headline marketing social_media promotion slogan tagline",
    "financial":   "ledger reconcile invoice budget forecast revenue opex burn runway audit accrual deferred variance balance statement depreciation payable receivable bookkeeping accounting profit expenses cashflow tax taxes investment",
    "sales":       "prospect pipeline lead deal quota demo discovery objection outreach opportunity crm churn upsell renewal cold_email pitch sales salesperson customers",
    "legal":       "contract clause nda msa sow indemnification indemnity liability termination renewal gdpr compliance counsel redline agreement lease tenancy warranty confidential non_disclosure terms_conditions terms_service privacy_policy plaintiff defendant lawsuit litigation attorney solicitor lawyer statute trademark copyright_infringement power_attorney affidavit settlement arbitration court legislation",
    "medical":     "patient diagnosis clinical icd prescription dose contraindication therapy discharge chart physician nurse symptom treatment surgery doctor medication medical clinic hospital",
    "hr":          "candidate interview hiring onboarding offer_letter performance_review headcount payroll benefits employee_handbook resume cover_letter recruiter job vacancy salary recruitment hr",
    "engineering": "code function bug refactor deploy commit merge branch test database migration api endpoint server script python javascript css html sql react bash software programming",
}
# A term written with "_" is a PHRASE: contiguous words, matched after the stopwords below are
# removed from the text. (The lists were written as phrases and `.split()` had shredded them
# into unigrams -- terms, service, power, open, rate, page ... -- a parser defect, fixed here
# without changing any word. "terms_service" matches "terms of service"; "power_attorney"
# matches "power of attorney"; standalone "attorney" stays a term of its own.)
KWSET = {d: set(w.split()) for d, w in KW.items()}
STOP = {"of", "and", "the", "a"}

# ⚠️ Domain words inside code or quoted documents are not domain WORK. `invoice_id` in a SQL
# schema is engineering. Fenced code and inline code are removed before the keyword scan.
# An unterminated fence (e.g. cut by the window) runs to the end of the text.
CODE = re.compile(r"```.*?```|```.*$|`[^`]+`", re.S)


def strip_code(text):
    return CODE.sub(" ", text)


def keyword_arm(text):
    """Highest-scoring domain by distinct word hits, or None on a tie or no hit.

    None is an ABSTENTION: the scorer reads it as the prediction "none". It is excluded from
    precision, and counted as a prediction (right when truth is "none") in accuracy.
    """
    toks = re.sub(r"[^a-z0-9 ]+", " ", strip_code(window(text)).lower()).split()
    low = " " + " ".join(t for t in toks if t not in STOP) + " "
    # plural-tolerant on the last word of a phrase too ("contracts", "landing pages").
    score = {d: sum(1 for w in ws
                    if any(f" {w.replace('_', ' ')}{suf} " in low for suf in ("", "s", "es")))
             for d, ws in KWSET.items()}
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


def gliner_raw(texts, with_null=False):
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
    descs = [JOB_LABELS[n] for n in DOMAINS] + ([NULL_LABEL] if with_null else [])
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


def label_to_domain(label):
    """Description -> domain; the null label -> None (an abstention, scored as "none")."""
    descs = [JOB_LABELS[n] for n in DOMAINS]
    return DOMAINS[descs.index(label)] if label in descs else None


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
CACHE8 = "/tmp/claude-1000/convdomain/gliner8_raw.json"
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
    def cached(path, with_null):
        if os.path.exists(path):
            c = json.load(open(path))
            assert [r["sha"] for r in c] == digest, "cache does not match frame"
            return [r["raw"] for r in c]
        raw = gliner_raw(texts, with_null)
        json.dump([{"sha": d, "raw": r} for d, r in zip(digest, raw)], open(path, "w"))
        return raw

    g7 = [label_to_domain(r["label"]) for r in cached(CACHE, False)]
    g8 = [label_to_domain(r["label"]) for r in cached(CACHE8, True)]
    kw = [keyword_arm(t) for t in texts]
    un = union_arm(kw, g8)            # keyword, else gliner_8
    sh = shuffled_arm(un)             # the control for the UNION arm
    json.dump({"order": "frame line index", "keyword": kw, "gliner_7": g7, "gliner_8": g8,
               "union": un, "shuffled_of_union": sh, "shuffle_seed": SHUFFLE_SEED},
              open(PREDS, "w"))
    print(f"wrote {PREDS} ({len(texts)} conversations)")


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

    # plurals hit; legal vocabulary reaches a legal request.
    assert keyword_arm("USER: review these contracts and the termination clauses") == "legal"
    assert label_to_domain(NULL_LABEL) is None

    # phrases are pinned from both sides: ordinary "terms" is not legal, "terms of service" is.
    assert keyword_arm("USER: in terms of cost, which option is cheaper") != "legal"
    assert keyword_arm("USER: summarise the terms of service") == "legal"
    assert keyword_arm("USER: do I need power of attorney here") == "legal"
    # an unterminated fence runs to the end: trailing code words are not scored.
    assert keyword_arm("USER: fix\n```sql\nSELECT invoice ledger budget forecast revenue") is None

    # the shuffle reproduces and permutes.
    x = ["a", None, "b", "c", "d", None]
    assert shuffled_arm(x) == shuffled_arm(x) and sorted(map(str, shuffled_arm(x))) == sorted(map(str, x))
    print("selftest: RF5 code-strip, RF4 abstain, clear case, union, window, shuffle — all pass")


LABELS = "scripts/convdomain-labels.txt"
ARMS = ("keyword", "gliner_7", "gliner_8", "union", "shuffled_of_union")
CAN_EXPRESS = {
    "keyword": "7 domains or abstain",
    "gliner_7": "7 domains; CANNOT abstain, so never predicts `none`",
    "gliner_8": "7 domains plus a null label, so it CAN predict `none`",
    "union": "keyword, else gliner_8",
    "shuffled_of_union": "the union's own predictions permuted (seed 20261001): the noise floor",
}


def _score():
    import collections
    lab = {}
    for line in open(LABELS):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        i, d, m = line.split()
        lab[i] = (d, m)
    rows = [json.loads(l) for l in open(FRAME)]
    preds = json.load(open(PREDS))
    assert all(len(preds[a]) == len(rows) for a in ARMS)
    ids = [r["id"] for r in rows]          # predictions are joined by POSITION
    assert set(ids) == set(lab) and len(rows) == len(lab)
    strata = {"candidate": [], "random": [], "ALL": list(range(len(rows)))}
    for j, r in enumerate(rows):
        strata[r["stratum"]].append(j)

    print("=" * 78)
    print("RANDOM STRATUM DECIDES THE BAR (spec s7). Candidates were chosen BY A KEYWORD")
    print("NET: a keyword arm scoring well there measures the selector, not the method.")
    print("Strata are never pooled into a headline; ALL is shown for reference only.")
    print("NO arm can express `other` (scored wrong for all). acc(expr) removes those rows.")
    print("Abstention = prediction `none`. Precision is over NAMED domains only; n/a = zero named.")
    print("=" * 78)
    for a in ARMS:
        print(f"  {a:18} {CAN_EXPRESS[a]}")

    print("\nTRUTH DISTRIBUTION / BASELINE (majority class)")
    for st, idx in strata.items():
        c = collections.Counter(lab[ids[j]][0] for j in idx)
        top, n = c.most_common(1)[0]
        print(f"   {st:10} n={len(idx):3} baseline {100*n/len(idx):5.1f}% ({top})  "
              + " ".join(f"{k}={v}" for k, v in c.most_common()))

    results = {}
    for a in ARMS:
        print(f"\n{a}  [{CAN_EXPRESS[a]}]")
        for st, idx in strata.items():
            truth = [lab[ids[j]][0] for j in idx]
            pred = [preds[a][j] for j in idx]
            said = [p if p is not None else "none" for p in pred]
            ans = [(p, t) for p, t in zip(pred, truth) if p is not None]
            prec = sum(p == t for p, t in ans) / len(ans) if ans else None
            acc = sum(p == t for p, t in zip(said, truth)) / len(truth)
            ex = [(p, t) for p, t in zip(said, truth) if t != "other"]
            acce = sum(p == t for p, t in ex) / len(ex) if ex else None
            base = collections.Counter(truth).most_common(1)[0][1] / len(truth)
            abst = sum(p is None for p in pred)
            results[(a, st)] = (prec, acc, base)
            f = lambda v: "  n/a" if v is None else f"{100*v:5.1f}%"
            print(f"   {st:10} n={len(idx):3} named {len(ans):3} abstained {abst:3} "
                  f"({100*abst/len(idx):4.1f}%)  precision {f(prec)}  accuracy {f(acc)}  "
                  f"acc(expr) {f(acce)}  baseline {f(base)}  margin {100*(acc-base):+6.1f}")

    print("\nCONFUSION vs TRUTH, per arm and stratum (rows=truth, cols=predicted; none=abstain/none)")
    labels = DOMAINS + ["none", "other"]
    for a in ARMS:
        for st in ("random", "candidate"):
            idx = strata[st]
            m = collections.Counter((lab[ids[j]][0], preds[a][j] or "none") for j in idx)
            print(f"\n  {a} / {st}")
            print("   " + " " * 12 + "".join(f"{c[:6]:>7}" for c in labels))
            for t in labels:
                if not any(m[(t, c)] for c in labels):
                    continue
                print(f"   {t:12}" + "".join(f"{m[(t, c)]:7d}" for c in labels))
            print("   per-domain recall: " + "  ".join(
                f"{t}={m[(t, t)]}/{sum(m[(t, c)] for c in labels)}" for t in labels
                if sum(m[(t, c)] for c in labels)))

    print("\nMODE (doing/asking) -- scored separately from domain (spec s7).")
    print("   No arm predicts mode, so there is no arm accuracy to report. The labelled")
    print("   distribution is the baseline any future mode classifier must beat.")
    for st, idx in strata.items():
        c = collections.Counter(lab[ids[j]][1] for j in idx)
        print(f"   {st:10} " + " ".join(f"{k}={v}" for k, v in c.most_common())
              + f"  majority baseline {100*c.most_common(1)[0][1]/len(idx):.1f}%")
        cd = collections.Counter((lab[ids[j]][0], lab[ids[j]][1]) for j in idx)
        print("              by domain: " + " ".join(
            f"{d}:{cd[(d,'doing')]}d/{cd[(d,'asking')]}a" for d in labels
            if cd[(d, 'doing')] + cd[(d, 'asking')]))

    print(f"\nBAR on RANDOM: precision >= {100*BAR_PRECISION:.0f}% AND accuracy >= baseline + "
          f"{100*BAR_ACCURACY_MARGIN:.0f} points")
    for a in ARMS:
        prec, acc, base = results[(a, "random")]
        ok_p = prec is not None and prec >= BAR_PRECISION
        ok_a = acc >= base + BAR_ACCURACY_MARGIN
        print(f"   {a:18} precision {'PASS' if ok_p else 'FAIL'}  accuracy {'PASS' if ok_a else 'FAIL'}"
              f"  => {'PASS' if ok_p and ok_a else 'FAIL'}")


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "predict":
        predict()
    elif "--score" in sys.argv:
        _selftest()
        _score()
    else:
        _selftest()
