"""Run: cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_attribution_quality.py

OPT-IN, like sidecar/loadtest — loads the real Qwen3-Embedding-0.6B encoder and the
real Gemma-4-E2B verifier and takes minutes. Exits 0 immediately with a `SKIPPED:`
line unless `KELD_ATTRIBUTION_EVAL=1` is set, and again (still exit 0) if the real
model weights aren't reachable — absent weights are a stated skip here, never a
fetch and never a failure, matching every other opt-in eval in this codebase.

    KELD_ATTRIBUTION_EVAL=1 KELD_VERIFIER_GGUF=/path/to/gemma-4-E2B-it-Q4_K_M.gguf \\
        PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_attribution_quality.py

The Qwen3-Embedding-0.6B encoder is found automatically: `KELD_TEXTEMBED_DIR` if set,
else the newest snapshot under the local Hugging Face cache
(`~/.cache/huggingface/hub/models--Qwen--Qwen3-Embedding-0.6B/snapshots/*`) — the
same weights `attribution.attribute_block`'s real encoder loads in production,
just resolved from the cache the original benchmark already populated rather than
`KELD_TEXTEMBED_DIR`'s usual `~/.keld/models/qwen3-embedding-0.6b` layout. The
verifier GGUF has no such auto-discovery — it is not cached under a fixed path the
way the encoder is, so `KELD_VERIFIER_GGUF` must be set explicitly (see
`verifier.weights_path`).

## What this ports

Fixtures (`evaldata/attribution/{projects,conversations}.json`) are copied verbatim
from `~/projects/keld/embedding-experiment/data/`: 10 declared projects and 100
labeled conversations, each carrying `gold_projects`, `difficulty`,
`difficulty_reason`, `metadata` (cwd/git_branch/repo — may be empty), and
`messages` (role + content). Each conversation maps to `attribute_block`'s two
inputs: `texts` is its user-role message contents (matching production's
`_span_texts`, which is USER-stream only), `dims` is its `metadata` dict verbatim.

⚠️ **Field-name seam**: the experiment's project schema calls its ticket-prefix
field `jira_key`; this codebase's is `ticket_key` (`settings.RemoteProject`,
`attribution.metadata_boost`). Renamed on load below, values unchanged — everything
else (`id`/`title`/`team`/`description`/`repos`/`keywords`) matches as-is.

`evaluate_assignments` below is `embedding-experiment/src/metrics.py`'s function of
the same name, ported inline (~40 lines) rather than imported, since this script has
no dependency on a sibling repo at runtime: micro precision/recall/F1 pooled over
every (conversation, project) pair, plus a per-difficulty breakdown. This is the
exact arithmetic the 0.929 benchmark number was computed with.

## Wiring to the real pipeline

`attribution.attribute_block(texts, dims, encoder, verifier_obj)` is exercised
directly (no HTTP, no daemon) against:
  - `textembed.Encoder()` wrapped in `_EncoderAdapter` — the same shape adaptation
    `app/main.py`'s own `_EncoderAdapter` does, because the real encoder returns
    `(vectors, status)` and never raises, while `score_block` wants
    `.encode(texts) -> [vec, ...]`. One persistent encoder child across all 100
    conversations plus the 10 project docs, exactly like production's single
    warm child.
  - `verifier.Verifier()` — loads the GGUF directly in this process (no worker
    child, no recycling machinery), since a one-shot 100-conversation eval has no
    resource-safety story to exercise; `Verifier.verify()` already matches
    `apply_verifier`'s expected interface.

## Measured result — BELOW the 0.85 floor, and why

F1=0.823 (precision=0.760, recall=0.896), measured 2026-09-01 on this machine
(Apple Silicon, CPU-only): wall clock 1015.2s (~16.9 min) for all 100
conversations — embed 206.8s total, verify 808.3s total (encoder: the local HF
cache's Qwen3-Embedding-0.6B snapshot; verifier: KELD_VERIFIER_GGUF pointed at
~/projects/keld/embedding-experiment/models/gemma-4-E2B-it-Q4_K_M.gguf). Per
difficulty: easy=0.847, medium=0.872, hard=0.691. 35 of 100 conversations
mispredicted.

⚠️ **This is a real, reproduced measurement, not a fluke or a wiring bug in this
script** — it was investigated, not just reported. The benchmark's own
`hybrid+e2b` scored precision=0.933/recall=0.925/f1=0.929 (`embedding-experiment/
report.md`) on the SAME 100 conversations under a DIFFERENT input: its `hybrid`
strategy scores `max(chunked, distilled) + boost`, where `chunked` embeds the
FULL conversation text (user *and* assistant messages,
`dataset.conversation_text`) and its verifier call is handed that same full text
(`_digest = conversation_text`). Production's `attribute_block` — and this eval,
faithfully porting it — never sees assistant text: `_span_texts` (`app/main.py`)
is USER-stream only, by design ("scoring a project against the model's own prose
would attribute work to whatever the assistant happened to name"). Spot-checked
several failures against the fixtures directly: e.g. `conv_074` is a
`difficulty: hard` **"no-project trap"** by design (`difficulty_reason`: "generic
RAG/embeddings tutoring — closest neighbor is the chatbot project but this is
conceptual learning", `gold_projects: []`) where the user's own two messages
literally contain the words "RAG" and "embeddings" — `proj_chatbot`'s own
declared keywords — with no assistant text and no metadata to disambiguate.
The benchmark's richer input (assistant text for both the embedding score and
the verifier's judgment) gives it more context to catch traps like this one;
production's narrower, privacy-preserving input structurally cannot. All three
difficulty bands measure below the benchmark's own per-difficulty numbers
(easy 1.0→0.847, medium 0.981→0.872, hard 0.745→0.691), consistent with "less
signal available system-wide" rather than one localized defect.

**This means the ported PRODUCTION pipeline — not this script — measures below
the spec's 0.85 quality gate on this benchmark**, a real gap between what was
benchmarked (richer, assistant-text-inclusive input) and what `attribute_block`
actually runs on (user-only text, an earlier task's deliberate privacy
decision).

⚠️ **RE-RUN 2026-09-02: THE FLOOR NOW FAILS, AND THE 0.823 ABOVE DESCRIBES A
PIPELINE THAT NO LONGER EXISTS.** Measured today, same machine, with the
`NULL_DOC` reword of the same date: **f1 0.779 (precision 0.854, recall 0.717)**
— easy 0.980, medium 0.796, hard 0.522, 36/100 mispredicted, wall 412.3s
(embed 201.9s, verify 210.3s). That is BELOW `F1_FLOOR` and the assert trips.

**The cause is not the reword.** `git` settles the ordering: 0.823 was measured
by `f89a8ac` (2026-09-01) under the old ABSOLUTE-threshold rule, and
`f472daa` (2026-09-02) replaced that rule with rank-and-margin
(`cut = max(null, top - MARGIN)`) — **this eval was never re-run after it.** The
rule change was validated on 21 REAL blocks instead (trust 53% → 74%, exact
blocks 6/21 → 11/21, coverage 86% → 76%; see
`docs/notes/whats-next-attribution.md`), so the fixture gate has been failing
since that commit with nobody able to see it: the number it asserts against
describes the superseded rule. The paragraph below still names
`attribution.THRESHOLD` — a constant that has been DELETED — which is the same
"doc asserting something the code does not do" failure the ⚠️ below it corrects
one level up.
Today's precision/recall confirm the mechanism rather than merely coexisting with
it: **0.760/0.896 → 0.854/0.717**, fewer and better assignments, which is exactly
the trade rank-and-margin was chosen for. Micro-F1 penalises it because these
fixtures average over multi-label recall; the customer slice moved the other way
(trust 77% → **85%**, clean blocks → **83%**).

**The reword's own effect is separately measured and POSITIVE**, on a controlled
comparison rather than on this noisy arm: re-encoding only the null string against
shared, cached conversation and project vectors gives embedding-only
**f1 0.724 → 0.740, precision 0.774 → 0.787**, fixing conv_046 and breaking
nothing. Today's run reports 0.734 for that same embedding-only arm.

**RE-RUN 2026-09-03, LATER THE SAME DAY, ON THE CONFIGURATION THAT NOW SHIPS —
whole block (user + assistant turns), MEAN-pooled, centred per stream with the
baseline primed over the fixtures, NO verifier: f1 0.811 (precision 0.760,
recall 0.868), easy 0.867 / medium 0.870 / hard 0.644, 34/100 mispredicted,
coverage 94%, trust 76%, clean blocks 78%; 80 of the 82 conversations with a
real project received a correct label (98%). The gate is GREEN again, without
the 3 GB model that contributed ~+0.05 to the 0.823 above.** Read the three
rows together: 0.823 (user-only, absolute threshold, verifier) -> 0.779
(user-only, rank-and-margin, verifier) -> 0.811 (whole block, mean, centred,
none). What the pipeline lost by dropping the verifier it more than recovered
by reading the assistant's turns — the input the external benchmark always had
and this eval never fed it. Priming the baseline over the very conversations
then scored is in-sample; on real blocks, leave-one-out offsets scored
identically to in-sample (means over hundreds of vectors), so the flattery is
negligible, and it is stated rather than hidden. `F1_FLOOR` is re-baselined to
0.79 from that 0.811 — the same ~0.02 buffer the original floor sat under its
own measurement, because a floor a hundredth under the number fails on encoder
drift alone (~0.006 between two runs of one arm).

⚠️ **`F1_FLOOR` WAS DELIBERATELY LEFT AT 0.80 AND FAILING** (resolved the same
day, above — kept as the record of why it was not simply lowered to fit).
Re-baselining a
quality gate is a measurement, not a paperwork edit, and the one measurement that
would justify a new number — this eval WITH the verifier under the OLD wording,
today — has not been taken: the machine's swap was exhausted (16.6 GB of 18.4 GB,
~750 MB free) by the model loads of the runs above, and spawning another 1.7 GB
encoder plus 3 GB verifier on it was refused rather than risked. Estimating it
from the pieces (0.724 embedding-only + the verifier's measured +0.046) lands
~0.77, i.e. also below the floor — an ESTIMATE, named as one, and not a basis for
moving a gate. Run
`app/test_attribution_quality.py` with `attribution.NULL_DOC` patched to the
pre-2026-09-02 wording on an idle machine, then set the floor from the pair.

⚠️ **THE ASSERT BELOW WAS `F1_FLOOR = 0.80`, NOT THE SPEC'S 0.85, AND THIS
PARAGRAPH USED TO CLAIM OTHERWISE.** It read "left as spec'd (§6: micro-F1 ≥
0.85)" while the constant beside it was already 0.80 — a doc asserting a gate
the code does not enforce, which is worse than either number on its own,
because a reader takes the prose as the contract and never looks. The 0.85 gate
is NOT met by the ported production pipeline: it measures 0.823 here, and the
0.80 floor is a regression tripwire sitting just below that measurement, not a
quality target (see F1_FLOOR's own comment). The shortfall is real and
unresolved — it is a finding for whoever owns the attribution design to weigh
(recalibrate THRESHOLD/BAND for the user-only-text regime, widen the input, or
accept a lower bar for this narrower contract). Reporting it accurately is the
point; the previous wording hid it behind a bar nothing checked. A future
re-run after such a change should update the numbers above rather than silently
drift.
"""
import glob
import json
import os
import sys
import time
from collections import defaultdict

