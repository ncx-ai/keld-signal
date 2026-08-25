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
import argparse
import concurrent.futures, collections, glob, hashlib, json, math, os, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
from app.analysis.text import clip, is_command_echo, text_of  # noqa: E402
from app.analysis.shell import bash_refs  # noqa: E402
from app.analysis.workspace import resolve_workspace  # noqa: E402
from app.analysis.reconcile import reconcile  # noqa: E402
from app.analysis.transcript import iter_turns  # noqa: E402
from app.analysis.levels import LEVELS, events_for_turns  # noqa: E402
# The SHIPPED analysis path -- what `context`/`contexts` now project (see `digest`/`executive`
# below). Everything above this line is the study's OWN pandas pipeline, kept for the other
# subcommands (`extract`, `series`, `synopsis`, `at`, `episodes`, `window`) and for the
# frame-derived "vs repo history" lift section `digest`/`executive` accept alongside the payload.
from app.analysis.analyze import PromptNotFound, StoreBehind, WindowExpired, analyze_window  # noqa: E402
from app.analysis.dynamics import DEFAULT_SIZER  # noqa: E402
from app.analysis.ingest import ingest_file, is_current  # noqa: E402
from app.analysis.store import open_store  # noqa: E402
from app.analysis.workstreams import ALLOCATION  # noqa: E402

import numpy as np
import pandas as pd

import yaml


class Para(str):
    """A string YAML should emit as a literal block, so prose stays readable."""


yaml.add_representer(Para, lambda d, x: d.represent_scalar("tag:yaml.org,2002:str", str(x),
                                                           style="|"),
                     Dumper=yaml.SafeDumper)

OUTDIR = "/tmp/refseries"
CLAUDE_ROOTS = [os.path.expanduser("~/.claude/projects")]
REPO_ROOT = os.path.expanduser("~/keld")

# The ONE level that reads message text. Every other level above comes from tool-call inputs, and
# that is exactly why attribution stalls at ~37%: a customer, a supplier or an initiative is only
# ever spoken. See sidecar/app/analysis/terms.py for the three measured decisions behind how it
# reads.
#
# spaCy is loaded once, lazily, and only if it is installed — the frames must still build on a
# machine without it, with the level simply absent rather than the run failing. KELD_TERMS=0
# switches it off (it roughly doubles extract time on a large corpus).
_NLP = ["unset"]


def term_nlp():
    if _NLP[0] == "unset":
        _NLP[0] = None
        if os.environ.get("KELD_TERMS", "1") != "0":
            try:
                import spacy
                m = spacy.load("en_core_web_sm",
                               exclude=["tagger", "parser", "lemmatizer", "attribute_ruler"])
                m.max_length = 20_000_000
                _NLP[0] = m
            except Exception as e:          # not installed, or no model: degrade, never fail
                print(f"  terms: spaCy unavailable ({e.__class__.__name__}), level disabled",
                      file=sys.stderr)
    return _NLP[0]


# LEVELS, MCP_TOOL, URL_HOST/SSH_HOST/services_in moved to app.analysis.levels — the
# classification vocabulary and helpers for events_for_turns, imported above.
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


ROLL = ["1h", "6h", "24h", "168h"]


def _roll(df, keys, col, window, how):
    """A trailing time-window statistic, aligned back onto the caller's index.

    Written out rather than expressed as groupby().apply(rolling(...)) because that returns a
    Series with several groups and a DataFrame with exactly one, so scoping the frames to a
    single transcript broke an assignment that worked fine across a whole repository."""
    out = []
    for _, g in df.groupby(keys, sort=False):
        sr = getattr(g.set_index("bin")[col].rolling(window), how)()
        sr.index = g.index
        out.append(sr)
    return pd.concat(out).reindex(df.index) if out else pd.Series(index=df.index, dtype=float)


def build_frames(ev, bin_h, outdir, baseline_ev=None, scope_map=None):
    """Materialise the series themselves, with the rolling statistics as COLUMNS.

    Three time-indexed frames, so a point-in-time question is an INDEXING operation rather than a
    computation: take the last row at or before the moment you care about and read it.

      refs.parquet     one row per (repo, level, ref, bin) actually observed, carrying that
                       reference's count, its share of its level, rolling counts and shares over
                       1h/6h/24h/168h, an EWMA, its causal baseline share, the lift of each
                       window against that baseline, its rank within the level, and the gap since
                       it was last seen.
      levels.parquet   one row per (repo, level, bin): total count, breadth, entropy, turnover,
                       rolling mean and median of the count, and pace against its own habit.
      speaker.parquet  one row per (repo, bin): every speaker channel with rolling sums and a
                       z-score against that person's own history.

    EVERY statistic is CAUSAL — expanding or trailing-window only. A baseline computed over the
    whole corpus would let a row know its own future, and indexing a moment six weeks back would
    silently describe it using work that had not happened yet.

    COMPOSITION AND BASELINE ARE DIFFERENT SCOPES, and that is the default. Composition — share,
    breadth, entropy, turnover, the rolling windows — is the entity's own: for a session, what
    this conversation is doing. The baseline that `lift` divides by comes from `baseline_ev`,
    normally the whole history of the repositories the entity touches. Scoped to a session it has
    to: at 74 minutes old a transcript's own history IS the window, so every lift collapsed to
    x1.0 and the column carried no information, while the repository's history at the same moment
    put the branch at x33.6 and the file at x64.3. "Unusual" is only meaningful against a
    yardstick longer than the thing being measured.
    """
    ev = ev[ev.kind == "ref"].copy()
    freq = pd.Timedelta(hours=bin_h)
    ev["bin"] = ev.ts.dt.floor(freq)
    for c in ("repo", "level", "ref"):
        ev[c] = ev[c].astype(str)

    refs = (ev.groupby(["repo", "level", "ref", "bin"], as_index=False).n.sum()
            .sort_values("bin").reset_index(drop=True))
    lvls = (refs.groupby(["repo", "level", "bin"], as_index=False).n.sum()
            .rename(columns={"n": "lvl_n"}).sort_values("bin").reset_index(drop=True))

    def roll_sum(df, keys, col, out):
        for w in ROLL:
            df[f"{out}_{w}"] = _roll(df, keys, col, w, "sum")
        return df

    lvls = roll_sum(lvls, ["repo", "level"], "lvl_n", "lvl")
    lvls["cum_lvl"] = lvls.groupby(["repo", "level"]).lvl_n.cumsum()
    refs = roll_sum(refs, ["repo", "level", "ref"], "n", "n")
    refs["cum_n"] = refs.groupby(["repo", "level", "ref"]).n.cumsum()
    refs = refs.merge(lvls[["repo", "level", "bin", "lvl_n", "cum_lvl"] +
                          [f"lvl_{w}" for w in ROLL]], on=["repo", "level", "bin"], how="left")
    refs = refs.sort_values(["repo", "level", "ref", "bin"]).reset_index(drop=True)

    refs["share"] = refs.n / refs.lvl_n
    refs["base_share"] = refs.cum_n / refs.cum_lvl          # causal: history up to this bin
    refs["base_scope"] = "entity"
    if baseline_ev is not None and scope_map:
        # PER ENTITY, against the repositories that entity actually touches. Pooling every repo
        # into one baseline would judge keld-atlas's Go against keld-signal's Go-heavy history,
        # and pooling nothing would judge a 74-minute session against itself.
        b_all = baseline_ev[baseline_ev.kind == "ref"].copy()
        b_all["bin"] = b_all.ts.dt.floor(freq)
        for c in ("level", "ref", "repo"):
            b_all[c] = b_all[c].astype(str)
        parts, hits, miss, base_tables = [], 0, 0, []
        for entity, sub in refs.groupby("repo", sort=False):
            scope = scope_map.get(entity) or {entity}
            b = b_all[b_all.repo.isin(scope)]
            if b.empty:
                parts.append(sub)
                miss += len(sub)
                continue
            br = (b.groupby(["level", "ref", "bin"], as_index=False).n.sum()
                  .sort_values("bin").reset_index(drop=True))
            bl = (br.groupby(["level", "bin"], as_index=False).n.sum()
                  .rename(columns={"n": "b_lvl"}).sort_values("bin").reset_index(drop=True))
            br["b_cum"] = br.groupby(["level", "ref"]).n.cumsum()
            bl["b_cum_lvl"] = bl.groupby("level").b_lvl.cumsum()
            br = br.merge(bl[["level", "bin", "b_cum_lvl"]], on=["level", "bin"], how="left")
            br["base_share_scope"] = br.b_cum / br.b_cum_lvl
            # As-of: a bin takes the newest baseline at or before it, never a later one.
            m = pd.merge_asof(sub.sort_values("bin"),
                              br[["level", "ref", "bin", "base_share_scope"]].sort_values("bin"),
                              on="bin", by=["level", "ref"], direction="backward")
            hit = m.base_share_scope.notna()
            m.loc[hit, "base_share"] = m.loc[hit, "base_share_scope"]
            m.loc[hit, "base_scope"] = ",".join(sorted(scope))
            base_tables.append(br.assign(entity=entity)[["entity", "level", "ref", "bin",
                                                         "base_share_scope"]])
            hits += int(hit.sum())
            miss += int((~hit).sum())
            parts.append(m.drop(columns=["base_share_scope"]))
        refs = pd.concat(parts, ignore_index=True)
        if base_tables:
            bt = pd.concat(base_tables, ignore_index=True)
            bp = os.path.join(outdir, "baseline.parquet")
            bt.to_parquet(bp, index=False)
            print(f"  baseline  {bt.shape} -> {bp}")
        print(f"  baseline: {hits} rows against their entity's own scope, {miss} fell back to "
              f"the entity's history (a reference the scope had not seen at that bin)")
        refs = refs.sort_values(["repo", "level", "ref", "bin"]).reset_index(drop=True)
    for w in ROLL:
        refs[f"share_{w}"] = refs[f"n_{w}"] / refs[f"lvl_{w}"]
        refs[f"lift_{w}"] = refs[f"share_{w}"] / refs.base_share
    if "scope_repo" in ev.columns:
        # Which checkout a reference belongs to. A path maps to exactly one repository, so the
        # modal value is exact for the path levels; it is meaningless for `tool` or `verb`, which
        # is why only the path levels surface it.
        owner = (ev.dropna(subset=["scope_repo"])
                 .groupby(["repo", "level", "ref"], observed=True).scope_repo
                 .agg(lambda x: x.mode().iat[0] if len(x.mode()) else None)
                 .rename("owner").reset_index())
        refs = refs.merge(owner, on=["repo", "level", "ref"], how="left")
    refs["ewm_share"] = (refs.groupby(["repo", "level", "ref"], group_keys=False)
                         .share.apply(lambda x: x.ewm(halflife=4, adjust=False).mean()))
    refs["rank_1h"] = (refs.groupby(["repo", "level", "bin"]).share_1h
                       .rank(ascending=False, method="min"))
    refs["gap_h"] = (refs.groupby(["repo", "level", "ref"]).bin
                     .diff().dt.total_seconds() / 3600.0)
    refs["trend_6h"] = refs.share_1h - refs.share_6h        # rising if the last hour outruns 6h

    # level-wide shape
    def shape(g):
        p = g.n / g.n.sum()
        return pd.Series({"breadth": len(g),
                          "entropy": float(abs(-(p * np.log2(p)).sum()))})
    sh = refs.groupby(["repo", "level", "bin"]).apply(shape, include_groups=False).reset_index()
    lvls = lvls.merge(sh, on=["repo", "level", "bin"], how="left")
    for w in ROLL:
        lvls[f"mean_{w}"] = _roll(lvls, ["repo", "level"], "lvl_n", w, "mean")
        lvls[f"med_{w}"] = _roll(lvls, ["repo", "level"], "lvl_n", w, "median")
    lvls["gap_h"] = (lvls.groupby(["repo", "level"]).bin.diff().dt.total_seconds() / 3600.0)
    lvls["typ_gap_h"] = (lvls.groupby(["repo", "level"], group_keys=False)
                         .gap_h.apply(lambda x: x.expanding(min_periods=3).median()))
    lvls["pace"] = lvls.typ_gap_h / lvls.gap_h              # >1 = denser than its habit
    # turnover against the previous observed bin, from the share vectors
    piv = {}
    for (repo, level), g in refs.groupby(["repo", "level"]):
        w = g.pivot_table(index="bin", columns="ref", values="share", fill_value=0.0)
        a = w.to_numpy()
        nrm = np.linalg.norm(a, axis=1)
        cos = np.full(len(a), np.nan)
        if len(a) > 1:
            dot = (a[1:] * a[:-1]).sum(axis=1)
            den = np.maximum(nrm[1:] * nrm[:-1], 1e-12)
            cos[1:] = 1 - dot / den
        piv[(repo, level)] = pd.DataFrame({"repo": repo, "level": level, "bin": w.index,
                                           "turnover": cos})
    lvls = lvls.merge(pd.concat(piv.values(), ignore_index=True),
                      on=["repo", "level", "bin"], how="left")

    for name, df in (("refs", refs), ("levels", lvls)):
        path = os.path.join(outdir, f"{name}.parquet")
        df.to_parquet(path, index=False)
        print(f"  {name:9} {df.shape} -> {path}")
    return refs, lvls


