#!/usr/bin/env python3
"""Local stub for the client-telemetry (clientevents) endpoints — for live,
end-to-end verification of the daemon's capture->transport pipeline against a
real server, without touching Atlas.

Serves:
  GET  /v1/enrichment-settings   -> 200 + a client_telemetry block with
                                     deliberately LOW thresholds so a resource
                                     anomaly (sustained high RSS) fires almost
                                     immediately against the daemon's own
                                     process. Thresholds are env-overridable
                                     (see below); defaults chosen so the
                                     daemon's own RSS trivially exceeds
                                     rss_threshold_mb.
  POST /v1/signal/client-events  -> 200 {"ok": true}; pretty-prints each
                                     received envelope ({schema_version,
                                     install_id, events[]}) to stdout and
                                     appends it to an in-memory list.
  *    (any other path)          -> 200 {"ok": true} (e.g. /v1/enrichments —
                                     so the daemon's other publishers, if
                                     exercised, don't see errors)

Point the daemon at it (foreground, so the env overrides apply — use a fresh
KELD_HOME and put {"ml_backend":"off"} in $KELD_HOME/agent-config.json to
skip sidecar/model provisioning):

    KELD_HOME=$(mktemp -d)
    echo '{"ml_backend":"off"}' > "$KELD_HOME/agent-config.json"
    KELD_HOME="$KELD_HOME" \\
    KELD_CTX_ENDPOINT=http://127.0.0.1:8711 KELD_CTX_TOKEN=dev \\
    KELD_CLIENTEVENTS_SAMPLE=1s KELD_CLIENTEVENTS_FLUSH=2s KELD_SETTINGS_POLL=1s \\
    ./keld-agent run

Threshold env overrides (all optional; shown with their defaults):
    SINK_RSS_THRESHOLD_MB=1        SINK_CPU_THRESHOLD_PCT=0.1
    SINK_SUSTAINED_WINDOW_S=1      SINK_GAUGE_INTERVAL_S=2
    SINK_MIN_SEVERITY=info         SINK_SAMPLE_RATE=1.0
    SINK_GAUGES_ENABLED=true       SINK_ENABLED=true

Ctrl-C to stop. Received envelopes are also available in-process as
`Handler.received` (a list of parsed JSON bodies) for scripts that import
this module rather than running it standalone.
"""
import json
import os
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer


def _bool_env(name, default):
    v = os.environ.get(name)
    if v is None:
        return default
    return v.strip().lower() not in ("0", "false", "no", "")


def _settings_body():
    return {
        "client_telemetry": {
            "enabled": _bool_env("SINK_ENABLED", True),
            "min_severity": os.environ.get("SINK_MIN_SEVERITY", "info"),
            "sample_rate": float(os.environ.get("SINK_SAMPLE_RATE", "1.0")),
            "gauges_enabled": _bool_env("SINK_GAUGES_ENABLED", True),
            "gauge_interval_s": int(os.environ.get("SINK_GAUGE_INTERVAL_S", "2")),
            "rss_threshold_mb": float(os.environ.get("SINK_RSS_THRESHOLD_MB", "1")),
            "cpu_threshold_pct": float(os.environ.get("SINK_CPU_THRESHOLD_PCT", "0.1")),
            "sustained_window_s": int(os.environ.get("SINK_SUSTAINED_WINDOW_S", "1")),
        }
    }


class Handler(BaseHTTPRequestHandler):
    received = []  # class-level running record of posted client-event envelopes

    def _read_body(self):
        n = int(self.headers.get("content-length", 0) or 0)
        return self.rfile.read(n)

    def _reply_json(self, obj, code=200):
        payload = json.dumps(obj).encode("utf-8")
        self.send_response(code)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self):
        if self.path.startswith("/v1/enrichment-settings"):
            self._reply_json(_settings_body())
            return
        self._reply_json({"ok": True})

    def do_POST(self):
        body = self._read_body()
        if self.path.startswith("/v1/signal/client-events"):
            self._reply_json({"ok": True})
            try:
                envelope = json.loads(body)
            except Exception:
                print(body.decode("utf-8", "replace"), flush=True)
                return
            Handler.received.append(envelope)
            print(f"### POST {self.path}  (batch #{len(Handler.received)})", flush=True)
            print(json.dumps(envelope, indent=2), flush=True)
            print("-" * 60, flush=True)
            return
        # Any other path (e.g. /v1/enrichments): accept and print briefly.
        self._reply_json({"ok": True})
        try:
            print(f"### POST {self.path} (other)", flush=True)
            print(json.dumps(json.loads(body), indent=2), flush=True)
        except Exception:
            print(body.decode("utf-8", "replace"), flush=True)
        print("-" * 60, flush=True)

    def log_message(self, format, *args):  # silence default request logging
        pass


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8711
    print(f"signal-events sink on http://127.0.0.1:{port}  (Ctrl-C to stop)", flush=True)
    print(f"  GET  /v1/enrichment-settings -> {json.dumps(_settings_body())}", flush=True)
    HTTPServer(("127.0.0.1", port), Handler).serve_forever()
