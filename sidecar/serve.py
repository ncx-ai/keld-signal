"""Entry point the keld-agent daemon spawns: `keld-agent-sidecar --port <N>`.

Binds 127.0.0.1 on the given port and serves the FastAPI app. Imports the app
object directly (not by module string) so it works both under a plain Python
run and inside a PyInstaller-frozen binary.
"""
import argparse
import sys

# gliner2 prints a 🧠 emoji when it loads the model; on Windows the default
# cp1252 stream encoding raises UnicodeEncodeError and kills sidecar startup
# (macOS/Linux default to UTF-8). Force UTF-8 on our streams so the frozen
# binary starts wherever it's spawned.
for _stream in (sys.stdout, sys.stderr):
    try:
        _stream.reconfigure(encoding="utf-8")
    except (AttributeError, ValueError):
        pass

import uvicorn

from app.main import app


def _selftest_verifier() -> int:
    """Spawn the attribution verifier's worker child and take ONE real verdict.

    ⚠️ THIS EXISTS BECAUSE NO OTHER TEST CAN SEE THE FAILURE IT CATCHES. `llama_cpp` is a
    ctypes binding whose native libraries (`llama_cpp/lib/libllama.*` plus ~10 ggml shared
    objects) are opened by a path computed at import time, and the one import statement that
    reaches it lives inside `Verifier.__init__` — encrypted bytecode under KELD_OBFUSCATE=1.
    So PyInstaller cannot see the module OR its libraries, unit tests never freeze, and
    `/classify`/`/pii` do not touch this path: a spec missing `collect_all("llama_cpp")` ships
    a binary that starts, is healthy, classifies, scans for PII, and fails EVERY verifier
    verdict. That is precisely the shape freeze_support() and presidio each cost us once.

    Run through the SAME machinery production uses — `app.main._verifier_manager()`, so the
    child is spawned by multiprocessing-spawn re-execing this frozen binary (the freeze_support
    path below), imports llama_cpp inside the child, loads the native libs out of the bundle,
    loads the GGUF and answers. Nothing here is stubbed, which is the point.

    Requires the weights. It FAILS rather than skips when they are absent: a gate that
    quietly passes on the machines that lack the model is the same as no gate, and this one
    exists to be run deliberately (`make freeze-check`) rather than on every laptop.
    """
    from app import verifier as verifier_mod
    from app.main import _verifier_manager

    path = verifier_mod.weights_path()
    if path is None:
        print("FAIL: no verifier GGUF found. Set KELD_VERIFIER_GGUF=/path/to/model.gguf or "
              "provision ~/.keld/models/gemma-4-e2b/model.gguf, then re-run.", file=sys.stderr)
        return 1
    print(f"== verifier self-test: spawning the worker child on {path} ==", flush=True)
    wm = _verifier_manager()
    try:
        out = wm.call({
            "op": "verify",
            "block_text": "Rewrote the invoice reconciliation job and fixed the rounding bug.",
            "dims": {"repo": "acme-billing", "branch": "fix/rounding"},
            "project": {"id": "billing", "title": "Billing platform", "team": "Payments",
                        "description": "Invoicing, reconciliation and payment capture.",
                        "keywords": ["invoice", "reconciliation"]},
        })
    except Exception as exc:                      # noqa: BLE001 — the gate reports, never raises
        print(f"FAIL: the verifier child could not answer: {exc!r}\n"
              "      (llama_cpp or its native libs almost certainly did not travel into the "
              "frozen bundle — see keld-agent-sidecar.spec)", file=sys.stderr)
        return 1
    finally:
        try:
            wm.shutdown()
        except Exception:
            pass
    if not isinstance(out.get("verdict"), bool):
        print(f"FAIL: the verifier answered but not in the expected shape: {out!r}", file=sys.stderr)
        return 1
    print(f"PASS: the verifier worker child returned {out!r}")
    return 0


def main() -> None:
    ap = argparse.ArgumentParser(prog="keld-agent-sidecar")
    ap.add_argument("--port", type=int)
    ap.add_argument("--host", default="127.0.0.1")
    # Not a server mode: run one acceptance check and exit. `verifier` is the only
    # value today; it is the one import path the running server never exercises on
    # its own (see _selftest_verifier).
    ap.add_argument("--selftest", choices=("verifier",))
    args = ap.parse_args()
    if args.selftest == "verifier":
        raise SystemExit(_selftest_verifier())
    if args.port is None:
        ap.error("--port is required unless --selftest is given")
    uvicorn.run(app, host=args.host, port=args.port, log_level="warning")


if __name__ == "__main__":
    # In a PyInstaller-frozen binary, multiprocessing-spawn (the inference worker)
    # re-execs THIS binary to bootstrap the child. freeze_support() intercepts that
    # re-exec and runs the child, so it never falls through to main()'s argparse
    # (which would die on the missing --port the child launch doesn't pass).
    # No-op in a normal (non-frozen / non-child) run.
    import multiprocessing
    multiprocessing.freeze_support()
    main()