def build_speaker_frame(metrics, bin_h, outdir):
    """The speaker channels, with rolling sums and a causal z-score per channel."""
    sp = metrics[metrics.level == "speaker"].copy()
    # `metrics` already carries an integer `bin` ordinal; the frames key on the TIMESTAMP, so the
    # ordinal is dropped rather than shadowed.
    sp = (sp.drop(columns=[c for c in ("bin", "level") if c in sp.columns])
          .rename(columns={"bin_ts": "bin"}).sort_values(["repo", "bin"]))
    chans = [c for c in sp.columns
             if c not in ("repo", "bin", "bin_ts", "level")
             and pd.api.types.is_numeric_dtype(sp[c]) and sp[c].notna().any()]
    sp = sp.reset_index(drop=True)
    # A count rolls up by SUM; a ratio does not. Summing `user_short_share` over four bins gave
    # 1.50 — a "share" above one — and summing `user_chars_per_msg` over a day gave 33,089
    # characters per message. Ratios and per-item averages roll up by MEAN.
    RATIO = ("share", "per_msg", "gap", "leverage", "_med", "pace")
    for c in chans:
        how = "mean" if any(k in c for k in RATIO) else "sum"
        for w in ROLL:
            sp[f"{c}_{w}"] = _roll(sp, ["repo"], c, w, how)
        # Causal z-score: expanding mean and deviation, so a row never sees its own future.
        m = sp.groupby("repo", group_keys=False)[c].apply(
            lambda x: x.expanding(min_periods=8).mean())
        sd = sp.groupby("repo", group_keys=False)[c].apply(
            lambda x: x.expanding(min_periods=8).std())
        sp[f"{c}_z"] = (sp[c] - m) / sd.replace(0, np.nan)
    path = os.path.join(outdir, "speaker.parquet")
    sp.to_parquet(path, index=False)
    print(f"  speaker   {sp.shape} -> {path}")
    return sp


def at_view(args):
    """Index the frames at a moment and describe it. No statistics are computed here."""
    refs = pd.read_parquet(os.path.join(args.outdir, "refs.parquet"))
    lvls = pd.read_parquet(os.path.join(args.outdir, "levels.parquet"))
    spk = pd.read_parquet(os.path.join(args.outdir, "speaker.parquet"))
    repo = args.repo or refs.repo.value_counts().idxmax()
    when = (pd.Timestamp(args.at, tz="UTC") if args.at
            else refs[refs.repo == repo].bin.max())
    refs = refs[(refs.repo == repo) & (refs.bin <= when)]
    lvls = lvls[(lvls.repo == repo) & (lvls.bin <= when)]
    spk = spk[(spk.repo == repo) & (spk.bin <= when)]
    if refs.empty:
        sys.exit(f"no rows for {repo} at or before {when}")

    print(f"{repo} indexed at {when:%Y-%m-%d %H:%M}Z   "
          f"(rolling stats are columns in refs/levels/speaker.parquet; "
          f"every one is causal — trailing windows and expanding baselines only)")
    print(f"\n{'level':10} {'last':>7} {'n/1h':>6} {'n/24h':>6} {'breadth':>8} {'entropy':>8} "
          f"{'turnover':>9} {'pace':>6}   top references by share over the last {args.window}")
    for level in LEVELS:
        L = lvls[lvls.level == level]
        if L.empty:
            continue
        row = L.iloc[-1]
        age = (when - row.bin).total_seconds() / 3600.0
        R = refs[(refs.level == level) & (refs.bin == row.bin)]
        col, lift = f"share_{args.window}", f"lift_{args.window}"
        top = R.sort_values(col, ascending=False).head(args.topk)
        desc = ", ".join(
            f"{r.ref} {100*getattr(r, col):.0f}% (x{getattr(r, lift):.1f}"
            f"{' ↑' if r.trend_6h > 0.02 else ' ↓' if r.trend_6h < -0.02 else ''})"
            for r in top.itertuples())
        hidden = len(R) - len(top)
        if hidden:
            desc += f"  (+{hidden} more)"
        print(f"{level:10} {age:6.1f}h {row.get('lvl_1h', float('nan')):6.0f} "
              f"{row.get('lvl_24h', float('nan')):6.0f} {row.breadth:8.0f} "
              f"{row.entropy:8.2f} {row.turnover:9.2f} "
              f"{(row.pace if row.pace == row.pace else float('nan')):6.1f}   {desc}")

    if not spk.empty:
        r = spk.iloc[-1]
        print(f"\nspeaker channels at the same index (z = against this person's own history)")
        for c in ("user_msgs", "user_chars_per_msg", "user_short_share", "user_burst_share",
                  "asst_msgs", "tok_out", "unsaid_share_approx"):
            if c in spk.columns:
                print(f"  {c:22} now {r[c]:10.2f}   1h {r.get(c + '_1h', float('nan')):10.2f}"
                      f"   24h {r.get(c + '_24h', float('nan')):10.2f}"
                      f"   z {r.get(c + '_z', float('nan')):6.2f}")


PATH_LEVELS = ("component", "dir", "file", "ext", "lang")
RUNG_BAND = {"TOOLCHAIN": "the class of tooling used, from the programs invoked",
             "SERVICES": "what the work reached out to, from hosts named in tool inputs",
             "IDENTITY": "half-life >4wk — effectively constant",
             "TOOLING": "half-life >4wk for the mix, though its events are high-frequency",
             "BRANCH & SUBSYSTEM": "half-life 200-380h — days to weeks",
             "WORKING SET": "half-life 5-24h — hours"}


def episodes(refs, entity, watch=("branch", "component"), debounce=2, max_gap_h=1.0,
             max_span_h=6.0):
    """Windows aligned to EPISODES of work rather than to the wall clock.

    An hourly tiling splits work at an arbitrary mark. Measured on one transcript: a branch switch
    at :45 gives the incoming branch 15 minutes of a 60-minute window, so the outgoing branch
    outvotes it and the hourly summary names the WRONG branch for that hour — not a coarser truth,
    an inverted one. A 15-minute stride finds 6 branches where hourly tiling finds 5, so an entire
    branch was invisible. But emitting overlapping windows instead trades that for redundancy and
    for windows that are no longer independent samples. So the stride is used to find the
    BOUNDARIES, and the windows are cut there.

    A boundary is one of three things, and each carries its reason:

      a persistent change of state — the top reference of a watched level changes and STAYS
        changed for `debounce` bins. Without the debounce this is groupby(top1), and a single-bin
        excursion — a stray command against another branch — shatters an episode.
      a long idle — `max_gap_h` between consecutive active bins. Someone stopped and came back.
      the length cap — `max_span_h`, so an unbroken day on one branch is still divided.

    A level with NO row in a bin is missing evidence, not a change: the carried state stands. The
    episodes partition the active bins exactly — every bin in one, no bin in two.
    """
    d = refs[refs.repo == entity]
    if d.empty:
        return []
    bins = sorted(pd.unique(d.bin))
    # top reference per (level, bin), from the counts — the same rule the tables use.
    top = {}
    for lvl in watch:
        sub = d[d.level == lvl]
        if sub.empty:
            continue
        pick = sub.sort_values("n").groupby("bin").tail(1)
        top[lvl] = dict(zip(pick.bin, pick.ref))
    carried, states = {}, []
    for b in bins:
        for lvl in watch:
            v = top.get(lvl, {}).get(b)
            if v is not None:
                carried[lvl] = v
        states.append(tuple(carried.get(l) for l in watch))

    events = d.groupby("bin").n.sum().to_dict()
    out, start_i, pending, run, run_start = [], 0, None, 0, None
    open_reason = ["start of the transcript"]

    def close(end_i, why_next, next_start_i):
        """Emit bins[start_i:end_i]; end is EXCLUSIVE. `why_next` opens the NEXT episode."""
        seg = bins[start_i:end_i]
        if not seg:
            return next_start_i
        out.append({"start": seg[0], "end": bins[end_i] if end_i < len(bins)
                    else seg[-1] + (seg[-1] - seg[-2] if len(seg) > 1 else pd.Timedelta("15min")),
                    "bins": len(seg), "events": float(sum(events.get(b, 0.0) for b in seg)),
                    "state": {l: v for l, v in zip(watch, states[start_i]) if v is not None},
                    "reason": open_reason[0]})
        open_reason[0] = why_next
        return next_start_i

    def flush(i):
        """Close an unconfirmed run that has reached a boundary anyway.

        A change needs `debounce` bins to be believed MID-STREAM, where a one-bin excursion is a
        stray command. But a run that is still pending when the work stops — an idle gap or the
        length cap — is not an excursion, it is the end of the stretch, and absorbing it made an
        episode carry a state its own last bin contradicted: 16:30–18:30 was labelled
        feat/custom-pass-label-rules while its final bin was on main.
        """
        nonlocal start_i, pending, run, run_start
        if pending is not None and run_start is not None and states[i - 1] == pending:
            start_i = close(run_start, "state changed", run_start)
            pending, run, run_start = None, 0, None

    for i in range(1, len(bins)):
        gap_h = (bins[i] - bins[i - 1]).total_seconds() / 3600.0
        span_h = (bins[i] - bins[start_i]).total_seconds() / 3600.0
        if gap_h > max_gap_h:
            flush(i)
            start_i = close(i, f"resumed after {gap_h:.1f}h idle", i)
            pending, run, run_start = None, 0, None
            continue
        if states[i] != states[start_i]:
            if states[i] == pending:
                run += 1
            else:
                pending, run, run_start = states[i], 1, i
            if run >= debounce:
                start_i = close(run_start, "state changed", run_start)
                pending, run, run_start = None, 0, None
                continue
        else:
            pending, run, run_start = None, 0, None
        if span_h >= max_span_h:
            flush(i)
            start_i = close(i, f"reached the {max_span_h:g}h cap", i)
            pending, run, run_start = None, 0, None
    flush(len(bins))
    close(len(bins), "", len(bins))
    return out


