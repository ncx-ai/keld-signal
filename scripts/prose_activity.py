"""Can GLiNER2 name what the assistant is DOING, from its prose alone?

Pre-registration: ~/keld/refseries-context/prose-activity/PREREGISTRATION.md — read it first; the
decision rules were fixed before any run and this script does not get to reinterpret them.

Ground truth is DETERMINISTIC, rolled up from tool-call actions. It is never hand-labelled.
Run with the sidecar venv (bashlex lives there): ~/.keld/sidecar-venv/bin/python
"""
import json, os, re, sys, glob, csv
from datetime import datetime, timedelta

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "sidecar"))
from app.analysis.transcript import iter_turns
from app.analysis.text import text_of, is_command_echo
from app.analysis.vocab import action_for
from app.analysis.shell import parsed_command_names, unwrap_command

SPAN, STRIDE = 60, 50          # minutes; stride must not divide span (established)

# --- the pre-registered action -> activity map. Anything absent is UNMAPPED, by design. ---
RESEARCH = {"read", "search", "fetch", "query a database"}
EDIT     = {"edit", "create", "transform"}
MAPPED   = RESEARCH | EDIT
COVERAGE_FLOOR = 0.5           # mapped actions must be >=50% of tool events
EDIT_FLOOR = 3                 # 1-2 edits are incidental to reading; 3+ is authoring
RESEARCH_FLOOR = 3

CODE_FENCE = re.compile(r"^```(\w+)?\s*$")


def _epoch(ts):
    return datetime.fromisoformat(ts.replace("Z", "+00:00"))


def elide_code(text):
    """Fenced blocks over 5 lines -> a VISIBLE placeholder that keeps the signal (code was
    written, this much, this language) without the tokens. Short blocks stay: an inline
    identifier or a two-line snippet IS prose here."""
    out, buf, lang, inside = [], [], None, False
    for line in text.split("\n"):
        m = CODE_FENCE.match(line)
        if m and not inside:
            inside, lang, buf = True, m.group(1) or "", []
            continue
        if inside and line.strip().startswith("```"):
            if len(buf) > 5:
                out.append(f"[code omitted: {len(buf)} lines {lang or 'text'}]")
            else:
                out.extend(buf)
            inside, buf = False, []
            continue
        (buf if inside else out).append(line)
    if inside:                                    # unterminated fence
        out.append(f"[code omitted: {len(buf)} lines {lang or 'text'}]" if len(buf) > 5 else "\n".join(buf))
    return "\n".join(out)


def actions_of(line):
    """Every physical act in one assistant line, via its tool_use blocks."""
    acts = []
    content = line.get("message", {}).get("content")
    if not isinstance(content, list):
        return acts
    for b in content:
        if not isinstance(b, dict) or b.get("type") != "tool_use":
            continue
        name, inp = b.get("name"), b.get("input") or {}
        if name == "Bash":
            names = parsed_command_names(inp.get("command", "")) or []
            for exe in names:
                a = action_for(exe=exe, verb=None)
                if a:
                    acts.append(a)
            if not names:
                acts.append(None)
        else:
            acts.append(action_for(tool=name))
    return [a for a in acts if a]


def parse(path):
    """One pass: (ts, role, prose, actions, is_user_prompt) per turn."""
    turns = []
    for o in iter_turns(path):
        role = o.get("message", {}).get("role") or o.get("type")
        content = o.get("message", {}).get("content")
        txt = text_of(content)
        if role == "assistant":
            turns.append((_epoch(o["timestamp"]), "assistant", txt, actions_of(o), False))
        elif role == "user":
            real = bool(txt.strip()) and not is_command_echo(txt)
            turns.append((_epoch(o["timestamp"]), "user", txt if real else "", [], real))
    return turns


def windows(turns):
    if not turns:
        return
    t0, tN = turns[0][0], turns[-1][0]
    start, span, stride = t0, timedelta(minutes=SPAN), timedelta(minutes=STRIDE)
    while start < tN:
        end = start + span
        sl = [t for t in turns if start <= t[0] < end]
        if sl:
            yield start, end, sl
        start += stride


def counts_of(sl):
    acts = [a for t in sl for a in t[3]]
    return (sum(1 for a in acts if a in RESEARCH), sum(1 for a in acts if a in EDIT),
            sum(1 for a in acts if a not in MAPPED), len(acts))


def truth_of(sl):
    """The deterministic label — PRECEDENCE, not dominance (see PREREGISTRATION Amendment 1).

    These classes are a hierarchy of DEMAND, not a partition by volume. Sustained editing means the
    window needed a code-capable model however many reads preceded it, so `editing` is checked
    first. Rolling up by event count instead produced a 95.4% majority baseline, because an hour of
    authoring issues ~50 Reads and ~5 Edits.
    """
    n_res, n_edit, _, n_all = counts_of(sl)
    if n_all == 0:
        return ("conversing", 1.0, 0) if any(t[1] == "assistant" and t[2].strip() for t in sl) else (None, 0, 0)
    if n_edit >= EDIT_FLOOR:
        return ("editing", n_edit / n_all, n_all)
    if n_res >= RESEARCH_FLOOR:
        return ("researching", n_res / n_all, n_all)
    return (None, 0, n_all)