SKIP_ENV = "KELD_ATTRIBUTION_EVAL"
DATA_DIR = os.path.join(os.path.dirname(__file__), "evaldata", "attribution")
# ⚠️ A REGRESSION TRIPWIRE, NOT A QUALITY TARGET, and the difference matters.
#
# Measured 2026-09-01 on the synthetic fixtures in evaldata/attribution: micro-F1
# **0.823** (precision 0.760, recall 0.896; easy 0.847, medium 0.872, hard 0.691),
# ~17 minutes wall clock with the real Qwen3-Embedding-0.6B encoder and the
# Gemma-4-E2B verifier. The floor sits a little BELOW that measurement so a small
# platform or library difference in the encoder cannot fail the suite, while any
# real regression in chunking, boost weights, threshold or the verifier prompt
# still trips it.
#
# What this number is NOT: evidence that attribution is accurate. The fixtures are
# invented projects and invented conversations with hand-assigned gold labels, so a
# passing run proves only that this pipeline still behaves as it did when last
# measured. Real accuracy is answered by docs/attribution-smoke.md against real
# projects — and the decision rule's own constants (attribution.MARGIN 0.08 and
# VERIFY_HALO 0.04) are starting points fitted to a prototype's score distribution
# and should be recalibrated on real blocks before anyone reads much into either
# number.
#
# ⚠️ **THIS COMMENT NAMED `attribution.THRESHOLD, 0.49` UNTIL 2026-09-02, AND THAT
# CONSTANT HAD ALREADY BEEN DELETED.** `f472daa` replaced the absolute threshold
# with rank-and-margin and did not re-run this eval, so the floor below, the 0.823
# it sits under, and this pointer all described the superseded rule at once — which
# is why the gate could fail unnoticed. See the re-run block in the docstring.
F1_FLOOR = 0.79   # 0.811 measured 2026-09-03: whole block, mean, centred per stream, no verifier


