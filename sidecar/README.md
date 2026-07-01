# keld-agent GLiNER2 sidecar

The optional ML backend for keld-agent. A minimal FastAPI app running
`fastino/gliner2-large-v1`, exposing the `enrich.Model` contract over
`127.0.0.1`:

- `GET /health` → `{"ok": bool, "model": str}` (ok only once warm)
- `POST /classify` `{text, tasks}` → `{"results": {task: [{label, confidence}]}}`
- `POST /entities` `{text, labels}` → `{"entities": [{text, label, start, end, confidence}]}`
- `POST /extract` `{text, labels, tasks}` → `{"entities": [...], "results": {...}}`

Returns **raw** spans (surface text + offsets); the daemon's enrichment pipeline
masks sensitive spans — the sidecar never does.

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
