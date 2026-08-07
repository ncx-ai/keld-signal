# Prompted-LLM vs GLiNER2 study — results (part 1: cost)

Status: **cost measured, quality pending human adjudication.** Measured 2026-08-07.

Spec: `docs/superpowers/specs/2026-08-07-llm-classifier-study-design.md`
Plan: `docs/superpowers/plans/2026-08-07-llm-classifier-study.md`

This part reports what the candidates **cost**. It deliberately makes no quality
claim: that requires the blinded adjudication, which is the human's step.

## Host and corpus

- Host: 20 cores, 30.8 GB RAM, Linux 6.18 (Manjaro). **Desktop-class — a target
  laptop with 8-12 cores will be slower.**
- `llama-cpp b10221-1`, `ggml 0.17.0-1`.
- Corpus: 464 real Claude Code transcripts → 1,341 windows → **200 sampled**
  (`seed 7`, K=8).
- Window size, measured: **p50 523, p95 1,000, max 1,376 word tokens**
  (chars p50 1,883 / p95 3,603 / max 4,957).

## Headline: RAM is satisfiable; the 4B is not

All rows CPU-only (`--device none`), `validity = 1.000`, `ctx 2048`, `parallel 1`.

| Model | Config | Peak RSS | p50/window | vs GLiNER2 RAM |
|---|---|---:|---:|---|
| Qwen3-4B-Instruct-2507 | b256/ub64, t8 | 5,500 MB | 46.7 s | **1.9x worse** |
| Qwen3-1.7B (no-think) | b256/ub64, t8 | 2,864 MB | 14.6 s | parity (-30 MB) |
| Qwen3-1.7B (no-think) | b256/ub64, t2 | 2,862 MB | 35.0 s | parity |
| **Qwen3-0.6B** | b256/ub64, t8 | **1,757 MB** | 9.0 s | **39% less** |
| **Qwen3-0.6B** | b128/ub32, t4 | **1,752 MB** | 14.9 s | **39% less** |
| *GLiNER2 large-v1 (control)* | *production* | *2,894 MB* | *6.8 s* | — |

GLiNER2 reference, measured live: worker 2,861 MB + parent 32 MB = **2,894 MB**,
`peak_rss_mb` 3,817 MB, `model_cost_mb` 2,731 MB.

**`--batch-size` is the dominant memory knob, not model size.** It took the 4B
from 8,428 MB to 5,500 MB and is what brought the small models under budget.
Thread count moves RSS by ~5 MB while changing speed 2-4x, so threads are a pure
CPU-citizenship dial once wall-clock is not on the critical path.

## Latency is not the constraint; throughput comfortably fits

Enrichment is a background, asynchronous publisher, so the binding question is
whether service rate exceeds arrival rate. Measured from the same 464 transcripts:

- **1,347 prompts over 28 active days**: p50 **45/day**, p90 91, max 138.
- At 45 s/prompt: p50 day = **0.6 h** of compute, worst day = 1.7 h.
- At 90 s/prompt (laptop-speed estimate): p50 = **1.1 h**, worst day = 3.5 h.

Spread across a working day the queue drains with wide margin. What remains is not
delay but **CPU citizenship** — 1-3.5 h/day of multi-core work means fans and
battery, which users notice even when latency is invisible. That is what the
existing governor and thread scaler are for, and `--threads 2` costs only speed.

⚠️ **Coupled config change required.** `KELD_ENRICH_PASS_TIMEOUT` (30 s) would kill
these calls. `AGENTS.md` pins the invariant that `KELD_ENRICH_JOB_TIMEOUT` must stay
above `passes x PASS_TIMEOUT`, enforced by a unit test — so raising the pass budget
to ~180 s forces the job backstop above 6 min, past its current 5 min default.

## GLiNER2's own numbers, for the record

The control is **slower than every LLM arm except the 4B**: p50 6,830 ms, p95
17,793 ms, **max 40,383 ms**, 7.6 s/window over 200 windows. Its max already
exceeds the 30 s pass deadline, i.e. that job publishes `pipeline_status:"partial"`
in production today. The LLM's advantage in *time* is real; its cost is RAM only.

