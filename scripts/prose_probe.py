#!/usr/bin/env python3
"""Can GLiNER2 classify window-scale work into a closed extensive label set?

Pre-registered in docs/superpowers/specs/2026-08-22-prose-probe-preregistration.md.
Read it before changing anything here: the arms, the sample and the decision rule
are fixed in advance and this script only executes them.

    prose_probe.py sample     # seeded draw: 24 gated windows + 12 negative controls
    prose_probe.py render     # digest + window text + exact gliner2 word-token counts
    prose_probe.py sheets     # per-window label sheets (TEXT ONLY — never the digest)
    prose_probe.py run        # the sidecar arms
    prose_probe.py score      # against the committed truth.csv
"""
import argparse, glob, json, os, random, re, subprocess, sys, time, urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import pandas as pd
import yaml

from refseries import characterize, executive
from context_value import window_text

REPO_FRAMES = "/tmp/refseries"          # repo-scoped: used only for the gate counts
SESS_FRAMES = "/tmp/refseries-sess"     # session-scoped: what the digest is built from
ROOTS = [os.path.expanduser("~/.claude/projects"), "/tmp/john-projects"]
OUT = os.path.expanduser("~/keld/refseries-context/prose-probe")
SPAN, STRIDE = pd.Timedelta("60min"), pd.Timedelta("50min")
SEED = 20260822
N_GATED, N_CONTROL, MAX_PER_SESSION = 24, 12, 2

# Verbatim copy of gliner2 WhitespaceTokenSplitter._PATTERN (processor.py:150-157). max_len
# truncates WORD tokens, head-keep/tail-drop (processor.py:408-411), so this is the unit the
# budget is actually spent in. Copied rather than imported: gliner2 lives in the sidecar venv,
# this script runs in the study venv. A drift test asserts they still agree.
WORD = re.compile(
    r"""(?:https?://[^\s]+|www\.[^\s]+)
    |[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}
    |@[a-z0-9_]+
    |\w+(?:[-_]\w+)*
    |\S""",
    re.VERBOSE | re.IGNORECASE,
)


def ntok(s):
    """Exact gliner2 word-token count. Measured, never estimated from characters."""
    return sum(1 for _ in WORD.finditer(s.lower()))


# The label set. Deliberately NOT frame-determined — a file under specs/ can be any of the three
# — so the frame rule stays a genuine competitor rather than a tautology, which is what made the
# activity-type rule's 100% partly circular.
LABELS = [
    ("plan", "planning what to do next",
     "The writing decides or sequences work that has not happened yet: a plan, a spec for "
     "something still to be built, a task breakdown, a proposal."),
    ("record", "recording what was found",
     "The writing captures results, measurements, findings or decisions already reached: a "
     "report, a handoff, an experiment writeup, notes on what happened."),
    ("explain", "explaining how something works",
     "The writing is durable reference for a later reader: documentation, a README, a guide, a "
     "description of how a system is built."),
]
LABEL_TEXTS = [t for _, t, _ in LABELS]
ID_BY_TEXT = {t: i for i, t, _ in LABELS}
DESC = {t: d for _, t, d in LABELS}
QUESTION = "kind of writing work"


def transcript_paths():
    """session id -> transcript file."""
    out = {}
    for root in ROOTS:
        for p in glob.glob(os.path.join(root, "**", "*.jsonl"), recursive=True):
            out.setdefault(os.path.basename(p)[:8], p)
    return out


def windows():
    """Every (session, window) with its prose/code artifact evidence. The gate's input."""
    ev = pd.read_parquet(os.path.join(REPO_FRAMES, "events.parquet"))
    ev["ts"] = pd.to_datetime(ev.ts)
    rows = []
    for (repo, sess), g in ev.groupby(["repo", "session"]):
        t0, t1 = g.ts.min(), g.ts.max()
        t = t0.floor("h")
        while t < t1:
            w = g[(g.ts >= t) & (g.ts < t + SPAN)]
            if len(w):
                art = w[w.level == "artifact"]
                rows.append(dict(
                    repo=repo, session=str(sess), start=t,
                    events=len(w),
                    prose=float(art[art.ref == "prose"].n.sum()),
                    code=float(art[art.ref == "code"].n.sum())))
            t += STRIDE
    return pd.DataFrame(rows)


