# Codex 0.153.4 hook payloads — captured, then redacted

Captured on **2026-09-15** from `codex-cli 0.153.4` on macOS, by running a real
`codex exec` against a local mock Responses API in an isolated `CODEX_HOME`
whose `config.toml` registered three inline hooks whose command was `cat > …`.

```sh
# config.toml in the isolated CODEX_HOME
[model_providers.mock]
base_url  = "http://127.0.0.1:18231/v1"
wire_api  = "responses"
env_key   = "MOCK_API_KEY"

[[hooks.SessionStart]]
hooks = [ { type = "command", command = 'cat > <out>/SessionStart.json' } ]
[[hooks.UserPromptSubmit]]
hooks = [ { type = "command", command = 'cat > <out>/UserPromptSubmit.json' } ]
[[hooks.Stop]]
hooks = [ { type = "command", command = 'cat > <out>/Stop.json' } ]

# then, from a scratch working directory
env -i HOME=$HOME PATH=$PATH CODEX_HOME=<dir> MOCK_API_KEY=sk-mock \
  codex exec --skip-git-repo-check --dangerously-bypass-hook-trust \
  "reply with one word" < /dev/null
```

⚠️ **Without `--dangerously-bypass-hook-trust` the hooks do not run and Codex
says nothing about it.** That is the trust gate: Codex only executes a hook a
human has approved in the TUI's `/hooks`, recording the approval as a
`[hooks.state."<source>:<event>:i:j"]` table with `enabled = true` and a
`trusted_hash`. It is why keld's Codex row reads `approval_required` rather
than `broken` — see `tools.CodexHooksTrusted`.

## What was redacted

`prompt` (UserPromptSubmit) and `last_assistant_message` (Stop) are replaced
with `"<redacted>"`. Nothing else was touched: the fixture is the **shape**,
and the shape is the point — `turn_id` and no `prompt_id` anywhere, which is
why `hook.Run` had to learn `turn_id`.

The `transcript_path` and `cwd` are the capture's own scratch directory,
verbatim. They are long and meaningless; they are kept rather than tidied so
the file stays exactly what Codex wrote.

## What the payloads say

| Event | Carries |
|---|---|
| `SessionStart` | `session_id`, `transcript_path`, `cwd`, `model`, `permission_mode`, `source` — **no `turn_id`**, so no pointer |
| `UserPromptSubmit` | the above **plus `turn_id` and `prompt`** — the pointer's `<session_id>#<turn_id>` |
| `Stop` | the above plus `turn_id`, `stop_hook_active`, `last_assistant_message` |

**No `prompt_id` in any of them**, at any event. That single absence is why
Codex produced zero captured prompts: `hook.Run` read `prompt_id`, found "",
and returned silently.
