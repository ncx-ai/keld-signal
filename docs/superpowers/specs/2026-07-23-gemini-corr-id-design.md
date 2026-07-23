# Gemini enrichment↔telemetry correlation id — design

**Date:** 2026-07-23. **Goal:** Gemini enrichments correlate to Gemini activity in
Atlas (they currently never join, so enrichments are invisible).

## Root cause (evidenced)

Gemini uses two different ids for one prompt:
- **Chat transcript record `id`** = `randomUUID()` (e.g. `270f788c-…`).
- **OTEL telemetry `prompt_id`** = `config.getSessionId() + "########" + getPromptCount()`
  (e.g. `149ace4f-…########0`), 0-based, one per user prompt (confirmed against a
  real session: the first prompt of session `149ace4f-…` is `…########0`).

The daemon keys the enrichment on the transcript UUID; Atlas joins
`enrichments.corr_id == tool_events.prompt_id` (`models.py`). UUID ≠
`sessionId########N`, so the join always fails → activity shows, enrichment never
does. (Claude is unaffected: its transcript id *is* its OTEL `prompt.id`.)

## Fix

Emit the Gemini enrichment `corr_id` as **`<sessionId>########<ordinal>`**, matching
the telemetry:
- `sessionId` — from the transcript meta line (top-level `sessionId`, no `type`).
- `ordinal` — the 0-based index of the user record among genuine user prompts
  (`type=="user"`, non-`$set`, non-empty text) in the session file.

### `internal/agent/watch/gemini.go` (extractor)
`geminiExtractor.extract(path, line)` — for a genuine user line, resolve
`sessionId` + this record's ordinal by scanning the file from the start
(`geminiPromptIndex(path, recordID)`), and emit
`promptRec{PromptID: "<sessionId>########<ordinal>"}`. Scanning from the start
(not relying on the incremental cursor) makes the ordinal correct even when the
forward-only watcher first sees the file mid-session (it never reads line 0 / the
early records otherwise).

### `internal/agent/resolve/gemini.go` (reader)
`Read(path, corrID)` — parse the ordinal after `########`, return the text of the
ordinal-th genuine user record. Fall back to legacy UUID match when `corrID` has
no `########` (older spooled pointers). `RecentUserPrompts` excludes the current
prompt by ordinal (counting from the start), not by UUID.

## Testing
- extractor: a 3-user-prompt transcript → `sid########0/1/2`; `$set`/`gemini`
  lines don't advance the ordinal; forward-only (start mid-file) still yields the
  absolute ordinal.
- reader: `Read(path, "sid########1")` → 2nd prompt's text; legacy UUID still
  resolves.
- **Real-data proof:** the extractor on session `149ace4f-…`'s first user record
  produces exactly `149ace4f-a00f-416a-a01a-efe802ccee7f########0` — the value
  Atlas has for that prompt's telemetry.

## Note
Enrichments already published with UUID corr_ids stay orphaned (can't retro-fix);
only prompts captured after the fix correlate.
