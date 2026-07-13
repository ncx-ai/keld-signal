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


def main() -> None:
    ap = argparse.ArgumentParser(prog="keld-agent-sidecar")
    ap.add_argument("--port", type=int, required=True)
    ap.add_argument("--host", default="127.0.0.1")
    args = ap.parse_args()
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
