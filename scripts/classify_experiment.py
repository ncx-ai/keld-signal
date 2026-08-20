#!/usr/bin/env python3
"""Ask each classifier its own question, by the method its label set implies.

    scripts/classify_experiment.py                 # GPU smoke test, fast
    scripts/classify_experiment.py --device cpu    # confirm on the target platform

GPU IS A SMOKE TEST ONLY. The two backends use different quantised matmul kernels and diverge
on real decisions — measured: 3 demand markers on GPU against 1 on CPU for the same window,
complexity 8 against 5. Pinning -fa off and matching batch sizes did not reconcile them. So a
GPU run catches gross failures (everything abstaining, nothing parsing) and nothing else; any
result worth acting on gets re-run with --device cpu.

Two methods, chosen per classifier by classifier_shape():

  name-like  (project, repository, campaign, account, sprint, ticket, language)
      Ask the question with the options offered, and let it stop at the first of: pick an
      option / name something the options lack / say nothing. Measured 0 fabrications in 40,
      which is what a cost report needs, at roughly 1 answer in 10.

  concept-like  (work_function)
      Same question, but inference is PERMITTED and the team is given as a prior. Nobody writes
      "this is engineering work", so a ladder that forbids guessing silences the field: the
      three-step version answered 1 of 4 where permitting inference answered 4 of 4.

The instruction that suppresses fabrication also suppresses legitimate inference, and the model
cannot tell them apart — which is why the method has to be chosen per classifier rather than
per prompt.
"""
import argparse, json, os, re, socket, subprocess, sys, time, urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from classifier_schema import classifier_shape, _is_catch_all

DEFAULT_MODEL = os.path.expanduser("~/.keld/models/gguf/granite-4.1-3b-Q4_K_M.gguf")
SCHEMA = {"type": "object", "additionalProperties": False,
          "properties": {"answer": {"type": "string", "maxLength": 60},
                         "from_options": {"type": "boolean"}},
          "required": ["answer", "from_options"]}



# A question can PRESUPPOSE an entity another classifier is responsible for finding.
#
#   Work Function            "Which business function does this work serve?"   -> about the work
#   Customer Lifecycle Stage "Where is THIS CUSTOMER in their journey?"        -> about a customer
#
# The second is unanswerable unless a customer was found, and asked anyway it answered Active,
# Onboarding, Active, Prospect for four windows containing no customer relationship at all —
# four confident fabrications from one classifier, which in a cost report would attribute spend
# to lifecycle stages that do not exist. The presupposition is visible in the question's own
# wording, so it can be detected rather than configured.
PRESUPPOSES = [
    (re.compile(r"\bthis (customer|account|client)\b", re.I), "customer_account"),
    (re.compile(r"\bthis (ticket|issue)\b", re.I), "ticket_id"),
    (re.compile(r"\bthis (campaign)\b", re.I), "marketing_campaign"),
    (re.compile(r"\bthis (repository|repo)\b", re.I), "repository"),
]


def depends_on(question):
    """Which classifier must have answered before this question can be asked."""
    for pattern, key in PRESUPPOSES:
        if pattern.search(question):
            return key
    return None


def norm(s):
    return re.sub(r"[^a-z0-9]+", " ", s.lower()).strip()