def _skip(msg):
    print(f"SKIPPED: {msg}")
    sys.exit(0)


def _load_projects():
    raw = json.loads(open(os.path.join(DATA_DIR, "projects.json")).read())
    out = []
    for p in raw:
        q = dict(p)
        q["ticket_key"] = q.pop("jira_key", None)  # experiment's field name -> ours
        out.append(q)
    return out


def _load_conversations():
    return json.loads(open(os.path.join(DATA_DIR, "conversations.json")).read())


def _user_texts(conv):
    return [m["content"] for m in conv["messages"] if m["role"] == "user"]


def _asst_texts(conv):
    return [m["content"] for m in conv["messages"] if m["role"] == "assistant"]


def _block_texts(conv):
    """The whole conversation in production order — user turns first, then assistant —
    exactly what `_span_texts` returns for a real block since 2026-09-03. This is ALSO the
    input the external benchmark scored 0.929 on; the 0.823 recorded above came from feeding
    the pipeline user turns alone, a rule that was measured and reversed."""
    return _user_texts(conv) + _asst_texts(conv)


class _MemoEncoder:
    """Encode each distinct text once. The eval scores every conversation twice — a priming
    pass that only OBSERVES message vectors into the centring baseline, then the scored pass —
    and without a memo the second pass would repay ~200 s of encoding for nothing."""

    def __init__(self, inner):
        self.inner, self.seen = inner, {}

    def encode(self, texts):
        missing = [t for t in texts if t not in self.seen]
        if missing:
            self.seen.update(zip(missing, self.inner.encode(missing)))
        return [self.seen[t] for t in texts]


