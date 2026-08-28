# Incremental reference-series store — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ingest each transcript once, incrementally, into a persistent time series, so digests become queries instead of re-parses and dynamics become possible at all.

**Architecture:** A SQLite store in the sidecar. The watcher signals that a transcript grew; the sidecar parses only the appended tail from a byte-offset checkpoint, appends raw reference events, re-rolls the affected 5-minute bins, and advances a watermark. `/analyze` keeps its request/response contract and reads the store instead of opening a transcript.

**Spec:** `docs/superpowers/specs/2026-08-23-incremental-reference-series-store-design.md` — read it first; it carries the measurements behind every constant here.

## Global Constraints

- **Measured, not assumed:** a 60-minute window holds a mean of 3.8 prompts (max 20), so an hour is currently characterised ~4x and up to 20x, each from a full transcript parse (0.8-1.0s on a 64 MB file). Raw events run 3,882 rows/day, ~0.11 GB/year.
- **Native SQLite tables, never pickled pandas.** The analysis package is deliberately pandas-free (`app/analysis/__init__.py`; `window.py` argues `Counter` is the whole performance case).
- **Bins are 5 minutes.** Any window is an aggregation over the bins it covers.
- **Rollups are precomputed for the DEFAULT levels only** (the 7 ALLOCATION + 5 INVENTORY levels). `events_for_turns` emits 19; the rest, and anything invented later, backfill from stored raw events without re-reading a transcript. **`bin` is sparse by design: a level's absence means "not precomputed", never "no evidence".**
- **Never serve past the watermark.** A window ending after `watermark_ts` fails and the caller retries, rather than reporting a slice missing its last minutes — serving it would publish a confidently wrong attribution.
- **Retention:** raw events kept indefinitely; a size backstop (`KELD_REFSERIES_MAX_MB`, default 1024) prunes oldest raw events only. Rollups are never pruned.
- `/analyze` keeps its exact request and response contract — the Go client, the workstreams pass and the published payload must not change.
- `/analyze` takes COORDINATES, never text. No span, no offset, no prompt text in any response or log line. It must not use the inference single-flight `_dispatch`, and must not require GLiNER2.
- Sidecar tests are standalone scripts with a `__main__` runner, **never pytest**, run with `~/.keld/sidecar-venv/bin/python`.
- Never `git add -A`/`checkout`/`stash`/`clean`. Uncommitted user work must survive: `.gitignore`, `internal/agent/daemon/custom_passes*.go`, a `daemon.go` hunk, `scripts/context_value.py`, `scripts/prompt-v9.md`.

---

### Task 1: The store — schema, open, and the window query

**Files:** Create `sidecar/app/analysis/store.py`, `sidecar/app/test_store.py`.

**Produces:** `open_store(path) -> Store`; `Store.upsert_events(session, rows)`, `Store.rollup_window(session, start, end) -> dict` (the same shape `window.rollup` returns, so `workstreams.payload` consumes it unchanged), `Store.watermark(path) -> str|None`.

    ingest(path PK, offset, size, head_sha, mtime, watermark_ts, updated_at)
    event(session, ts, level, ref, n, source_line)
    bin(session, bin_ts, level, ref, n)   PK(session, bin_ts, level, ref)

- [ ] **Step 1: Write the failing test** — upsert events, then `rollup_window` returns counts identical to what `window.rollup` produces over the same rows. That equivalence is the contract; assert it directly against `window.rollup` rather than hand-writing expected counts.
- [ ] **Step 2: Run it and watch it fail** — `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_store.py`
- [ ] **Step 3: Implement.** Use stdlib `sqlite3`. Decide and justify: WAL mode, the busy timeout, and whether writes are batched per ingest — the daemon and the sidecar are separate processes and only one writes, but `/analyze` reads concurrently.
- [ ] **Step 4: Run.** All sidecar suites.
- [ ] **Step 5: Commit.**

---

### Task 2: Incremental ingest from a byte offset

**Files:** Create `sidecar/app/analysis/ingest.py`, `sidecar/app/test_ingest.py`.

**Produces:** `ingest_file(store, path) -> IngestResult(new_lines, watermark_ts, reparsed)`.

