# keld-agent GLiNER2 sidecar

The optional ML backend for keld-agent. A FastAPI app running
`fastino/gliner2-large-v1`, exposing the `enrich.Model` contract over
`127.0.0.1`. The endpoints are **async** and execute inference through an
internal governed, single-flight runner (see *Execution model* below):

- `GET /health` → `{"ok": bool, "model": str, "state": str}` (`state` is the
  worker state — `down`/`spawning`/`ready`/`held`; `ok` is true whenever the
  service can serve on demand, i.e. `state != "held"` — it does **not** require
  a worker to already be loaded)
- `POST /classify` `{text, tasks}` → `{"results": {task: [{label, confidence}]}}`
- `POST /entities` `{text, labels}` → `{"entities": [{text, label, start, end, confidence}]}`
- `POST /extract` `{text, labels, tasks}` → `{"entities": [...], "results": {...}}`
- Any of the three returns **503** when the runner's queue is full (overload
  backpressure — see below).

Returns **raw** spans (surface text + offsets); the daemon's enrichment pipeline
masks sensitive spans — the sidecar never does, and it never publishes to Atlas.

## Execution model & load protection

**Parent/worker split.** The FastAPI process is a thin control plane: it holds
**no model** and never imports torch, so its own RSS stays flat (~50 MB) no
matter how long it runs. Inference happens in a separate **inference worker**
child process (`app/worker.py`, spawned via `multiprocessing` `spawn` and owned
by `app/worker_manager.py`'s `WorkerManager`) that loads the GLiNER2 model and
runs exactly one `classify`/`extract`/`entities` call at a time over a pair of
request/response queues. Isolating inference in a child process means it can be
**recycled** — killed and respawned — to reclaim its heap via process exit, the
only cross-platform way to actually give memory back to the OS; the parent never
restarts.

Each endpoint clips the input, submits the worker call to an in-process
`InferenceRunner` (`app/runner.py`), and awaits the result. This is a lightweight
background-jobs mechanism — an `asyncio.Queue` + a single consumer task + a
`ThreadPoolExecutor(max_workers=1)` that runs the (blocking) IPC round-trip to
the worker off the event loop — in lieu of a full task-queue subsystem. It
guarantees **single-flight**: exactly one inference runs at any instant, so the
worker's resident memory is bounded to model weights + one call's footprint.
(This design replaced an earlier global mutex after the sidecar was OOM-killed by
concurrent inferences, and later moved the model itself into a recyclable child
so the service's own memory couldn't grow at all.)

**The worker is recycled** (killed + respawned on the next request) on any of:
an **RSS ceiling** (`model_cost_mb + KELD_SIDECAR_RSS_MARGIN_MB`), sustained
**memory pressure** (available RAM ≤ `KELD_SIDECAR_EVICT_AVAIL_PCT` — the worker
is held down until headroom returns, serving 503s meanwhile), **inactivity**
(`KELD_SIDECAR_IDLE_UNLOAD_S`, 0 disables), a **hung-job timeout**
(`KELD_SIDECAR_JOB_DEADLINE_S`), or a crash. A parent-side poll loop
(`WorkerManager.poll`, `KELD_SIDECAR_MEM_POLL_S`) drives ceiling/idle/pressure
checks; the worker respawns lazily on the next request. See
`sidecar/loadtest/README.md` for the full trigger list, env knobs, and
validation.

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

`GET /metrics` reports a `worker` block alongside `governor`/`runner`/`counts`:
`worker.state` (`down`/`spawning`/`ready`/`held`), `worker.worker_rss_mb`,
`worker.parent_rss_mb`, `worker.model_cost_mb`, `worker.recycles`, and
`worker.kills` (`{timeout, pressure, idle, crash}`).

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