# ---- ported from embedding-experiment/src/metrics.py:evaluate_assignments --------
def evaluate_assignments(predictions, conversations):
    """`predictions[i]` is the set of project ids `attribute_block` assigned to
    `conversations[i]`. Micro precision/recall/F1 pooled over every
    (conversation, project) pair, plus a per-difficulty F1 breakdown and the list
    of conversations whose prediction didn't exactly match gold. Ported verbatim
    (arithmetic unchanged) from the benchmark's own metric, so this eval measures
    the same quantity the 0.929 number does."""
    tp = fp = fn = 0
    per_difficulty = defaultdict(lambda: [0, 0, 0])  # tp, fp, fn
    failures = []
    for pred, conv in zip(predictions, conversations):
        gold = set(conv["gold_projects"])
        c_tp, c_fp, c_fn = len(pred & gold), len(pred - gold), len(gold - pred)
        tp += c_tp
        fp += c_fp
        fn += c_fn
        d = per_difficulty[conv["difficulty"]]
        d[0] += c_tp
        d[1] += c_fp
        d[2] += c_fn
        if pred != gold:
            failures.append((conv["id"], sorted(gold), sorted(pred)))

    def prf(tp, fp, fn):
        p = tp / (tp + fp) if tp + fp else (1.0 if fn == 0 else 0.0)
        r = tp / (tp + fn) if tp + fn else 1.0
        f = 2 * p * r / (p + r) if p + r else 0.0
        return p, r, f

    precision, recall, f1 = prf(tp, fp, fn)
    per_diff_f1 = {k: prf(*v)[2] for k, v in per_difficulty.items()}
    return {"precision": precision, "recall": recall, "f1": f1,
            "per_difficulty": per_diff_f1, "failures": failures}


