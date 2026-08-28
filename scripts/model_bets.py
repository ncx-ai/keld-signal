#!/usr/bin/env python3
"""The two bets that would keep a generative model in the classification path.

    scripts/model_bets.py                      # GPU smoke test
    scripts/model_bets.py --device cpu         # the number that counts

Handoff decision 1: the model's measured contribution to attribution is zero, so it stays only
if one of two things it can do and GLiNER2 cannot turns out to be real.

  NEW       the text names something the label set lacks. Measured by ABLATION: delete the true
            label, withhold the fact, and the only correct answer is a name quoted from the
            window. Truth comes from the cwd repo — a fact the pipeline already holds, so it is
            independent of anything the model or the text says.

  OVERRIDE  the work belongs to a function other than the team's. No transcript on this machine
            is genuinely non-engineering work (every finance-flavoured window is engineering
            work ABOUT money), so the positive half comes from INVERTING the hint: told the
            person is on Finance over unmistakably-engineering content, a model that answers
            Engineering has contradicted the recorded team from content, which is the whole
            capability. Two framings — "which function?" (measures hint-following) and the
            production shape "does it serve H, or another function?" (abstention is silence,
            the only abstention shape that has ever worked here).

Design, corpus limits and the PRE-REGISTERED decision rule:
docs/superpowers/specs/2026-08-21-model-bets-test-design.md
"""
import argparse, collections, json, os, re, socket, subprocess, sys, time, urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from classifier_schema import _is_catch_all

DEFAULT_MODEL = os.path.expanduser("~/.keld/models/gguf/granite-4.1-3b-Q4_K_M.gguf")
SCHEMA = {"type": "object", "additionalProperties": False,
          "properties": {"answer": {"type": "string", "maxLength": 60}},
          "required": ["answer"]}

# The record's project line answers the question for the model: a window naming no repository
# returned `keld-atlas` with it and fabricated `keld-cli` without it. Any control strips it.
BLIND = re.compile(r"^projects:.*\n?", re.M)

# Finance vocabulary density, computed rather than asserted: four windows carry 63-100 finance
# terms and the next highest carries 9, so the threshold is not a judgement call. These are the
# windows where a spurious Finance override is most likely and most expensive.
FIN = re.compile(r"\b(cost|costs|pricing|price|budget|spend|invoice|opex|capex|finance|"
                 r"financial|billing|usd)\b|\$", re.I)
FIN_SATURATED = 30

HINTS = ["Engineering", "Finance", "Marketing", "Legal", "Customer Support", None]
FALSE_DETECT_HINTS = ["Finance", "Marketing"]
PROJECT_OF = {"keld-atlas": "Atlas", "keld-signal": "Signal"}
SILENT = ["customer_account", "marketing_campaign", "sprint_iteration", "ticket_id"]


def norm(s):
    return re.sub(r"[^a-z0-9]+", " ", (s or "").lower()).strip()


def real(labels):
    return [l for l in (labels or []) if not _is_catch_all(l)]


def serve(model, device, threads, ctx, fn):
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    cmd = ["llama-server", "-m", model, "--ctx-size", str(ctx), "--parallel", "1", "--no-warmup",
           "--jinja", "--flash-attn", "off", "--batch-size", "512", "--ubatch-size", "512",
           "--port", str(port)]
    if device == "cpu":
        cmd += ["--device", "none", "--threads", str(threads), "--cache-ram", "512",
                "--no-repack"]
    proc = subprocess.Popen(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    url = f"http://127.0.0.1:{port}"
    try:
        for _ in range(900):
            try:
                urllib.request.urlopen(url + "/health", timeout=2)
                break
            except Exception:
                time.sleep(1)
        else:
            sys.exit("llama-server never became healthy")
        return fn(url)
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=20)
        except subprocess.TimeoutExpired:
            proc.kill()