def build(roots, out):
    rows = []
    files = [f for r in roots for f in glob.glob(os.path.join(r, "**", "*.jsonl"), recursive=True)]
    for i, path in enumerate(files):
        try:
            turns = parse(path)
        except Exception:
            continue
        for start, end, sl in windows(turns):
            label, share, nacts = truth_of(sl)
            n_res, n_edit, n_unmapped, n_all = counts_of(sl)
            prose = "\n\n".join(elide_code(t[2]).strip() for t in sl if t[1] == "assistant" and t[2].strip())
            prompts = "\n\n".join(t[2].strip() for t in sl if t[4])
            if not prose.strip():
                continue
            rows.append({"wid": f"{os.path.basename(path)[:8]}-{start:%Y%m%dT%H%M}",
                         "file": path, "start": start.isoformat(), "end": end.isoformat(),
                         "truth": label or "", "share": round(share, 3), "n_actions": nacts,
                         "n_res": n_res, "n_edit": n_edit, "n_unmapped": n_unmapped, "n_all": n_all,
                         "n_assist": sum(1 for t in sl if t[1] == "assistant" and t[2].strip()),
                         "prose": prose, "prompts": prompts})
    with open(out, "w") as f:
        for r in rows:
            f.write(json.dumps(r) + "\n")
    lab = [r for r in rows if r["truth"]]
    from collections import Counter
    print(f"files={len(files)} windows={len(rows)} labelable={len(lab)}")
    print("truth distribution:", dict(Counter(r["truth"] for r in lab)))
    print("unlabelable:", len(rows) - len(lab))


# --- label set: scored against readable DESCRIPTIONS, never bare ids (established finding) ---
LABELS = {
    "researching": "reading and searching to gather information: inspecting files, looking things up, exploring what already exists",
    "editing": "writing and changing content: authoring code, applying edits, creating and modifying files",
    "conversing": "talking with the person: answering a question, confirming, asking what they want, discussing what to do",
    "reasoning": "working out an answer by analysis: weighing options, deciding between alternatives, explaining why something is so",
}
BUDGET = 768                   # word tokens; the production ceiling (KELD_ENRICH_TOKEN_CEILING)
SENT = re.compile(r"(?<=[.!?])\s+")


def bound(messages, budget=BUDGET):
    """Fit the window to `budget` word tokens by trimming the LONGEST message first, at a SENTENCE
    boundary — never the last messages first, never mid-clause (AGENTS.md; standing instruction).
    Returns (text, dropped_tokens). Dropping is VISIBLE: a notice rides in the text."""
    msgs = [m for m in messages if m.strip()]
    tok = lambda m: len(m.split())
    total = sum(tok(m) for m in msgs)
    if total <= budget:
        return "\n\n".join(msgs), 0
    dropped = 0
    while total > budget and msgs:
        i = max(range(len(msgs)), key=lambda j: tok(msgs[j]))
        sents = SENT.split(msgs[i])
        if len(sents) <= 1:                       # single sentence: drop it whole, never mid-clause
            dropped += tok(msgs[i]); total -= tok(msgs[i]); msgs.pop(i); continue
        keep, n = [], 0
        target = max(1, tok(msgs[i]) - (total - budget))
        for s in sents:
            if n + len(s.split()) > target and keep:
                break
            keep.append(s); n += len(s.split())
        d = tok(msgs[i]) - n
        dropped += d; total -= d
        msgs[i] = " ".join(keep)
    text = "\n\n".join(m for m in msgs if m.strip())
    if dropped:
        text += f"\n\n[{dropped} further words of assistant prose omitted]"
    return text, dropped


def score(inp, out, arm, port, labels, limit=0):
    import urllib.request
    rows = [json.loads(l) for l in open(inp) if json.loads(l)["truth"]]
    if limit:
        rows = rows[:limit]
    lab = {k: LABELS[k] for k in labels}
    done = []
    t0 = datetime.now()
    for i, r in enumerate(rows):
        src = r["prose"] if arm == "B" else r["prompts"]
        if not src.strip():
            continue
        text, dropped = bound(src.split("\n\n"))
        body = json.dumps({"text": text, "tasks": {"activity": {"labels": lab}}, "max_len": BUDGET}).encode()
        req = urllib.request.Request(f"http://127.0.0.1:{port}/classify", body,
                                     {"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=180) as resp:
                res = json.load(resp)
        except Exception as e:
            done.append({**{k: r[k] for k in ("wid", "truth")}, "pred": "", "err": str(e)[:80]}); continue
        hits = (res.get("results") or {}).get("activity") or []
        pred = hits[0]["label"] if hits else ""
        conf = round(hits[0].get("confidence", 0), 4) if hits else 0.0
        done.append({"wid": r["wid"], "truth": r["truth"], "pred": pred, "conf": conf,
                     "dropped": dropped, "tokens": len(text.split())})
        if (i + 1) % 50 == 0:
            el = (datetime.now() - t0).total_seconds()
            print(f"  {i+1}/{len(rows)} {el:.0f}s ({el/(i+1):.2f}s/win)", flush=True)
    with open(out, "w") as f:
        for d in done:
            f.write(json.dumps(d) + "\n")
    print(f"arm {arm}: {len(done)} scored -> {out}")


if __name__ == "__main__":
    cmd = sys.argv[1]
    if cmd == "build":
        build(sys.argv[3:], sys.argv[2])
    elif cmd == "score":
        # score <in> <out> <arm> <port> <labels-csv> [limit]
        score(sys.argv[2], sys.argv[3], sys.argv[4], int(sys.argv[5]),
              sys.argv[6].split(","), int(sys.argv[7]) if len(sys.argv) > 7 else 0)