def characterize(refs, lvls, spk, entity, start, end, topk, usual=0.05, base=None):
    """The ground-truth statistics in play ACROSS a window, as a plain dict.

    This is the context an LLM is given when it is asked a question about that transcript window.
    It answers what the work was on, how concentrated it was, what was unusual about it, what
    normally present thing was missing, and how the two participants were behaving — in counts,
    from tool-call inputs and per-line metadata. No message text goes in, and nothing after
    `end` is consulted, so a window can be characterised as it was seen at the time.
    """
    # [start, end): the bin AT the window start belongs to the window — bins are floored
    # timestamps, so excluding it silently drops a whole bin of work.
    R = refs[(refs.repo == entity) & (refs.bin >= start) & (refs.bin < end)]
    L = lvls[(lvls.repo == entity) & (lvls.bin >= start) & (lvls.bin < end)]
    prior = refs[(refs.repo == entity) & (refs.bin < start)]
    mid = start + (end - start) / 2

    out = {
        "meta": {
            "what": "ground-truth statistics in play across the transcript window below",
            "derived_from": "tool-call inputs and per-line metadata (cwd, gitBranch, model) — "
                            "never message text",
            "causality": "every figure uses only observations in [window.start, window.end) "
                         "and history before it; nothing later is consulted",
            "how_to_read": {
                "share": "this reference's share of its level's events in the window",
                "lift": "share in the window divided by the same reference's share across the "
                        "whole prior history of the repositories this entity touches; >1 means "
                        "unusually prominent right now, ~1 means business as usual",
                "trend": "second half of the window against the first half",
                "turnover": "1 - cosine similarity between consecutive bins' share vectors; "
                            "0 = same mix as the bin before, 1 = no overlap",
                "entropy": "bits over the level's share vector; 0 = one reference, higher = "
                           "more scattered",
                "absent_but_usual": "references that normally take >=" + f"{usual:.0%}" +
                                    " of this level and did not appear in this window at all",
            },
        },
        "window": {
            "entity": str(entity),
            "entity_kind": ("transcript session" if len(str(entity)) <= 12
                            and not str(entity).startswith("keld") else "repository"),
            "start": start.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "end": end.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "duration_h": round((end - start).total_seconds() / 3600.0, 2),
            "active_bins": int(R.bin.nunique()),
            "reference_events": int(R.n.sum()),
            "units": "reference_events and every `events` field below count tool-call "
                     "references, not identifiers of any kind",
        },
        "rungs": {},
    }
    if R.empty:
        out["window"]["note"] = "no reference observations in this window"
        return out

    # Stated as facts in the header, not left to be read out of a distribution further down.
    cwd_repo = R[R.level == "workspace"]
    if not cwd_repo.empty:
        sh = cwd_repo.groupby("ref").n.sum()
        sh = (sh / sh.sum()).sort_values(ascending=False)
        out["window"]["workspace_of_cwd"] = (str(sh.index[0]) if len(sh) == 1 else
                                             [{"workspace": str(k), "share": round(float(v), 3)}
                                              for k, v in sh.items()])
        vcs = R[R.level == "vcs"]
        if not vcs.empty:
            vs = vcs.groupby("ref").n.sum().sort_values(ascending=False)
            out["window"]["version_control"] = str(vs.index[0])
    if "owner" in R.columns:
        touched = sorted(R[R.level.isin(PATH_LEVELS)].owner.dropna().unique())
        if touched:
            out["window"]["workspaces_whose_files_were_touched"] = [str(t) for t in touched]
    scopes = sorted({v for v in R.base_scope.dropna().unique() if v != "entity"})
    if scopes:
        out["window"]["lift_baseline_scope"] = sorted(
            {r for v in scopes for r in str(v).split(",")})
    if not cwd_repo.empty and "owner" in R.columns:
        touched = set(R[R.level.isin(PATH_LEVELS)].owner.dropna().astype(str))
        if len(touched - {str(sh.index[0])}) > 0:
            out["window"]["note"] = (
                "this window touched files in more than one workspace; workspace_of_cwd is where "
                "the session was running, workspaces_whose_files_were_touched is where the work "
                "landed")

    for rung, levels in LADDER_RUNGS:
        block = {"band": RUNG_BAND.get(rung, ""), "levels": {}}
        for level in levels:
            W = R[R.level == level]
            if W.empty:
                continue
            tot = W.n.sum()
            agg = W.groupby("ref", as_index=False).n.sum()
            agg["share"] = agg.n / tot
            cols = ["base_share", "gap_h", "n_24h"] + (["owner"] if "owner" in W.columns else [])
            last = W.sort_values("bin").groupby("ref").tail(1).set_index("ref")[cols]
            agg = agg.join(last, on="ref").sort_values("share", ascending=False)
            first_half = W[W.bin <= mid].groupby("ref").n.sum()
            second_half = W[W.bin > mid].groupby("ref").n.sum()
            f_tot, s_tot = max(first_half.sum(), 1), max(second_half.sum(), 1)

            def trend(ref):
                d = second_half.get(ref, 0) / s_tot - first_half.get(ref, 0) / f_tot
                return "rising" if d > 0.05 else "falling" if d < -0.05 else "flat"

            p = agg.share.to_numpy()
            Ls = L[L.level == level]
            entry = {
                "events": int(tot),
                "distinct_references": int(len(agg)),
                "entropy_bits": round(float(abs(-(p * np.log2(np.where(p > 0, p, 1))).sum())), 2),
                "turnover": (round(float(Ls.turnover.mean()), 2)
                             if not Ls.turnover.dropna().empty else None),
                "top": [],
            }
            for r in agg.head(topk).itertuples():
                item = {"ref": r.ref, "share": round(float(r.share), 3),
                        "events": int(r.n), "trend": trend(r.ref)}
                if level in PATH_LEVELS and getattr(r, "owner", None):
                    item["repo"] = str(r.owner)
                if r.base_share == r.base_share and r.base_share > 0:
                    item["lift"] = round(float(r.share / r.base_share), 1)
                if r.gap_h == r.gap_h:
                    item["hours_since_previously_seen"] = round(float(r.gap_h), 1)
                entry["top"].append(item)
            if len(agg) > topk:
                rest = agg.iloc[topk:]
                # Dropping must be visible: the tail is named as a tail, with its own share.
                entry["remainder"] = {"references": int(len(rest)),
                                      "share": round(float(rest.share.sum()), 3)}
            # From the baseline scope's own vocabulary as of the window start, not from the
            # entity's prior rows: a reference this session never touched has no row of its own
            # to be missing from, and it is exactly the interesting case.
            src = None
            if base is not None and not base.empty:
                b = base[(base.entity == entity) & (base.level == level) & (base.bin < end)]
                if not b.empty:
                    src = (b.sort_values("bin").groupby("ref").tail(1)
                           .set_index("ref").base_share_scope.dropna())
            elif not prior.empty:
                pl = prior[prior.level == level]
                if not pl.empty:
                    src = (pl.sort_values("bin").groupby("ref").tail(1)
                           .set_index("ref").base_share.dropna())
            if src is not None:
                gone = src[(src >= usual) & (~src.index.isin(set(agg.ref)))]
                if len(gone):
                    entry["absent_but_usual"] = [
                        {"ref": k, "usual_share": round(float(v), 3)}
                        for k, v in gone.sort_values(ascending=False).head(4).items()]
            block["levels"][level] = entry
        if block["levels"]:
            out["rungs"][rung.lower().replace(" & ", "_and_").replace(" ", "_")] = block

    S = spk[(spk.repo == entity) & (spk.bin >= start) & (spk.bin < end)]
    B = spk[spk.repo == entity]
    if not S.empty:
        def vs(col, value):
            med = B[col].replace(0, np.nan).median()
            if pd.isna(med) or med == 0 or pd.isna(value):
                return None
            return round(float(value / (med * len(S))), 2)
        msgs, chars = S.user_msgs.sum(), S.user_chars.sum()
        out["tempo"] = {
            "engineer_messages": int(msgs),
            "engineer_chars_per_message": (round(float(chars / msgs)) if msgs else None),
            "engineer_short_message_share": (round(float(S.user_short_share.mean()), 2)
                                             if S.user_short_share.notna().any() else None),
            "engineer_burst_share": (round(float(S.user_burst_share.mean()), 2)
                                     if S.user_burst_share.notna().any() else None),
            "assistant_messages": int(S.asst_msgs.sum()),
            "assistant_output_tokens": int(S.tok_out.sum()),
            "vs_own_median": {"engineer_messages": vs("user_msgs", msgs),
                              "assistant_messages": vs("asst_msgs", S.asst_msgs.sum()),
                              "output_tokens": vs("tok_out", S.tok_out.sum())},
        }
        out["tempo"]["reading"] = (
            "few engineer messages against many assistant messages and high output means work "
            "proceeding unattended; many short engineer messages in bursts means close steering")
    return out


