# keld-agent P2 — in-process Go+ONNX vs sidecar: decision

> **Superseded (2026-07-14):** the deterministic backend has been removed —
> Keld Signal is ML-only (GLiNER2 sidecar mandatory; no fallback). The
> "permanent fallback" posture below is historical.

**Date:** 2026-07-01
**Phase:** P2 2a (spike)
**Decision:** **NO-GO for in-process Go+ONNX → adopt the bundled GLiNER2 sidecar.**

## Method

Resolved on **architecture-cost evidence** (source inspection of the installed
`gliner2` package + live validation of the reference sidecar), not by building
the full prototype. Reading the decode and confirming the working fallback is the
cheap exit a spike exists to find — so Tasks 3/5/6 (ONNX export, Go decode
prototype, parity measurement) were intentionally **not** executed once the
evidence was conclusive. The durable eval harness (Tasks 1–2) shipped to `main`
regardless.

## Evidence

### GLiNER2 is not a stock encoder with simple span logits
- Encoder: **DeBERTaV2-large, 340M params** (`fastino/gliner2-large-v1`).
- Custom **learned heads** in `gliner2/layers.py`: `SpanMarkerV0`
  (start/end projection MLPs + final projection), `DownscaledTransformer`,
  GRU/LSTM "count" modules — part of the model forward, not the encoder.
- Decode spans **~3,900 lines**: `inference/engine.py` (1068),
  `processor.py` (1216), `layers.py` (444), `model.py` (957): schema-token
  prompt construction (`[DESCRIPTION]` separators), span↔schema-embedding
  scoring, thresholding, greedy per-task selection, token→word→char offset maps.
- The library has **no ONNX export path** and no Go/Rust runtime.

### In-process would require all of
1. Tracing the **full custom model** (encoder + learned heads) to ONNX — the
   library gives no supported path.
2. A Go **DeBERTa-v2 SentencePiece tokenizer with byte offsets** (another CGO
   dependency).
3. A **faithful Go reimplementation of ~2k lines** of schema decode, at parity.
4. Shipping **`libonnxruntime` via CGO** — which erodes the single-static-binary
   value and complicates cross-compilation for the P3 installers.

Disproportionate effort and high fidelity risk for a backend that already exists.

### The sidecar works end-to-end (validated live)
Reference `gliner2-sidecar` (`python:3.12`, `gliner2[local]`) stood up locally;
a real `POST /extract` returned exactly the `enrich.Model` contract:
- entities: `api_key` "sk-live-ABC123" (start 35, end 49), `email` "a@b.com",
  `language` "Python" — correct spans.
- results: `task_type = codegen`.

### Measurements (CPU, this machine)
- Warm `/extract` latency: **~0.46–0.71 s** per prompt (acceptable for an async,
  non-user-blocking daemon enrichment).
- Model footprint (HF cache): **~1.9 GB**.
- Dev Docker image: 8.27 GB (torch-heavy) — this is the *dev image*, not the
  shippable footprint; a packaged sidecar ships a runtime + the ~1.9 GB model.

## Decision

**NO-GO in-process; GO bundled sidecar.**

**Posture (important):** the **pure-Go deterministic backend (P1) remains the
zero-dependency default and permanent fallback.** The GLiNER2 sidecar is an
**opt-in ML upgrade** behind the same `enrich.Model` interface — no one is forced
to install it, and the daemon degrades cleanly to deterministic if the sidecar is
absent/unhealthy.

## Implications for 2b (next phase)

- **`sidecarClient` implementing `enrich.Model`** — `Classify`/`Entities`/`Extract`
  over `localhost` HTTP to the sidecar's `/classify`,`/entities`,`/extract`;
  preserve the P1 masking invariant (sensitivity spans masked before they leave).
- **Daemon lifecycle** — spawn + health-gate the sidecar; fall back to the
  deterministic backend when it is unavailable/unhealthy.
- **Adaptive governor** — the sidecar call is the expensive work the EWMA
  host-load governor paces (concurrency + admission/sample rate), graduating the
  P1 floor. This is why the spec pairs them.
- **Eval gate** — the Task 1–2 harness scores the sidecar backend the same way it
  scores deterministic; expand the gold set (8 → ~50–100) for a real gate.

## Implications for P3 (installers) — per product guidance

- The **installer provisions all sidecar dependencies transparently**; the user
  never manages Python. **The GUI stays simple** — one click; a first-run model
  download (~1.9 GB) with a progress indicator is the expected UX.
- Packaging options to evaluate in P3: a **frozen self-contained sidecar**
  (e.g. PyInstaller) shipped as its own artifact vs. a **managed venv +
  first-run model fetch**. Either way ked-agent stays the single thing the user
  installs; the sidecar is set up behind the scenes.
- Because the deterministic backend is pure-Go, keld-agent is **useful the moment
  it installs** — the ML sidecar can finish provisioning in the background without
  blocking first use.
