#!/usr/bin/env python3
"""Score `atv1` activity-type attribution against 100 blind hand labels.

Pre-registration: docs/superpowers/specs/2026-09-24-activity-atv1-preregistration.md (fde3297)
Labels:           scripts/activity-atv1-hand-labels.txt (ea4c6a5, committed before this ran)
Frame:            ~/keld/refseries-context/facets/activity-rerun-sample.ndjson (attempt four's)

ARMS
  D1/D2  deterministic: the `action` level -> atv1 verb, by PRECEDENCE (never dominance --
         Amendment 1 of the prior study measured dominance at a 95.4% majority class because
         reads swamp the rollup). The precedence ORDER is attempt four's, byte-for-byte in
         structure; only the class NAMES are translated into atv1. The one judgement call --
         where `run code` goes, since atv1 has no `analyze` verb -- is exposed as two arms
         rather than buried: D1 sends it to `review`, D2 abstains.
  P      prose, flat: GLiNER2 scores the 17 verbs in one call.
  H      prose, hierarchical: GLiNER2 scores the 7 families, then the verbs within the winner.
  F      prose, flat 68: GLiNER2 scores all 68 activity_type_ids in one call.

The context axis is NOT scored. This corpus is 100% engineering; every gold context is
general/none, so a context number from it would be measuring nothing. That is Study 4's job.
"""
import argparse, collections, json, os, re, sys

SAMPLE = os.path.expanduser("~/keld/refseries-context/facets/activity-rerun-sample.ndjson")
LABELS = os.path.join(os.path.dirname(os.path.abspath(__file__)), "activity-atv1-hand-labels.txt")
CSV = os.path.expanduser("~/Downloads/keld-activity-types-v1.csv")

FLOOR = 5          # attempt four's evidence floor, unchanged
TEXT_CAP = 2000    # ~512 deberta positions; sentence-aligned, never mid-sentence
SENT = re.compile(r"(?<=[.!?])\s+")

# --- arm D: precedence, most-specific act first (attempt four's order, atv1 names) ----------
PRECEDENCE_D1 = [
    ("code.write",   ("create", "publish")),
    ("code.edit",    ("edit", "transform", "convert a document")),
    ("review.general", ("test", "build", "run code")),      # run code -> review
    ("research.general", ("read", "search", "fetch", "query a database")),
]
PRECEDENCE_D2 = [
    ("code.write",   ("create", "publish")),
    ("code.edit",    ("edit", "transform", "convert a document")),
    ("review.general", ("test", "build")),
    ("other.general", ("run code",)),                        # run code -> abstain
    ("research.general", ("read", "search", "fetch", "query a database")),
]


def arm_d(rec, precedence):
    acts = rec.get("actions") or {}
    if not acts and not rec.get("tools"):
        return "converse.general", "structural"
    for label, keys in precedence:
        if sum(acts.get(k, 0) for k in keys) >= FLOOR:
            return label, "ok"
    return None, "thin"


def window_text(rec):
    parts = list(rec.get("prompts") or []) + list(rec.get("prose") or [])
    out, n = [], 0
    for p in parts:
        p = re.sub(r"\s+", " ", str(p)).strip()
        if not p:
            continue
        for s in SENT.split(p):
            if n + len(s) + 1 > TEXT_CAP:
                return " ".join(out)
            out.append(s)
            n += len(s) + 1
    return " ".join(out)


def fact_preamble(rec):
    """Deterministic facts stated as a hint, the `work_function`-with-team-stated idiom.
    Acts only -- these come from tool names and shell argv, never from message text."""
    acts = rec.get("actions") or {}
    if not acts:
        return "No tool activity was recorded in this window. "
    top = sorted(acts.items(), key=lambda kv: -kv[1])[:6]
    return ("Tool activity recorded in this window: "
            + ", ".join(f"{k} {v}" for k, v in top) + ". ")


def load_labels():
    gold = {}
    for line in open(LABELS):
        m = re.match(r"^(\d{3})\s+(\S+)", line)
        if m:
            gold[int(m.group(1))] = m.group(2)
    return gold


def load_vocab():
    import csv as _csv
    rows = list(_csv.DictReader(open(CSV)))
    verbs, fams, ids = {}, collections.defaultdict(list), []
    for r in rows:
        ids.append(r["activity_type_id"])
        verbs[r["verb"]] = r["family"]
        fams[r["family"]].append(r["verb"])
    for f in fams:
        fams[f] = sorted(set(fams[f]))
    return rows, verbs, fams, ids