# ------------------------------------------------------------------ payload projection
#
# `digest`/`executive` used to derive from `characterize`'s OWN pandas frame -- a second
# implementation of facts the shipped `analyze_window` payload already computes (effort/tempo was
# the clearest case: two code paths free to drift on the exact same conclusion). Both now PROJECT
# that payload instead: `doc` below is `analyze_window(...)`'s return value (workstreams,
# inventory, inventory_omitted, dynamics, prior, effort, evidence, schema, session, window_start,
# window_end -- see sidecar/app/analysis/analyze.py), not a `characterize()` document. That is a
# call-signature break for the two existing callers of the old shape, `context_value.py` and
# `prose_probe.py` (`executive(characterize(...))`) -- reported, not silently patched around, per
# this task's brief.
#
# THE CONSTRAINT THAT DECIDES THIS SHAPE (AGENTS.md, "Stating the conclusion IS the feature"): a
# 16 KB characterisation of raw window numbers scored -3.3/-20.0 on synthesis accuracy -- worse
# than emitting nothing -- against +36.7 for a digest that LABELLED each number and STATED the
# conclusion. So every helper below reads a field the payload already computed and states its
# conclusion (`changed: branch steady`, not `turnover: 0.0`); none of them re-derive anything, and
# none of them emit a per-side `dynamics[*].slice`/`.baseline` object -- the same cut the Go wire
# boundary makes for privacy, since `term` (inside those objects) has held real people's names.
#
# `frame`, where accepted, is the OLD `characterize()` document for the SAME entity and window --
# entirely optional, and it feeds a section with NO payload equivalent: `lift` against the
# repositories' whole prior history (`series`'s "COMPOSITION AND BASELINE ARE DIFFERENT SCOPES").
# The session-scoped `prior` block is not a substitute for that -- it is the configuration lift
# was already measured in and found degenerate for "unusual" (a 74-minute-old transcript's own
# history IS the window, so lift collapsed to x1.0 while the repo's history put the same branch at
# x33.6). The two baselines answer different questions and are kept in clearly separate,
# clearly-labelled sections rather than blended, for the same reason numbers stay labelled: an
# unlabelled mix of two scopes is a reader who cannot tell which yardstick a figure is against.


def _work_summary(payload):
    """One clause per ALLOCATION dimension, in `workstreams.ALLOCATION` order. An unattributed
    dimension is NAMED (`skill unattributed`), never silently dropped -- the ALLOCATION set is
    fixed at 7, so a reader can always tell "the pipeline didn't say" from "this wasn't asked"."""
    ws = payload.get("workstreams") or {}
    bits = []
    for name, _level, _floor in ALLOCATION:
        v = ws.get(name)
        bits.append(f"{v['value']} {100*v['share']:.0f}%" if v else f"{name} unattributed")
    return " · ".join(bits)


def _hotspots(payload):
    """Inventory counts + the hottest couple of values, with the top-N cut named rather than
    silently absorbed (`inventory_omitted` -- see `workstreams.payload`'s own "dropping must be
    visible" note, one level up)."""
    inv = payload.get("inventory") or {}
    omitted = payload.get("inventory_omitted") or {}
    counts = [f"{len(inv[name]) + omitted.get(name, 0)} {name}"
              for name in ("files", "components") if inv.get(name)]
    if not counts:
        return None
    head = ", ".join(counts)
    top = [it["value"] for it in (inv.get("files") or [])[:2]]
    if top:
        head += " — " + ", ".join(top)
    if omitted:
        head += "  (truncated: " + ", ".join(f"{k} +{v}" for k, v in omitted.items()) + ")"
    return head


def _changed_line(payload):
    """The dynamics block, STATED. `reading` for every `compared` dimension; every other
    dimension is named alongside its `status` rather than dropped -- `MIN_EVIDENCE`'s own
    argument is that a status other than `compared` is not "nothing happened", it is "this
    comparison could not be made", and those are different facts."""
    dyn = payload.get("dynamics")
    if not dyn:
        return None
    dims = dyn["dimensions"]
    compared = [f"{name} {v['reading']}" for name, v in dims.items() if v["status"] == "compared"]
    other = [f"{name} {v['status']}" for name, v in dims.items() if v["status"] != "compared"]
    head = ", ".join(compared) if compared else "nothing comparable"
    if other:
        head += "   [not comparable: " + ", ".join(other) + "]"
    return head


def _prior_line(payload):
    """The session prior, STATED as a departure conclusion rather than the per-dimension value/
    share/evidence dicts. `novel` (new to the session) takes precedence over `agrees` because it
    is the narrower, more informative fact (see `prior.py`'s own docstring on `skill`); a
    dimension the WINDOW itself could not attribute is skipped here because `_work_summary`
    already named it `unattributed` -- restating it as "no prior" would be the same fact twice,
    not two facts."""
    pr = payload.get("prior")
    if not pr:
        return None
    ws = payload.get("workstreams") or {}
    novel, diverge, no_prior = [], [], []
    for name, v in pr["dimensions"].items():
        if ws.get(name) is None:
            continue
        if v.get("novel"):
            novel.append(name)
        elif v.get("agrees") is False:
            diverge.append(name)
        elif v.get("agrees") is None:
            no_prior.append(name)
    parts = [("new: " + ", ".join(novel)) if novel else "nothing new"]
    if diverge:
        parts.append("diverges on " + ", ".join(diverge))
    if no_prior:
        parts.append("no session history for " + ", ".join(no_prior))
    line = "; ".join(parts)
    return line + " (baseline clamped by retention)" if pr.get("clamped") else line


def _effort_line(payload):
    """ONE tempo, from `effort.tempo` -- the ratio-derived reading `refseries.py` used to compute
    separately is deleted; `_effort`'s own docstring is the reason (a stated conclusion beat a
    stated number +36.7 to -3.3, and a bare ratio invites exactly that recompute-it-yourself
    reading). Each clause is dropped, not defaulted, when its own status says the check did not
    run -- `authored_bytes`/`fast_share` are `None` on purpose in that case (see `magnitude.py`/
    `latency.py`), and 0 would be a confident wrong answer, not an honest absence."""
    e = payload.get("effort") or {}
    parts = []
    if e.get("authored_status") == "attributed":
        n = e["authoring_turns"]
        parts.append(f"{e['authored_bytes']} B over {n} turn{'s' if n != 1 else ''}")
    if e.get("tempo_status") == "attributed" and e.get("tempo"):
        parts.append(e["tempo"])
    return ", ".join(parts) if parts else None


def _window_span(payload):
    s, e = pd.Timestamp(payload["window_start"]), pd.Timestamp(payload["window_end"])
    e_str = e.strftime("%H:%MZ") if e.date() == s.date() else e.strftime("%Y-%m-%dT%H:%MZ")
    return f"{s.strftime('%Y-%m-%dT%H:%MZ')}..{e_str}  {payload.get('evidence', 0)} refs"


def _frame_lift_section(frame):
    """The OLD `characterize()` document's lift-against-repo-history facts, for the section
    `digest`/`executive` keep SEPARATE from the payload projection (see the module note above).
    `frame` is optional and this returns `None` when it is absent or empty -- there is no payload
    field this could silently stand in for, so "no frame" and "frame found nothing" both mean
    "say nothing here", unlike every payload-derived helper above."""
    if not frame or not frame.get("rungs"):
        return None
    levels = {lv: blk for r in frame["rungs"].values() for lv, blk in r["levels"].items()}
    notable = [(it["lift"], f"{lv} {it['ref']} x{it['lift']:g}")
              for lv, blk in levels.items() for it in blk.get("top", [])
              if (it.get("lift") or 0) >= 3]
    gone = [(a["usual_share"], f"{lv} {a['ref']} (usually {100*a['usual_share']:.0f}%)")
            for lv, blk in levels.items() for a in blk.get("absent_but_usual", [])]
    out = {}
    if notable:
        out["unusually_prominent"] = [t for _, t in sorted(notable, reverse=True)[:6]]
    if gone:
        out["absent_but_usual"] = [t for _, t in sorted(gone, reverse=True)[:6]]
    return out or None


_FRAME_NOTE = ("frame-derived (scripts/refseries.py's own pandas pipeline); NOT part of what "
              "analyze_window ships. Baseline is the repositories' WHOLE PRIOR HISTORY, a wider "
              "yardstick than vs_session's session-only scope -- the two are not comparable and "
              "are kept in separate sections for exactly that reason.")


def digest(payload, frame=None):
    """Compress `analyze_window`'s payload to the lines that carry it -- a projection, not a
    second implementation, of the same rule the old frame-based digest stated: derived from the
    full document rather than computed separately, so the two can never disagree. See the module
    note above for `frame` and for why every line here states a conclusion rather than a number.
    """
    if not payload.get("evidence"):
        return {"window": f"{payload.get('window_start')}..{payload.get('window_end')} — "
                          f"no reference observations"}
    out = {"window": _window_span(payload), "work": _work_summary(payload)}
    hot = _hotspots(payload)
    if hot:
        out["hotspots"] = hot
    changed = _changed_line(payload)
    if changed:
        out["changed"] = changed
    prior = _prior_line(payload)
    if prior:
        out["vs_session"] = prior
    effort = _effort_line(payload)
    if effort:
        out["effort"] = effort
    lift = _frame_lift_section(frame)
    if lift:
        out["vs_repo_history"] = dict(note=_FRAME_NOTE, **lift)
    return out


def executive(payload, frame=None):
    """The same facts, in sentences -- the "normal" (non `--brief`) mode. Keeps the old
    executive()'s rule that a clause is DROPPED when the number behind it is missing, so the
    summary never asserts anything the payload did not carry; see the module note above for what
    changed and why.
    """
    if not payload.get("evidence"):
        return {"headline": "no recorded activity",
                "summary": f"No reference observations between {payload.get('window_start')} "
                           f"and {payload.get('window_end')}."}
    ws = payload.get("workstreams") or {}
    inv = payload.get("inventory") or {}
    start = pd.Timestamp(payload["window_start"]).strftime("%d %b %H:%M")
    end = pd.Timestamp(payload["window_end"]).strftime("%H:%M")
    sents = [f"{start}–{end}Z, {payload['evidence']} recorded references."]

    attributed = [(name, ws[name]) for name, _l, _f in ALLOCATION if ws.get(name)]
    unattributed = [name for name, _l, _f in ALLOCATION if ws.get(name) is None]
    if attributed:
        sents.append("The work was " + ", ".join(
            f"{v['value']} ({100*v['share']:.0f}% of {name})" for name, v in attributed) + ".")
    if unattributed:
        sents.append("No dominant value for " + ", ".join(unattributed) + ".")

    hot = _hotspots(payload)
    if hot:
        sents.append(f"Hotspots: {hot}.")

    changed = _changed_line(payload)
    if changed:
        sents.append(f"Against the baseline inside this same window: {changed}.")

    prior = _prior_line(payload)
    if prior:
        sents.append(f"Against the session so far: {prior}.")

    effort = _effort_line(payload)
    if effort:
        sents.append(f"Effort: {effort}.")

    lift = _frame_lift_section(frame)
    if lift:
        bits = []
        if lift.get("unusually_prominent"):
            bits.append("unusually prominent — " + "; ".join(lift["unusually_prominent"]))
        if lift.get("absent_but_usual"):
            bits.append("normally present and missing — " + "; ".join(lift["absent_but_usual"]))
        sents.append("Separately, against the repositories' whole prior history rather than "
                     "this session (frame-derived, not part of the shipped payload): " +
                     "; ".join(bits) + ".")

    key_facts = [f"{name}: {v['value']} {100*v['share']:.0f}%" for name, v in attributed]
    if effort:
        key_facts.append(f"effort: {effort}")

    top_component = (inv.get("components") or [{}])[0].get("value")
    slots = [(ws.get("project") or {}).get("value"), (ws.get("output_type") or {}).get("value"),
             top_component, (ws.get("branch") or {}).get("value")]
    headline = " · ".join(x if x else "—" for x in slots)
    return {"headline": headline,
            "headline_format": "project · output_type · top component · branch; — means that "
                               "dimension was unattributed or empty in this window",
            "window": {"start": payload["window_start"], "end": payload["window_end"],
                       "evidence": f"{payload['evidence']} recorded tool references"},
            "summary": Para(" ".join(sents)),
            "key_facts": key_facts,
            "basis": "workstreams/inventory/dynamics/prior/effort from analyze_window's "
                     "reference-series payload (tool-call metadata only, never message text, "
                     "except inventory.named_terms); dynamics compares a recent slice to a "
                     "preceding baseline INSIDE this window, prior compares this window to the "
                     "session before it -- two different, narrower baselines than vs_repo_history "
                     "above, when that section is present"}


