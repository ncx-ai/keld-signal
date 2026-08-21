#!/usr/bin/env python3
"""Time series over the references a session touches, and over who is doing the talking.

    PY=~/.keld/study-venv/bin/python
    $PY scripts/refseries.py extract                  # transcripts -> events.parquet
    $PY scripts/refseries.py series                   # tables + bins/metrics/levels.parquet
    $PY scripts/refseries.py series --repo keld-atlas --bin 1 --detail

Needs pandas + pyarrow: `python -m venv ~/.keld/study-venv && ~/.keld/study-venv/bin/pip install
pandas pyarrow`. A study venv of its own — the sidecar venv is 3.12 + torch and is not a place
to put analysis deps.

THE RULE: state carries, rates don't. A gap means time passed, not that focus moved — someone
slept. So `count`/`rate` is genuinely zero across a gap and idle bins stay in the frame as idle,
while state (which repo/branch/component/file is in focus) is FORWARD-FILLED across it with an
`age_h` beside it. A fill without an age is a lie at long lags; the age, judged against the
level's own measured half-life, is what makes it honest.

Two families of channel:

  reference levels   repo, branch, component, dir, file, lang, tool, verb, agent, skill, model.
                     Categorical: the metric of interest is the COMPOSITION and how fast it
                     turns over. Deterministic, no model. Taken from tool-call INPUTS and
                     per-line metadata, never tool OUTPUT — output records what was observed
                     rather than worked on, and one `ls -R` would own the vocabulary.

  speaker channels   how much each side is talking, and in what shape. Message COUNT and message
                     SIZE are deliberately separate series: a burst of short messages ("ok",
                     "continue") and a burst of long ones are both spikes in count and opposite
                     in kind, and `chars_per_msg` is what tells them apart. Tokens are real —
                     `message.usage`, deduped by requestId, which repeats across a response's
                     thinking/text/tool_use lines and would otherwise treble every count.

Command echoes and injected skill documents are counted as `user_echo`, not as the engineer
speaking (qwen_windows.is_command_echo — a window of five /login echoes once scored frustration
4). Assistant thinking is its own channel, separate from what it said out loud.

Design: docs/superpowers/specs/2026-08-21-reference-series-design.md
"""
import argparse, glob, hashlib, json, math, os, re, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from qwen_windows import EXT_LANG, is_command_echo

import numpy as np
import pandas as pd

OUTDIR = "/tmp/refseries"
CLAUDE_ROOTS = [os.path.expanduser("~/.claude/projects")]
REPO_ROOT = os.path.expanduser("~/keld")

WORKTREE = re.compile(r"/\.claude/worktrees/[^/]+")
PATH_TOKEN = re.compile(r"[\w.\-/]*/[\w.\-/]+|[\w.\-]+\.(?:" +
                        "|".join(e[1:] for e in EXT_LANG) + r")\b")
PLAUSIBLE_PATH = re.compile(r"^(?:[\w.@+\-]+/)+[\w.@+\-]*[A-Za-z][\w.@+\-]*\.[A-Za-z0-9]{1,6}$")


def plausible_path(tok):
    """A slash and a dotted extension, and no segment that is only digits. Measured against the
    alternative: without this, the top `dir` references included `chars/msg` and `0/20`, and the
    top `file` references included `r.h` — all three quoted out of our own command text."""
    return bool(PLAUSIBLE_PATH.match(tok)) and not any(
        seg.isdigit() for seg in tok.split("/"))
ENVVAR = re.compile(r"^[A-Z_][A-Z0-9_]*=")
SHELL_KEYWORD = {"if", "then", "fi", "else", "elif", "for", "do", "done", "while", "until",
                 "case", "esac", "in", "function", "return", "local", "declare", "read",
                 "true", "false", "test", "[", "[[", "{", "}", "(", ")", ":"}
TWO_WORD = {"git", "go", "npm", "pnpm", "yarn", "uv", "pip", "python3", "python", "make",
            "docker", "kubectl", "cargo", "gh", "systemctl", "launchctl", "brew", "poetry"}
PATH_INPUTS = ("file_path", "notebook_path", "path")

LEVELS = ["repo", "branch", "component", "dir", "file", "lang", "tool", "verb", "agent",
          "skill", "model", "mcp_server", "mcp_tool"]
MCP_TOOL = re.compile(r"^mcp__(?P<server>[^_]+(?:_[^_]+)*?)__(?P<tool>.+)$")
SAY = ["user", "user_echo", "asst", "asst_think"]
TOK = ["out", "in_fresh", "in_cached"]

SHORT_CHARS = 40        # "ok", "continue", "do it" — the low-information engineer turn
BURST_S = 60            # a message within a minute of the last is the same breath
VOCAB_CAP = 4000        # above the largest real vocabulary here (verb, 3365), so nothing folds.
                        # When it does bite, __other__ is excluded from the similarity vector:
                        # a fold bucket cannot turn over, so counting it as a reference makes a
                        # fast level look slow. Measured: 25.4% of keld-atlas `file` mass landed
                        # in __other__ at a cap of 500.
