# Transcript fixtures for the usage mirror

Every file here is a **real excerpt** of a transcript one of the three tools
wrote on a developer machine on 2026-09-19. None of it is hand-written, and
none of it is paraphrased — paraphrase would produce a fixture that agrees with
the parser because both were written from the same idea of the format, which is
the failure `sidecar/app/test_prompt_id_seam.py` exists to prevent one layer
down.

## How text was kept out

Text-bearing keys were **deleted outright**, not reworded:

| File | Source | What was removed |
|---|---|---|
| `claude_code_session.jsonl` | `~/.claude/projects/…jsonl` | `message.content` emptied; `wireToolInputs`, `toolUseResult`, `summary`, `thinkingMetadata`, `diagnostics`, `context_management`, `container` deleted |
| `codex_rollout.jsonl` | `~/.codex/sessions/2026/09/18/rollout-…jsonl` | `session_meta.base_instructions` (Codex's own system prompt) and every non-listed payload key deleted; the `token_count` records are numbers only and are verbatim |
| `codex_reemission.jsonl` | same rollout, later in the file | as above |
| `gemini_session.json` | `~/.gemini/tmp/…/chats/session-…json` | `content` replaced with a fixed literal, `thoughts` and `toolCalls` deleted, `projectHash` blanked |
| `claude_code_native_api_request.json` | `internal/agent/teleproxy/testdata/claude_code_logs.json` | the one real captured `claude_code.api_request` record, lifted verbatim, with the `prompt` and `response` attributes removed |

**Two placeholders, and they are placeholders rather than excerpts.** The
Claude Code user line's `message.content` and the Gemini user message's
`content` hold the literal `"x"`, because the mirror's human-prompt gate
requires *some* text (it is how a synthetic user record is told from a typed
one) and a fixture with the field deleted could not exercise it. Nothing anyone
typed survives in either file.

Paths (`cwd`, `gitBranch`) are left as written: they are this repository's own
paths, they are not prompt text, and the mirror never reads them.

## Why these particular excerpts

- **`claude_code_session.jsonl`** holds one request written as **three**
  assistant lines and one written as **one**. That is the defect the mirror
  exists to avoid: measured over the 40 largest real transcripts here, 13,755
  assistant lines carrying a `message.usage` resolve to 7,683 distinct
  `requestId`s (4,088 written as more than one line, max 11, 0 disagreeing with
  themselves about their tokens), so a record per LINE reports **1.79×** the
  tokens the work cost. A fixture holding only single-line requests would pass
  either way.
- **`codex_reemission.jsonl`** holds two **adjacent real** `token_count`
  records, 1.2 s apart, carrying an identical `total_token_usage` *and* an
  identical `last_token_usage`. Measured over the 23 most recent rollouts here,
  936 of 10,061 records (9.3%) have that shape; pricing both double-counts
  41,958 tokens.
- **`claude_code_native_api_request.json`** is the tool-sent half of the dedup
  comparison. It is a capture, so what the mirror is compared against is what
  Claude Code actually exported rather than what this package believes it
  exports.

Regenerating any of these means re-running the extraction against a real
machine and re-checking that the text keys are gone.
