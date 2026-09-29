#!/usr/bin/env python3
"""Can the Claude Code CLI, invoked headlessly and cheaply, replace GLiNER2 as the
`named_terms` extractor? Scored on the SAME two gold corpora, with the SAME matching
rule, as every other arm in this series (see `eval_business_terms.py`'s module
docstring for the full methodology). This file adds a fifth kind of arm -- an LLM
invoked via `claude -p --output-format json` -- and asks three questions the other
arms don't need to: is the output valid JSON every time, what does it cost, and how
fast is it.

FILE DISCIPLINE: new file. Imports READ-ONLY from `business_terms_corpora` (the
corpus loader) and `eval_business_terms` (target-type sets + `normalize`, the
matching rule). Does not modify either. Does not touch `eval_tab.py`, `eval_uner.py`,
`eval_tab_piitracer.py`, `gliner2_selection_bench.py` or `README.md`.

Mechanism under test: the Claude Code CLI (`claude -p --output-format json`), which
is what a `keld-agent` hook would actually shell out to on a machine with no
`anthropic` SDK and no API key configured -- Claude Code itself is already
authenticated. Each invocation is a fresh, isolated process (no `--continue` /
--resume`); run from a scratch directory with no CLAUDE.md so the measured cost
reflects a plain extraction call, not this repo's own (very large) project
instructions being loaded into context on every batch.

Batches N sentences per call (a hook would send a window of conversation, not one
sentence at a time) and asks for a single JSON object mapping each input's string
index to its list of extracted terms.
"""
from __future__ import annotations

import argparse
import json
import pathlib
import random
import re
import statistics
import subprocess
import sys
import time
from collections import Counter
from dataclasses import dataclass, field

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))  # sidecar/ on sys.path

from piibench.business_terms_corpora import load_soner, load_wnut, GoldSentence  # noqa: E402
from piibench.eval_business_terms import (  # noqa: E402
    normalize, SONER_TARGET_TYPES, WNUT_TARGET_TYPES, WNUT_SECONDARY_TYPES,
)

CLAUDE_BIN = "claude"
CALL_TIMEOUT_S = 180

# ---------------------------------------------------------------------------
# Prompt variants -- the wording IS the extractor, same as GLiNER2's label sets.
# ---------------------------------------------------------------------------
PROMPTS: dict[str, str] = {
    "software": (
        "You are an information-extraction API for software/technical entity "
        "mentions in developer text (forum posts, code discussion, Q&A). "
        "For EACH numbered input line, extract every mention of a specific "
        "software library, framework, programming language, operating system, "
        "developer tool, platform, or named website/service. Do not extract "
        "generic terms (\"the code\", \"a function\"), people, or file names "
        "unless the file name IS the product (e.g. \"nginx.conf\" -> no, "
        "\"nginx\" -> yes). "
        "Respond with STRICT JSON ONLY -- a single JSON object whose keys are "
        "the input line numbers as strings and whose values are arrays of the "
        "exact extracted substrings (empty array if none). No prose, no markdown "
        "code fences, no explanation -- the first character of your response "
        "must be '{' and the last must be '}'."
    ),
    "general": (
        "You are an information-extraction API for general business/brand "
        "entity mentions in short informal text (social media, forum posts). "
        "For EACH numbered input line, extract every mention of a specific "
        "product name, brand, company/organisation name, campaign, or named "
        "creative work (movie, show, song, book title). Do not extract generic "
        "common nouns, people's personal names, or place names. "
        "Respond with STRICT JSON ONLY -- a single JSON object whose keys are "
        "the input line numbers as strings and whose values are arrays of the "
        "exact extracted substrings (empty array if none). No prose, no markdown "
        "code fences, no explanation -- the first character of your response "
        "must be '{' and the last must be '}'."
    ),
}

DISALLOWED_TOOLS = "Bash,Read,Write,Edit,Glob,Grep,WebFetch,WebSearch,Task,NotebookEdit"

FENCE_RE = re.compile(r"^```(?:json)?\s*|\s*```$", re.MULTILINE)


@dataclass
class CallRecord:
    corpus: str
    prompt_name: str
    model: str
    batch_idx: int
    n_sentences: int
    ok: bool
    parse_error: str | None
    raw_result_snippet: str
    duration_ms: float
    ttft_ms: float | None
    duration_api_ms: float | None
    is_error: bool
    cost_usd: float
    input_tokens: int
    output_tokens: int
    thinking_tokens: int
    cache_creation_input_tokens: int
    cache_read_input_tokens: int