# --- descriptions: the "one-line gloss" style. Study 2 varies THIS. ------------------------
GLOSS = {
    "text.create": "writing new prose from scratch",
    "text.transform": "rewriting, translating or reformatting existing prose",
    "text.summarize": "condensing existing content into something shorter",
    "transcribe": "turning speech or images into text",
    "classify": "assigning things to categories",
    "extract": "pulling structured facts out of content",
    "research": "gathering information, looking things up, investigating",
    "review": "checking existing work for errors or judging whether it is right",
    "converse": "interactive question answering or brainstorming",
    "plan": "deciding an approach or a sequence of steps",
    "code.write": "authoring new software code",
    "code.edit": "changing software code that already exists, including tests and debugging",
    "image.create": "generating images",
    "video.create": "generating video",
    "audio.create": "generating audio",
    "embed": "turning content into vectors",
    "other": "none of these",
}

# --- Study 2: three description styles. BARE = id only, GLOSS = one line, RICH = cues. -----
BARE = {k: "" for k in GLOSS}
RICH = {
    "text.create": "writing new prose from scratch: drafting a document, a report, an email. NOT editing something that exists",
    "text.transform": "rewriting, translating or reformatting prose that already exists. NOT writing it from scratch",
    "text.summarize": "condensing existing content into something shorter. NOT writing new material",
    "transcribe": "turning speech or images into text",
    "classify": "assigning things to categories or labels",
    "extract": "pulling structured facts or fields out of content",
    "research": "gathering information to UNDERSTAND something: reading code, searching, investigating, mapping how a system works, answering a question about it. NOT judging whether it is correct",
    "review": "checking finished work for ERRORS and judging whether it is right: code review, verdicts, approvals, spec compliance. NOT reading code to learn how it works",
    "converse": "interactive question answering, discussion or brainstorming with a person",
    "plan": "deciding an approach or a sequence of steps before doing the work: writing a spec or an implementation plan",
    "code.write": "authoring NEW software code: creating a new file, module or function that did not exist",
    "code.edit": "CHANGING software code that already exists: debugging, refactoring, fixing tests, applying review feedback, wiring call sites",
    "image.create": "generating images",
    "video.create": "generating video",
    "audio.create": "generating audio",
    "embed": "turning content into vectors",
    "other": "none of these",
}
STYLES = {"bare": BARE, "gloss": GLOSS, "rich": RICH}

FAM_GLOSS = {
    "language": "producing or rewriting prose",
    "understanding": "making sense of existing content",
    "agentic": "investigating, planning or conversing",
    "media": "producing images, video or audio",
    "code": "writing or changing software code",
    "infra": "vectorising content",
    "fallback": "none of these",
}


