#!/usr/bin/env python3
"""Attribution coverage by source — the number a cost report depends on.

    scripts/coverage.py --cases /tmp/real_cases.json            # GPU smoke
    scripts/coverage.py --cases /tmp/real_cases.json --device cpu

THE GENERAL RULE THIS ENCODES: for any dimension where we already hold the fact, do not ask the
model. Ask only where nothing else can answer.

    fact held and representable in the label set   -> answer it, free, exact
    fact held and NOT representable                -> report unrepresentable. Do not ask.
    no fact held                                   -> ask the model

The third case is the only one the model is for. The second is the important one: John is on
Product Research, the Work Function template offers eight functions and none is product, and
asking anyway produced Finance / Engineering / Engineering — confident, wrong, and unfixable by
wording. Six variants of "you may abstain" were ignored (`__none__`, a readable label, `noise`,
`Backlog`, `Other` chosen 0 times in 130). Not asking is the only thing that has ever worked, so
the gate is structural: the question is never posed.

`unrepresentable` is also more useful than either a wrong bucket or silence — it names a
dimension whose label set does not cover the org's real work, with the fact that broke it.
"""
import argparse, json, os, re, socket, subprocess, sys, time, urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from classifier_schema import classifier_shape, _is_catch_all

DEFAULT_MODEL = os.path.expanduser("~/.keld/models/gguf/granite-4.1-3b-Q4_K_M.gguf")
SCHEMA = {"type": "object", "additionalProperties": False,
          "properties": {"answer": {"type": "string", "maxLength": 60}},
          "required": ["answer"]}

# A question can presuppose an entity another dimension is responsible for finding. "Where is
# THIS CUSTOMER in their journey?" is unanswerable until a customer is found; asked anyway it
# answered Active / Onboarding / Active / Prospect for four windows with no customer at all.
PRESUPPOSES = [(re.compile(r"\bthis (customer|account|client)\b", re.I), "customer_account"),
               (re.compile(r"\bthis (ticket|issue)\b", re.I), "ticket_id"),
               (re.compile(r"\bthis (campaign)\b", re.I), "marketing_campaign")]


def norm(s):
    return re.sub(r"[^a-z0-9]+", " ", (s or "").lower()).strip()


def presupposes(question):
    for pattern, key in PRESUPPOSES:
        if pattern.search(question):
            return key
    return None


def representable(value, labels):
    """Can the label set express this value at all? Catch-alls do not count: answering Other
    when we KNOW the fact is not an answer, it is a shrug with our own data in hand."""
    v = norm(value)
    return any(norm(l) == v or v in norm(l) or norm(l) in v
               for l in labels if not _is_catch_all(l))


def serve(model, device, threads, fn):
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    cmd = ["llama-server", "-m", model, "--ctx-size", "8192", "--parallel", "1", "--no-warmup",
           "--jinja", "--flash-attn", "off", "--batch-size", "512", "--ubatch-size", "512",
           "--port", str(port)]
    if device == "cpu":
        cmd += ["--device", "none", "--threads", str(threads), "--cache-ram", "512",
                "--no-repack"]
    proc = subprocess.Popen(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    url = f"http://127.0.0.1:{port}"
    try:
        for _ in range(600):
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
    with urllib.request.urlopen(req, timeout=1800) as r:
        return json.loads(json.load(r)["choices"][0]["message"]["content"])["answer"].strip()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--device", choices=["gpu", "cpu"], default="gpu")
    ap.add_argument("--threads", type=int, default=18)
    ap.add_argument("--model", default=DEFAULT_MODEL)
    ap.add_argument("--pool", default="/tmp/pool5.json")
    ap.add_argument("--cases", default="/tmp/real_cases.json")
    args = ap.parse_args()

    pool = json.load(open(args.pool))
    cases = json.load(open(args.cases))
    src = dict.fromkeys(("known", "inferred", "extracted", "other", "unrepresentable",
                         "unattributed"), 0)

    if args.device == "gpu":
        print("[GPU smoke test — confirm anything real with --device cpu]")

    def run(url):
        for tag, wpath, rpath, team, repo, lang in cases:
            window, record = open(wpath).read(), open(rpath).read()
            hay = norm(window + " " + record)
            # The facts we already hold, by dimension. In production these arrive with the job:
            # daemon/context.go builds Meta{Repo: j.Cwd, GitBranch, Project} from the hook, and
            # the team comes from Atlas. Nothing here is parsed out of a transcript.
            facts = {"project": repo, "repository": repo, "work_function": team,
                     "programming_language": lang}
            prefix = (f"Someone is asked a question about a stretch of work. They are on the "
                      f"{team} team.\n\nSESSION RECORD:\n{record}\nTHE STRETCH:\n{window}\n")
            got, answered = [], {}
            for f in sorted(pool, key=lambda g: presupposes(g["question"]) is not None):
                key, labs = f["key"], [l for l in (f.get("labels") or [])
                                       if not _is_catch_all(l)]
                fact = facts.get(key)
                if key in facts:
                    if not fact:
                        src["unattributed"] += 1
                    elif not labs or representable(fact, labs):
                        src["known"] += 1
                        answered[key] = fact
                        got.append(f"{key}=K:{fact}")
                    else:
                        src["unrepresentable"] += 1
                        got.append(f"{key}=!({fact} not a label)")
                    continue
                need = presupposes(f["question"])
                if need and not answered.get(need):
                    src["unattributed"] += 1
                    continue
                shape = classifier_shape(f["type"], f.get("labels") or [])
                if shape == "extractive":
                    tail = (f"QUESTION: {f['question']}\n  Options: {', '.join(labs)}\n"
                            "Answer with an option exactly if it fits, or a name the stretch "
                            "uses, or an empty string. Do not offer an option because it is on "
                            "the list.\n")
                else:
                    tail = (f"QUESTION: {f['question']}\n  {f['description']}\n"
                            f"  Options: {', '.join(labs)}\n"
                            f"This is a judgement: nothing will say it outright. The {team} team "
                            "is a hint, not the answer. Answer with one option, or an empty "
                            "string if there is nothing to judge from.\n")
                a = ask(url, prefix + tail)
                if not a:
                    src["unattributed"] += 1
                    continue
                m = next((l for l in labs if norm(l) == norm(a)), None)
                if m and shape != "extractive":
                    src["inferred"] += 1
                    answered[key] = m
                    got.append(f"{key}=I:{m}")
                elif m and norm(m) in hay:
                    src["extracted"] += 1
                    answered[key] = m
                    got.append(f"{key}=X:{m}")
                elif norm(a) in hay:
                    src["extracted"] += 1
                    answered[key] = a
                    got.append(f"{key}=NEW:{a[:18]}")
                else:
                    src["unattributed"] += 1
            print(f"  {tag:12} " + "  ".join(got))
    serve(args.model, args.device, args.threads, run)

    n = sum(src.values())
    print(f"\n{'='*70}\nCOVERAGE over {n} classifier-windows ({args.device}):")
    for k in src:
        print(f"  {k:16} {src[k]:4}  {100*src[k]/n:5.1f}%")
    att = src["known"] + src["inferred"] + src["extracted"]
    print(f"  {'ATTRIBUTED':16} {att:4}  {100*att/n:5.1f}%")


if __name__ == "__main__":
    main()
