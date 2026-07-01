#!/usr/bin/env python3
"""Local enrichment sink — prints each enrichment the daemon POSTs, as it is generated.

Point the daemon at it (foreground, so the env override applies):
    KELD_CTX_ENDPOINT=http://localhost:8710 KELD_CTX_TOKEN=dev keld-agent
The daemon publishes to <endpoint>/v1/enrichments; this sink accepts any path,
replies 200, and pretty-prints the body. Ctrl-C to stop.
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("content-length", 0) or 0)
        body = self.rfile.read(n)
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"ok":true}')
        try:
            print(json.dumps(json.loads(body), indent=2))
        except Exception:
            print(body.decode("utf-8", "replace"))
        print("-" * 60, flush=True)

    def log_message(self, format, *args):  # silence default request logging
        pass


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8710
    print(f"enrichment sink on http://localhost:{port}  (Ctrl-C to stop)", flush=True)
    HTTPServer(("127.0.0.1", port), Handler).serve_forever()