@dataclass
class ArmResult:
    corpus: str
    prompt_name: str
    model: str
    calls: list[CallRecord] = field(default_factory=list)
    tp: int = 0
    fp: int = 0
    fn: int = 0
    total_predicted: int = 0
    total_gold: int = 0
    scored_sentences: int = 0
    excluded_sentences: int = 0  # from failed-parse batches


def build_batch_prompt(sentences: list[str]) -> str:
    lines = [f"{i}: {s}" for i, s in enumerate(sentences)]
    return "Extract from these inputs:\n\n" + "\n".join(lines)


def call_claude(model: str, system_prompt: str, user_text: str) -> dict:
    """Invoke the Claude Code CLI headlessly. Returns the parsed --output-format
    json envelope (raw CLI usage/cost fields), or a synthetic envelope with
    is_error=True if the process itself failed (timeout, non-zero exit with no
    parseable stdout)."""
    cmd = [
        CLAUDE_BIN, "-p", "--output-format", "json",
        "--model", model,
        "--system-prompt", system_prompt,
        "--disallowedTools", DISALLOWED_TOOLS,
    ]
    t0 = time.perf_counter()
    try:
        proc = subprocess.run(
            cmd, input=user_text, capture_output=True, text=True,
            timeout=CALL_TIMEOUT_S,
        )
    except subprocess.TimeoutExpired:
        wall_ms = (time.perf_counter() - t0) * 1000
        return {"is_error": True, "result": "", "_process_error": "timeout",
                "duration_ms": wall_ms, "usage": {}, "total_cost_usd": 0.0}
    wall_ms = (time.perf_counter() - t0) * 1000
    try:
        envelope = json.loads(proc.stdout)
    except json.JSONDecodeError:
        return {"is_error": True, "result": proc.stdout[-2000:],
                "_process_error": f"cli_stdout_not_json (exit={proc.returncode}, "
                                   f"stderr={proc.stderr[-500:]!r})",
                "duration_ms": wall_ms, "usage": {}, "total_cost_usd": 0.0}
    envelope.setdefault("duration_ms", wall_ms)
    return envelope


def extract_terms_map(envelope: dict) -> tuple[dict[str, list[str]] | None, str | None]:
    """Parse the model's `result` text as {index_str: [terms]}. Returns (map, None)
    on success, or (None, error_description) on any failure -- malformed JSON,
    wrong top-level shape, or non-list values."""
    if envelope.get("is_error"):
        return None, envelope.get("_process_error") or "cli_reported_error"
    raw = envelope.get("result", "")
    if not isinstance(raw, str) or not raw.strip():
        return None, "empty_result"
    stripped = FENCE_RE.sub("", raw.strip()).strip()
    try:
        obj = json.loads(stripped)
    except json.JSONDecodeError as e:
        return None, f"json_decode_error: {e}"
    if not isinstance(obj, dict):
        return None, f"top_level_not_object: {type(obj).__name__}"
    out: dict[str, list[str]] = {}
    for k, v in obj.items():
        if not isinstance(v, list):
            return None, f"value_not_list_for_key_{k}: {type(v).__name__}"
        terms = []
        for t in v:
            if isinstance(t, str):
                terms.append(t)
            else:
                return None, f"non_string_term_in_key_{k}: {type(t).__name__}"
        out[str(k)] = terms
    return out, None


def usage_fields(envelope: dict) -> dict:
    usage = envelope.get("usage") or {}
    return {
        "cost_usd": float(envelope.get("total_cost_usd") or 0.0),
        "input_tokens": int(usage.get("input_tokens") or 0),
        "output_tokens": int(usage.get("output_tokens") or 0),
        "thinking_tokens": int((usage.get("output_tokens_details") or {}).get("thinking_tokens") or 0),
        "cache_creation_input_tokens": int(usage.get("cache_creation_input_tokens") or 0),
        "cache_read_input_tokens": int(usage.get("cache_read_input_tokens") or 0),
        "duration_ms": float(envelope.get("duration_ms") or 0.0),
        "ttft_ms": envelope.get("ttft_ms"),
        "duration_api_ms": envelope.get("duration_api_ms"),
        "is_error": bool(envelope.get("is_error")),
    }


