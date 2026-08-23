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
- **No health-gated fallback — but a third, always-on `ml_backend` mode is not
  that.** `ml_backend:"auto"` (default) always runs on GLiNER2; a
  reloading/evicted/not-yet-provisioned sidecar is waited out (jobs
  queue/spool until it's ready), **never** silently swapped for a
  lower-fidelity substitute of the same facets — that substitution is what's
  forbidden. `ml_backend:"deterministic"` is a different thing: it runs a
  different, smaller set of facets that need no model (e.g. credential
  detection) and is **always** on when selected, never health-gated — there is
  no sidecar in this mode to be healthy or not. `ml_backend:"off"` means
  enrichment is **disabled entirely** (no enrichment worker, `/enrich`
  accepts-and-discards). Don't reintroduce a *substitute* for the model's
  facets; a *different* facet set that needs no model is fine and already
  exists. Bound per-job work with a cancellable deadline + re-spool cap (see AGENTS.md
  → Delivery reliability); don't reintroduce a deterministic fallback or a
  health-gated substitute.
- **Use the superpowers workflow** (brainstorm → plan → TDD → systematic
  debugging) for non-trivial changes; no ad-hoc edits.
- **Latest models:** Opus 4.8 / Sonnet 4.6 / Haiku 4.5 / Fable 5. Use the official
  Anthropic SDK only if/when adding inference; keep provider *reporting* on httpx.