def serve(model, device, threads, fn):
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    cmd = ["llama-server", "-m", model, "--ctx-size", "8192", "--parallel", "1",
           "--no-warmup", "--jinja", "--flash-attn", "off",
           "--batch-size", "512", "--ubatch-size", "512", "--port", str(port)]
    if device == "cpu":
        cmd += ["--device", "none", "--threads", str(threads),
                "--cache-ram", "512", "--no-repack"]
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
            "max_tokens": 128,
            "response_format": {"type": "json_schema",
                                "json_schema": {"name": "a", "strict": True, "schema": SCHEMA}}}
    req = urllib.request.Request(url + "/v1/chat/completions",
                                 data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json"})
    t0 = time.time()
    with urllib.request.urlopen(req, timeout=1800) as r:
        return json.loads(json.load(r)["choices"][0]["message"]["content"]), time.time() - t0


def tail_for(f, shape, team):
    labs = [l for l in (f.get("labels") or []) if not _is_catch_all(l)]
    opts = ("  Options: " + ", ".join(labs) + "\n") if labs else ""
    if shape == "extractive":
        return (f"QUESTION: {f['question']}\n" + opts +
                "Answer in three steps, stopping at the first that applies:\n"
                "  1. If one of the options is what this work is for, answer with it exactly "
                "and set from_options true.\n"
                "  2. Otherwise, if the stretch NAMES something that answers the question but "
                "is not in the options, answer with that name and set from_options false.\n"
                "  3. Otherwise answer with an empty string. Do not offer an option because it "
                "looks plausible.\n")
    return (f"QUESTION: {f['question']}\n  {f['description']}\n" + opts +
            f"This is a judgement, not a name to find: nothing in the text will say "
            f"\"{labs[0] if labs else 'this'}\" outright, so decide from what the work IS. "
            f"Being on the {team} team is a hint, not the answer. Answer with one option and "
            "set from_options true, or with an empty string only if the stretch gives you "
            "nothing at all to judge from.\n")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--device", choices=["gpu", "cpu"], default="gpu",
                    help="gpu = smoke test (fast, NOT the target platform); cpu = confirm")
    ap.add_argument("--threads", type=int, default=18)
    ap.add_argument("--model", default=DEFAULT_MODEL)
    ap.add_argument("--pool", default="/tmp/pool4.json")
    ap.add_argument("--windows", default="/tmp/windows.json",
                    help="json list of [tag, window, record, team]")
    args = ap.parse_args()

    pool = json.load(open(args.pool))
    windows = json.load(open(args.windows))
    tot = {"option": 0, "free": 0, "none": 0, "bogus": 0, "skipped": 0}
    secs = 0.0

    if args.device == "gpu":
        print("[GPU smoke test — not the target platform. Confirm anything real with "
              "--device cpu]")

    for tag, wpath, rpath, team in windows:
        window, record = open(wpath).read(), open(rpath).read()
        hay = norm(window + " " + record)
        prefix = (f"Someone is asked a question about a stretch of work they can see below. "
                  f"They are on the {team} team.\n\nSESSION RECORD:\n{record}"
                  f"\nTHE STRETCH:\n{window}\n")

        def run(url, tag=tag, prefix=prefix, hay=hay, team=team):
            nonlocal secs
            print(f"\n{'='*72}\n{tag}  (team: {team})\n{'='*72}")
            answered = {}
            # Ordered so a presupposed classifier runs before the one presupposing it.
            ordered = sorted(pool, key=lambda g: depends_on(g["question"]) is not None)
            for f in ordered:
                need = depends_on(f["question"])
                if need and not answered.get(need):
                    tot["skipped"] += 1
                    print(f"  ·   . {f['key']:24} not asked — no {need} in this stretch")
                    continue
                labs = [l for l in (f.get("labels") or []) if not _is_catch_all(l)]
                shape = classifier_shape(f["type"], f.get("labels") or [])
                v, s = ask(url, prefix + tail_for(f, shape, team))
                secs += s
                a = v["answer"].strip()
                mark = "E" if shape == "extractive" else "I"
                if not a:
                    tot["none"] += 1
                    print(f"  -   {mark} {f['key']:24} (nothing)")
                    continue
                matched = next((l for l in labs if norm(l) == norm(a)), None)
                if matched:
                    tot["option"] += 1
                    answered[f["key"]] = matched
                    print(f"  OK  {mark} {f['key']:24} {matched}")
                elif norm(a) in hay:
                    tot["free"] += 1
                    answered[f["key"]] = a
                    print(f"  NEW {mark} {f['key']:24} {a!r} — in the text, no label for it")
                else:
                    tot["bogus"] += 1
                    print(f"  X   {mark} {f['key']:24} {a!r} — not in the text, dropped")
        serve(args.model, args.device, args.threads, run)

    n = sum(tot.values())
    print(f"\n{'='*72}\n{args.device}: {tot['option']}/{n} chose an option · "
          f"{tot['free']}/{n} named something unlabelled · {tot['none']}/{n} nothing · "
          f"{tot['bogus']}/{n} fabricated · {tot['skipped']}/{n} not asked   "
          f"({secs:.0f}s inference)")


if __name__ == "__main__":
    main()
