# Codex rollout fixtures — where each one came from

These are **captured** files, not hand-written ones (TR-AC-4). The identity
scheme they replace — a file-local `ordinal` on a `user_message` line — was
invented against no real rollout and produced **zero** captured Codex prompts
for the whole life of the feature. A fixture that does not resemble production
is how that stayed invisible, so each file here is a real rollout from
`~/.codex/sessions` on Gabriel's machine, put through
`scripts/redact-rollout.py` and nothing else.

The script replaces the *language* in a rollout with a **same-length**
placeholder, so byte offsets, message lengths and file size are unchanged. It
leaves every structural field alone: `type`, ids, `timestamp`, `cwd`,
`workspace_roots`, `model`, `cli_version`, token counts, and a
`CommandExecution`'s `command` array.

| Fixture | Source rollout | cli_version | Slice | Human-turn shape |
|---|---|---|---|---|
| `rollout-0.125.jsonl` | `2026/04/29/rollout-2026-04-29T01-55-23-019dd64d-e967-7c82-93b8-313f862b6702.jsonl` | 0.125.0 | whole file, 23 lines | 2 × `event_msg`/`user_message` |
| `rollout-0.151.jsonl` | `2026/09/05/rollout-2026-09-05T01-14-16-01a06e7c-a243-70f1-bb8d-ad8239bc843c.jsonl` | 0.151.0 | **first 40 lines** of 1,920 (the whole file is 12.8 MB) | 3 × `item_completed` `UserMessage`, **0** `user_message` |
| `rollout-0.153.4.jsonl` | `2026/09/14/rollout-2026-09-14T00-18-02-01a09cd9-4ff9-7cb3-b217-4d622483e83d.jsonl` | 0.153.4 | whole file, 48 lines | 4 × `event_msg`/`user_message` |

Reproduce:

```sh
python3 scripts/redact-rollout.py \
  ~/.codex/sessions/2026/09/05/rollout-2026-09-05T01-14-16-01a06e7c-a243-70f1-bb8d-ad8239bc843c.jsonl \
  internal/agent/watch/testdata/codex/rollout-0.151.jsonl --head 40
```

## Two facts these fixtures exist to pin

**0.151 writes the human turn ONLY as an item.** 1,920 lines, 47
`item_completed` `UserMessage` items and **zero** `event_msg/user_message`
lines. A watcher that reads only the classic shape captures nothing at all on
a 0.148–0.151 machine, and nothing would say so.

**`ordinal` is not an identity and never was.** 0.125.0 and 0.153.4 stamp
**no** `ordinal` on any line; 0.151 stamps one on **every** line, human turns
included. So the old `<session>#<ordinal>` id was unresolvable on two of the
three and ambiguous on the third. ⚠️ The 0.151 fixture therefore **does** carry
`ordinal` keys, on its non-human lines — that is production, and stripping them
would make the fixture stop being the file it was captured from.
`TestCodexFixturesMatchProduction` asserts AC-4 as written (no `ordinal` on a
**human-turn** line) and `TestCodexIdentityNeverComesFromOrdinal` asserts no Go
code reads the field either way.
