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

> ⚠️ **CORRECTION 2026-08-07 (supersedes the RAM column below).** Every peak-RSS
> figure in the next table came from a **6-window probe** and is invalid. Measured
> over a sustained run, **Qwen3-0.6B — which probed at 1,757 MB — reached 9,157 MB**,
> and the 4B reached 8,428 MB. RSS growth is **not model-size dependent**: it tracks
> **request count** and converges near ~9 GB for every model.
>
> **Cause, identified:** `llama-server --cache-ram` defaults to **8192 MiB**. It is a
> prompt-prefix cache that fills as distinct prompts arrive and plateaus at its
> budget, so total RSS ≈ weights + ~8 GB of cache regardless of model.
>
> Our prompts share a large common prefix (the label menus), so the cache was doing
> real work: capping it to 0 costs ~2.5x speed (9.0 -> 22.4 s/window on 0.6B).
> Acceptable, since latency is not on the critical path — but the RAM figures must be
> re-measured with `--cache-ram` bounded before any of them are trusted. A sweep of
> `--cache-ram 0` and `512` over sustained 80-window runs is in progress.
>
> **This is the same error three times: peaks read before the plateau.** The
> plateau needs ~60 s / dozens of requests. Short probes are worthless for RSS, and
> that is now the standing rule for this study.
>
> ### Resolved: `--cache-ram 512` is the deployable setting
>
> Measured on Qwen3-0.6B, CPU-only, **80 sustained windows per config**, RSS sampled
> every 5 s throughout (so these numbers are past the plateau, unlike the table below):
>
> | 0.6B CPU-only | RSS | Peak | p50 | p95 | validity |
> |---|---:|---:|---:|---:|---|
> | `--cache-ram 8192` (default) | 9,157 MB | 9,197 MB | 9.0 s | — | 1.000 |
> | `--cache-ram 0` | 1,007 MB | 1,018 MB | 19.9 s | 29.1 s | 1.000 |
> | **`--cache-ram 512`** | **1,463 MB** | **1,520 MB** | **8.3 s** | **12.2 s** | 1.000 |
> | *GLiNER2 (control)* | *2,894 MB* | *3,817 MB* | *6.8 s* | *17.8 s* | *1.000* |
>
> Both bounded configs are genuinely flat — `cache-ram 0` held 1,005 MB unchanged for
> 7 minutes; `512` oscillated 1,439-1,510 MB. Neither drifts, so no recycle apparatus
> is needed (contrast the sidecar, which needs one).
>
> **`--cache-ram 512` wins:** +456 MB over disabling the cache buys back nearly all
> the speed, because our prompts share a long common prefix (the label menus). Versus
> the shipped control it is **49% less RAM with a BETTER p95** (12.2 s vs 17.8 s) and
> only a marginally worse p50. On cost, this is strictly better than what ships today
> on every axis except p50, which is not on the critical path for a background
> publisher.

## Superseded first pass (RAM figures INVALID — retained for the record)

All rows CPU-only (`--device none`), `validity = 1.000`, `ctx 2048`, `parallel 1`,
**`--cache-ram` left at its 8192 MiB default and RSS read after only 6 windows**.
The latency column is sound; **every RAM figure here is void** — see the correction
above for measured, plateaued numbers.

| Model | Config | ~~Peak RSS~~ (invalid) | p50/window |
|---|---|---:|---:|
| Qwen3-4B-Instruct-2507 | b256/ub64, t8 | ~~5,500 MB~~ | 46.7 s |
| Qwen3-1.7B (no-think) | b256/ub64, t8 | ~~2,864 MB~~ | 14.6 s |
| Qwen3-1.7B (no-think) | b256/ub64, t2 | ~~2,862 MB~~ | 35.0 s |
| Qwen3-0.6B | b256/ub64, t8 | ~~1,757 MB~~ | 9.0 s |
| Qwen3-0.6B | b128/ub32, t4 | ~~1,752 MB~~ | 14.9 s |

GLiNER2 reference, measured live and valid: worker 2,861 MB + parent 32 MB =
**2,894 MB**, `peak_rss_mb` 3,817 MB, `model_cost_mb` 2,731 MB.

**`--cache-ram` is the dominant memory knob — not model size, and not
`--batch-size`.** An earlier draft credited `--batch-size`, on the strength of the
4B appearing to drop from 8,428 to 5,500 MB; both readings were pre-plateau and the
apparent effect was an artifact. With `--cache-ram` bounded, the 0.6B sits at
1,463 MB. Thread count moves RSS by ~5 MB while changing speed 2-4x, so threads
remain a pure CPU-citizenship dial.

⚠️ **The 4B and 1.7B RAM figures have NOT been re-measured with `--cache-ram`
bounded.** Both are probably far below the struck-through values. The 4B was
rejected on the invalid numbers, so that rejection is **not currently supported by
evidence** — it should be re-tested if a larger model ever becomes interesting.

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

- **Qwen3-4B — rejection NOT currently evidenced.** Its 5,500 MB figure was pre-plateau and measured with --cache-ram unbounded. Re-test before relying on this.

- **Qwen3-1.7B — RAM not yet re-measured** with --cache-ram bounded; likely well under the struck-through 2,864 MB.
- **Qwen3-0.6B — viable and reduces footprint. 1,463 MB with --cache-ram 512, 49% below the control, flat under sustained load.**
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

`--device none --ctx-size 2048 --parallel 1 --batch-size 256 --ubatch-size 64 --cache-ram 512
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