Transcripts are append-only JSONL, so a byte offset is a valid resume point.

- [ ] **Step 1: Write the failing tests** — (a) ingesting twice with no change parses zero new lines; (b) appending lines parses only those; (c) a file that SHRANK below its offset triggers a full reparse; (d) a file whose head bytes changed (rotation) triggers a full reparse; (e) the watermark advances to the last ingested turn's timestamp and never past it.
- [ ] **Step 2: Run and watch them fail.**
- [ ] **Step 3: Implement.** Reuse `transcript.iter_turns` / `levels.events_for_turns` / `reconcile.reconcile` — do NOT write a second parser. Note `events_for_turns` needs `root` and `repo_root`; `analyze.py` already derives them and its reasoning is documented there. `reconcile` is not optional: `file`/`dir`/`ext`/`lang`/`component` rows come only from it.
- [ ] **Step 4: Run.** All sidecar suites.
- [ ] **Step 5: Commit.**

---

### Task 3: `/analyze` reads the store

**Files:** Modify `sidecar/app/analysis/analyze.py`, `sidecar/app/main.py`; tests alongside.

- [ ] **Step 1: Write the failing test** — `analyze_window` returns the SAME payload for a transcript whether computed by parsing or served from the store. Build it by running the current parse path, ingesting the same file, then asserting the store path matches field for field. **That equivalence is the whole task**; a test that only checks the store path returns *something* proves nothing.
- [ ] **Step 2: Run and watch it fail.**
- [ ] **Step 3: Implement.** A window ending after the watermark must fail distinguishably — pick the status and say why; the caller re-spools, so it must not look like "prompt not found" (404) or a permanent error.
- [ ] **Step 4: Measure.** Report `/analyze` latency before and after on a real 64 MB transcript. The parse path measured 0.8-1.0s; state the new number rather than claiming an improvement.
- [ ] **Step 5: Run** every sidecar suite, plus `go test ./...` (the Go client and workstreams pass must be untouched).
- [ ] **Step 6: Commit.**

---

### Task 4: The daemon signals that a transcript advanced

**Files:** Modify `internal/agent/watch/`, `internal/agent/enrich/sidecar/client.go`, `internal/agent/daemon/daemon.go`; tests alongside.

The watcher already tails transcripts and knows when one grows. It signals; the sidecar ingests. Do not poll from the sidecar.

- [ ] **Step 1: Write the failing test** — a watcher observing new lines calls the ingest signal exactly once per advanced file per batch, and never carries text.
- [ ] **Step 2: Run and watch it fail.**
- [ ] **Step 3: Implement.** Coordinates only — path, never content. Decide what happens when the sidecar is unreachable: ingest is resumable from the offset, so dropping a signal is recoverable, but say so explicitly rather than leaving it implicit.
- [ ] **Step 4: Run.** `go build ./...`, `go test ./...`, `gofmt -l`.
- [ ] **Step 5: Commit.**

---

### Task 5: Retention backstop and visibility

**Files:** Modify `sidecar/app/analysis/store.py`, `sidecar/app/metrics.py`; tests alongside.

- [ ] **Step 1: Write the failing test** — exceeding `KELD_REFSERIES_MAX_MB` prunes oldest raw events and never prunes a `bin` row; `/metrics` reports store size, row counts and the oldest retained event.
- [ ] **Step 2: Run and watch it fail.**
- [ ] **Step 3: Implement.** Pruning must be visible in `/metrics`, not silent — the standing rule in this repo is that dropping is visible.
- [ ] **Step 4: Run** every sidecar suite.
- [ ] **Step 5: Commit.**

## Not in this plan

- **The moving characterization** (`docs/superpowers/specs/2026-08-23-moving-characterization-design.md`) — the consumer side; this plan only makes it possible.
- **Which dynamics to publish.** Enabled by the store, chosen by a measurement.
- **Backfilling existing transcripts.** Ingest is forward-only by default, matching `KELD_WATCH_BACKFILL`.
- **Codex/Gemini.** `/analyze` cannot resolve their prompt ids; unchanged here.