# ---- wiring the real pipeline -----------------------------------------------------
def _qwen_weights_dir():
    """`KELD_TEXTEMBED_DIR` if it already points at a real directory, else the
    newest snapshot of the local HF cache's Qwen3-Embedding-0.6B download — the
    same weights, just found where the original benchmark run already put them
    rather than at `textembed.weights_dir()`'s usual `~/.keld/models` layout."""
    explicit = os.environ.get("KELD_TEXTEMBED_DIR")
    if explicit and os.path.isdir(explicit):
        return explicit
    hits = sorted(glob.glob(os.path.expanduser(
        "~/.cache/huggingface/hub/models--Qwen--Qwen3-Embedding-0.6B/snapshots/*")))
    return hits[-1] if hits else None


class _EncoderAdapter:
    """`textembed.Encoder` in the shape `attribution.score_block` wants:
    `.encode(texts) -> [vec, ...]`. Mirrors `app/main.py`'s own `_EncoderAdapter` —
    production's encoder returns `(vectors, status)` and never raises, so the
    translation to "raise if not ok" lives here rather than inside `attribution`,
    exactly as it does on the real request path."""

    def __init__(self, child):
        self._child = child

    def encode(self, texts):
        from app.analysis import textembed
        vectors, status = self._child.encode(texts)
        if status != textembed.STATUS_OK or len(vectors) != len(texts):
            raise RuntimeError(f"encoder not ready: {status}")
        return vectors