REF_LAG_H = 1.0         # the fixed baseline the decay is measured FROM (see lag_curve)
LAG_BUCKETS_H = [0.0833, 0.25, 0.5, 1, 2, 4, 8, 16, 24, 48, 96, 168, 336, 720, 2160]


# ------------------------------------------------------------------ extract

def repo_of(cwd, repo_roots):
    """The repository, against any of the configured roots — a colleague's export carries macOS
    paths (/Users/<name>/...), so the root is a list, not a constant."""
    if not cwd:
        return None
    cwd = WORKTREE.sub("", cwd)
    for r in repo_roots:
        root = r.rstrip("/") + "/"
        if cwd.startswith(root):
            return cwd[len(root):].split("/")[0] or os.path.basename(r)
    return os.path.basename(cwd.rstrip("/")) or None


def rel_within(path, root):
    """The path relative to the repo, or None if it points outside it — a file in ~/.claude is
    not this repo's work."""
    if not path or not root:
        return None
    p = WORKTREE.sub("", path)
    if not p.startswith("/"):
        return p.lstrip("./") or None
    root = root.rstrip("/") + "/"
    return p[len(root):] if p.startswith(root) else None


def bash_refs(command):
    """Verbs and path-looking tokens from a shell command. Split on the operators so a pipeline
    contributes every verb in it, not just the first."""
    verbs, paths = [], []
    for seg in re.split(r"[|;&\n]+|&&|\|\|", command or ""):
        toks = [t for t in seg.strip().split() if t]
        while toks and (ENVVAR.match(toks[0]) or toks[0] in ("sudo", "time", "command", "exec")):
            toks.pop(0)
        if not toks:
            continue
        head = os.path.basename(toks[0])
        if head in TWO_WORD and len(toks) > 1 and not toks[1].startswith("-"):
            head = f"{head} {toks[1]}"
        if (head and head not in SHELL_KEYWORD and not head[0].isdigit()
                and re.fullmatch(r"[\w.\- ]{1,40}", head)):
            verbs.append(head)
        for t in toks[1:]:
            if t.startswith("-"):
                continue
            tok = t.strip("'\"(),")
            m = PATH_TOKEN.fullmatch(tok)
            if m and plausible_path(m.group(0)):
                paths.append(m.group(0))
    return verbs, paths


def text_of(content):
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "\n".join(b.get("text", "") for b in content
                         if isinstance(b, dict) and b.get("type") == "text")
    return ""


def think_blocks(content):
    """Thinking block sizes, which in practice means block COUNTS.

    Measured across 43 sessions: all 9,148 blocks in the platform-written Claude Code transcripts
    carry a SIGNATURE and an empty `thinking` string. The only corpus with real thinking text was
    a MANUAL claude.ai session export (61 blocks, 100,665 chars) — and this system reads only
    what the agent platforms write for themselves, never an export a person produced by hand. So
    treat thinking volume as UNAVAILABLE: `asst_think_chars` is recorded when a store happens to
    carry it, but nothing downstream may depend on it. `asst_think_msgs` (incidence) and the
    `unsaid_tok_approx` upper bound are the designed-for signals."""
    if not isinstance(content, list):
        return []
    return [len(b.get("thinking") or "") for b in content
            if isinstance(b, dict) and b.get("type") == "thinking"]