## Measurement errors made and corrected

Recorded because each was caught only by checking real output, and each would have
produced a confidently wrong recommendation.

1. **Benchmarked a GPU while claiming CPU viability.** The Arch `llama-cpp` build
   ships a **Vulkan** backend and silently used an RTX 5060. Prompt eval was 2,914
   tok/s; CPU-only is **27-35 tok/s (~90x slower)**. Every early latency figure was
   GPU-accelerated. Fixed with `--device none`, verified by compute rate and by
   `nvidia-smi` showing idle VRAM.
2. **Generalised steady state from an 8-request sample.** RSS read 1,841 MB after 8
   windows and **8,758 MB** after 200. The plateau is only reached ~60 s in. This is
   the same error `AGENTS.md` documents for the sidecar: *"an instantaneous sample is
   exactly what made the oscillation look healthy."*
3. **Blamed glibc malloc arenas.** `MALLOC_ARENA_MAX=2` changed nothing (8,629 MB).
   A 2,565 MB reading that appeared to confirm it was another too-early sample from a
   run that had failed fast on 503s.
4. **Claimed mmap made it evictable.** `smaps_rollup` showed **Anonymous 8,154 MB vs
   Pss_File 398 MB** — real heap, only swappable. Retracted.
5. **Ran an uncapped control.** The encoder arm used a bare sidecar client, so no
   `max_len` rode the request, and gliner2's own default is *no truncation*. The
   sidecar reported **`kills.hard: 45`, `crash: 45`** — the worker was killed
   mid-inference, so the control's answers were failures, not classifications. Now
   binds `lenstat.Cap()` exactly as `daemon.go:246` does.
6. **Arm B was not a size ablation.** `Qwen3-1.7B` is a hybrid *thinking* model with
   thinking ON by default: **512 reasoning tokens** to answer a one-word question,
   then discarded. Disabling it took that arm 4,759 → **756 ms** and 2,726 → 1,507 MB.

## Candidate assessment

- **Qwen3-4B — rejected on RAM.** 5,500 MB CPU-only even tuned, ~1.9x the control,
  against 4-8 GB of free headroom on a 16 GB laptop.
- **Qwen3-1.7B — viable at parity.** 2,864 MB, no footprint win but no regression.
- **Qwen3-0.6B — viable and *reduces* footprint.** 1,757 MB, 39% below the control.
- **BitNet b1.58-2B-4T — deferred, conditionally interesting.** Its 0.4 GB figure is
  *non-embedding weights*, and our data shows runtime buffers dominate (1.03 GB of
  1.7B weights → 2,864 MB), so it would likely land near the 0.6B rather than at
  0.4 GB. Its real pitch is 2B-class *quality* at that footprint. Costs: requires
  **`bitnet.cpp`**, a second native runtime to build/sign/notarize (its GGUF is
  [not llama.cpp-compatible](https://github.com/ggml-org/llama.cpp/issues/12997));
  Microsoft designates it research-only, not for production; and it is **unverified
  whether its server supports `response_format: json_schema`**, which this entire
  design depends on. Evaluate only if 0.6B quality proves inadequate.

## Deployment envelope, if quality holds

`--device none --ctx-size 2048 --parallel 1 --batch-size 256 --ubatch-size 64
--threads 2 --chat-template-kwargs '{"enable_thinking":false}'`

Sizing note: `ctx 2048` is sound because the measured max window is 1,376 tokens
plus ~600 of label-menu scaffolding. It is **not** slack to spend — the
agentic-scale payloads in `2026-07-24-agentic-scale-input-bounding.md` would blow it.

## Not yet answered

**Quality.** The blinded adjudication set is the measurement and is the human's
step; filling it in automatically would make the study worthless. Note also a
corpus caveat that will shape how the result should be read: this corpus is one
engineer's Claude Code sessions, so `domain` is **198/200 "software"** and
`personal` 198/200 "work". Those two facets have almost no variance here, and any
`domain` win rate will mostly reflect GLiNER2 noise rather than LLM discernment.
The informative facets are **`task_type` (9 distinct), `activity_type` (6), and
`subcategory` (15)**.