def _resolve_transcript(session, roots):
    """A session-id PREFIX -> the one transcript file it names, under `roots`.

    `analyze_window` answers exactly one transcript at a time, unlike the old frame-based
    `context`/`contexts`, whose `--repo` could equally name a REPOSITORY (many transcripts
    pooled) or a session. Only the session form has a `(path, end_ts)` `analyze_window` can
    accept -- a repo-level window pooling several transcripts is NOT expressible against the
    shipped payload, and is the one thing the old `--repo <repo-name>` form could do that the
    re-pointed commands cannot. Say so loudly rather than silently picking one transcript.
    """
    hits = sorted({p for root in roots
                   for p in glob.glob(os.path.join(root, "*", f"{session}*.jsonl"))})
    if not hits:
        sys.exit(f"no transcript matching session '{session}' under {', '.join(roots)} "
                 f"(a repo NAME, pooling several transcripts, has no equivalent here -- see "
                 f"_resolve_transcript's docstring)")
    if len(hits) > 1:
        sys.exit(f"session '{session}' matches {len(hits)} transcripts, be more specific:\n  " +
                 "\n  ".join(hits))
    return hits[0]


def _frame_doc(outdir, repo, payload, start, end, topk):
    """The OLD `characterize()` document for the SAME window, if the study frames for a matching
    repo exist under `outdir` (`extract` + `series` were run) -- optional, and feeds ONLY the
    `vs_repo_history` lift section `digest`/`executive` keep separate from the payload projection
    (see the module note above `digest`). `None` when there is nothing to look up; that is not an
    error, since the lift section is supplementary rather than part of what `analyze_window`
    itself ships.
    """
    refs_path = os.path.join(outdir, "refs.parquet")
    if not os.path.exists(refs_path):
        return None
    entity = repo or (payload.get("workstreams", {}).get("project") or {}).get("value")
    if not entity:
        return None
    refs = pd.read_parquet(refs_path)
    if entity not in set(refs.repo.astype(str)):
        return None
    lvls = pd.read_parquet(os.path.join(outdir, "levels.parquet"))
    spk = pd.read_parquet(os.path.join(outdir, "speaker.parquet"))
    bpath = os.path.join(outdir, "baseline.parquet")
    base = pd.read_parquet(bpath) if os.path.exists(bpath) else None
    return characterize(refs, lvls, spk, entity, start, end, topk, base=base)


def payload_for_window(path, start, end, store=None, nlp=None):
    """The SHIPPED payload for a (transcript, start, end) window.

    `executive`/`digest` used to take `characterize()`'s frame document directly; they now
    project `analyze_window`'s payload, so every study script that still addresses a window by
    (path, start, end) needs one place to cross that boundary. Without this the crossing gets
    reimplemented per script, which is how the two renderers drifted from what ships in the
    first place.

    `refresh` is left at its default (True): a study script may be the first thing ever to touch
    this transcript's store, and an un-ingested transcript would otherwise answer empty rather
    than answering at all.

    Raises `PromptNotFound` / `StoreBehind` / `WindowExpired` unchanged — a caller sweeping many
    windows must decide for itself whether to skip or stop, and swallowing them here would hide
    a pruned-retention horizon behind an empty digest.
    """
    st = store if store is not None else open_store()
    span_minutes = (pd.Timestamp(end) - pd.Timestamp(start)).total_seconds() / 60.0
    return analyze_window(path, None, span_minutes=span_minutes,
                          nlp=term_nlp() if nlp is None else nlp, store=st,
                          end_ts=pd.Timestamp(end).isoformat(), prior=True, sizer=DEFAULT_SIZER)


def _watermark_iso(store, path, nlp):
    """The transcript's own last-ingested instant, as the ISO string `analyze_window` wants for
    `end_ts` -- the re-pointed commands' default window END when `--to`/`--at` is not given,
    matching the old frame-based default (the entity's last observed bin). `Store.watermark`
    already returns the turn's timestamp AS WRITTEN (an ISO string, see `ingest.py`), not epoch
    seconds -- there is no conversion to do here."""
    if not is_current(store, path, nlp):
        ingest_file(store, path, nlp)
    wm = store.watermark(path)
    if wm is None:
        sys.exit(f"{path} has no ingested turns")
    return wm


def contexts_cmd(args):
    """A multi-document YAML across ONE transcript, one document per sliding window, each
    rendered as `digest`/`executive` over `analyze_window`'s payload -- see `context`'s docstring
    for why this addresses a SESSION rather than a pooled repo now that it projects the shipped
    payload, and `_resolve_transcript` for what that costs.

    STRIDE AND SPAN ARE SEPARATE, and the stride should not divide the span. Aligned hourly
    windows land on the same clock mark every time, so a transition sitting mid-window is smeared
    with no clean window anywhere. A stride that does not divide the span PRECESSES, and the grid
    keeps sliding relative to the work. Measured over one transcript's 8 branch transitions, with
    the span held at 60 minutes:

        stride 60m   median 22 min from the nearest window edge, worst case 60 min
        stride 50m   median 12 min,                              worst case 20 min
        stride 45m   median  ~0 min, smeared windows 25% -> 14%, clean coverage 73% -> 87%

    Finer strides plateau at ~90% coverage rather than continuing to improve, so the benefit is
    alignment, not resolution: below ~45 minutes you buy volume, not fidelity.
    """
    path = _resolve_transcript(args.session, args.roots)
    st = open_store()
    nlp = term_nlp()
    times = [pd.Timestamp(o["timestamp"]) for o in iter_turns(path) if o.get("timestamp")]
    if not times:
        sys.exit(f"no turns in {path}")
    lo, hi = min(times).floor("h"), max(times).ceil("h")
    span, stride = pd.Timedelta(args.span), pd.Timedelta(args.stride)
    span_minutes = span.total_seconds() / 60.0
    docs, t = [], lo
    while t < hi:
        end = t + span
        try:
            payload = analyze_window(path, None, span_minutes=span_minutes, nlp=nlp, store=st,
                                     end_ts=end.isoformat(), prior=True, sizer=DEFAULT_SIZER)
        except (PromptNotFound, StoreBehind, WindowExpired):
            # Past the watermark (the `hi = ceil("h")` overshoot) or before the retention floor:
            # neither is an error worth stopping the sweep for, so the window is skipped exactly
            # as the old code skipped a window with no rows in [t, t+span).
            t += stride
            continue
        if payload.get("evidence"):
            frame = _frame_doc(args.outdir, args.repo, payload, t, end, args.topk)
            docs.append(digest(payload, frame) if args.brief else executive(payload, frame))
        t += stride
    out = "\n---\n".join(yaml.safe_dump(d, sort_keys=False, width=110, allow_unicode=True,
                                         default_flow_style=False) for d in docs)
    if args.out:
        open(args.out, "w").write(out)
        print(f"{len(docs)} windows (span {args.span}, stride {args.stride}) -> {args.out}")
    else:
        print(out)


def episodes_cmd(args):
    refs = pd.read_parquet(os.path.join(args.outdir, "refs.parquet"))
    entity = args.repo or refs.repo.value_counts().idxmax()
    for e in episodes(refs, entity, watch=tuple(args.watch), debounce=args.debounce,
                      max_gap_h=args.max_gap_h, max_span_h=args.max_span_h):
        print(f"{e['start']:%Y-%m-%dT%H:%M}\t{e['end']:%Y-%m-%dT%H:%M}\t{e['bins']}\t"
              f"{e['events']:.0f}\t{e['reason']}\t"
              + " ".join(f"{k}={v}" for k, v in e["state"].items()))


def context(args):
    """One window's worth of the SHIPPED payload (`analyze_window`), rendered `--brief` (`digest`)
    or as prose (`executive`) -- the digest is the deliverable; the full document underneath it
    measured WORSE as context than not supplying it at all (synthesis accuracy 47-57% against
    67-60% for the window text alone, 93-97% for the digest, on 13x the bytes and 3.3x the
    prefill -- see docs/superpowers/specs/2026-08-21-reference-series-design.md), which is the
    same finding AGENTS.md's dynamics section restates for the payload itself.

    Re-pointed from the study's own pandas frame to the production analysis path. `--session`
    (a transcript-id prefix under `--roots`) replaces `--repo` as the window's own identity,
    because `analyze_window` answers exactly one transcript -- see `_resolve_transcript`'s
    docstring for what a pooled-repo window (the old `--repo <repo-name>` form) cannot do here.
    `--repo` stays as an optional override for the SEPARATE, optional `vs_repo_history` lift
    section, where it keeps its old meaning (a study-frame entity) because that lookup still
    reads the pandas frame under `--outdir`.
    """
    path = _resolve_transcript(args.session, args.roots)
    st = open_store()
    nlp = term_nlp()
    end_ts = args.to or args.at
    if end_ts:
        end_ts = pd.Timestamp(end_ts, tz="UTC").isoformat()
    else:
        end_ts = _watermark_iso(st, path, nlp)
    from_ts = getattr(args, "from")
    if from_ts:
        span_minutes = ((pd.Timestamp(end_ts) - pd.Timestamp(from_ts, tz="UTC"))
                        .total_seconds() / 60.0)
    else:
        span_minutes = pd.Timedelta(args.span).total_seconds() / 60.0
    try:
        payload = analyze_window(path, None, span_minutes=span_minutes, nlp=nlp, store=st,
                                 end_ts=end_ts, prior=True, sizer=DEFAULT_SIZER)
    except (PromptNotFound, StoreBehind, WindowExpired) as e:
        sys.exit(f"{type(e).__name__}: {e}")
    start, end = pd.Timestamp(payload["window_start"]), pd.Timestamp(payload["window_end"])
    frame = _frame_doc(args.outdir, args.repo, payload, start, end, args.topk)
    doc = digest(payload, frame) if args.brief else executive(payload, frame)
    print(yaml.safe_dump(doc, sort_keys=False, width=110, allow_unicode=True,
                         default_flow_style=False))