def score(name, gold, pred, reason):
    answered = [i for i in gold if pred.get(i)]
    correct = sum(1 for i in answered if pred[i] == gold[i])
    cov = len(answered) / len(gold)
    acc = correct / len(answered) if answered else 0.0
    base_cls = collections.Counter(gold[i] for i in answered).most_common(1)
    base = base_cls[0][1] / len(answered) if answered else 0.0
    thin = sum(1 for i in gold if reason.get(i) == "thin")
    return dict(arm=name, n=len(gold), answered=len(answered), coverage=round(cov, 3),
                accuracy=round(acc, 3), baseline=round(base, 3),
                lift=round(acc - base, 3), thin=thin)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--arms", default="D1,D2")
    ap.add_argument("--wording", default="gloss")
    ap.add_argument("--model", default="fastino/gliner2-large-v1")
    ap.add_argument("--json-out")
    a = ap.parse_args()

    gold = load_labels()
    recs = [json.loads(l) for l in open(SAMPLE)][: len(gold)]
    rows, verb_fam, fams, ids = load_vocab()
    results = []

    if "D1" in a.arms or "D2" in a.arms:
        for nm, prec in (("D1", PRECEDENCE_D1), ("D2", PRECEDENCE_D2)):
            if nm not in a.arms:
                continue
            pred, reason = {}, {}
            for i, r in enumerate(recs, 1):
                p, why = arm_d(r, prec)
                pred[i], reason[i] = p, why
            res = score(nm, gold, pred, reason)
            # BAR 2: accuracy restricted to gold code.* windows
            code_idx = [i for i in gold if gold[i] in ("code.write", "code.edit")]
            ans = [i for i in code_idx if pred.get(i)]
            res["code_n"] = len(code_idx)
            res["code_answered"] = len(ans)
            res["code_accuracy"] = round(sum(1 for i in ans if pred[i] == gold[i]) / len(ans), 3) if ans else None
            res["confusion"] = collections.Counter(
                f"{gold[i]}->{pred[i]}" for i in gold if pred.get(i) and pred[i] != gold[i]).most_common(6)
            results.append(res)

    for nm in ("P", "H", "F", "A", "T"):
        if nm not in a.arms:
            continue
        from gliner2 import GLiNER2
        ex = GLiNER2.from_pretrained(a.model)
        print(f"  [model {a.model}]")
        STY = STYLES[a.wording]
        verb_labels = [f"{v}: {STY[v]}".rstrip(": ") for v in STY]
        fam_labels = [f"{f}: {FAM_GLOSS[f]}" for f in FAM_GLOSS]
        id_labels = [f"{r['activity_type_id']}: {STY.get(r['verb'], r['verb'])}"
                     + ("" if r["context"] in ("none", "general") else f", applied to {r['context']} work")
                     for r in rows]
        pred, reason = {}, {}
        for i, r in enumerate(recs, 1):
            t = window_text(r)
            if not t:
                pred[i], reason[i] = None, "thin"
                continue
            if nm == "T":
                pre = ("The person doing this work is on the Engineering team. ")
                out = ex.classify_text(pre + t, {"activity": {"labels": id_labels}},
                                       include_confidence=True)
                pred[i] = out["activity"]["label"].split(":")[0]
            elif nm == "A":
                out = ex.classify_text(fact_preamble(r) + t, {"verb": {"labels": verb_labels}},
                                       include_confidence=True)
                v = out["verb"]["label"].split(":")[0]
                pred[i] = v if v in ("plan", "embed") else (v + ".general" if v not in ("code.write", "code.edit") else v)
            elif nm == "F":
                out = ex.classify_text(t, {"activity": {"labels": id_labels}}, include_confidence=True)
                pred[i] = out["activity"]["label"].split(":")[0]
            elif nm == "P":
                out = ex.classify_text(t, {"verb": {"labels": verb_labels}}, include_confidence=True)
                v = out["verb"]["label"].split(":")[0]
                pred[i] = v if v in ("plan", "embed") else (v + ".general" if v not in ("code.write", "code.edit") else v)
            else:  # H
                out = ex.classify_text(t, {"family": {"labels": fam_labels}}, include_confidence=True)
                fam = out["family"]["label"].split(":")[0]
                cand = [f"{v}: {STY[v]}".rstrip(": ") for v in fams.get(fam, []) if v in STY] or verb_labels
                out2 = ex.classify_text(t, {"verb": {"labels": cand}}, include_confidence=True)
                v = out2["verb"]["label"].split(":")[0]
                pred[i] = v if v in ("plan", "embed") else (v + ".general" if v not in ("code.write", "code.edit") else v)
            reason[i] = "ok"
        res = score(nm + "/" + a.wording + "/" + a.model.split("/")[-1], gold, pred, reason)
        res["predictions"] = {str(k): v for k, v in pred.items()}
        code_idx = [i for i in gold if gold[i] in ("code.write", "code.edit")]
        ans = [i for i in code_idx if pred.get(i)]
        res["code_n"] = len(code_idx)
        res["code_accuracy"] = round(sum(1 for i in ans if pred[i] == gold[i]) / len(ans), 3) if ans else None
        res["confusion"] = collections.Counter(
            f"{gold[i]}->{pred[i]}" for i in gold if pred.get(i) and pred[i] != gold[i]).most_common(6)
        results.append(res)

    for r in results:
        print(f"\n=== arm {r['arm']}")
        print(f"  coverage {r['coverage']}  answered {r['answered']}/{r['n']}  thin {r['thin']}")
        print(f"  accuracy {r['accuracy']}   majority baseline {r['baseline']}   LIFT {r['lift']:+.3f}")
        print(f"  BAR 2 (code.* windows): n={r['code_n']} accuracy={r['code_accuracy']}  (bar >= 0.70)")
        print(f"  top confusions: {r['confusion']}")
    if a.json_out:
        json.dump(results, open(a.json_out, "w"), indent=2)
        print(f"\nwrote {a.json_out}")


if __name__ == "__main__":
    main()