def run_arm(corpus_name: str, sentences: list[GoldSentence], target_types: set[str],
            prompt_name: str, model: str, n_batches: int, batch_size: int,
            seed: int) -> ArmResult:
    rng = random.Random(seed)
    pool = list(sentences)
    rng.shuffle(pool)
    sample = pool[: n_batches * batch_size]

    result = ArmResult(corpus=corpus_name, prompt_name=prompt_name, model=model)
    system_prompt = PROMPTS[prompt_name]

    for b in range(n_batches):
        batch = sample[b * batch_size:(b + 1) * batch_size]
        if not batch:
            break
        texts = [s.text for s in batch]
        user_text = build_batch_prompt(texts)
        envelope = call_claude(model, system_prompt, user_text)
        uf = usage_fields(envelope)
        term_map, err = extract_terms_map(envelope)

        raw_result = envelope.get("result", "")
        if not isinstance(raw_result, str):
            raw_result = str(raw_result)
        rec = CallRecord(
            corpus=corpus_name, prompt_name=prompt_name, model=model, batch_idx=b,
            n_sentences=len(batch), ok=(term_map is not None), parse_error=err,
            raw_result_snippet=raw_result[:400],
            duration_ms=uf["duration_ms"], ttft_ms=uf["ttft_ms"],
            duration_api_ms=uf["duration_api_ms"], is_error=uf["is_error"],
            cost_usd=uf["cost_usd"], input_tokens=uf["input_tokens"],
            output_tokens=uf["output_tokens"], thinking_tokens=uf["thinking_tokens"],
            cache_creation_input_tokens=uf["cache_creation_input_tokens"],
            cache_read_input_tokens=uf["cache_read_input_tokens"],
        )
        result.calls.append(rec)

        if term_map is None:
            result.excluded_sentences += len(batch)
            print(f"    [{corpus_name}/{prompt_name}/{model}] batch {b}: "
                  f"INVALID ({err})", file=sys.stderr)
            continue

        for i, s in enumerate(batch):
            predicted_raw = term_map.get(str(i), [])
            predicted = [normalize(t) for t in predicted_raw]
            predicted = [t for t in predicted if t]
            pred_counter = Counter(predicted)
            result.total_predicted += len(predicted)

            gold_target = [e for e in s.entities if e.etype in target_types]
            gold_counter = Counter(normalize(e.surface) for e in gold_target)
            result.total_gold += len(gold_target)

            inter = gold_counter & pred_counter
            tp_s = sum(inter.values())
            result.tp += tp_s
            result.fn += sum(gold_counter.values()) - tp_s
            result.fp += sum(pred_counter.values()) - tp_s
            result.scored_sentences += 1

        print(f"    [{corpus_name}/{prompt_name}/{model}] batch {b}: ok "
              f"({len(batch)} sentences, cost=${uf['cost_usd']:.4f}, "
              f"{uf['duration_ms']:.0f}ms)", file=sys.stderr)

    return result


def summarize(r: ArmResult) -> dict:
    precision = r.tp / (r.tp + r.fp) if (r.tp + r.fp) else 0.0
    recall = r.tp / (r.tp + r.fn) if (r.tp + r.fn) else 0.0
    f1 = (2 * precision * recall / (precision + recall)) if (precision + recall) else 0.0

    n_calls = len(r.calls)
    n_ok = sum(1 for c in r.calls if c.ok)
    total_cost = sum(c.cost_usd for c in r.calls)
    total_in = sum(c.input_tokens for c in r.calls)
    total_out = sum(c.output_tokens for c in r.calls)
    total_thinking = sum(c.thinking_tokens for c in r.calls)
    total_cache_creation = sum(c.cache_creation_input_tokens for c in r.calls)
    total_cache_read = sum(c.cache_read_input_tokens for c in r.calls)
    durations = [c.duration_ms for c in r.calls]

    return {
        "corpus": r.corpus, "prompt": r.prompt_name, "model": r.model,
        "precision": precision, "recall": recall, "f1": f1,
        "tp": r.tp, "fp": r.fp, "fn": r.fn,
        "total_predicted": r.total_predicted, "total_gold": r.total_gold,
        "scored_sentences": r.scored_sentences,
        "excluded_sentences": r.excluded_sentences,
        "n_calls": n_calls, "n_calls_ok": n_ok,
        "n_calls_invalid": n_calls - n_ok,
        "reliability": (n_ok / n_calls) if n_calls else 0.0,
        "total_cost_usd": total_cost,
        "total_input_tokens": total_in,
        "total_output_tokens": total_out,
        "total_thinking_tokens": total_thinking,
        "total_cache_creation_input_tokens": total_cache_creation,
        "total_cache_read_input_tokens": total_cache_read,
        "mean_duration_ms": statistics.mean(durations) if durations else 0.0,
        "median_duration_ms": statistics.median(durations) if durations else 0.0,
        "min_duration_ms": min(durations) if durations else 0.0,
        "max_duration_ms": max(durations) if durations else 0.0,
    }


