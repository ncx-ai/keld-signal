# Signal v3 — contracts between the lanes

Status: **binding for the v3 build on `worktree-signal-health`** (2026-09-04). The plan
these contracts serve is the artifact "Keld Signal" (section 6). Five lanes build in
parallel against these shapes; a lane that needs a shape changed edits THIS file first
and says so in its commit.

Everything below is loopback-only, behind the daemon's per-user secret
(`x-keld-agent-secret`, the header `/enrich` already requires). Nothing here holds
prompt text, a span, an offset or an absolute path. Reasons are codes from
`internal/agent/ledger/recorder.go`; the page renders each code with its own sentence.

## Settings (`~/.keld/agent-config.json`)

New keys, all local, all optional, defined in `internal/agent/settings/settings.go`:

| key | type | default | env | meaning |
|---|---|---|---|---|
| `send_to_atlas` | bool | true (absent = on) | `KELD_ATLAS=0/1` | the connector is constructed or not |
| `dev_blocks` | `""` \| `prompt` \| `bin` \| `minute` | `""` | `KELD_DEV_BLOCKS` | developer granularity; **refused unless `send_to_atlas` is false** |
| `show_breaks` | bool | false | — | page preference |
| `workstreams_off` | [string] | [] | — | workstream keys excluded from attribution locally |

Existing keys this build reads: `attribution` (vector attribution toggle, already sets
`KELD_TEXTEMBED=1` for the sidecar), `blocks`.

## `GET /v1/ledger?since=<unix>&limit=<n>`

```json
{
  "generated_at": "2026-09-04T17:34:02Z",
  "health": [
    {"key": "daemon",    "status": "ok",     "detail": "2.5.0",  "at": "…"},
    {"key": "sidecar",   "status": "ok",     "detail": "2.5.0",  "at": "…"},
    {"key": "telemetry", "status": "ok",     "detail": "",       "at": "…"},
    {"key": "atlas",     "status": "failed", "detail": "atlas_rejected", "at": "…"},
    {"key": "store",     "status": "ok",     "detail": "",       "at": "…"}
  ],
  "blocks": [
    {
      "key": {"session": "0fc1a437347a8f95", "start": 1788543000},
      "end": 1788544200,
      "source": "claude_code",
      "start_reason": "idle", "end_reason": "budget",
      "cells": {
        "cut":        {"status": "ok", "at": "…"},
        "measured":   {"status": "ok", "at": "…", "tokens": {"input": 2, "output": 277, "cache_read": 26915, "cache_creation": 86736, "request": 41000}, "requests": 12, "model": "claude-opus-4-8", "estimate_usd": 1.84},
        "attributed": {"status": "ok", "at": "…", "project_id": "p_keld_signal", "method": "repo"},
        "sent":       {"status": "ok", "at": "…"},
        "received":   {"status": "failed", "at": "…", "reason": "atlas_rejected", "http_status": 401}
      }
    }
  ],
  "pending": [
    {"session": "9eb2b3ff…", "reason": "sidecar_outdated", "at": "…"}
  ]
}
```

Rules: a cell that never happened is **absent from `cells`**, never `{"status":"failed"}`.
`estimate_usd` is 0 when no price applies; the page prints "est." on every dollar figure
regardless. `pending` holds sessions for which blocks could not be asked for at all.

Breaks are NOT stored: the page derives them as the gap between consecutive blocks of
one session when the gap ≥ 15 minutes (a cap-cut block abuts the next with gap 0).

## `GET|PUT /v1/settings`

GET returns the effective values (file merged with env), plus `"readonly": [...]` naming
keys an env var currently pins. PUT takes any subset of the four keys above plus
`attribution`; the daemon validates (`dev_blocks` with Atlas on → `409
{"error":"turn_off_send_to_atlas_first"}`), writes the file, and answers
`{"restart_required": true|false}`. `send_to_atlas` and `dev_blocks` require a restart;
the daemon restarts itself when the caller passes `?restart=1`.

## `POST /v1/config`

