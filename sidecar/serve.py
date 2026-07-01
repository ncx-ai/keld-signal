"""Entry point the keld-agent daemon spawns: `keld-agent-sidecar --port <N>`.

Binds 127.0.0.1 on the given port and serves the FastAPI app. Imports the app
object directly (not by module string) so it works both under a plain Python
run and inside a PyInstaller-frozen binary.
"""
import argparse

import uvicorn

from app.main import app


def main() -> None:
    ap = argparse.ArgumentParser(prog="keld-agent-sidecar")
    ap.add_argument("--port", type=int, required=True)
    ap.add_argument("--host", default="127.0.0.1")
    args = ap.parse_args()
    uvicorn.run(app, host=args.host, port=args.port, log_level="warning")


if __name__ == "__main__":
    main()