def cmd_sample(args):
    d = windows()
    d["prose_share"] = d.prose / (d.prose + d.code).replace(0, pd.NA)
    gated = d[d.prose >= 5].copy()
    control = d[(d.prose == 0) & (d.code >= 20)].copy()
    rng = random.Random(SEED)

    def draw(pool, n, per_session):
        idx, taken = list(pool.index), {}
        rng.shuffle(idx)
        out = []
        for i in idx:
            s = pool.at[i, "session"]
            if taken.get(s, 0) >= per_session:
                continue
            taken[s] = taken.get(s, 0) + 1
            out.append(i)
            if len(out) == n:
                break
        return pool.loc[out]

    g = draw(gated, N_GATED, MAX_PER_SESSION).assign(arm_group="gated")
    c = draw(control, N_CONTROL, 1).assign(arm_group="control")
    s = pd.concat([g, c]).sort_values(["arm_group", "session", "start"])
    s.insert(0, "wid", [f"{r.arm_group[0]}{i:02d}" for i, r in enumerate(s.itertuples(), 1)])
    os.makedirs(OUT, exist_ok=True)
    s.to_csv(f"{OUT}/sample.csv", index=False)
    print(f"gated pool {len(gated)}  control pool {len(control)}  seed {SEED}")
    print(s.to_string(index=False))


def cmd_render(args):
    s = pd.read_csv(f"{OUT}/sample.csv", parse_dates=["start"])
    refs = pd.read_parquet(f"{SESS_FRAMES}/refs.parquet")
    lvls = pd.read_parquet(f"{SESS_FRAMES}/levels.parquet")
    spk = pd.read_parquet(f"{SESS_FRAMES}/speaker.parquet")
    base = pd.read_parquet(f"{SESS_FRAMES}/baseline.parquet")
    paths, cases = transcript_paths(), []
    for r in s.itertuples():
        st = pd.Timestamp(r.start, tz="UTC") if r.start.tzinfo is None else r.start
        en = st + SPAN
        path = paths.get(r.session)
        if not path:
            print(f"  !! {r.wid} {r.session}: no transcript", file=sys.stderr)
            continue
        doc = characterize(refs, lvls, spk, r.session, st, en, 5, base=base)
        if not doc.get("rungs"):
            print(f"  !! {r.wid} {r.session}: no rungs", file=sys.stderr)
            continue
        digest = yaml.safe_dump(executive(doc), sort_keys=False, width=110, allow_unicode=True)
        text = window_text(path, st, en)
        cases.append(dict(wid=r.wid, group=r.arm_group, session=r.session, repo=r.repo,
                          start=str(st), end=str(en), prose=r.prose, code=r.code,
                          digest=digest, text=text,
                          tok_digest=ntok(digest), tok_text=ntok(text),
                          tok_both=ntok(digest + "\n" + text), chars_text=len(text)))
    json.dump(cases, open(f"{OUT}/cases.json", "w"), indent=1)
    df = pd.DataFrame([{k: c[k] for k in
                        ("wid", "group", "session", "prose", "tok_digest", "tok_text", "tok_both",
                         "chars_text")} for c in cases])
    print(df.to_string(index=False))
    print(f"\n{len(cases)} cases -> {OUT}/cases.json")
    print("token budget is 768; over-budget arms are head-truncated by gliner2:")
    for col in ("tok_digest", "tok_text", "tok_both"):
        print(f"  {col:10} median {df[col].median():6.0f}  max {df[col].max():6.0f}  "
              f"over 768: {(df[col] > 768).sum()}/{len(df)}")


def cmd_sheets(args):
    """Label sheets carry the TEXT ONLY. The digest is an experimental arm and must not touch
    ground truth."""
    cases = json.load(open(f"{OUT}/cases.json"))
    os.makedirs(f"{OUT}/sheets", exist_ok=True)
    for c in cases:
        if c["group"] != "gated":
            continue
        with open(f"{OUT}/sheets/{c['wid']}.md", "w") as fh:
            fh.write(f"# {c['wid']} — {c['session']} {c['start']} .. {c['end']}\n\n"
                     f"prose={c['prose']} code={c['code']} text_tokens={c['tok_text']}\n\n"
                     f"## Window text (tools stripped)\n\n```\n{c['text']}\n```\n")
    print(f"wrote {len(glob.glob(f'{OUT}/sheets/*.md'))} sheets to {OUT}/sheets")