`{"code": "atlas-dev.keld.co/ABCD-EFGH"}` → runs the existing `auth.ParsePairingCode` +
`LoginWithCode` + setup path, writes `auth.json`/`hook.json`, answers
`{"host": "https://atlas-dev.keld.co", "restart_required": true}`. Malformed → 400, no
file touched. Refused with 409 while `send_to_atlas` is false.

## Projects (`~/.keld/state/projects.json`) and `/v1/projects`

The file is the **`KELD_PROJECTS_FILE` shape attribution already reads**, extended with
fields the daemon ignores when it reads it as a project list:

```json
{
  "version": 1,
  "workstreams": [
    {"key": "development", "name": "Development", "question": "Which project is this work for?", "template_id": "project", "origin": "atlas|local", "off": false}
  ],
  "projects": [
    {
      "id": "p_sdk_work",
      "title": "SDK work",
      "description": "",
      "team": "",
      "repos": ["github.com/ncx-ai/sdk-testbench", "github.com/ncx-ai/atlas-telemetry-typescript", "github.com/ncx-ai/atlas-telemetry-python"],
      "keywords": [],
      "ticket_key": "",
      "workstream": "development",
      "origin": "suggested|user|atlas",
      "hidden": false,
      "atlas_value_id": null
    }
  ]
}
```

### ⚠️ What Atlas actually offers today — VERIFIED 2026-09-04, and it is not what an
### earlier draft of this file said

Read against `keld-atlas` on this machine, not assumed:

1. **The vocabulary already reaches the daemon, and needs no Atlas release.**
   `GET /v1/enrichment-settings` already serves the org's **pooled workstream VALUES** under
   the existing `projects` key (`services/workstream_attribution.wire_projects`), shaped
   `{id, title, description, team, keywords}`. So "the org's workstream values are the
   attribution vocabulary" is true **now**.
   - `team` carries the **workstream's name** when a value has no owning team — that is how
     the daemon can group values by bucket and honour `workstreams_off` without a new field.
   - `keywords` are the value's authored **tags with their prefix STRIPPED**: an admin types
     `repository: acme/web` in the Atlas editor and the daemon receives `acme/web`. So a
     deterministic repo rule must recognise a repository **by shape**
     (`host/org/name` or `org/name`), not by a prefix that does not survive the wire. Do not
     wait for Atlas to stop stripping.
   - The list is **pooled deliberately** — one flat competition across all workstreams — and
     Atlas's own comment says four lists would need a Signal change. Do not ask for one.
2. **Publishing an attribution already works.** Signal publishes `raw->'projects'` as a flat
   array of `{id, confidence, source}`; Atlas matches a workstream to a block by containment
   of any of its value ids (`attributed_to`) and writes the workstream key into
   `blocks.dimensions` itself. **The daemon never sends a workstream key.**
3. **⚠️ WRITING A VALUE BACK DOES NOT WORK, and an earlier draft of this file claimed it
   did.** `PATCH /v1/workstreams/{key}` is really `PATCH /api/workstreams/{key}`, mounted
   with `dependencies=[Depends(require_admin)]` behind a **user session** — not the daemon's
   ingest token. There is no `/v1/signal/*` route that accepts a project or a tag from a
   machine. Therefore, for this build:
   - "same as" and "new project" are **LOCAL edits** to `projects.json`. They re-attribute
     this machine's blocks immediately and completely.
   - The page states plainly that the change is local, and offers the Atlas workstream editor
     link for the org-wide edit. It must not imply the org has been taught anything.
   - The org-wide loop needs **one Atlas change**: a route under `/v1/signal/*` accepting the
     ingest token that appends a tag to a value. That is D8's ask, and it is a server change,
     not a client one. Until it exists, the "one person fixes it for the fleet" story in the
     plan is aspirational and the page must not tell it.

`repos` ARE the rules (one `repo:` rule per entry, full remote `host/org/name`, lowercase);
`ticket_key` is the second rule kind (a Jira-style prefix, e.g. `KELD`). Everything else
the machine collects under a project (branches, languages, tools, workspaces) is
**evidence, computed on read, never stored as a rule and never matched on**.

Routes (all behind the secret):