def extract(args):
    rows = []
    n_files = n_lines = n_dup = 0
    seen_hash = set()
    for root in args.roots:
        for path in sorted(glob.glob(os.path.join(root, "*", "*.jsonl"))):
            with open(path, "rb") as fh:
                h = hashlib.sha256(fh.read()).hexdigest()
            if h in seen_hash:
                n_dup += 1
                print(f"  duplicate, skipped: {path}")
                continue
            seen_hash.add(h)
            n_files += 1
            session = os.path.basename(path)[:8]
            seen_req = set()
            for line in open(path, errors="replace"):
                if '"type":"user"' not in line and '"type":"assistant"' not in line:
                    continue
                # A tool_result carries no speech and no reference; it is also where the huge
                # lines are. Skipping it is what keeps this a seconds-long parse.
                if '"tool_result"' in line and '"tool_use"' not in line:
                    continue
                try:
                    o = json.loads(line)
                except Exception:
                    continue
                ts = o.get("timestamp")
                if not ts:
                    continue
                n_lines += 1
                t = pd.Timestamp(ts).timestamp()
                repo = repo_of(o.get("cwd"), args.repo_root)
                root_dir = next((os.path.join(r, repo) for r in args.repo_root
                                 if os.path.isdir(os.path.join(r, repo))), None) if repo else None
                if root_dir is None and repo and o.get("cwd"):
                    # A path we cannot stat (another machine's export). Take the repo segment of
                    # the recorded cwd itself, so file paths still resolve relative to it.
                    c = WORKTREE.sub("", o["cwd"])
                    i = c.find("/" + repo + "/")
                    root_dir = c[:i] + "/" + repo if i >= 0 else (
                        c if c.endswith("/" + repo) else None)
                base = (round(t, 1), session, repo, o.get("gitBranch") or None,
                        bool(o.get("isSidechain")))

                def add(kind, level, ref, n):
                    rows.append(base + (kind, level, ref, float(n)))

                if repo:
                    add("ref", "repo", repo, 1)
                if base[3]:
                    add("ref", "branch", base[3], 1)
                if o.get("attributionSkill"):
                    add("ref", "skill", o["attributionSkill"], 1)
                if o.get("attributionMcpServer"):
                    add("ref", "mcp_server", o["attributionMcpServer"], 1)
                if o.get("attributionMcpTool"):
                    add("ref", "mcp_tool", o["attributionMcpTool"], 1)

                msg = o.get("message") or {}
                content = msg.get("content")
                if o.get("type") == "user":
                    body = text_of(content)
                    if body.strip():
                        add("say", "user_echo" if is_command_echo(body) else "user", "",
                            len(body))
                else:
                    if msg.get("model"):
                        add("ref", "model", msg["model"], 1)
                    said = text_of(content)
                    if said.strip():
                        add("say", "asst", "", len(said))
                    for nchars in think_blocks(content):
                        add("say", "asst_think", "", nchars)   # 0 = not persisted by this store
                    u, rid = msg.get("usage"), o.get("requestId")
                    if u and rid and rid not in seen_req:
                        seen_req.add(rid)
                        add("tok", "out", "", u.get("output_tokens", 0))
                        add("tok", "in_fresh", "", (u.get("input_tokens", 0) +
                                                    u.get("cache_creation_input_tokens", 0)))
                        add("tok", "in_cached", "", u.get("cache_read_input_tokens", 0))

                paths = []
                if isinstance(content, list):
                    for b in content:
                        if not (isinstance(b, dict) and b.get("type") == "tool_use"):
                            continue
                        name, inp = b.get("name"), b.get("input") or {}
                        m = MCP_TOOL.match(name or "")
                        if m:
                            add("ref", "tool", "mcp:" + m["tool"], 1)
                            add("ref", "mcp_server", m["server"], 1)
                            add("ref", "mcp_tool", m["tool"], 1)
                        else:
                            add("ref", "tool", name, 1)
                        if name == "Agent" and inp.get("subagent_type"):
                            add("ref", "agent", inp["subagent_type"], 1)
                        if name == "Skill" and inp.get("skill"):
                            add("ref", "skill", inp["skill"], 1)
                        for k in PATH_INPUTS:
                            if isinstance(inp.get(k), str):
                                paths.append((inp[k], True))     # a tool's file_path IS a file
                        if name == "Bash":
                            verbs, bp = bash_refs(inp.get("command"))
                            for v in verbs:
                                add("ref", "verb", v, 1)
                            paths += [(q, False) for q in bp]
                for p, from_input in paths:
                    rel = rel_within(p, root_dir)
                    if not rel or rel.startswith("."):
                        continue
                    rel = rel.rstrip("/")
                    ext = os.path.splitext(rel)[1].lower()
                    # `cd services/web` names a DIRECTORY. Counted as a file it made the file
                    # level look slower-moving than the directory level — a path token from a
                    # shell command is only a file if it carries a known extension, whereas a
                    # tool's own file_path always is one.
                    is_file = (from_input or ext in EXT_LANG
                               or "." in os.path.basename(rel))
                    d = os.path.dirname(rel) if is_file else rel
                    if is_file:
                        add("ref", "file", rel, 1)
                        if ext in EXT_LANG:
                            add("ref", "lang", EXT_LANG[ext], 1)
                    if d:
                        add("ref", "dir", d, 1)
                        add("ref", "component", "/".join(d.split("/")[:args.component_depth]), 1)

    ev = pd.DataFrame(rows, columns=["t", "session", "repo", "branch", "side", "kind", "level",
                                     "ref", "n"])
    ev["ts"] = pd.to_datetime(ev["t"], unit="s", utc=True)
    for c in ("session", "repo", "branch", "kind", "level", "ref"):
        ev[c] = ev[c].astype("category")
    os.makedirs(args.outdir, exist_ok=True)
    out = os.path.join(args.outdir, "events.parquet")
    ev.to_parquet(out, index=False)
    print(f"{n_files} transcripts ({n_dup} duplicates skipped), {n_lines} lines, "
          f"{len(ev)} observations -> {out}")
    print(ev.groupby(["kind", "level"], observed=True)
            .agg(rows=("n", "size"), total=("n", "sum")).to_string())


