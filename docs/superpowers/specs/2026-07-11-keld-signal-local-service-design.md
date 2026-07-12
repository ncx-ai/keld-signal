# Design: surface the local signal service under `keld signal`

**Date:** 2026-07-11
**Status:** approved (design), pending implementation plan

## Problem

`keld` (the CLI users install) and `keld-agent` (the enrichment daemon) are
separate binaries. Users interact with `keld`, but the daemon's operational
surface — is the service running? is enrichment healthy? what does the pipeline
make of a prompt? — is only reachable via `keld-agent` subcommands
(`status`/`metrics`/`enrich`, the latter two added this session). `keld signal
status` today reports only *tool-configuration* status (login + which AI tools
are configured + hook version); it says nothing about the local service.

We want a **unified, singular `keld` CLI** as the face of the product: `keld
signal` should expose the daemon's health and diagnostics, so a user never has
to reach for `keld-agent` directly for day-to-day inspection.

## Goals

- `keld signal status` also reports the **local signal service**: OS service
  state plus, when reachable, a one-line live-health summary (backend, model
  state, RSS).
- `keld signal enrich [prompt]` — run the enrichment pipeline on a test prompt
  and print the profile JSON (local only; never published).
- `keld signal metrics` — print the running sidecar's `/metrics` JSON.
- One implementation shared between `keld` and `keld-agent`; no behavior change
  to the existing `keld-agent` commands.

## Non-goals

- Merging the two binaries into one (`keld agent run …`) — deferred; `keld-agent`
  remains the daemon binary and a power-user/automation surface.
- Shelling out from `keld` to the `keld-agent` binary (rejected: needs binary
  discovery, fails if not installed, adds a subprocess hop).
- A `--json` machine interface for these commands (YAGNI; add if an installer
  needs it).

## Chosen approach: shared in-process package

`keld` and `keld-agent` are two `main` packages over one shared `internal/` tree
in the same Go module, so `keld` can call daemon/enrichment code **in-process**.
It stays a single static binary (it merely starts depending on `enrich`,
`enrich/sidecar`, `agentcfg`, and `service` — all pure Go, no runtime deps).

Extract the client-side logic (currently in `internal/agentcli/{metrics,enrichcmd}.go`)
into a new neutral package **`internal/localagent`**. Both cobra layers become
thin wrappers over it, so there is exactly one implementation of each behavior.

### `internal/localagent` API

```go
// Prompt input (args joined, else stdin), shared by both `enrich` commands.
func ReadPrompt(args []string, stdin io.Reader) (string, error)

// Pick the enrichment backend + a human note naming it.
func ResolveModel(info *agentcfg.Info, forceDeterministic bool) (enrich.Model, string)

// Sidecar /metrics URL from agent.json, or an explanatory error.
func MetricsURL(info *agentcfg.Info) (string, error)

// GET a URL, returning the body (errors on non-200). Small-body capped.
func FetchText(url string) (string, error)

// Combined local-service health for the status view. Dependencies are
// injected so it is unit-testable with no running daemon.
func Health(statusFn func() (string, error), fetchFn func() (string, error)) Health

type Health struct {
    Service     string  // service.Status() string ("active", "not running", …)
    DaemonUp    bool    // agent.json present
    Backend     string  // "GLiNER2 sidecar" | "deterministic (ML disabled)" | "sidecar unreachable"
    ModelState  string  // "loaded" / "evicted" / … when the sidecar answered
    RSSMB       float64
    ModelCostMB float64
    MetricsOK   bool    // /metrics parsed successfully
}
```

`Health` reads `agentcfg` for the daemon/sidecar port, calls `fetchFn` for
`/metrics`, and parses the small subset it needs (`model_state`,
`memory.rss_mb`, `memory.model_cost_mb`). Production callers pass
`service.Status` and a closure that fetches the sidecar `/metrics`; tests pass
fakes.

## Component changes

### `keld signal status` (`internal/cli/status.go`)

After the existing login + tool rows + hook line, append a section:

```
Local signal service:
  service     active (systemd --user)
  daemon      reachable
  backend     GLiNER2 sidecar · loaded
  memory      rss 2743 MB (model 2650)
```

Rendering is driven by `localagent.Health`. All best-effort — a missing daemon,
unreachable sidecar, or metrics-parse failure degrades to a shorter section and
**never** fails the whole `status` command. Fallbacks:

- `agent.json` absent → `daemon      not running` (omit backend/memory).
- `sidecar_port == 0` → `backend     deterministic (ML disabled)`.
- `sidecar_port` set but `/metrics` unreachable → `backend     sidecar unreachable`.

### `keld signal enrich` / `keld signal metrics` (new files in `internal/cli`)

Thin cobra commands that mirror the `keld-agent` equivalents:

- `enrich [prompt]` — flags `--deterministic`, `--source` (default `claude_code`);
  reads the prompt (`ReadPrompt`), resolves the model (`ResolveModel`, note to
  stderr), runs `enrich.Run`, prints the profile as indented JSON to stdout.
  Local only; never publishes. Carries the same shell-quoting tip in its help.
- `metrics` — resolves the sidecar `/metrics` URL and prints the body to stdout.

Registered on the `signal` group in `internal/cli/root.go`.

### `internal/agentcli` refactor

`metrics.go` / `enrichcmd.go` keep their cobra command constructors but delegate
their bodies to `internal/localagent`. The helper functions (`readPrompt`,
`resolveEnrichModel`, `sidecarMetricsURL`, `fetchText`) move to `localagent`;
the agentcli tests move or adapt to the new package. No user-visible change to
`keld-agent enrich`/`metrics`.

## Data flow

```
keld signal status
  └─ localagent.Health(service.Status, fetch /metrics)
       ├─ agentcfg.Read()            → daemon/sidecar ports
       └─ GET 127.0.0.1:<scPort>/metrics → model_state, rss, model_cost

keld signal enrich "<prompt>"
  └─ localagent.ReadPrompt → ResolveModel(agentcfg.Read()) → enrich.Run → JSON stdout
       (ResolveModel builds a sidecar.Client at the agent.json sidecar_port, else deterministic)

keld signal metrics
  └─ localagent.MetricsURL(agentcfg.Read()) → FetchText → stdout
```

## Error handling

- `status`: daemon introspection is best-effort; errors shrink the section, never
  abort. Tool/login status is unaffected.
- `enrich`/`metrics`: surface a clear error (`keld-agent is not running`,
  `sidecar is not running (ML disabled …)`, `sidecar returned HTTP <code>`) and a
  non-zero exit, consistent with the existing `keld-agent` commands.

## Testing (TDD)

- `internal/localagent`: unit tests for `MetricsURL`, `FetchText` (httptest),
  `ResolveModel`, `ReadPrompt` (ported from agentcli), and `Health` driven by a
  fake `statusFn` and an httptest sidecar — covering reachable, unreachable,
  deterministic (`sidecar_port==0`), and daemon-absent cases.
- `internal/cli`: a status-section rendering test given a fabricated `Health`
  (each fallback), and command-wiring smoke tests for `enrich`/`metrics`.
- `internal/agentcli`: existing tests keep passing after delegation.
- Live end-to-end: `keld signal status` against the running service; `keld signal
  enrich` and `keld signal metrics` verified against the live sidecar.

## Risks / notes

- `keld` binary grows modestly (pulls in `enrich`/`sidecar`); still one static
  binary, acceptable.
- `service` is per-OS with build tags; `keld` already builds per-OS, so importing
  it is fine.
- "daemon reachable" is inferred from `agent.json` presence + a sidecar response;
  the daemon's own ingress has no status endpoint and we deliberately don't add
  one here.
