# CLAUDE.md

The full architecture, layout, conventions, build/test commands, and gotchas for
the **Keld client** (the `keld` CLI + the `keld-agent` on-device enrichment daemon
+ its GLiNER2 sidecar) live in **AGENTS.md** — imported here so this is the single
source of truth:

@AGENTS.md

## Claude Code specifics

- **This repo is the on-device half of Keld.** The enrichment agent
  (`keld-agent` + sidecar) is the core, not the CLI. Keep the privacy invariant
  front of mind: **raw prompt text is read locally and must never be transmitted**
  — only masked labels + masked spans are published.
- **Do work, then verify with real output.** Run `go test ./...` for Go and the
  standalone sidecar test scripts before claiming something passes; paste results.
- **Go → host toolchain; sidecar → the venv.** Run sidecar code/tests with
  `~/.keld/sidecar-venv/bin/python` (Python 3.12), never the host interpreter.
  Sidecar tests are standalone scripts (no pytest).
- **Load tests are heavy and opt-in** (real model, minutes-long; CPU-saturating).
  Don't run them casually on a shared machine; prefer the fast unit tests. See
  `sidecar/loadtest/README.md`.
- **Don't fan out inference.** Single-flight in the sidecar is deliberate load
  protection; RAM is bounded by eviction, CPU by the governor + thread scaler.
- **Never silently degrade to deterministic.** The daemon stays fully functional
  without the sidecar (ML disabled ⇒ deterministic), but when ML is *enabled*
  enrichment must always run on GLiNER2 — wait out a reloading/evicted sidecar,
  never substitute deterministic. Bound per-job work with a cancellable deadline +
  re-spool cap (see AGENTS.md → Delivery reliability); don't reintroduce a
  health-gated fallback.
- **Use the superpowers workflow** (brainstorm → plan → TDD → systematic
  debugging) for non-trivial changes; no ad-hoc edits.
- **Latest models:** Opus 4.8 / Sonnet 4.6 / Haiku 4.5 / Fable 5. Use the official
  Anthropic SDK only if/when adding inference; keep provider *reporting* on httpx.