def fmt_pct(x: float) -> str:
    return f"{100*x:.1f}%"


def print_summary(s: dict) -> None:
    print(f"\n=== {s['corpus']} / prompt={s['prompt']} / model={s['model']} ===")
    print(f"  P={fmt_pct(s['precision'])} R={fmt_pct(s['recall'])} F1={fmt_pct(s['f1'])} "
          f"(tp={s['tp']} fp={s['fp']} fn={s['fn']}, "
          f"predicted={s['total_predicted']}, gold={s['total_gold']}, "
          f"scored_sentences={s['scored_sentences']}, excluded={s['excluded_sentences']})")
    print(f"  reliability: {s['n_calls_ok']}/{s['n_calls']} calls valid JSON "
          f"({fmt_pct(s['reliability'])})")
    print(f"  cost: ${s['total_cost_usd']:.4f} total over {s['n_calls']} calls "
          f"(${s['total_cost_usd']/s['n_calls']:.4f}/call avg)" if s['n_calls'] else "  cost: n/a")
    print(f"  tokens: in={s['total_input_tokens']} out={s['total_output_tokens']} "
          f"thinking={s['total_thinking_tokens']} "
          f"cache_creation={s['total_cache_creation_input_tokens']} "
          f"cache_read={s['total_cache_read_input_tokens']}")
    print(f"  latency: mean={s['mean_duration_ms']:.0f}ms median={s['median_duration_ms']:.0f}ms "
          f"min={s['min_duration_ms']:.0f}ms max={s['max_duration_ms']:.0f}ms")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="claude-haiku-4-5")
    ap.add_argument("--corpus", choices=["soner", "wnut", "both"], default="both")
    ap.add_argument("--prompt", choices=list(PROMPTS) + ["both"], default="both")
    ap.add_argument("--batches", type=int, default=10)
    ap.add_argument("--batch-size", type=int, default=10)
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--json-out", default=None)
    ap.add_argument("--calls-out", default=None, help="jsonl of every raw call record")
    args = ap.parse_args()

    corpora_to_run = []
    if args.corpus in ("soner", "both"):
        corpora_to_run.append(("soner", load_soner(), SONER_TARGET_TYPES))
    if args.corpus in ("wnut", "both"):
        corpora_to_run.append(("wnut", load_wnut(), WNUT_TARGET_TYPES))

    prompts_to_run = list(PROMPTS) if args.prompt == "both" else [args.prompt]

    all_summaries = []
    all_calls: list[CallRecord] = []

    for cname, sentences, target_types in corpora_to_run:
        for pname in prompts_to_run:
            print(f"\nRunning {cname}/{pname}/{args.model} "
                  f"({args.batches} batches x {args.batch_size} sentences)...", file=sys.stderr)
            r = run_arm(cname, sentences, target_types, pname, args.model,
                        args.batches, args.batch_size, args.seed)
            s = summarize(r)
            print_summary(s)
            all_summaries.append(s)
            all_calls.extend(r.calls)

    if args.json_out:
        with open(args.json_out, "w") as f:
            json.dump(all_summaries, f, indent=2, default=str)
        print(f"\nWrote {args.json_out}")

    if args.calls_out:
        with open(args.calls_out, "w") as f:
            for c in all_calls:
                f.write(json.dumps(c.__dict__) + "\n")
        print(f"Wrote {args.calls_out}")


if __name__ == "__main__":
    main()
