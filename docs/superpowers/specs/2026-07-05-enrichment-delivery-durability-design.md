# Durable enrichment delivery (hook → daemon spool fallback)

**Status:** APPROVED IN BRAINSTORM (dg, 2026-07-05) — approach B1.
**Date:** 2026-07-05
**Author:** Claude (with dg)
**Repo:** keld-signal

## 1. Problem & motivation

The `keld` hook fire-and-forgets an *enrich pointer* (transcript path + prompt id
— never text) to the local `keld-agent` over loopback HTTP. The daemon binds an
**ephemeral TCP port** (`127.0.0.1:0`) and advertises it in `~/.keld/agent.json`;
the hook reads that and POSTs to `/enrich` with a 500 ms timeout, **silent-skip on
any failure**. There is **no retry and no durability**.

Observed failure (2026-07-05): the daemon was down/rebinding for ~2 hours, leaving
a stale port in `agent.json`. Every hook POST hit `connection refused` (hundreds
of lines in `~/.keld/agent.log`) and the pointer was **silently lost**. Telemetry
(a separate lane straight to Atlas) kept flowing, so Activity items appeared **with
no enrichment at all** — and therefore no sensitivity/compliance flag. From the UI
it just looks like "enrichment doesn't work."

Root cause: **fire-and-forget with no durability.** Any daemon downtime
permanently loses every prompt's enrichment in that window. (The ephemeral port is
not the disease — the daemon rewrites `agent.json` on restart and the hook
self-heals once it's back; the loss is strictly during downtime, which a stable
transport would not prevent.)

## 2. Approach (B1) & non-goals

**B1 — HTTP push + durable spool fallback.** Keep the fast HTTP path; when the
daemon is unreachable, the hook writes the pointer to an on-disk spool; the daemon
drains the spool on startup and on a periodic sweep. Incremental, low-risk, keeps
the tested `/enrich` ingress path.

**Non-goals:**
- Stable transport (unix socket / fixed port). Unnecessary once delivery is
  durable — a POST to a down daemon is refused regardless of transport.
- Changing the enrichment pipeline, masking, or Atlas wire contract.
- Persisting prompt **text**. The spool stores only the pointer, exactly like the
  HTTP payload.

## 3. Design

### 3.1 `internal/spool` (new; shared by hook + daemon)

- `Pointer` — the payload the hook already builds:
  `{ Source{ID,Origin}, Correlation{Scheme,ID,SessionID}, Pointer{TranscriptPath,PromptID,Cwd} }`
  (a typed struct, JSON-serialized). **No prompt text.**
- `Dir() string` — `paths.SpoolDir()` = `<KELD_HOME>/spool`.
- `Write(p Pointer) error` — ensure dir `0700`; atomic write to
  `spool/<sanitized prompt_id>.json` via `tmp`+`os.Rename`, file `0600`. Enforce
  the cap (see below) before writing.
- `Drain(fn func(Pointer) error) (drained int, err error)` — list `*.json`
  oldest-first (by mtime); for each: decode → `fn(p)`; on `fn` success `os.Remove`
  the file; on `fn` error leave it (retry next sweep); on **decode** error move the
  file to `spool/bad/` (poison quarantine) and continue.
- **Cap:** `KELD_SPOOL_MAX` (default 500). When at/over cap on `Write`, delete the
  **oldest** files down to cap-1 and record the dropped count via `debuglog`
  (never silent). Bounds disk if the daemon is down indefinitely.
- All ops best-effort and panic-free; a broken spool must never block the hook or
  crash the daemon.

### 3.2 Hook (`internal/hook/forward.go`)

Refactor so the `spool.Pointer` is built once. Try the HTTP POST (unchanged fast
path). **Spool the pointer** when the daemon can't be reached or rejects it:
- `agentcfg.Read()` fails / `info == nil` / `Port == 0` (daemon never started), or
- POST transport error, or
- POST returns ≥ 400.

Still silent-skip toward the host tool (never return an error, never block); record
the spool action in `debuglog` (endpoint/status/prompt_id only — never text).

### 3.3 Ingress mapper (`internal/agent/ingress/ingress.go`)

Extract the `Request → queue.Job` mapping into an exported helper (e.g.
`JobFrom(spool.Pointer) queue.Job`) so the HTTP handler and the spool drain build
the identical `queue.Job`. The HTTP `Request` decodes into a `spool.Pointer`.
(DRY: one mapping, two callers.)

### 3.4 Daemon (`internal/agent/daemon/daemon.go`)

After ingress is listening and the worker is started:
- **Startup drain:** `spool.Drain` → for each pointer, `q.Offer(ingress.JobFrom(p))`;
  treat `Offer==false` (queue full) as a drain failure so the file is kept.
- **Periodic sweep:** a ticker (`KELD_SPOOL_SWEEP`, default 30 s) runs `Drain`
  again for anything spooled while the daemon was up (rare) or left by backpressure.
- Idempotent: delete-after-enqueue; Atlas dedups on `dedup_key`; re-draining the
  same pointer is harmless.

### 3.5 `internal/paths`

Add `SpoolDir()` → `filepath.Join(Home(), "spool")`.

## 4. Data flow

- **Normal:** hook → POST `/enrich` → 202 → queue → worker → mask → publish. (unchanged)
- **Daemon down:** hook POST fails → `spool.Write` → … daemon returns → startup
  `Drain` → queue → worker. No loss.
- **Backpressure:** `Offer` false → file kept → retried on the next sweep.

## 5. Error handling & security

- Spool dir/files are user-only (`0700`/`0600`) — the pointer carries no text, and
  filesystem perms gate access (mirrors `~/.keld` token handling).
- Cap prevents unbounded disk growth; drops are counted, not silent.
- Poison files quarantined to `spool/bad/`, never block the drain.
- Every spool operation is best-effort; failures degrade to the current behavior
  (skip) rather than breaking the hook or daemon.

## 6. Testing

- **spool** (unit): `Write` creates an atomic `0600` file; `Drain` calls `fn`,
  deletes on success, leaves on `fn` error; cap drops oldest + counts; decode-error
  file is quarantined.
- **hook** (unit): POST to a dead port → pointer is spooled; POST to a live stub
  returning 202 → **not** spooled; missing agent info → spooled.
- **ingress mapper** (unit): `JobFrom(pointer)` equals the job the HTTP handler
  builds for the same payload.
- **daemon drain** (unit/integration): a pre-seeded spool is drained into a fake
  queue on startup; queue-full leaves the file.

## 7. Rollout

Code-only; no config migration. Effective after rebuilding + reinstalling `keld`
(the hook) and `keld-agent`, and restarting the daemon. Deploying the (separate)
memory-eviction build reduces the daemon restarts that trigger the loss in the
first place.
