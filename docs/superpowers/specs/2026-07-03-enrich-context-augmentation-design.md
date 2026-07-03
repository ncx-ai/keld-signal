# On-Device Enrichment — Context Augmentation

**Date:** 2026-07-03
**Repo:** keld-cli (on-device agent + vendored GLiNER2 sidecar)
**Status:** Spec for review (design defaults chosen while user away — see "Open for review")

## Problem

Each prompt is classified by GLiNER in isolation. A fragment like `"Ok, that's fine"` or `"do it"`
carries almost no signal alone, so function/subcategory/activity classification is unreliable. The
model already receives a tiny context preamble (`repository`, `tool`) but nothing about **what the
person is actually working on** — the surrounding conversation.

## Goal

Prepend a compact, **source-gated** context block — session metadata + the last few user prompts —
to the **classification passes only**, so ambiguous fragments are classified against the work they
continue. Prove the lift with the existing eval harness; never regress isolated-prompt accuracy or
on-device latency/privacy.

## Grounding (verified 2026-07-03)

- Classification passes already prepend `Meta.Preamble()` (`internal/agent/enrich/pass.go:33`) —
  currently `"[Context — repository: <cwd>; tool: <source>]\nTask: " + promptText`
  (`internal/agent/enrich/meta.go`). Entity/sensitivity passes get **raw** text (need offsets) — so
  context belongs only in the classification preamble, which is already the case.
- `queue.Job` carries `SessionID`, `TranscriptPath`, `Cwd`, `PromptID`, `Source`
  (`internal/agent/queue/queue.go`). `resolve.Resolve(source, transcriptPath, promptID, inline)`
  already opens the transcript to fetch the current prompt — so **prior user prompts are in a file we
  already read**.
- `daemon.go:71` builds `enrich.Run(text, j.Source, enrich.Meta{Repo: j.Cwd, Tool: j.Source}, m)` —
  one prompt at a time, no history.
- **No hard context wall:** GLiNER2 `classify_text`/`extract` default `max_len=None` (no truncation)
  and the encoder (`deberta-v3-large`) uses relative position encoding. The practical budget is the
  sidecar's char clip (`KELD_SIDECAR_MAX_CHARS`, default 20000) plus on-device latency/memory
  (attention is O(n²); the `InferenceRunner` is single-flight + mem-capped). So a few bounded prior
  prompts fit comfortably.
- An eval harness exists: `internal/agent/enrich/eval/` (`GoldRow`, `Pred`, `Score` for per-field
  accuracy) + the inference-enrichment `test_eval.py` lineage.

## Decisions (defaults; open for review)

- **Content:** the last **N=3** *user* prompts from the same session, newest-first (no assistant
  turns in v1).
- **Budget:** each prior prompt truncated; total recent-prompt block capped at **~1500 chars**.
- **Metadata added to the preamble:** **git branch** (`<cwd>/.git/HEAD`) + **`.keld.toml` project**
  name/description. (Language + recent-files-touched deferred.)
- **Source gating:** applied only for **interactive coding tools** — default allowlist
  `{claude_code, codex}` — off for all other/future sources; configurable via settings.
- **Eval:** extend `enrich/eval` with a gold set including fragment prompts + preceding context;
  assert augmented accuracy ≥ baseline. Gate the merge on it.

## Architecture

### Components

1. **`ContextGatherer`** — new, `internal/agent/enrich/context` (or `enrich`), pure/testable.
   `Gather(job queue.Job) enrich.SessionContext` returns:
   - `RecentPrompts []string` — up to N prior **user** prompts before `PromptID`, newest-first,
     truncated to the char budget. Per-source transcript parsing: Claude Code → parse the JSONL for
     user-role entries preceding the current one; Codex → best-effort from its transcript/rollout
     format; unknown/unavailable → empty.
   - `GitBranch string` — parsed from `<Cwd>/.git/HEAD` (`ref: refs/heads/<branch>` → `<branch>`),
     empty on any failure.
   - `Project string` — `name` (+ short `description`) from `<Cwd>/.keld.toml`, empty if absent.
   - **Best-effort:** every field independently degrades to empty on read/parse error; never errors.

2. **`Meta` extension** (`enrich/meta.go`): add `RecentPrompts []string`, `GitBranch string`,
   `Project string`. `Preamble()` renders only non-empty fields, compactly, e.g.:
   ```
   [Context — repository: <cwd>; branch: <branch>; project: <name>; tool: <tool>]
   Recent prompts:
    1. <most-recent prior prompt>
    2. <next>
   Task: <current prompt>
   ```
   Ordering keeps `Task: <current prompt>` **last** so it reads as the thing to classify; the recent
   block is bounded so the head-clip (sidecar) never reaches the current prompt in practice.

3. **Source policy** — `enrich.ContextEligible(source, settings) bool`: true iff `source` is in the
   configured interactive-coding-tool allowlist (default `{claude_code, codex}`). New setting
   `enrich.context_sources` (or equivalent) in `settings`.

4. **Wire-up** (`daemon.go process()`): if `ContextEligible(j.Source, set)`, build
   `Meta{Repo, Tool, RecentPrompts, GitBranch, Project}` via `ContextGatherer.Gather(j)`; else the
   current `Meta{Repo, Tool}`. Unchanged downstream (`enrich.Run` → passes → sidecar).

### Data flow

hook → `queue.Job` (SessionID, TranscriptPath, …) → `daemon.process()` → [eligible source]
`ContextGatherer.Gather(job)` → `Meta{…context…}` → classification passes use `Meta.Preamble()` →
sidecar `/classify`. Entity/sensitivity passes untouched (raw text).

### Error handling / degradation

`ContextGatherer` is best-effort and total-failure-safe: missing transcript, unreadable `.git`, or
absent `.keld.toml` each yield empty fields; classification proceeds with whatever context exists.
`process()` is already panic-isolated per job.

### Privacy

Context is used **only** in the on-device classification input; it is never sent to Atlas or stored
— the persisted enrichment is just labels/confidences. Recent prompts never leave the device.
Sensitivity/entity passes keep raw text (no preamble), so masking offsets are unaffected.

## Testing

- **ContextGatherer (unit, fixtures):** a Claude Code transcript fixture → correct last-3 user
  prompts, newest-first, budget/truncation respected; current prompt excluded; missing/short
  transcript → fewer/empty; Codex fixture → best-effort/empty; `.git/HEAD` + `.keld.toml` parsing
  incl. failure→empty.
- **Meta.Preamble (unit):** renders branch/project/recent-prompts when present; omits empty; stable
  shape; still classification-only.
- **Source policy (unit):** `claude_code`/`codex` eligible; other sources not; setting override.
- **Eval (gate):** extend `enrich/eval` gold set with fragment prompts + preceding context; run
  Score baseline (no context) vs augmented; require augmented ≥ baseline on function/subcategory,
  and no regression on standalone (non-fragment) prompts.

## Open for review

- N=3 and ~1500-char budget — tune after the eval.
- Metadata set (branch + project) — add language/recent-files if the eval shows they help.
- Whether Codex history extraction is in v1 or a fast-follow (depends on transcript availability).
- Assistant-turn gists — deferred; revisit if user-prompts-only underperforms on the eval.

## Non-goals

- No assistant-turn content in v1 (user prompts only).
- No server-side (Atlas) augmentation — enrichment stays on-device.
- No change to entity/sensitivity passes.
- No cross-session context (same SessionID only).
