# keld

The Keld CLI configures your local AI coding tools (Claude Code, Codex, Gemini
CLI) to send telemetry to Keld Atlas.

## Install

Recommended (isolated, works on every platform):

```bash
pipx install keld
```

If you don't have pipx yet: `python -m pip install --user pipx && python -m pipx ensurepath`.

Alternatives: `uvx keld` / `uv tool install keld` (if you use uv), or
`pip install keld` **inside a virtual environment** (a bare `pip install` into
system Python fails with `externally-managed-environment` on modern distros).

## Usage

```bash
keld login      # authenticate (also happens automatically on first `setup`)
keld setup      # detect tools, show changes, configure telemetry + install hook
keld status     # see what's configured
keld doctor     # diagnose problems
keld uninstall  # cleanly remove everything Keld added
```

`keld setup` flags: `--tool claude_code,codex` (target specific tools),
`--dry-run` (show changes only), `--yes` (skip confirmation),
`--no-login` (fail instead of opening a browser, for CI).

## Environment

- `KELD_HOME` — where credentials, the hook, and the manifest live (default `~/.keld`).
- `KELD_API_URL` — Atlas base URL (default `https://atlas.keld.co`).