| route | body | effect |
|---|---|---|
| `GET /v1/projects` | — | `{workstreams, projects, suggestions, coverage}` where `suggestions[]` = `{id, kind: "repo"\|"ticket"\|"workspace", value, blocks, minutes, tokens}` and `coverage` = `{attributed, total, since}` for the current week |
| `POST /v1/projects/bundle` | `{"title","workstream","suggestions":[ids]}` | one project with those rules; re-attributes |
| `POST /v1/projects/{id}/rules` | `{"add":[…],"remove":[…]}` | split/extend; a removed repo returns to suggestions with its stable id |
| `POST /v1/projects/{id}/hide` | `{"hidden":true}` | local only |
| `POST /v1/projects/place` | `{"suggestion":id,"same_as":projectId}` | adds the rule to an existing project (LOCAL — see the verified note above) |
| `PUT /v1/workstreams/{key}/off` | `{"off":true}` | writes `workstreams_off` |

Every one of these edits is local to this machine. The response carries
`{"local_only": true, "atlas_editor_url": "<endpoint>/workstreams"}` so the page can say so
and link to where the org-wide edit is actually made.

Suggestion ids are **stable**: `sha1(kind + ":" + value)[:12]`, so a description written
against a suggestion survives, and a split repo comes back with the id it had.

Attribution order (deterministic pass, `internal/agent/projects`):
1. block `repo` dim ∈ some non-hidden project's `repos` whose workstream is not off → that
   project, method `repo`. Two matches → `conflict`, NOT the first.
2. else block branch carries a ticket key matching a project's `ticket_key` → method `ticket`.
3. else (vector toggle on) → the encoder path, method `embedding`.
4. else unattributed → grouped into a suggestion by repo, then ticket key, then workspace.

## Block dims from the sidecar (`POST /blocks`, existing)

Each block already carries `workstreams.{repo, project, branch, model, output_type,
language, skill, tooling}` as `Labeled{value, share, evidence, status}`. **D5 adds**
`tokens: {input, output, cache_read, cache_creation, request}` and `requests: n` per
block, and `/health` gains `dev_blocks: ""|"prompt"|"bin"|"minute"`.

## The block generator (`scripts/blockgen/`)

Writes Claude Code JSONL the sidecar ingests unchanged. A generated transcript MUST:

- open with the untimestamped bookkeeping records real ones open with
  (`custom-title`/`mode`/`file-history-snapshot` shapes are fine as stubs);
- carry `user` lines with `promptId`, `uuid`, `parentUuid`, `timestamp`, `sessionId`,
  `cwd`, `gitBranch`, `version`, `message.content` (string);
- carry `assistant` lines with `requestId`, `message.model`, `message.usage`
  (`input_tokens`, `output_tokens`, `cache_read_input_tokens`,
  `cache_creation_input_tokens`), `message.content` blocks of type `text` and
  `tool_use` (Bash/Read/Edit/Write/Grep, with `command`/`file_path` inputs);
- plant the repository so `workspace.py` resolves it: `cwd` under a workspace dir, a
  `tool_use` Bash `command` mentioning `git remote -v` output or a
  `https://github.com/<org>/<repo>` URL (REMOTE_REPO), and a Read/Edit `file_path` under
  `cwd` touching a REPO_MARKER (`go.mod`, `package.json`, `.git/…`);
- use a FIXED profile of repositories (default 5, org-like: keld-signal, keld-atlas,
  sdk-testbench, atlas-telemetry-typescript, atlas-telemetry-python), each session bound
  to ONE repository (cwd + remote + branch), so the metadata is deterministic per seed
  and counts match the profile rather than being random;
- shape time from today's measured blocks: active runs of 1–6 × 20-minute caps, idle gaps
  15–60 min, sessions 5 min – 4.5 h, 50–80k tokens per active minute, request tokens ≈
  half of raw tokens, 2–4 assistant turns per human prompt;
- be deterministic per `--seed` and never reuse a session/prompt id across seeds.

Output: `<out>/<sanitised-cwd>/<session-uuid>.jsonl`, the layout the watcher expects under
a `claude_code` root, so `KELD_WATCH_ROOTS=claude_code:<out>` is the whole wiring.