def main():
    if os.environ.get(SKIP_ENV, "") != "1":
        _skip(f"set {SKIP_ENV}=1 to run the real-model attribution quality eval "
              "(loads multi-gigabyte models, takes minutes)")

    weights = _qwen_weights_dir()
    if not weights:
        _skip("no Qwen3-Embedding-0.6B weights found (KELD_TEXTEMBED_DIR unset and "
              "no snapshot under ~/.cache/huggingface/hub) — this eval never "
              "downloads")

    # The verifier is OFF in production by default (2026-09-03) and is not part of the
    # shipped decision, so the eval's default run measures the shipped configuration and
    # loads no GGUF. KELD_ATTRIBUTION_EVAL_VERIFIER=1 adds the A/B arm back.
    from app import verifier as verifier_mod
    want_verifier = os.environ.get("KELD_ATTRIBUTION_EVAL_VERIFIER", "") == "1"
    if want_verifier and not verifier_mod.weights_path():
        _skip("KELD_ATTRIBUTION_EVAL_VERIFIER=1 but no verifier GGUF found — set "
              "KELD_VERIFIER_GGUF to a real gemma-4-E2B-it-Q4_K_M.gguf path")

    os.environ["KELD_TEXTEMBED"] = "1"
    os.environ["KELD_TEXTEMBED_DIR"] = weights
    os.environ.setdefault("KELD_SIDECAR_MAX_THREADS", "2")

    from app.analysis import attribution, textembed

    projects = _load_projects()
    conversations = _load_conversations()
    attribution.set_projects(projects)

    encoder = _MemoEncoder(_EncoderAdapter(textembed.Encoder()))
    verifier_obj = verifier_mod.Verifier() if want_verifier else None

    # PRIMING PASS: fold every conversation's message vectors into a fresh centring baseline
    # (attribution.Offsets), so the scored pass runs with the gate met — the state a machine
    # is in after its first ~50 messages. Nothing is decided here; observe() is the only
    # effect, and the memo makes the second pass's encodes free.
    offsets = attribution.Offsets(None)
    t_prime = time.time()
    for conv in conversations:
        attribution.score_block(_block_texts(conv), conv["metadata"], encoder, offsets,
                                n_user=len(_user_texts(conv)), block_key=conv["id"])
    prime_s = time.time() - t_prime
    keys = [attribution.Offsets.key(attribution.project_doc(p), st) for p in projects
            for st in (attribution.USER_STREAM, attribution.ASST_STREAM)] + \
           [attribution.Offsets.key(attribution.NULL_DOC, st)
            for st in (attribution.USER_STREAM, attribution.ASST_STREAM)]
    print(f"centring baseline primed over {offsets.count(keys)} messages in {prime_s:.0f}s "
          f"(gate {attribution.MIN_BACKGROUND})")

    predictions = []
    # THE VERIFIER A/B, from the same single pass. The embedding-only decision
    # already exists inside every run — it is `assigned` before the verdicts
    # are applied — so both arms cost one set of model calls. This is the
    # instrument for "is Gemma E2B worth its runtime": if the two arms score
    # the same, the verifier's minutes buy nothing and it should be skipped.
    cut_only_predictions = []
    flips_good = flips_bad = 0
    embed_ms_total = verify_ms_total = 0
    t0 = time.time()
    centred_blocks = 0
    for conv in conversations:
        texts = _block_texts(conv)
        t_embed = time.time()
        # Same block_key as the priming pass, so the scored pass is a retry in the baseline's
        # eyes and folds nothing in a second time.
        scores, borderline, assigned, used, _tv, centring = attribution.score_block(
            texts, conv["metadata"], encoder, offsets, n_user=len(_user_texts(conv)),
            block_key=conv["id"])
        centred_blocks += centring["applied"]
        embed_ms_total += int((time.time() - t_embed) * 1000)
        cut_only = set(assigned)
        cut_only_predictions.append(cut_only)
        t_verify = time.time()
        overrides, pairs, _ms = attribution.apply_verifier(
            _user_texts(conv), conv["metadata"], scores, borderline, verifier_obj)
        verify_ms_total += int((time.time() - t_verify) * 1000)
        # Mirror attribute_block's assembly: a verdict wins in both directions.
        final = {pid for pid in scores
                 if overrides.get(pid, pid in cut_only)}
        predictions.append(final)
        gold = set(conv["gold_projects"])
        for pid, verdict in overrides.items():
            if verdict == (pid in cut_only):
                continue  # the verdict agreed with the cut: no flip
            if (pid in gold) == verdict:
                flips_good += 1
            else:
                flips_bad += 1
    wall_s = time.time() - t0

    result = evaluate_assignments(predictions, conversations)
    cut_only_result = evaluate_assignments(cut_only_predictions, conversations)

    print(f"{'conv_id':<12} {'difficulty':<8} {'gold':<28} {'predicted':<28} match")
    for conv, pred in zip(conversations, predictions):
        gold = set(conv["gold_projects"])
        mark = "OK" if pred == gold else "FAIL"
        print(f"{conv['id']:<12} {conv['difficulty']:<8} "
              f"{','.join(sorted(gold)) or '-':<28} {','.join(sorted(pred)) or '-':<28} {mark}")

    print()
    print(f"precision={result['precision']:.3f} recall={result['recall']:.3f} "
          f"f1={result['f1']:.3f}")
    print("per-difficulty f1: " + ", ".join(
        f"{k}={v:.3f}" for k, v in sorted(result["per_difficulty"].items())))
    print(f"wall={wall_s:.1f}s embed={embed_ms_total}ms verify={verify_ms_total}ms "
          f"failures={len(result['failures'])}/{len(conversations)} "
          f"centred={centred_blocks}/{len(conversations)} "
          f"scoring={attribution.MODEL_VERSIONS['scoring']}")

    # ---- Verifier A/B: what did Gemma E2B's minutes actually buy? ----
    # Only when the verifier arm was asked for. With it off (the shipped default), `result`
    # IS the embedding-only result and there is nothing to compare.
    if want_verifier:
        print()
        print("verifier A/B (same pass, embedding-only vs after-verdicts):")
        print(f"  embedding-only: f1={cut_only_result['f1']:.3f} "
              f"p={cut_only_result['precision']:.3f} r={cut_only_result['recall']:.3f}   "
              f"cost {embed_ms_total / 1000:.0f}s")
        print(f"  with verifier:  f1={result['f1']:.3f} "
              f"p={result['precision']:.3f} r={result['recall']:.3f}   "
              f"cost +{verify_ms_total / 1000:.0f}s")
        print(f"  verdicts that changed the decision: {flips_good} corrected it, "
              f"{flips_bad} broke it "
              f"(delta f1 {result['f1'] - cut_only_result['f1']:+.3f})")

    # ---- The customer-facing layer: the SAME measurements, re-sliced. ----
    # Micro P/R/F1 above is the internal instrument (it sees every missed
    # second label). A customer experiences exactly two things: an unlabeled
    # block (COVERAGE) or a label they can check (TRUST). Both derive from the
    # identical predictions — this is a re-slicing, never a softer measurement,
    # and if the two layers ever disagree in spirit, the internal one is the
    # one that is right.
    #   coverage        — share of blocks given at least one label. Gold plays
    #                     no part: it is what a user SEES.
    #   trust           — of the labels shown, how many were correct (micro
    #                     precision restated per shown label).
    #   clean blocks    — of the blocks we labeled, how many carried no wrong
    #                     label at all (a block showing one right + one wrong
    #                     label reads as wrong to the person billed by it).
    labeled = [(c, p) for c, p in zip(conversations, predictions) if p]
    coverage = len(labeled) / len(conversations)
    shown = sum(len(p) for _, p in labeled)
    shown_right = sum(len(p & set(c["gold_projects"])) for c, p in labeled)
    clean = sum(1 for c, p in labeled if p <= set(c["gold_projects"]))
    print()
    print("customer layer (same data, re-sliced):")
    print(f"  coverage: {coverage:.0%} of blocks auto-attributed "
          f"({len(labeled)}/{len(conversations)}; the rest stated as unassigned)")
    print(f"  trust: {shown_right}/{shown} shown labels correct "
          f"({shown_right / shown:.0%})" if shown else "  trust: n/a (nothing labeled)")
    print(f"  clean blocks: {clean}/{len(labeled)} labeled blocks carry no wrong label "
          f"({clean / len(labeled):.0%})" if labeled else "")

    assert result["f1"] >= F1_FLOOR, (
        f"micro-F1 {result['f1']:.3f} below the {F1_FLOOR} floor for the shipped "
        f"configuration ({attribution.MODEL_VERSIONS['scoring']}"
        f"{', verifier on' if want_verifier else ', no verifier'}) — see F1_FLOOR's "
        "comment for what was measured and when")
    print(f"test_attribution_quality: PASS (f1={result['f1']:.3f} >= {F1_FLOOR})")


if __name__ == "__main__":
    main()
