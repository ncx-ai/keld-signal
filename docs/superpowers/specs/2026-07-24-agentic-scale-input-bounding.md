# Bounding enrichment input at agentic-workflow scale

Status: **design note** (not implemented). Written alongside the RAM-oscillation
fix, which bounds input for *chat-scale* prompts. Records why that fix does not
extend to agentic-workflow prompts and what has to change before it does.

## What changes at agentic scale

Today a prompt is one human message: chat-scale, measured p50 ~13 word tokens
across the whole eval corpus, max 380. Agentic workflow steps instead submit a
composed payload — **system prompt + work prompt + metadata** — which is
routinely thousands of tokens and structurally heterogeneous.

Three properties of the current design break on that input.

### 1. mu + 2*sigma is the wrong estimator on a bimodal population

`enrich/lenstat` tracks one streaming mean/variance over *all* prompt lengths and
truncates at mu+2*sigma. That is sound for a unimodal population. A mixed
chat + agentic population is bimodal: e.g. 80% at ~20 tokens and 20% at ~3000
gives mu ~616 and sigma ~1200, so mu+2*sigma ~3000 — pinned to the ceiling by the
clamp, permanently. The estimator stops adapting and the clamp becomes the only
policy, which is precisely what the adaptive design existed to avoid.

**Fix:** key the accumulator by prompt *kind* (source, or an explicit
chat/agentic-step discriminator), so mu+2*sigma is computed within a homogeneous
population. `lenstat.Tracker` already isolates the policy; this is a map of
accumulators plus a kind on the observe/cap calls, and the persisted state gains
one level of nesting. Cheap, and it keeps the estimator meaningful.

### 2. The memory budget cannot be met by truncation alone

Peak worker RSS was measured against real gliner2-large (2 threads,
`MALLOC_ARENA_MAX=2`), and the marginal cost per token *rises* with sequence
length:

| max_len | worker peak | delta over model | MB per token |
|--------:|------------:|-----------------:|-------------:|
|     512 |     3043 MB |           316 MB |         0.62 |
|    1024 |     3416 MB |           689 MB |         0.67 |
|    1280 |     3666 MB |           939 MB |         0.73 |
|    1536 |     3870 MB |          1143 MB |         0.74 |
|    1800 |     4305 MB |          1578 MB |         0.88 |

Extrapolating past 1.0 MB/token, a 4000-token agentic payload implies a >4 GB
transient on top of the model's ~2.7 GB resident cost — roughly 7 GB, against a
4 GB total budget. There is no token cap that both (a) admits a full agentic
payload and (b) fits the budget. Truncation can only pick which requirement to
fail.

**Fix: windowed inference.** Split long input into <= window-size token windows and
run them sequentially (single-flight already serializes inference, so this adds
no concurrency). Peak memory is then a function of the *window*, not the payload:
flat at ~3.4 GB for a 1024-token window no matter how long the input. Cost grows
**linearly** in payload length instead of superlinearly — the opposite of the
current failure mode.

### 3. Head-truncation discards the part that carries the signal

`max_len` drops tokens beyond the limit, i.e. it keeps the *head*. In an agentic
payload the head is typically the system prompt — largely boilerplate, often
identical across every step of a workflow — while the work prompt and metadata
carry the actual signal. Head-truncating a 3000-token payload to 1024 tokens can
therefore discard nearly everything that distinguishes one step from another,
while spending the whole window on shared preamble. Facet quality would degrade
in a way no threshold tuning fixes.

**Fix: structure-aware segmentation.** Have the ingestion path mark segments
(system / work / metadata) rather than handing the pipeline one opaque string,
and select over them by facet instead of truncating blindly.

## Direction: a distinct control-flow branch

Agreed approach: when agentic work starts flowing in, handle it as a **separate
branch** keyed on prompt kind rather than by generalizing the chat path. The
chat-scale path stays exactly as it is — single inference per pass, adaptive
mu+2*sigma truncation, bounded by the measured token ceiling — and is not
destabilized by requirements it was never sized for. The agentic branch owns
segmentation, windowing, and per-facet aggregation.

That also makes the bimodal-estimator problem above mostly disappear as a
statistics question: with two branches, each keeps its own accumulator, so each
population is homogeneous by construction. The remaining work is the branch
itself, not a smarter shared estimator.

## Proposed shape (the agentic branch)

Combining the fixes, and keeping the inference count comparable to today's 8-9:

- **Segment-aware, facet-selective windowing.** Most facets (`task_type`,
  `speech_act`, `activity_type`, `function_guess`, `domain`, `personal`,
  `subcategory`) classify *intent* and need the work prompt + metadata, not the
  whole payload: give them **one** window over the prioritized segments.
  `sensitivity` is the exception — it must see everything, since a leaked
  credential anywhere in the payload matters — so it runs over **all** windows,
  unioning spans.
- Worked example, 4000-token payload, 1024-token window: 7 single-window facets +
  4 windows for sensitivity = **11 inferences**, against 9 today. Peak memory
  stays at the single-window figure (~3.4 GB).
- Aggregation is per-facet and must be explicit: classification facets take the
  argmax over windows weighted by confidence (or simply the prioritized window);
  span facets take the union, de-duplicated by offset after remapping window
  offsets back to absolute positions.

This also closes a gap the current fix knowingly leaves open: with head
truncation, NER-derived PII (including the `ssn`=>`phi` and `credit_card`=>`pci`
rollups) is only detected inside the window. Windowed sensitivity restores full
coverage. Go-side `creddetect` already scans the full text, so secrets/API keys
are unaffected either way.

## Invariants to preserve

- **Bounded peak memory is the hard requirement**, expressed as a total budget
  (`KELD_SIDECAR_MEM_BUDGET_MB`), not a token count. Windows are sized *from* the
  budget; the budget is never raised to fit an input.
- **Never fan out.** Windows run sequentially through the existing single-flight
  runner. Parallel windows would multiply peak memory by the window count and
  reintroduce the original bug in a new place.
- **Per-pass deadlines still apply**, and a windowed pass is one pass: its
  deadline must scale with its window count, or long payloads will time out on a
  deadline sized for a single inference.
- **Privacy unchanged.** Windowing is on-device; only masked labels and masked
  spans are ever published. Length statistics remain counts only, never text.