def ask(url, prompt):
    body = {"messages": [{"role": "user", "content": prompt}], "temperature": 0,
            "max_tokens": 96,
            "response_format": {"type": "json_schema",
                                "json_schema": {"name": "a", "strict": True, "schema": SCHEMA}}}
    req = urllib.request.Request(url + "/v1/chat/completions",
                                 data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=3600) as r:
        return json.loads(json.load(r)["choices"][0]["message"]["content"])["answer"].strip()


# ---------------------------------------------------------------------------- tails
# Every varying part goes in the TAIL. The window is the cached prefix, so per-pass content must
# come last or the prefix cache misses and every ask pays the full prefill.

def extractive_tail(question, labels):
    opts = f"  Options: {', '.join(labels)}\n" if labels else ""
    return (f"QUESTION: {question}\n{opts}"
            "Answer with an option exactly if it fits, or a name the stretch uses, or an empty "
            "string. Do not offer an option because it is on the list.\n")


def infer_tail(question, description, labels, hint):
    h = f"The person doing this work is on the {hint} team — a hint, not the answer.\n" if hint \
        else ""
    return (f"QUESTION: {question}\n  {description}\n  Options: {', '.join(labels)}\n{h}"
            "This is a judgement: nothing will say it outright. Answer with one option, or an "
            "empty string if there is nothing to judge from.\n")


def detect_tail(labels, hint):
    return (f"QUESTION: Our records say the person doing this work is on the {hint} team. Does "
            f"this stretch of work serve {hint}, or does it serve a different business "
            f"function?\n  The functions we track: {', '.join(labels)}.\n"
            f"Answer with the function it serves if it is NOT {hint}, or an empty string if "
            f"{hint} is right.\n")


def bucket(answer, truths, offered, window):
    """Where an answer came from. `other_in_text` is the handoff's finding that grounding proves
    presence, not relevance — a real string from the window that answers a different question.

    An offered label is checked BEFORE a loose truth match, and the loose match is whole-token.
    Substring-first scored `keld-signal` as a discovery of the truth `keld`, which is a
    nearest-match to an option — the same ordinary-English trap AGENTS.md records for `general`
    matching `Generali`, this time in the scorer rather than in the model."""
    if not answer:
        return "empty"
    n = norm(answer)
    if any(norm(t) == n for t in truths):
        return "truth"
    if any(norm(l) == n for l in offered):
        return "wrong_label"
    if any(n in norm(t).split() or norm(t) in n.split() for t in truths):
        return "truth"
    return "other_in_text" if n in norm(window) else "not_in_text"