def bar(x, width=10):
    """A share as a bar, so a distribution can be read down the column."""
    if x != x:
        return " " * width
    full = int(round(min(max(x, 0.0), 1.0) * width))
    return ("█" * full).ljust(width, "·")


def synopsis(args):
    """The distribution across and within rungs at one moment, as tables."""
    refs = pd.read_parquet(os.path.join(args.outdir, "refs.parquet"))
    lvls = pd.read_parquet(os.path.join(args.outdir, "levels.parquet"))
    spk = pd.read_parquet(os.path.join(args.outdir, "speaker.parquet"))
    repo = args.repo or refs.repo.value_counts().idxmax()
    when = (pd.Timestamp(args.at, tz="UTC") if args.at else refs[refs.repo == repo].bin.max())
    R, L = refs[(refs.repo == repo) & (refs.bin <= when)], lvls[(lvls.repo == repo) &
                                                               (lvls.bin <= when)]
    if R.empty:
        sys.exit(f"no rows for {repo} at or before {when}")
    W, LIFT = f"share_{args.window}", f"lift_{args.window}"
    NW = f"n_{args.window}"

    # One window, used by BOTH tables. Selecting references from the last BIN while showing
    # rolling-window shares made the distribution incomplete — a reference touched 40 minutes ago
    # was missing, and the shares summed to 25% instead of 100%.
    span = pd.Timedelta(args.window)
    win = R[R.bin > when - span]
    rows, dist = [], []
    for rung, levels in LADDER_RUNGS:
        for level in levels:
            Ls, Ws = L[L.level == level], win[win.level == level]
            if Ls.empty or Ws.empty:
                continue
            last = Ls.iloc[-1]
            tot = Ws.n.sum()
            agg = (Ws.groupby("ref", as_index=False)
                   .agg(n=("n", "sum"), bin=("bin", "max")))
            agg["share"] = agg.n / tot
            # lift, trend and gap are read from each reference's own latest row in the window —
            # they are columns in refs.parquet, not recomputed here.
            latest = (Ws.sort_values("bin").groupby("ref").tail(1)
                      .set_index("ref")[["base_share", "trend_6h", "gap_h", "n_24h", "rank_1h"]])
            agg = agg.join(latest, on="ref")
            agg["lift"] = agg.share / agg.base_share
            agg = agg.sort_values("share", ascending=False)
            p = agg.share.to_numpy()
            ent = float(abs(-(p * np.log2(np.where(p > 0, p, 1))).sum()))
            rows.append((rung, level, tot, len(agg), ent, float(agg.share.iloc[0]),
                         last.turnover, last.pace,
                         (when - last.bin).total_seconds() / 3600.0))
            for i, r in enumerate(agg.head(args.topk).itertuples()):
                dist.append((rung if i == 0 else "", level if i == 0 else "",
                             r.share, r.lift, r.trend_6h, int(r.rank_1h) if r.rank_1h == r.rank_1h
                             else 0, r.n, r.n_24h, r.gap_h, r.ref))
            if len(agg) > args.topk:
                rest = agg.iloc[args.topk:]
                dist.append(("", "", rest.share.sum(), np.nan, np.nan, 0, rest.n.sum(),
                             rest.n_24h.sum(), np.nan,
                             f"[{len(rest)} more references, not shown]"))

    print(f"\n{'='*118}\n{repo}  @  {when:%Y-%m-%d %H:%M}Z   "
          f"(all figures indexed from refs/levels/speaker.parquet; window = {args.window})")
    print(f"\nRUNGS\n{'rung':20} {'level':11} {'events':>7} {'breadth':>8} "
          f"{'entropy':>8} {'top1':>6} {'turnover':>9} {'pace':>6} {'last seen':>10}")
    print("-" * 100)
    prev = None
    for rung, level, n, brd, ent, top1, turn, pace, age in rows:
        print(f"{(rung if rung != prev else ''):20} {level:11} {n:7.0f} {brd:8d} {ent:8.2f} "
              f"{100*top1:5.0f}% {turn:9.2f} "
              f"{(f'{pace:.1f}x' if pace == pace else '—'):>6} {age:9.1f}h")
        prev = rung
    print(f"\nDISTRIBUTION WITHIN EACH RUNG\n{'rung':20} {'level':11} {'share':>6} "
          f"{'':10} {'lift':>6} {'tr':>3} {'rk':>3} {'n/'+args.window:>7} {'n/24h':>7} "
          f"{'gap':>6}  reference")
    print("-" * 118)
    for rung, level, sh, lift, tr, rk, n, n24, gap, ref in dist:
        arrow = "" if tr != tr else ("↑" if tr > 0.02 else "↓" if tr < -0.02 else "→")
        print(f"{rung:20} {level:11} {100*sh:5.0f}% {bar(sh)} "
              f"{(f'x{lift:.1f}' if lift == lift else '—'):>6} {arrow:>3} "
              f"{(str(rk) if rk else '—'):>3} {n:7.0f} {n24:7.0f} "
              f"{(f'{gap:.1f}h' if gap == gap else '—'):>6}  {ref}")

    S = spk[(spk.repo == repo) & (spk.bin <= when)]
    if not S.empty:
        r = S.iloc[-1]
        print(f"\nTEMPO\n{'channel':22} {'now':>10} {'/'+args.window:>10} {'/24h':>10} "
              f"{'z':>6}")
        print("-" * 62)
        for c in ("user_msgs", "user_chars_per_msg", "user_short_share", "user_burst_share",
                  "asst_msgs", "tok_out", "unsaid_share_approx"):
            if c in S.columns:
                print(f"{c:22} {r[c]:10.2f} {r.get(c + '_' + args.window, np.nan):10.2f} "
                      f"{r.get(c + '_24h', np.nan):10.2f} {r.get(c + '_z', np.nan):6.2f}")


_TURNS = {}


def load_turns(path):
    """Parse one transcript's turns ONCE, in time order. Callers slice; nobody re-reads.

    The previous shape re-opened and re-scanned the whole file for every window asked about, so
    characterising N windows of one session cost N full passes over a file that runs to 86 MB.
    Time-ordered so a slice is a bisect rather than a filter.

    The line filter — the `"type":"user"`/`"tool_result"` substring pre-check, the JSON parse, the
    `timestamp` presence check — is `transcript.iter_turns`, the same filter the extract path runs.
    This function only adds what that shared filter deliberately leaves out: rendering
    (`said`/`calls`/`role`) and the cache (`iter_turns` is a plain generator with no cache of its
    own — see `transcript.turns_between`'s docstring for why a sidecar-imported package must not
    accumulate per-path state on its own say-so; this CLI-only cache is exactly that exception,
    made where a real caller — `window`, asking about many slices of one long-lived transcript —
    needs it)."""
    if path in _TURNS:
        return _TURNS[path]
    turns = []
    for o in iter_turns(path):
        ts = o["timestamp"]
        content = (o.get("message") or {}).get("content")
        said = text_of(content)
        calls = []
        if isinstance(content, list):
            for b in content:
                if not (isinstance(b, dict) and b.get("type") == "tool_use"):
                    continue
                name, inp = b.get("name"), b.get("input") or {}
                if isinstance(inp.get("file_path"), str):
                    arg = inp["file_path"]                 # an identifier: whole or not at all
                elif name == "Bash":
                    verbs, _exes, paths, _acts = bash_refs(inp.get("command"))
                    arg = "; ".join(list(dict.fromkeys(verbs))[:4])
                    if paths:
                        arg += f" · {len(paths)} path{'s' if len(paths) > 1 else ''}"
                else:
                    arg = clip(str(inp.get("description") or inp.get("skill")
                                   or inp.get("query") or ""), 90)
                calls.append(f"{name}({arg})")
        role = "engineer" if o.get("type") == "user" else "assistant"
        if role == "engineer" and said and is_command_echo(said):
            role = "echo"
        if not said.strip() and not calls:
            continue
        turns.append((pd.Timestamp(ts), role, said.strip(), calls, o.get("cwd") or ""))
    turns.sort(key=lambda r: r[0])
    _TURNS[path] = turns
    return turns


def turns_between(path, start, end):
    """The turns in [start, end) — a bisect over the parsed list, not another file read.

    Deliberately NOT a call to `transcript.turns_between`: that function re-reads and re-sorts the
    whole file on every call, by design (it has no long-lived caller yet that would benefit — see
    its docstring). `window` here is exactly that caller: it asks about many windows of the same
    long-lived, possibly 86 MB transcript, and the point of `load_turns`'s cache is that the file
    is parsed once regardless of how many windows are asked about. Delegating here would parse the
    file once per call again, silently reintroducing the N-full-passes cost `load_turns` exists to
    remove. `start`/`end` are also `pd.Timestamp`, not the ISO strings `transcript.turns_between`
    takes, so the two aren't even call-compatible today."""
    import bisect
    turns = load_turns(path)
    keys = [t[0] for t in turns]
    return turns[bisect.bisect_left(keys, start):bisect.bisect_left(keys, end)]


def window(args):
    """Print the transcript turns leading up to a moment, to check a ladder against reality.

        refseries.py window --at 2026-07-28T15:20 --repo keld-atlas --turns 14

    Re-rendered FROM THE TRANSCRIPT at display time, never from the event store: events.parquet
    holds counts and identifiers and deliberately no text at all, which is the same rule
    `spool.Pointer` follows — keep coordinates, resolve the text on the machine that owns it.
    """
    at = pd.Timestamp(args.at, tz="UTC")
    lo = at - pd.Timedelta(hours=args.hours)
    turns = []
    for root in args.roots:
        for path in sorted(glob.glob(os.path.join(root, "*", "*.jsonl"))):
            sess = os.path.basename(path)[:8]
            projdir = os.path.basename(os.path.dirname(path))
            for t, role, said, calls, cwd in turns_between(path, lo, at):
                if args.repo:
                    # The turn's own cwd decides its workspace, as it does everywhere else.
                    ws = resolve_workspace(cwd, projdir, {}, set(), args.repo_root)[1]
                    if ws != args.repo:
                        continue
                turns.append((t, role, clip(said, args.chars) if said else "", calls, sess))
    turns.sort(key=lambda r: r[0])
    turns = turns[-args.turns:]
    print(f"TRANSCRIPT — {args.repo or 'any repo'}, the {len(turns)} turns before "
          f"{at:%Y-%m-%d %H:%M}Z (re-read from the transcript; the event store holds no text)")
    for t, role, said, calls, sess in turns:
        head = f"  {t:%H:%M:%S} {role:9}"
        if said:
            print(f"{head} {said}")
            head = f"  {'':8} {'':9}"
        for c in calls:
            print(f"{head} -> {c}")
    print()