# ------------------------------------------------------------------ series helpers

def fmt_hl(hl, bin_h=1.0, max_lag_h=None):
    """A half-life that never crossed 0.5 is reported against the longest lag the series
    CONTAINS, not against the bucket ceiling. A 7-hour session cannot evidence ">4wk" — no pair
    of its bins is a day apart — and printing it did exactly what a silent cap does."""
    if hl is None:
        if max_lag_h is None:
            return "n/a"          # too few observed bins to estimate anything
        return f">{max_lag_h:.0f}h" if max_lag_h < 336 else ">4wk"
    return f"<{bin_h:g}h" if hl < bin_h else f"{hl:.0f}h"


def spark(v, width=72):
    """A series you can read at a glance. Downsampled by mean, never by dropping points."""
    v = np.asarray(pd.Series(v).fillna(0.0), dtype=float)
    if not len(v):
        return ""
    if len(v) > width:
        e = np.linspace(0, len(v), width + 1).astype(int)
        v = np.array([v[a:b].mean() if b > a else 0.0 for a, b in zip(e[:-1], e[1:])])
    hi = v.max()
    if hi <= 0:
        return "·" * len(v)
    bars = "▁▂▃▄▅▆▇█"
    return "".join("·" if x <= 0 else bars[min(7, int(x / hi * 7.999))] for x in v)


def lag_curve(vectors, idx, bin_h, kind):
    """Self-similarity against wall-clock lag, and the lag where it crosses half.

    Wall-clock lag is the right unit precisely BECAUSE state is forward-filled: the question a
    carried value raises is how long it stays true. `composition` uses cosine over the share
    vector; `scalar` uses Pearson correlation of a single channel."""
    if len(idx) < 6:
        return {}, None, None
    if kind == "composition":
        v = vectors / np.where(np.linalg.norm(vectors, axis=1, keepdims=True) == 0, 1,
                               np.linalg.norm(vectors, axis=1, keepdims=True))
        sim = v @ v.T
    else:
        x = vectors.ravel().astype(float)
        x = (x - x.mean()) / (x.std() or 1.0)
        sim = np.outer(x, x)
    lag = np.abs(idx[:, None] - idx[None, :]) * bin_h
    iu = np.triu_indices(len(idx), k=1)
    sim, lag = sim[iu], lag[iu]
    curve = {}
    for lo, hi in zip([0] + LAG_BUCKETS_H[:-1], LAG_BUCKETS_H):
        sel = (lag > lo) & (lag <= hi)
        if sel.sum() >= 8:
            curve[hi] = (float(sim[sel].mean()), int(sel.sum()))
    # Half-life is measured RELATIVE to a FIXED reference lag, not against an absolute 0.5 and
    # not against the nearest lag available.
    #
    # Absolute 0.5 measures the bin's sample size as much as the work: a bin holding few events
    # gives a noisy composition estimate and noise decorrelates instantly, so keld-atlas `verb`
    # crossed 0.5 at >4wk, 168h and 1h for 1h, 15min and 5min bins. Dividing by the nearest lag
    # fixes that but introduces its own artefact, because "nearest" is a DIFFERENT lag per bin
    # size — a finer bin gets a higher baseline, hence a higher target, hence an apparently
    # shorter half-life, and the runs stop being comparable. A fixed reference lag is the only
    # baseline that means the same thing at every bin size.
    ordered = [(h, v) for h, v in sorted(curve.items()) if h >= REF_LAG_H]
    hl = None
    if ordered:
        base = ordered[0][1][0]
        target = 0.5 * base
        prev = (ordered[0][0], base)
        for h, (m, _) in ordered[1:]:
            if m < target <= prev[1]:
                x0, y0 = prev
                hl = x0 + (h - x0) * (y0 - target) / max(y0 - m, 1e-9)
                break
            prev = (h, m)
    return curve, hl, float(lag.max()) if len(lag) else None


def entropy_rows(m):
    tot = m.sum(axis=1, keepdims=True)
    p = np.divide(m, np.where(tot == 0, 1, tot))
    with np.errstate(divide="ignore", invalid="ignore"):
        return -(p * np.where(p > 0, np.log2(np.where(p > 0, p, 1)), 0)).sum(axis=1)


def forward_fill_state(top1, observed):
    """State carries; the age says how far it was carried. NaN age = never yet observed."""
    state, age, last = [], [], None
    for i, obs in enumerate(observed):
        if obs:
            last = (i, top1[i])
            state.append(top1[i])
            age.append(0.0)
        elif last is not None:
            state.append(last[1])
            age.append(float(i - last[0]))
        else:
            state.append(None)
            age.append(np.nan)
    return state, age


# ------------------------------------------------------------------ series