def pct(a, b):
    return f"{100*a/b:5.1f}%" if b else "    - "


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--device", choices=["gpu", "cpu"], default="gpu")
    ap.add_argument("--threads", type=int, default=18)
    ap.add_argument("--ctx", type=int, default=12288)
    ap.add_argument("--model", default=DEFAULT_MODEL)
    ap.add_argument("--pool", default="/tmp/pool5.json")
    ap.add_argument("--cases", default="/tmp/real_cases.json")
    ap.add_argument("--arms", default="new,silence,classify,detect")
    ap.add_argument("--limit", type=int, default=0, help="first N cases only (smoke)")
    ap.add_argument("--out", default="/tmp/model_bets.json")
    args = ap.parse_args()

    arms = set(a.strip() for a in args.arms.split(","))
    pool = {f["key"]: f for f in json.load(open(args.pool))}
    cases = json.load(open(args.cases))
    if args.limit:
        cases = cases[:args.limit]
    if args.device == "gpu":
        print("[GPU smoke test — confirm anything real with --device cpu]")
    wf = pool["work_function"]
    wf_labels = real(wf["labels"])
    rows = []

    def run(url):
        for tag, wpath, rpath, team, repo, _lang in cases:
            window = open(wpath).read()
            record = BLIND.sub("", open(rpath).read())
            fin = len(FIN.findall(window))
            prefix = (f"Someone is asked a question about a stretch of work.\n\nSESSION RECORD:"
                      f"\n{record}\nTHE STRETCH:\n{window}\n")
            t0 = time.time()
            emit = []

            def row(arm, sub, offered, truths, tail):
                a = ask(url, prefix + tail)
                b = bucket(a, truths, offered, window)
                rows.append({"case": tag, "team": team, "repo": repo, "fin": fin, "arm": arm,
                             "sub": sub, "answer": a, "bucket": b})
                emit.append(f"{arm}/{sub}={b}:{a[:22] or '-'}")
                return a

            if "new" in arms:
                for key in ("repository", "project"):
                    f = pool[key]
                    labels = real(f["labels"])
                    truth = repo if key == "repository" else PROJECT_OF.get(repo)
                    if not truth:
                        continue                      # no defensible ground truth (john/project)
                    truths = [truth] + ([truth.split("-")[-1]] if "-" in truth else [])
                    ablated = [l for l in labels if norm(l) != norm(truth)]
                    native = len(ablated) == len(labels)   # truth was never a label to remove
                    row("new", f"{key}.control", labels, truths,
                        extractive_tail(f["question"], labels))
                    row("new", f"{key}.ablated" + ("*native" if native else ""), ablated, truths,
                        extractive_tail(f["question"], ablated))

            if "silence" in arms:
                for key in SILENT:
                    f = pool[key]
                    labels = real(f["labels"])
                    row("silence", key, labels, [],
                        extractive_tail(f["question"], labels))

            if "classify" in arms:
                for hint in HINTS:
                    row("classify", hint or "none", wf_labels, ["Engineering"],
                        infer_tail(wf["question"], wf["description"], wf_labels, hint))

            if "detect" in arms:
                for hint in [team] + FALSE_DETECT_HINTS:
                    sub = ("true:" if hint == team else "false:") + hint
                    row("detect", sub, wf_labels, ["Engineering"], detect_tail(wf_labels, hint))

            print(f"  {tag:10} fin={fin:3}  {time.time()-t0:5.1f}s  " + "  ".join(emit))
        json.dump(rows, open(args.out, "w"), indent=1)

    serve(args.model, args.device, args.threads, args.ctx, run)
    report(rows, args.device, args.out)


