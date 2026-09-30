# Per-request usage — implementation plan

Spec: `docs/superpowers/specs/2026-09-29-per-request-usage-proposal.html`
(published at https://claude.ai/artifact/TY6sn3twnjzRw2dvKhNLwx, version 14).
Branch: `feat/per-request-usage`, stacked on `feat/signal-2c`.

Test IDs (T1–T17, T6b, A1, A2) are the spec's. Every task is written test-first.

## Decisions this plan builds on (all 2026-09-29, Gabriel)

- Count tokens and spend per request; blocks keep repo, project and the activity grid.
- One `requests` table in `ledger.db`, every row kept for good. No rollup, no pruning.
- D1: Focus blocks rows may not add up to its Tokens / Est. spend tiles. Accepted.
- D2: the Overview shows only what the table holds; days with no transcript on disk
  show no usage, and the page says where each tool's data starts.
- R2, R3, R4 mitigations accepted. R1 downgraded to a note.
- **Not yet answered, planned on the proposed default:** N1 (drop the engine fix for
  Codex's missing block models) and the Overview histogram (stays block-based).

## The one design point the spec left implicit

The transcript reader (`internal/agent/promptlog`) runs a source only when it is
mirrored to Atlas **and** a token exists (`Telemetry.eligible`), and its per-file
bookkeeping — `lastReq`, the Codex state, the Gemini turn counts — is shared with
the Atlas emission. Reusing that bookkeeping for the local count would change what
Atlas receives: a Gemini chat counted locally while unpaired would advance the
cursor, and those turns would never be mirrored after pairing.

So: **one parse, two consumers, separate bookkeeping.** The parsing becomes pure
functions that turn a line (or a Gemini document) into zero or more `Request`
records. The Atlas mirror keeps its own state and gating, unchanged. A new local
recorder keeps its own state and always runs — unpaired, Atlas off, `tool_otlp`
on or off.

Keys, all built from the tool's own data (checked 2026-09-29):

| Source | Key |
|---|---|
| Claude Code / Cowork | `requestId` from the line |
| Codex | `session @ record timestamp # ordinal` — `ordinal` is a field Codex writes into the record (`codex.go:47`) |
| Gemini | session id + the response's own `message id` |

## Tasks

### 1 · Freeze what Atlas receives today (A1 baseline)
Before touching the reader: golden files of the exact OTLP bodies the mirror posts,
for every source, from the real captured fixtures in `promptlog/testdata`. A test
compares against them byte for byte. Everything after this must keep it green.
- Tests: **A1** (golden payloads, and the mirrored source set unchanged for both
  `tool_otlp` values).

### 2 · Extract the parse from the emit
Split each of `observeClaudeLine`, `observeCodexLine`, `observeGeminiFile` into a
pure `parse… → []Request` plus the existing emitter. `Request` = source, session,
key, ts, model, input, output, cache_read, cache_creation. Dedup rules (first line
of a Claude Code request, Codex only-when-total-advances, Gemini per message) live
in the parse, with the state passed in rather than held by `Telemetry`.
- Tests: **A1** stays green; **T1** (6-line Claude Code request → 1 record),
  **T2** (repeated Codex total adds nothing), **T3** (rewritten Gemini document adds
  nothing), **T5** (no model named → record with empty model).

### 3 · The `requests` table
Additive table in `ledger.db`: `(source, session, request_key)` primary key, `ts`
(indexed), `model`, the four token classes, `estimate_usd`. Store methods:
`InsertRequests` (one transaction per batch, insert-or-ignore), `UsageRows(since,
until)`, `RepriceUnpricedRequests`, `RequestStats` (rows, bytes), and a
`backfill_done` marker row in the same file. Contract documented in
`docs/v3/contracts.md` first.
- Tests: **T4** (same records inserted twice → no change), **T6** (a year-old key
  re-inserted → nothing), **T12** (a model gains a price → its rows re-priced),
  **T9** (rows and totals survive the transcript being deleted).

### 4 · The local recorder, always on
`internal/agent/usage`: its own parse state, fed by the same watcher observe hooks
as the mirror (`daemon.go` ~1386), buffering one poll's records into one
`InsertRequests`. Prices with `pricing.Estimate` at write. Not gated on pairing,
Atlas, or `tool_otlp`.
- Tests: **R2** (a tool with its own telemetry on is still counted locally, and
  still not mirrored — A1), unpaired machine still counts.

### 5 · The one-time backfill
On start, if the table has no `backfill_done` marker: read every transcript still on
disk from the start, through the recorder's parse, into the table only — never the
mirror. Paced like the existing first-sight backfill (a few files per poll),
resumable, marker set when done. Tied to the table, not to `~/.keld/watch` cursors.
- Tests: **A2** (nothing from the backfill reaches Atlas), **T6b** (ledger removed,
  cursors kept → the next start refills), **R3** (paced: no more than N files per
  poll).

### 6 · `GET /v1/usage`
Rows for a range: ts, source, session, model, tokens, `estimate_usd`, plus `repo`
and `projects` joined from the block covering (session, ts), empty when none (R4).
Also each source's first `ts`, for the "data starts" note. Loopback, secret-gated,
documented in `contracts.md` first.
- Tests: route shape, join (request inside / outside a block), per-source first-day.

### 7 · The page reads usage
Overview tiles, the cost & volume chart and "By model" from `/v1/usage`; "By repo"
and "By project" from the joined fields; the histogram stays on blocks. Days before a
source's first row show no usage, with a note naming where each tool's data starts.
Focus blocks' Tokens and Est. spend tiles from the same data; its rows unchanged.
- Tests: **T13** (6 Opus + 4 Haiku priced per request), **T14** (Overview Today =
  Focus blocks tiles), **T15** (an open block's requests already count), **T16**
  (no usage before a source's first row, and the note), plus the existing Overview
  node and e2e suites.

### 8 · Visibility and scale
`keld signal doctor` reports the table's rows and size. A synthetic benchmark of
three years at ten times this machine's rate records file size and a 30-day
`/v1/usage` read time.
- Tests: **T10** (doctor line), **T11** (benchmark, bar set from its first run).

### 9 · Prove it end to end
- **T7:** on real transcripts, per-session token totals from the table match the
  analysis engine's own per-request token store (tokens only; a script, since it
  needs the sidecar's store).
- **T8:** the table and the Atlas mirror, fed by the same parse, give identical sums.
- **T17:** e2e over the generated corpus — Overview totals equal the corpus's
  requests; no "no model" where every request named one.
- Full `go test ./...`, node, Playwright, vocabulary check. Rebuild the dev daemon
  on this Mac and compare the Overview against the table directly.
- Phase-2 review against the spec.

## Order and why

1 must come first (its golden files are the "before"). 2 before 4 (the recorder
needs the pure parse). 3 before 4 and 5. 5 after 4 (same parse, same sink). 6 before
7. 8 and 9 last. Each task is one commit.