def series(args):
    ev = pd.read_parquet(os.path.join(args.outdir, "events.parquet"))
    if args.repo:
        ev = ev[ev.repo == args.repo]
    if args.since:
        ev = ev[ev.ts >= pd.Timestamp(args.since, tz="UTC")]
    ev = ev.dropna(subset=["repo"])
    if ev.empty:
        sys.exit("no rows — run `extract` first, or widen --repo/--since")
    bin_s = args.bin * 3600.0
    bins_out, metrics_out, levels_out = [], [], []

    for repo, g in sorted(ev.groupby("repo", observed=True), key=lambda kv: -len(kv[1])):
        if len(g) < args.min_rows:
            continue
        t0, t1 = g.t.min(), g.t.max()
        n_bins = int((t1 - t0) // bin_s) + 1
        g = g.assign(b=((g.t - t0) // bin_s).astype(int))
        bin_ts = pd.to_datetime(t0 + np.arange(n_bins) * bin_s, unit="s", utc=True)
        span_d = (t1 - t0) / 86400
        print(f"\n{'='*104}\nREPO {repo}   {len(g)} obs   {g.session.nunique()} sessions   "
              f"{span_d:.1f} days   {n_bins} bins x {args.bin}h")

        allc = np.bincount(g.b.to_numpy(), minlength=n_bins).astype(float)
        act = int((allc > 0).sum())
        print(f"  active bins {act}/{n_bins} = {100*act/n_bins:.1f}% — the rest are gaps: "
              f"rates are zero there, state is carried forward")
        print(f"  all activity   {spark(allc)}")

        # ---------------- reference levels: composition and its turnover
        print(f"\n  REFERENCE LEVELS\n  {'level':10} {'obs':>7} {'vocab':>9} {'act%':>5} "
              f"{'per act':>8} {'breadth':>8} {'entropy':>8} {'turnover':>9} "
              f"{'half-life':>10} {'med life':>10}  top")
        rows_ = g[g.kind == "ref"]
        for level in LEVELS:
            d = rows_[rows_.level == level]
            if d.empty:
                continue
            counts = d.groupby(["b", "ref"], observed=True).n.sum()
            wide = counts.unstack(fill_value=0.0).reindex(range(n_bins), fill_value=0.0)
            vocab_total = wide.shape[1]
            keep = wide.sum().nlargest(VOCAB_CAP).index
            other = wide.drop(columns=keep).sum(axis=1)
            wide = wide[keep]
            if other.any():
                wide["__other__"] = other
            m = wide.to_numpy()
            tot = m.sum(axis=1)
            real_cols = [i for i, c in enumerate(wide.columns) if c != "__other__"]
            observed = tot > 0
            oi = np.flatnonzero(observed)
            top1 = [wide.columns[i] if observed[j] else None
                    for j, i in enumerate(m.argmax(axis=1))]
            state, age = forward_fill_state(top1, observed)
            ent, brd = entropy_rows(m), (m > 0).sum(axis=1)
            shares = np.divide(m, np.where(tot[:, None] == 0, 1, tot[:, None]))
            turn = np.full(n_bins, np.nan)
            for a, b_ in zip(oi[:-1], oi[1:]):
                den = np.linalg.norm(shares[a]) * np.linalg.norm(shares[b_])
                turn[b_] = 1 - float(np.dot(shares[a], shares[b_]) / max(den, 1e-9))
            curve, hl, max_lag = lag_curve(shares[oi][:, real_cols], oi, args.bin,
                                           "composition")
            life = (d.groupby("ref", observed=True).t.agg(lambda s: (s.max()-s.min())/3600.0))
            med_life = float(life.median()) if len(life) else float("nan")

            per_level = pd.DataFrame({
                "repo": repo, "bin_ts": bin_ts, "bin": np.arange(n_bins), "level": level,
                "count": tot, "breadth": brd, "entropy": ent, "turnover": turn,
                "top1": top1, "state": state, "state_age_bins": age,
                "state_age_h": np.array(age) * args.bin})
            metrics_out.append(per_level)
            bins_out.append(counts.reset_index().assign(repo=repo, level=level,
                            bin_ts=lambda x: bin_ts[x.b.to_numpy()]))
            top = ", ".join(d.ref.astype(str).value_counts().head(4).index)
            levels_out.append({"repo": repo, "kind": "ref", "level": level,
                               "obs": float(tot.sum()), "vocab": int(len(wide.columns)), "vocab_total": int(vocab_total),
                               "active_pct": 100*observed.sum()/n_bins,
                               "per_active": float(tot[observed].mean()),
                               "breadth": float(brd[observed].mean()),
                               "entropy": float(ent[observed].mean()),
                               "turnover": float(np.nanmean(turn)),
                               "half_life_h": hl, "max_lag_h": max_lag, "support_bins": int(observed.sum()),
                               "median_lifetime_h": med_life,
                               "curve": json.dumps({str(k): v[0] for k, v in curve.items()}),
                               "top": top})
            vshow = (f"{len(wide.columns)}/{vocab_total}" if vocab_total > VOCAB_CAP
                     else str(vocab_total))
            print(f"  {level:10} {tot.sum():>7.0f} {vshow:>9} "
                  f"{100*observed.sum()/n_bins:>4.0f}% {tot[observed].mean():>8.1f} "
                  f"{brd[observed].mean():>8.1f} {ent[observed].mean():>8.2f} "
                  f"{np.nanmean(turn):>9.2f} {fmt_hl(hl, args.bin, max_lag):>10} "
                  f"{int(observed.sum()):>5} {med_life:>9.0f}h  {top[:40]}")

        # ---------------- speaker channels: how much, how often, in what shape
        say = g[g.kind == "say"]
        tok = g[g.kind == "tok"]
        sp = pd.DataFrame({"repo": repo, "bin_ts": bin_ts, "bin": np.arange(n_bins)})
        for lvl in SAY:
            d = say[say.level == lvl]
            grp = d.groupby("b", observed=True).n
            sp[f"{lvl}_msgs"] = grp.size().reindex(range(n_bins), fill_value=0).to_numpy()
            sp[f"{lvl}_chars"] = grp.sum().reindex(range(n_bins), fill_value=0.0).to_numpy()
            sp[f"{lvl}_chars_med"] = grp.median().reindex(range(n_bins)).to_numpy()
        for lvl in TOK:
            d = tok[tok.level == lvl]
            sp[f"tok_{lvl}"] = (d.groupby("b", observed=True).n.sum()
                                .reindex(range(n_bins), fill_value=0.0).to_numpy())
        u = say[say.level == "user"].sort_values("t")
        # Rapid succession: the gap to the PREVIOUS engineer message, so a burst of "ok" /
        # "continue" is visible as many messages with tiny gaps, and a spec dump as one large
        # message after a long gap. Count and size are kept apart on purpose.
        gaps = u.t.diff()
        sp["user_gap_med_s"] = (gaps.groupby(u.b).median()
                                .reindex(range(n_bins)).to_numpy())
        sp["user_burst_share"] = ((gaps < BURST_S).groupby(u.b).mean()
                                  .reindex(range(n_bins)).to_numpy())
        sp["user_short_share"] = ((u.n < SHORT_CHARS).groupby(u.b).mean()
                                  .reindex(range(n_bins)).to_numpy())
        sp["user_chars_per_msg"] = sp.user_chars / sp.user_msgs.replace(0, np.nan)
        sp["asst_chars_per_msg"] = sp.asst_chars / sp.asst_msgs.replace(0, np.nan)
        sp["leverage_chars"] = sp.asst_chars / sp.user_chars.replace(0, np.nan)
        # Generated but not said, recovered indirectly: output_tokens counts everything the
        # model emitted, the transcript keeps no thinking text, and ~4 chars per token is the
        # standard rough conversion. The residual is therefore thinking PLUS tool-call arguments
        # — a Write with a 400-line body is output too — so it is an upper bound on reasoning,
        # not a measure of it. Labelled `_approx` so it can never be quoted as a measurement.
        sp["said_tok_approx"] = sp.asst_chars / 4.0
        sp["unsaid_tok_approx"] = (sp.tok_out - sp.said_tok_approx).clip(lower=0)
        sp["unsaid_share_approx"] = sp.unsaid_tok_approx / sp.tok_out.replace(0, np.nan)
        metrics_out.append(sp.assign(level="speaker"))

        print(f"\n  SPEAKER CHANNELS\n  {'channel':22} {'total':>12} {'per act bin':>12} "
              f"{'median':>10} {'half-life':>10}  series")
        for col, unit in [("user_msgs", "msgs"), ("user_chars", "chars"),
                          ("user_chars_per_msg", "chars/msg"), ("user_short_share", "share"),
                          ("user_burst_share", "share"), ("user_gap_med_s", "s"),
                          ("user_echo_msgs", "msgs"), ("asst_msgs", "msgs"),
                          ("asst_chars", "chars"), ("asst_think_msgs", "blocks"), ("asst_think_chars", "chars"),
                          ("asst_chars_per_msg", "chars/msg"),
                          ("leverage_chars", "ratio"), ("tok_out", "tok"),
                          ("tok_in_fresh", "tok"), ("tok_in_cached", "tok"),
                          ("unsaid_tok_approx", "tok"), ("unsaid_share_approx", "share")]:
            x = sp[col].to_numpy(dtype=float)
            obs = ~np.isnan(x) & (x != 0) if "share" not in unit else ~np.isnan(x)
            oi = np.flatnonzero(obs)
            _, hl, max_lag = lag_curve(np.nan_to_num(x[oi])[:, None], oi, args.bin,
                                       "scalar")
            tot_s = np.nansum(x) if unit not in ("share", "ratio", "chars/msg", "s") else np.nan
            levels_out.append({"repo": repo, "kind": "speaker", "level": col,
                               "obs": float(len(oi)), "vocab": 0,
                               "active_pct": 100*len(oi)/n_bins,
                               "per_active": float(np.nanmean(x[oi])) if len(oi) else np.nan,
                               "breadth": np.nan, "entropy": np.nan, "turnover": np.nan,
                               "half_life_h": hl, "max_lag_h": max_lag,
                               "support_bins": int(len(oi)),
                               "median_lifetime_h": float(np.nanmedian(x[oi]))
                               if len(oi) else np.nan,
                               "curve": "", "top": unit})
            print(f"  {col:22} {(f'{tot_s:,.0f}' if not np.isnan(tot_s) else '—'):>12} "
                  f"{(np.nanmean(x[oi]) if len(oi) else np.nan):>12.2f} "
                  f"{(np.nanmedian(x[oi]) if len(oi) else np.nan):>10.2f} "
                  f"{fmt_hl(hl, args.bin, max_lag):>10}  {spark(np.nan_to_num(x))}")

        lv = pd.DataFrame([r for r in levels_out if r["repo"] == repo])
        ref_order = (lv[lv.kind == "ref"]
                     .assign(h=lambda d: d.half_life_h.fillna(d.max_lag_h + 1e6))
                     .sort_values("h", ascending=False))
        print("\n  NATURAL FREQUENCY, slowest first (composition half-life):\n    " +
              "  ->  ".join(
                  f"{r.level}({fmt_hl(None if not (r.h < 1e6) else r.h, args.bin, r.max_lag_h)})"
                  for r in ref_order.itertuples() if r.h == r.h) +
              ("   [no estimate: " + ", ".join(
                  lv[(lv.kind == "ref") & lv.half_life_h.isna() & lv.max_lag_h.isna()].level)
               + "]" if lv[(lv.kind == "ref") & lv.half_life_h.isna()
                           & lv.max_lag_h.isna()].size else ""))

        if args.detail:
            for level in LEVELS:
                sel = [m for m in metrics_out
                       if "level" in m and (m.level == level).all() and (m.repo == repo).all()]
                if not sel:
                    continue
                d = sel[-1]
                print(f"\n  --- {level}")
                print(f"      count      {spark(d['count'])}")
                print(f"      turnover   {spark(d['turnover'].fillna(0))}")
                print(f"      state age  {spark(d['state_age_h'].fillna(0))}")
                for w in args.windows:
                    if w >= len(d):
                        print(f"      roll{w:>4}b   skipped — longer than the "
                              f"{len(d)}-bin series")
                        continue
                    r = d["count"].rolling(w, min_periods=w)
                    print(f"      roll{w:>4}b   mean {spark(r.mean())}")
                    print(f"                 med  {spark(r.median())}")

    os.makedirs(args.outdir, exist_ok=True)
    mt = pd.concat(metrics_out, ignore_index=True)
    bn = pd.concat(bins_out, ignore_index=True)
    lv = pd.DataFrame(levels_out)
    for name, df in (("metrics", mt), ("bins", bn), ("levels", lv)):
        p = os.path.join(args.outdir, f"{name}.parquet")
        df.to_parquet(p, index=False)
        print(f"  {name:8} {df.shape} -> {p}")


# ---------------------------------------------------------------- the ladder
#
# Each rung carries the levels whose measured half-life puts them at the same natural frequency,
# and every rung's LOOKBACK and STALENESS BUDGET are read out of levels.parquet rather than
# written here. That is the whole point: the refresh interval is a measurement, and when a repo's
# rhythm differs the ladder follows it without being re-tuned. keld-atlas holds a component for
# ~330h and keld-signal for ~45h; one hard-coded interval would be wrong for one of them.
RUNGS = [
    ("IDENTITY   ", ["repo", "lang", "model", "tool"], 3),
    ("WORKSTREAM ", ["branch", "component"], 4),
    ("WORKING SET", ["dir", "file"], 5),
]
# Carry a rung until it has aged past this fraction of its own half-life, then say so out loud.
STALE_FRACTION = 0.5


def ladder(args):
    lv = pd.read_parquet(os.path.join(args.outdir, "levels.parquet"))
    bn = pd.read_parquet(os.path.join(args.outdir, "bins.parquet"))
    mt = pd.read_parquet(os.path.join(args.outdir, "metrics.parquet"))
    repo = args.repo or lv.loc[lv.kind == "ref", "repo"].value_counts().idxmax()
    lv, bn, mt = lv[lv.repo == repo], bn[bn.repo == repo], mt[mt.repo == repo]
    span_h = (bn.bin_ts.max() - bn.bin_ts.min()).total_seconds() / 3600.0
    at = pd.Timestamp(args.at, tz="UTC") if args.at else bn.bin_ts.max()

    print(f"CONTEXT LADDER — {repo} @ {at:%Y-%m-%d %H:%M}Z")
    print(f"(lookback and staleness per rung are read from the measured half-life, not set here)")
    for name, levels, topk in RUNGS:
        print(f"\n{name}")
        for level in levels:
            row = lv[(lv.kind == "ref") & (lv.level == level)]
            if row.empty:
                continue
            hl = float(row.half_life_h.iloc[0]) if pd.notna(row.half_life_h.iloc[0]) else span_h
            look = max(1.0, min(hl, span_h))
            d = bn[(bn.level == level) & (bn.bin_ts <= at) &
                   (bn.bin_ts >= at - pd.Timedelta(hours=look))]
            seen = mt[(mt.level == level) & (mt.bin_ts <= at) & mt["count"].fillna(0).gt(0)]
            age = ((at - seen.bin_ts.max()).total_seconds() / 3600.0
                   if not seen.empty else float("nan"))
            if d.empty:
                print(f"  {level:10} (nothing in the last {look:.0f}h)")
                continue
            tot = d.groupby("ref", observed=True).n.sum().sort_values(ascending=False)
            head = tot.head(topk)
            shown = ", ".join(f"{k} {100*v/tot.sum():.0f}%" for k, v in head.items())
            # An identifier is never truncated — a path cut short is a FALSE path — so whole
            # terms are dropped and the count of dropped terms is stated (AGENTS.md).
            more = f"  (+{len(tot)-len(head)} more not shown)" if len(tot) > len(head) else ""
            stale = ""
            if not math.isnan(age) and age > STALE_FRACTION * hl:
                stale = (f"   [CARRIED {age:.0f}h — past {STALE_FRACTION:g}x its {hl:.0f}h "
                         f"half-life, treat as aged]")
            elif not math.isnan(age) and age > 0:
                stale = f"   [as of {age:.0f}h ago]"
            print(f"  {level:10} {shown}{more}{stale}")
            print(f"  {'':10} └ half-life {hl:.0f}h -> lookback {look:.0f}h, "
                  f"refresh when older than {STALE_FRACTION*hl:.0f}h")

    look_h = args.tempo_hours
    win = mt[(mt.level == "speaker") & (mt.bin_ts <= at) &
             (mt.bin_ts >= at - pd.Timedelta(hours=look_h))]
    base = mt[mt.level == "speaker"]
    if not win.empty:
        sums = win[["user_msgs", "user_chars", "asst_msgs", "asst_chars", "tok_out"]].sum()
        # A per-bin median is the wrong yardstick for a multi-bin window, so the baseline is the
        # same window length taken across the whole series.
        nb = max(1, len(win))
        def rel(col, value):
            med = base[col].replace(0, np.nan).median()
            if pd.isna(med) or med == 0:
                return f"{value:.0f}"
            return f"{value:.0f} ({value/(med*nb):.1f}x typical)"
        cpm = sums.user_chars / sums.user_msgs if sums.user_msgs else float("nan")
        short = win.user_short_share.mean()
        burst = win.user_burst_share.mean()
        print(f"\nTEMPO      (half-life ~1h — refresh every window; last {look_h:g}h)")
        print(f"  engineer   {rel('user_msgs', sums.user_msgs)} messages, "
              f"{cpm:.0f} chars each, "
              f"{'?' if pd.isna(short) else f'{100*short:.0f}%'} short, "
              f"{'?' if pd.isna(burst) else f'{100*burst:.0f}%'} in bursts")
        print(f"  assistant  {rel('asst_msgs', sums.asst_msgs)} messages, "
              f"{rel('tok_out', sums.tok_out)} output tokens")


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    e = sub.add_parser("extract")
    e.add_argument("--roots", nargs="+", default=CLAUDE_ROOTS)
    e.add_argument("--repo-root", nargs="+", default=[REPO_ROOT])
    e.add_argument("--component-depth", type=int, default=3)
    e.add_argument("--outdir", default=OUTDIR)
    e.set_defaults(fn=extract)
    s = sub.add_parser("series")
    s.add_argument("--outdir", default=OUTDIR)
    s.add_argument("--repo", default=None)
    s.add_argument("--bin", type=float, default=1.0, help="bin size in HOURS (default 1)")
    s.add_argument("--since", default=None, help="ISO date, e.g. 2026-07-01")
    s.add_argument("--min-rows", type=int, default=500)
    s.add_argument("--windows", type=int, nargs="+", default=[6, 24, 168])
    s.add_argument("--detail", action="store_true")
    s.set_defaults(fn=series)
    d = sub.add_parser("ladder")
    d.add_argument("--outdir", default=OUTDIR)
    d.add_argument("--repo", default=None)
    d.add_argument("--at", default=None, help="ISO timestamp; default = last active bin")
    d.add_argument("--tempo-hours", type=float, default=1.0)
    d.set_defaults(fn=ladder)
    args = ap.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