def report(rows, device, out):
    def sel(**kw):
        return [r for r in rows if all(r[k] == v for k, v in kw.items())]

    print(f"\n{'='*78}\nRESULTS ({device}, {len(rows)} asks, raw answers in {out})")
    order = ["truth", "wrong_label", "other_in_text", "not_in_text", "empty"]

    new = sel(arm="new")
    if new:
        print("\nARM 1 — NEW (ablation). Correct = `truth`: the name in the model's own words.")
        print(f"  {'sub-arm':26} {'n':>3}  " + "  ".join(f"{o:>13}" for o in order))
        for sub in sorted({r["sub"] for r in new}):
            g = sel(arm="new", sub=sub)
            c = collections.Counter(r["bucket"] for r in g)
            print(f"  {sub:26} {len(g):>3}  " +
                  "  ".join(f"{c[o]:>4} {pct(c[o], len(g))}" for o in order))
        for mode in ("control", "ablated"):
            g = [r for r in new if mode in r["sub"]]
            t = sum(1 for r in g if r["bucket"] == "truth")
            print(f"  {mode.upper():>10} recall over both dimensions: {t}/{len(g)}"
                  f" = {pct(t, len(g))}")

    sil = sel(arm="silence")
    if sil:
        c = collections.Counter(r["bucket"] for r in sil)
        fab = len(sil) - c["empty"]
        print("\nARM 1 — SILENCE (four dimensions this corpus has no instance of).")
        print(f"  correct (empty) {c['empty']:>3}/{len(sil)} = {pct(c['empty'], len(sil))}   "
              f"FABRICATION {fab}/{len(sil)} = {pct(fab, len(sil))}")
        for o in order:
            if c[o] and o != "empty":
                ex = [r["answer"] for r in sil if r["bucket"] == o][:4]
                print(f"    {o:15} {c[o]:>3}  e.g. {ex}")

    cls = sel(arm="classify")
    if cls:
        print("\nARM 2 — CLASSIFY. Does the answer track the hint or the content?")
        eng = [r for r in cls if r["team"] == "Engineering"]
        print(f"  {'hint':18} {'n':>3}  {'== hint':>9}  {'Engineering':>12}  answers")
        for sub in [h or "none" for h in HINTS]:
            g = [r for r in eng if r["sub"] == sub]
            if not g:
                continue
            follow = sum(1 for r in g if norm(r["answer"]) == norm(sub))
            anchor = sum(1 for r in g if norm(r["answer"]) == "engineering")
            top = collections.Counter(r["answer"] or "-" for r in g).most_common(3)
            print(f"  {sub:18} {len(g):>3}  {follow:>3} {pct(follow, len(g))}  "
                  f"{anchor:>3} {pct(anchor, len(g))}  {top}")
        false_h = [r for r in eng if r["sub"] not in ("Engineering", "none")]
        anchor = sum(1 for r in false_h if norm(r["answer"]) == "engineering")
        follow = sum(1 for r in false_h if norm(r["answer"]) == norm(r["sub"]))
        print(f"  under a FALSE hint: content-anchored {anchor}/{len(false_h)} = "
              f"{pct(anchor, len(false_h))}   hint-following {follow}/{len(false_h)} = "
              f"{pct(follow, len(false_h))}")
        for grp, name in ((lambda r: r["fin"] >= FIN_SATURATED, "finance-saturated"),
                          (lambda r: r["fin"] < FIN_SATURATED, "plain")):
            g = [r for r in eng if grp(r) and r["sub"] in ("Engineering", "none")]
            bad = [r for r in g if norm(r["answer"]) not in ("engineering", "")]
            print(f"  spurious (true/no hint, {name:17}) {len(bad)}/{len(g)} = "
                  f"{pct(len(bad), len(g))}  {collections.Counter(r['answer'] for r in bad)}")

    det = sel(arm="detect")
    if det:
        print("\nARM 2 — DETECT (the production shape; silence = no override).")
        # Answering the hint back ("Engineering" when told Engineering) violates the protocol —
        # the tail asks for an empty string in that case — but it MEANS no override, so it is
        # scored as one. Counting it as an override would invent a misattribution that is really
        # a format slip.
        def outcome(r):
            hint = r["sub"].split(":", 1)[1]
            if not r["answer"]:
                return "silence"
            return "echo" if norm(r["answer"]) == norm(hint) else "override:" + r["answer"]

        eng = [r for r in det if r["team"] == "Engineering"]
        true_h = [r for r in eng if r["sub"].startswith("true:")]
        spur = [r for r in true_h if outcome(r).startswith("override")]
        print(f"  precision: no override under the TRUE hint "
              f"{len(true_h)-len(spur)}/{len(true_h)} = {pct(len(true_h)-len(spur), len(true_h))}"
              f"   {collections.Counter(outcome(r) for r in true_h)}")
        for grp, name in ((lambda r: r["fin"] >= FIN_SATURATED, "finance-saturated"),
                          (lambda r: r["fin"] < FIN_SATURATED, "plain")):
            g = [r for r in true_h if grp(r)]
            bad = [r for r in g if outcome(r).startswith("override")]
            print(f"    {name:18} {len(bad)}/{len(g)} spurious = {pct(len(bad), len(g))}")
        for hint in FALSE_DETECT_HINTS:
            g = [r for r in eng if r["sub"] == "false:" + hint]
            ok = sum(1 for r in g if norm(r["answer"]) == "engineering")
            print(f"  recall under false hint {hint:10} {ok}/{len(g)} = {pct(ok, len(g))}"
                  f"  {collections.Counter(outcome(r) for r in g)}")
        other = [r for r in det if r["team"] != "Engineering"]
        if other:
            print("  non-engineering team (truth is unrepresentable in the label set):")
            for r in other:
                print(f"    {r['case']:10} {r['sub']:22} -> {r['answer'] or '(silence)'!r}")


if __name__ == "__main__":
    main()