def normalize(args):
    """Reduce an exported session directory to the layout the platforms write themselves.

        refseries.py normalize ~/Downloads/transcripts-john -o /tmp/john-projects

    A manual export is not an input to this system (see the scope note in the design doc), so the
    only supported way to look at one is to reduce it to the SAME shape a platform-written
    transcript has — `<projects>/<cwd-with-slashes-as-dashes>/<sessionId>.jsonl` — and then run
    the ordinary pipeline over it with no special cases. Duplicates are dropped by content hash
    (an export can ship the same session twice under two names), and the project directory name
    is derived from the session's own recorded `cwd`, exactly as Claude Code encodes it.
    """
    os.makedirs(args.outdir, exist_ok=True)
    seen, written = set(), 0
    for path in sorted(glob.glob(os.path.join(args.export, "**", "*.jsonl"), recursive=True)):
        with open(path, "rb") as fh:
            h = hashlib.sha256(fh.read()).hexdigest()
        if h in seen:
            print(f"  duplicate, skipped: {os.path.basename(path)}")
            continue
        seen.add(h)
        cwd = session = None
        for line in open(path, errors="replace"):
            try:
                o = json.loads(line)
            except Exception:
                continue
            cwd = cwd or o.get("cwd")
            session = session or o.get("sessionId") or o.get("cliSessionId")
            if cwd and session:
                break
        if not cwd:
            print(f"  no cwd recorded, skipped: {path}")
            continue
        proj = cwd.replace("/", "-")
        dest_dir = os.path.join(args.outdir, proj)
        os.makedirs(dest_dir, exist_ok=True)
        dest = os.path.join(dest_dir, f"{session or h[:8]}.jsonl")
        with open(path, "rb") as src, open(dest, "wb") as dst:
            dst.write(src.read())
        written += 1
        print(f"  {os.path.basename(path)} -> {os.path.relpath(dest, args.outdir)}  (cwd {cwd})")
    print(f"{written} session(s) normalised into {args.outdir}\n"
          f"now run the ordinary pipeline, adding the export's repo root:\n"
          f"  refseries.py extract --roots ~/.claude/projects {args.outdir} "
          f"--repo-root ~/keld <export-root>")


def canonicalize_terms(rows):
    """Fold a term's casings together, corpus-wide, keeping the spelling the corpus uses most.

    terms.tally folds case within one call, and extract calls it per MESSAGE, so "Magenta" said in
    one turn and "magenta" in the next survive as two refs — halving the prominence of the thing
    the hour was about (measured: 24 mentions split 17/7). Folding has to happen once the whole
    corpus is in hand, which is here.

    Only `term` rows are touched; every other level's ref is a path, an identifier or a controlled
    vocabulary where case is meaningful.
    """
    best = {}
    for r in rows:
        if r[6] != "term":
            continue
        best.setdefault(r[7].lower(), collections.Counter())[r[7]] += r[8]
    canon = {k: c.most_common(1)[0][0] for k, c in best.items()}
    return [(r[:7] + (canon[r[7].lower()],) + r[8:]) if r[6] == "term" and isinstance(r, tuple)
            else ([*r[:7], canon[r[7].lower()], *r[8:]] if r[6] == "term" else r)
            for r in rows]


def _process_transcript(job):
    """One transcript -> its rows, pending paths and line count.

    Module-level and self-contained because a ProcessPoolExecutor has to pickle what it calls: the
    previous closure captured `args` and the shared `rows` list. Nothing here is shared between
    transcripts — each is parsed independently and the only cross-file steps (hash dedup,
    `reconcile`, `canonicalize_terms`) stay in the parent, where they need the whole corpus anyway.

    The hash is computed here rather than in the parent so the file is read once, in the worker.

    Thin by design: `transcript.iter_turns` owns the file I/O and the per-line filtering (the
    `tool_result` skip that keeps this a seconds-long parse), `levels.events_for_turns` owns the
    classification. This function is left with the one step that only makes sense in the worker
    (the hash, so the file is read for it exactly here) and wiring the two together.
    """
    path, root, repo_root = job
    with open(path, "rb") as fh:
        h = hashlib.sha256(fh.read()).hexdigest()
    turns = list(iter_turns(path))
    rows, pending, n_lines = events_for_turns(turns, path, root, repo_root, term_nlp())
    return h, path, rows, pending, n_lines


def extract(args):
    # Transcripts are independent, and parsing one is the expensive part (spaCy over every message
    # plus a bash parse per tool call). Order is preserved by `map`, so the event frame is
    # byte-identical to the serial build regardless of which worker finishes first.
    jobs = [(path, root, args.repo_root)
            for root in args.roots
            for path in sorted(glob.glob(os.path.join(root, "*", "*.jsonl")))]
    if not jobs:
        sys.exit("no transcripts found under " + ", ".join(args.roots))
    workers = args.workers or max(1, int((os.cpu_count() or 4) * 0.8))
    workers = max(1, min(workers, len(jobs)))
    print(f"parsing {len(jobs)} transcripts on {workers} worker(s)")
    if workers > 1:
        with concurrent.futures.ProcessPoolExecutor(max_workers=workers) as ex:
            results = list(ex.map(_process_transcript, jobs))
    else:
        results = [_process_transcript(j) for j in jobs]

    rows, pending = [], []
    seen_hash = set()
    n_files = n_lines = n_dup = 0
    for h, path, r, pnd, nl in results:
        if h in seen_hash:
            n_dup += 1
            print(f"  duplicate, skipped: {path}")
            continue
        seen_hash.add(h)
        n_files += 1
        n_lines += nl
        rows += r
        pending += pnd

    recon_rows, recon_stats = reconcile(pending, args.component_depth)
    rows += recon_rows
    if recon_stats:
        print("  prose paths reconciled against declared ones: " +
              ", ".join(f"{k}={v}" for k, v in sorted(recon_stats.items())))

    rows = canonicalize_terms(rows)

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
    ev = ev.assign(scope_repo=ev.repo)
    if args.entity == "session":
        # Every frame keys on one entity column. Scoping to a session substitutes it, so the
        # rolling windows and expanding baselines are that transcript's own, not the repo's.
        ev = ev.assign(repo=ev.session.astype(str))
    if args.session:
        ev = ev[ev.session.astype(str) == args.session]
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
    for name, df in (("metrics", mt), ("bins", bn), ("summary", lv)):
        p = os.path.join(args.outdir, f"{name}.parquet")
        df.to_parquet(p, index=False)
        print(f"  {name:8} {df.shape} -> {p}")

    print("\nmaterialising the indexable frames (rolling statistics as columns):")
    base_ev, scope_map = None, None
    if args.baseline == "repo":
        # Every entity is baselined against the repositories IT touches, across all sessions —
        # not against its own history, which for a young session is the window itself.
        base_ev = pd.read_parquet(os.path.join(args.outdir, "events.parquet"))
        scope_map = {str(k): set(g.scope_repo.dropna().unique())
                     for k, g in ev.groupby("repo", sort=False)}
        for k, v in scope_map.items():
            print(f"  baseline scope for {k}: {', '.join(sorted(v)) or '(none)'}")
    build_frames(ev, args.bin, args.outdir, base_ev, scope_map)
    build_speaker_frame(mt, args.bin, args.outdir)


# ---------------------------------------------------------------- the ladder
#
# The ladder decides, at a point in time, what is at a HIGH LEVEL and what is background — read
# off the series itself, not off a fitted constant.
#
# An earlier version set each rung's lookback and staleness budget from the corpus half-life
# (0.5 x 32h, and so on). That was the wrong use of that measurement. A half-life is a summary of
# a DISTRIBUTION of change rates, so using it as a per-decision threshold applies a 34-day
# average to a specific moment: during a focused three-hour stretch on one file the file level is
# stable, during a sweeping refactor it turns over every ten minutes, and one number cannot
# express either. It is also backward-looking by construction — measured on history, applied to
# now — and it would need refitting per repo forever.
#
# The half-life keeps exactly one job: deciding which levels belong on the same RUNG, which is a
# question about the population and is answered once. Nothing below reads it at runtime.
#
# What is read instead, per level, per moment:
#
#   EVENT CLOCK   the window is "the last N observations of this level", however long that took.
#                 A wall-clock window mis-serves both a burst and an idle stretch; N observations
#                 is the same amount of evidence either way, and it needs no gap handling at all.
#   LIFT          a reference's share in that window over its share across all history. 55% now
#                 against 3% usually is the current focus; 5% now against 5% usually is
#                 background. This is the "relative level" the rung exists to report.
#   TREND         the recent half of the window against the older half, by event count — rising,
#                 steady or falling, measured inside the window rather than against a baseline.
#   LIVENESS      wall-clock age of the last observation against the MEDIAN GAP between this
#                 level's own recent observations. "Quiet for 5x its usual gap" is a local,
#                 self-normalising statement; "past 0.5x its 32h half-life" is not.
#
# The only knobs are shape parameters — how many observations make a window, what share is worth
# showing — not per-level constants fitted to a corpus.
# Grouped by the measured band, which is the one job the half-life keeps. IDENTITY and TOOLING
# both sit at >4wk but answer different questions — what the work IS against how it is DONE — so
# they are separate rows in the synopsis and would be separate payloads in a prompt.
LADDER_RUNGS = [
    ("IDENTITY", ["workspace", "repo", "repo_from_text", "vcs", "artifact", "action", "ext", "lang", "model"]),
    ("TOOLCHAIN", ["toolchain"]),
    ("TOOLING", ["tool", "exe", "verb", "agent", "skill", "mcp_server", "mcp_tool"]),
    ("SERVICES", ["service"]),
    ("NAMED TERMS", ["term"]),
    ("BRANCH & SUBSYSTEM", ["branch", "component"]),
    ("WORKING SET", ["dir", "file"]),
]
QUIET_GAPS = 3.0        # age > this many typical gaps = the level has gone quiet
NEW_LIFT = 2.5          # lift above this = elevated well beyond its own baseline


def _tail_by_events(d, n_events):
    """The last n_events observations, however long they took. Returns the slice in time order."""
    r = d.iloc[::-1]
    c = r.n.cumsum()
    win = r[c <= n_events]
    if win.empty:
        win = r.head(1)
    return win.iloc[::-1]