def post(url, body, timeout=600):
    req = urllib.request.Request(url, data=json.dumps(body).encode(),
                                 headers={"content-type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.load(r)


def cmd_run(args):
    cases = json.load(open(f"{OUT}/cases.json"))
    url = f"http://127.0.0.1:{args.port}"
    inputs = {"digest": lambda c: c["digest"],
              "text": lambda c: c["text"],
              "both": lambda c: c["digest"] + "\n" + c["text"]}
    rows = []
    for mode in ("single", "multi"):
        task = {"labels": {t: DESC[t] for t in LABEL_TEXTS}}
        if mode == "multi":
            task["multi_label"] = True
            task["cls_threshold"] = 0.5
        for arm, get in inputs.items():
            for c in cases:
                t0 = time.time()
                res = post(f"{url}/classify",
                           {"text": get(c), "tasks": {QUESTION: task}, "max_len": args.max_len})
                dt = time.time() - t0
                rows.append(dict(wid=c["wid"], group=c["group"], mode=mode, arm=arm,
                                 secs=round(dt, 2), raw=json.dumps(res)))
                print(f"  {mode:6} {arm:6} {c['wid']:5} {dt:6.1f}s", flush=True)
    m = post(f"{url}/metrics", {}) if False else json.load(
        urllib.request.urlopen(f"{url}/metrics", timeout=30))
    pd.DataFrame(rows).to_csv(f"{OUT}/{args.out}", index=False)
    json.dump(m, open(f"{OUT}/metrics-{args.out}.json", "w"), indent=1)
    print(f"\n{len(rows)} calls -> {OUT}/{args.out}")
    print("worker:", json.dumps(m.get("worker", {})))

# --- the deterministic frame rule (arm `rule`) ---------------------------------------------
# Written before any accuracy number was computed. It reads only the frame levels the
# pre-registration named — file, dir, skill, action — and maps them by what the repository's own
# conventions mean, not by what scores well.
#
#   docs/superpowers/plans/**  or the writing-plans skill  -> plan   (sequencing work not yet done)
#   docs/superpowers/specs/**                              -> plan   (a spec describes what to build)
#   README / AGENTS / CLAUDE / docs/*.md outside those     -> explain (durable reference)
#   anything else                                          -> record (the residual: writeups)
#
# `record` is the catch-all deliberately: it is the majority class, so the rule is not allowed to
# win by having a cleverer default than the baseline it is measured against.
def frame_facts(refs, lvls, spk, base, session, st, en):
    doc = characterize(refs, lvls, spk, session, st, en, 8, base=base)
    L = {lv: blk for r in doc.get("rungs", {}).values() for lv, blk in r["levels"].items()}
    return {lv: [i["ref"] for i in L.get(lv, {}).get("top", [])] for lv in
            ("file", "dir", "skill", "action", "component", "ext")}


def rule_label(f):
    paths = [p.lower() for p in f.get("file", []) + f.get("dir", [])]
    skills = [s.lower() for s in f.get("skill", [])]
    if any("writing-plans" in s for s in skills):
        return "plan"
    if any("superpowers/plans" in p or "superpowers/specs" in p for p in paths):
        return "plan"
    if any(os.path.basename(p) in ("readme.md", "agents.md", "claude.md") for p in paths):
        return "explain"
    if any(p.startswith("docs/") and p.endswith(".md") for p in paths):
        return "explain"
    return "record"


def cmd_rule(args):
    s = pd.read_csv(f"{OUT}/sample.csv", parse_dates=["start"])
    refs = pd.read_parquet(f"{SESS_FRAMES}/refs.parquet")
    lvls = pd.read_parquet(f"{SESS_FRAMES}/levels.parquet")
    spk = pd.read_parquet(f"{SESS_FRAMES}/speaker.parquet")
    base = pd.read_parquet(f"{SESS_FRAMES}/baseline.parquet")
    rows = []
    for r in s[s.arm_group == "gated"].itertuples():
        st = pd.Timestamp(r.start, tz="UTC") if r.start.tzinfo is None else r.start
        f = frame_facts(refs, lvls, spk, base, r.session, st, st + SPAN)
        rows.append(dict(wid=r.wid, rule=rule_label(f),
                         skill=";".join(f["skill"][:3]), file=";".join(f["file"][:3])))
    d = pd.DataFrame(rows)
    d.to_csv(f"{OUT}/rule.csv", index=False)
    print(d.to_string(index=False))


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(required=True)
    for name, fn in (("sample", cmd_sample), ("render", cmd_render), ("sheets", cmd_sheets),
                     ("rule", cmd_rule)):
        sub.add_parser(name).set_defaults(fn=fn)
    r = sub.add_parser("run")
    r.add_argument("--port", type=int, required=True)
    r.add_argument("--max-len", type=int, default=768)
    r.add_argument("--out", default="runs.csv")
    r.set_defaults(fn=cmd_run)
    a = ap.parse_args()
    a.fn(a)
