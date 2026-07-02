# keld-agent GLiNER2 sidecar

The optional ML backend for keld-agent. A FastAPI app running
`fastino/gliner2-large-v1`, exposing the `enrich.Model` contract over
`127.0.0.1`. The endpoints are **async** and execute inference through an
internal governed, single-flight runner (see *Execution model* below):

- `GET /health` → `{"ok": bool, "model": str}` (ok only once the model is warm
  **and** the runner is started)
- `POST /classify` `{text, tasks}` → `{"results": {task: [{label, confidence}]}}`
- `POST /entities` `{text, labels}` → `{"entities": [{text, label, start, end, confidence}]}`
- `POST /extract` `{text, labels, tasks}` → `{"entities": [...], "results": {...}}`
- Any of the three returns **503** when the runner's queue is full (overload
  backpressure — see below).

Returns **raw** spans (surface text + offsets); the daemon's enrichment pipeline
masks sensitive spans — the sidecar never does, and it never publishes to Atlas.

## Execution model & load protection

Inference does **not** run inline in the request handlers. Each endpoint clips
the input, submits the model call to an in-process `InferenceRunner`
(`app/runner.py`), and awaits the result. This is a lightweight background-jobs
mechanism — an `asyncio.Queue` + a single consumer task + a
`ThreadPoolExecutor(max_workers=1)` — in lieu of a full task-queue subsystem. It
guarantees **single-flight**: exactly one model inference runs at any instant, so
resident memory is bounded to one call's footprint. (This design replaced an
earlier global mutex after the sidecar was OOM-killed by concurrent inferences.)

A `Governor` (`app/governor.py`) paces the **individual model-invocation rate**
by host CPU load: it keeps an EWMA of CPU% and imposes a growing minimum interval
between invocation *starts* as load climbs, leaving headroom for other processes
on a shared machine. The governor only paces — it does not shed. Shedding emerges
from backpressure: sustained overload fills the bounded queue, `submit()` then
rejects immediately, and the endpoint returns 503 (the daemon treats that facet
as abstained → the enrichment publishes as `partial`).

Two further guards: inputs are truncated to `KELD_SIDECAR_MAX_CHARS` (transformer
memory grows ~quadratically with sequence length), and the queue is bounded by
`KELD_SIDECAR_QUEUE_MAX`.

### Tuning (env)
| Var | Default | Effect |
|-----|---------|--------|
| `KELD_SIDECAR_MAX_CHARS` | `20000` | Clip input length (`<=0` disables) |
| `KELD_SIDECAR_QUEUE_MAX` | `64` | Runner queue capacity before 503 |
| `KELD_GOV_HIGH` | `85` | CPU EWMA (%) at/above which pacing is maximal |
| `KELD_GOV_LOW` | `60` | CPU EWMA (%) at/below which pacing is zero |
| `KELD_GOV_MAX_INTERVAL_MS` | `2000` | Max delay between invocation starts at high load |
| `KELD_GOV_DISABLED` | `0` | `1` disables pacing (still single-flight) |

## How the daemon uses it
The daemon spawns `keld-agent-sidecar --port <N>` (the frozen `serve.py`) on an
ephemeral loopback port and health-gates it. Point it at a locally-provisioned
model directory with `KELD_GLINER2_DIR=/path/to/model`; otherwise it loads the
pinned HF model id (`SIDECAR_MODEL`, default `fastino/gliner2-large-v1`).

## Dev run
```sh
python -m venv .venv && . .venv/bin/activate   # Python 3.12 (NOT host 3.14)
pip install -r requirements.txt
python serve.py --port 8300
curl -s localhost:8300/health
```
First run downloads the model (~1.9 GB) into the HF cache; set
`KELD_GLINER2_DIR` to use a pre-provisioned copy.

## Packaging (P2b T11 / P3)
Frozen per-OS with PyInstaller into a self-contained `keld-agent-sidecar`
binary (bundles Python + torch + gliner2) shipped beside `keld-agent`. See the
plan's Task 11.