def ladder(args):
    bn = pd.read_parquet(os.path.join(args.outdir, "bins.parquet"))
    mt = pd.read_parquet(os.path.join(args.outdir, "metrics.parquet"))
    repo = args.repo or bn.repo.value_counts().idxmax()
    bn, mt = bn[bn.repo == repo].sort_values("bin_ts"), mt[mt.repo == repo]
    at = pd.Timestamp(args.at, tz="UTC") if args.at else bn.bin_ts.max()
    bn = bn[bn.bin_ts <= at]

    print(f"CONTEXT LADDER — {repo} @ {at:%Y-%m-%d %H:%M}Z")
    print(f"(every figure below is read off the series at this moment: share in the last "
          f"{args.events} observations of each level, its lift against that level's own "
          f"all-history share, and liveness against its own typical gap. No fitted constants.)")

    for rung, levels in LADDER_RUNGS:
        print(f"\n{rung}")
        for level in levels:
            d = bn[bn.level == level]
            if d.empty:
                print(f"  {level:10} —")
                continue
            win = _tail_by_events(d, args.events)
            span_h = (at - win.bin_ts.min()).total_seconds() / 3600.0
            now = win.groupby("ref", observed=True).n.sum()
            base = d.groupby("ref", observed=True).n.sum()
            share_now, share_base = now / now.sum(), base / base.sum()

            # Rate relative to this level's own habit, on the event clock: how long the last N
            # observations took, against how long they usually take.
            hist = d.bin_ts.drop_duplicates()
            gaps = hist.diff().dropna().dt.total_seconds() / 3600.0
            typ_gap = float(gaps.tail(200).median()) if len(gaps) else float("nan")
            samples = [(g.bin_ts.max() - g.bin_ts.min()).total_seconds() / 3600.0
                       for g in [_tail_by_events(d.iloc[:i], args.events)
                                 for i in range(len(d), 0, -max(1, len(d) // 12))]
                       if len(g) > 1]
            usual_span = float(np.median(samples)) if samples else float("nan")
            rate = (usual_span / span_h
                    if span_h > 0 and usual_span == usual_span else float("nan"))
            age_h = (at - d.bin_ts.max()).total_seconds() / 3600.0
            live = "live"
            if typ_gap == typ_gap and typ_gap > 0 and age_h > QUIET_GAPS * typ_gap:
                live = f"QUIET {age_h/typ_gap:.0f}x its usual {typ_gap*60:.0f}m gap"
            elif age_h > 0:
                live = f"last seen {age_h*60:.0f}m ago"
            hdr = (f"{args.events} obs over "
                   + (f"{span_h*60:.0f}m" if span_h < 1 else f"{span_h:.1f}h"))
            if rate == rate:
                hdr += (f" · {rate:.2f}x its usual pace" if rate < 0.1
                        else f" · {rate:.1f}x its usual pace")
            print(f"  {level:10} {hdr} · {live}")

            # Split the window by event count to get a trend from inside it.
            c = win.n.cumsum()
            half = win.n.sum() / 2.0
            older, recent = win[c <= half], win[c > half]
            so = (older.groupby("ref", observed=True).n.sum() / max(older.n.sum(), 1)
                  if not older.empty else None)
            sr = (recent.groupby("ref", observed=True).n.sum() / max(recent.n.sum(), 1)
                  if not recent.empty else None)

            top = share_now[share_now >= args.min_share].sort_values(ascending=False)
            parts = []
            for ref, sh in top.head(args.topk).items():
                lift = sh / share_base.get(ref, np.nan)
                arrow = "→"
                if so is not None and sr is not None:
                    delta = sr.get(ref, 0.0) - so.get(ref, 0.0)
                    arrow = "↑" if delta > 0.05 else "↓" if delta < -0.05 else "→"
                tag = " NEW" if lift == lift and lift >= NEW_LIFT else ""
                parts.append(f"{ref} {100*sh:.0f}% (x{lift:.1f} {arrow}{tag})")
            hidden = len(top) - min(len(top), args.topk)
            # An identifier is never truncated — a path cut short is a false path — so whole
            # terms are dropped and the count of dropped terms is stated (AGENTS.md).
            more = f"  (+{hidden} more above {100*args.min_share:.0f}%)" if hidden else ""
            below = int((share_now < args.min_share).sum())
            if below:
                more += f"  (+{below} below {100*args.min_share:.0f}%)"
            print(f"  {'':10} {', '.join(parts) if parts else '(nothing above threshold)'}{more}")

            faded = share_base[(share_base >= args.min_share) &
                               (~share_base.index.isin(share_now[share_now > 0].index))]
            if len(faded):
                print(f"  {'':10} absent from the window: " +
                      ", ".join(f"{k} (all-history {100*v:.0f}%)"
                                for k, v in faded.sort_values(ascending=False).head(3).items()))

    base = mt[(mt.level == "speaker") & (mt.bin_ts <= at)]
    recent = base[base.bin_ts >= at - pd.Timedelta(hours=args.tempo_hours)]
    if not recent.empty:
        def rel(col):
            v, med = recent[col].sum(), base[col].replace(0, np.nan).median()
            if pd.isna(med) or med == 0:
                return f"{v:.0f}"
            return f"{v:.0f} (x{v / (med * len(recent)):.1f})"
        msgs = recent.user_msgs.sum()
        cpm = recent.user_chars.sum() / msgs if msgs else float("nan")
        base_cpm = base.user_chars_per_msg.median()
        size = (f", {cpm:.0f} chars each (x{cpm / base_cpm:.1f})"
                if cpm == cpm and base_cpm and base_cpm == base_cpm else "")
        print(f"\nTEMPO   last {args.tempo_hours:g}h, each figure against this person's own "
              f"median")
        print(f"  engineer   {rel('user_msgs')} messages{size}")
        print(f"  assistant  {rel('asst_msgs')} messages, {rel('tok_out')} output tokens")


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    e = sub.add_parser("extract")
    e.add_argument("--roots", nargs="+", default=CLAUDE_ROOTS)
    e.add_argument("--repo-root", nargs="+", default=[REPO_ROOT])
    e.add_argument("--component-depth", type=int, default=3)
    e.add_argument("--outdir", default=OUTDIR)
    e.add_argument("--workers", type=int, default=0,
                   help="parallel transcript parsers (default: 80%% of cores)")
    e.set_defaults(fn=extract)
    w = sub.add_parser("window")
    w.add_argument("--roots", nargs="+", default=CLAUDE_ROOTS)
    w.add_argument("--repo-root", nargs="+", default=[REPO_ROOT])
    w.add_argument("--repo", default=None)
    w.add_argument("--at", required=True)
    w.add_argument("--hours", type=float, default=2.0)
    w.add_argument("--turns", type=int, default=14)
    w.add_argument("--chars", type=int, default=260)
    w.set_defaults(fn=window)
    n = sub.add_parser("normalize")
    n.add_argument("export", help="an exported session directory")
    n.add_argument("-o", "--outdir", default="/tmp/normalized-projects")
    n.set_defaults(fn=normalize)
    s = sub.add_parser("series")
    s.add_argument("--outdir", default=OUTDIR)
    s.add_argument("--repo", default=None)
    s.add_argument("--bin", type=float, default=1.0, help="bin size in HOURS (default 1)")
    s.add_argument("--since", default=None, help="ISO date, e.g. 2026-07-01")
    s.add_argument("--min-rows", type=int, default=500)
    s.add_argument("--entity", choices=["repo", "session"], default="repo",
                   help="what the frames are keyed on and baselined against")
    s.add_argument("--session", default=None, help="restrict to one transcript (8-char id)")
    s.add_argument("--baseline", choices=["repo", "entity"], default="repo",
                   help="what `lift` is measured against: the repositories the entity touches "
                        "across all sessions (default), or the entity's own history")
    s.add_argument("--windows", type=int, nargs="+", default=[6, 24, 168])
    s.add_argument("--detail", action="store_true")
    s.set_defaults(fn=series)
    c = sub.add_parser("context")
    c.add_argument("--outdir", default=OUTDIR,
                   help="where the OPTIONAL vs_repo_history frame lives (extract+series); the "
                        "window itself is served live from analyze_window, not from --outdir")
    c.add_argument("--session", required=True,
                   help="a transcript-id prefix under --roots -- analyze_window answers exactly "
                        "one transcript; a pooled REPO name (the old --repo meaning) has no "
                        "(path, end_ts) here, see _resolve_transcript")
    c.add_argument("--roots", nargs="+", default=CLAUDE_ROOTS)
    c.add_argument("--repo", default=None,
                   help="override the entity used for the OPTIONAL vs_repo_history lift lookup "
                        "in --outdir's frame; defaults to the window's own project workstream")
    c.add_argument("--at", default=None, help="window END (alias of --to)")
    c.add_argument("--to", default=None)
    c.add_argument("--from", default=None)
    c.add_argument("--span", default="1h", help="window length when --from is not given")
    c.add_argument("--topk", type=int, default=5, help="top-N for the vs_repo_history frame only")
    c.add_argument("--brief", action="store_true",
                   help="compact structured view, one line per level, instead of the summary")
    c.set_defaults(fn=context)
    cs = sub.add_parser("contexts")
    cs.add_argument("--outdir", default=OUTDIR,
                    help="where the OPTIONAL vs_repo_history frame lives (extract+series)")
    cs.add_argument("--session", required=True,
                    help="a transcript-id prefix under --roots, see `context --session`")
    cs.add_argument("--roots", nargs="+", default=CLAUDE_ROOTS)
    cs.add_argument("--repo", default=None,
                    help="override the entity used for the OPTIONAL vs_repo_history lift lookup")
    cs.add_argument("--span", default="60min")
    cs.add_argument("--stride", default="50min",
                    help="should NOT divide the span: a precessing grid keeps window edges from "
                         "landing on the same clock mark every time")
    cs.add_argument("--topk", type=int, default=5, help="top-N for the vs_repo_history frame only")
    cs.add_argument("--brief", action="store_true",
                    help="compact structured view instead of the summary")
    cs.add_argument("--out", default=None)
    cs.set_defaults(fn=contexts_cmd)
    ep = sub.add_parser("episodes")
    ep.add_argument("--outdir", default=OUTDIR)
    ep.add_argument("--repo", default=None)
    ep.add_argument("--watch", nargs="+", default=["branch", "component"])
    ep.add_argument("--debounce", type=int, default=2)
    ep.add_argument("--max-gap-h", type=float, default=1.0)
    ep.add_argument("--max-span-h", type=float, default=6.0)
    ep.set_defaults(fn=episodes_cmd)
    y = sub.add_parser("synopsis")
    y.add_argument("--outdir", default=OUTDIR)
    y.add_argument("--repo", default=None)
    y.add_argument("--at", default=None)
    y.add_argument("--window", default="1h", choices=ROLL)
    y.add_argument("--topk", type=int, default=4)
    y.set_defaults(fn=synopsis)
    a = sub.add_parser("at")
    a.add_argument("--outdir", default=OUTDIR)
    a.add_argument("--repo", default=None)
    a.add_argument("--at", default=None, help="ISO timestamp; default = the last observed bin")
    a.add_argument("--window", default="1h", choices=ROLL, help="which rolling column to read")
    a.add_argument("--topk", type=int, default=5)
    a.set_defaults(fn=at_view)
    d = sub.add_parser("ladder")
    d.add_argument("--outdir", default=OUTDIR)
    d.add_argument("--repo", default=None)
    d.add_argument("--at", default=None, help="ISO timestamp; default = last active bin")
    d.add_argument("--tempo-hours", type=float, default=1.0)
    d.add_argument("--events", type=int, default=40, help="observations per level in the window")
    d.add_argument("--min-share", type=float, default=0.05)
    d.add_argument("--topk", type=int, default=5)
    d.set_defaults(fn=ladder)
    args = ap.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
