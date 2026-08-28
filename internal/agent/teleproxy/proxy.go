// Package teleproxy is the daemon's loopback OTLP receiver.
//
// ⚠️ IT EXISTS SO THAT NO AI TOOL EVER HOLDS AN ATLAS CREDENTIAL. Tools read
// their configuration once, at startup, and keep it in memory; when the org's
// ingest token rotates, every running tool goes on POSTing the old one and its
// telemetry is rejected until a human restarts the editor. Measured: telemetry
// died for 40 minutes and `keld signal doctor` reported no problems throughout,
// correctly — the stale copy lives inside a process it cannot inspect.
//
// The fix is not to detect that (a tool's child process cannot even see the
// token — Claude Code applies its `env` block to its own OTEL SDK and exports
// nothing). It is to stop handing the tool a credential: tools POST here, and
// the daemon forwards with the token it already self-heals.
//
// See docs/superpowers/specs/2026-08-27-telemetry-loopback-proxy-design.md.
package teleproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// DefaultPort is the daemon's loopback OTLP port.
//
// ⚠️ FIXED, and deliberately NOT 4317/4318. It has to be fixed because it is
// written into tool configs that are read once at startup — the ingress port is
// ephemeral (127.0.0.1:0, observed as three different ports within one hour) and
// writing THAT would go stale on every daemon restart. And it must not be OTLP's
// own 4317/4318, which any real local collector may already hold; losing
// telemetry to a port collision would be silent.
const DefaultPort = 14318

// EnvPort overrides DefaultPort. Named here as well as in settings so the
// precedence chain is stated where the port is resolved.
const EnvPort = settings.TelemetryPortEnv

// maxBody caps one OTLP batch. Generous for real payloads, bounded so a runaway
// exporter cannot turn the daemon into a memory-pressure problem.
const maxBody = 4 << 20

// Port returns the loopback telemetry port: KELD_TELEMETRY_PORT, else
// agent-config.json's `telemetry_port`, else DefaultPort.
//
// ⚠️ ONE RESOLVER FOR BOTH HALVES. The daemon binds this and `keld signal setup`
// writes it into every tool config; if they disagreed the tools would post into a
// socket nobody holds. Routing both through settings is also what makes the
// documented remedy for a port collision actually reachable — no service
// definition on any OS carries an environment block, so an env-only knob could
// not be applied to an installed daemon at all.
func Port() int { return settings.Load().TelemetryPortOrDefault(DefaultPort) }

// Addr is the host:port tools are configured to POST to.
func Addr() string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(Port())) }

// Proxy receives OTLP on loopback and forwards it to Atlas.
type Proxy struct {
	secret string
	logs   *clientevents.Transport
	metric *clientevents.Transport

	mu          sync.Mutex
	lastForward time.Time
	sessions    map[string]time.Time
	// lastPersisted throttles the on-disk record: doctor needs a coarse "when did
	// telemetry last arrive", not a write per OTLP batch.
	lastPersisted time.Time
	statePath     string

	wg sync.WaitGroup
}

// New builds a Proxy forwarding to Atlas's logs and metrics endpoints, using
// token for the Atlas credential (a GETTER: it is read per request, so a
// rotation mid-flight is picked up — capturing it would rebuild this package's
// own reason for existing inside the daemon) and secret to authenticate tools.
//
// Logs and metrics get separate spool subdirectories: a poison metrics batch
// must not be able to block logs behind it.
func New(logsEndpoint, metricsEndpoint string, token func() string, secret, spoolDir string) *Proxy {
	p := &Proxy{
		secret:    secret,
		logs:      clientevents.NewTransport(logsEndpoint, token, filepath.Join(spoolDir, "logs")),
		metric:    clientevents.NewTransport(metricsEndpoint, token, filepath.Join(spoolDir, "metrics")),
		statePath: StatePath(),
	}
	// Stamp every forward so a proxied machine stays distinguishable from a
	// direct-push one while both populations exist. See PathHeader.
	p.logs.SetHeader(PathHeader, "proxy")
	p.metric.SetHeader(PathHeader, "proxy")
	return p
}

// OnAuthRejection registers the re-onboard hook on both transports. The daemon
// points this at its existing reauther, whose single-flight cooldown is what
// keeps a rejection from becoming a burst.
func (p *Proxy) OnAuthRejection(fn func()) {
	p.logs.OnAuthRejection(fn)
	p.metric.OnAuthRejection(fn)
}

// LastForward is the instant of the most recent SUCCESSFUL forward, or the zero
// time if none has happened. `keld signal doctor` reads it to tell "configured
// and flowing" from "configured and silent".
func (p *Proxy) LastForward() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastForward
}

// persistEvery throttles the on-disk record of the last forward.
const persistEvery = time.Minute

// noteForward records a successful delivery, persisting it at most once per
// persistEvery.
//
// ⚠️ IT GOES TO DISK, not just to memory, and that is the same choice
// localagent.ModelState makes and for the same reason: `keld signal doctor` must
// be able to answer "is telemetry flowing" with the daemon STOPPED. A fact only
// reachable from a running daemon would make daemon-down look like
// telemetry-broken, which is precisely the confusion this check exists to end.
func (p *Proxy) noteForward(now time.Time, sessions []string) {
	p.mu.Lock()
	p.lastForward = now
	if p.sessions == nil {
		p.sessions = map[string]time.Time{}
	}
	// ⚠️ A SESSION SEEN FOR THE FIRST TIME FORCES THE WRITE, throttle or not. The
	// throttle exists so a busy machine does not write per batch; applied to a
	// new id it would lose the very fact the per-session check needs, since a
	// tool that emits one batch and stops is indistinguishable from one that
	// never emitted at all.
	fresh := false
	for _, id := range sessions {
		if _, ok := p.sessions[id]; !ok {
			fresh = true
		}
		p.sessions[id] = now
	}
	evictOldest(p.sessions, maxTrackedSessions)
	due := fresh || now.Sub(p.lastPersisted) >= persistEvery
	if due {
		p.lastPersisted = now
	}
	snapshot := make(map[string]time.Time, len(p.sessions))
	for k, v := range p.sessions {
		snapshot[k] = v.UTC()
	}
	path := p.statePath
	p.mu.Unlock()
	if !due || path == "" {
		return
	}
	buf, err := json.Marshal(state{LastForward: now.UTC(), Sessions: snapshot})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	// Best effort: a failed write costs a stale diagnostic, never telemetry.
	_ = os.WriteFile(path, buf, 0o600)
}

// state is the on-disk shape of the telemetry record.
type state struct {
	LastForward time.Time `json:"last_forward"`
	// Sessions maps a tool session id to when telemetry for it last reached
	// Atlas. Bounded by maxTrackedSessions; `omitempty` so a machine that has
	// forwarded nothing writes exactly what it wrote before.
	Sessions map[string]time.Time `json:"sessions,omitempty"`
}

// maxTrackedSessions bounds the on-disk per-session record. Oldest-first
// eviction: the check this feeds only ever asks about sessions whose transcript
// is still being written, so an old id has no reader.
const maxTrackedSessions = 64

// PathHeader marks a batch as having come through the daemon rather than
// straight from a tool.
//
// ⚠️ Required by the spec's Migration section: during the rollout BOTH
// populations exist — machines that re-ran setup post through here, machines
// that have not still post direct — and a debugging session that cannot tell
// them apart cannot reason about either.
const PathHeader = "x-keld-telemetry-path"

// DrainSpools retries everything spooled for both routes. Metrics first is
// arbitrary; the two are independent by construction (separate directories), so
// neither can block the other.
func (p *Proxy) DrainSpools(ctx context.Context) {
	_ = p.logs.DrainSpool(ctx)
	_ = p.metric.DrainSpool(ctx)
}

// WaitIdle blocks until in-flight forwards finish. For shutdown and for tests;
// never call it on the request path.
func (p *Proxy) WaitIdle() { p.wg.Wait() }

// Handler routes /v1/logs and /v1/metrics.
func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/logs", p.receive(p.logs))
	mux.HandleFunc("/v1/metrics", p.receive(p.metric))
	return mux
}

// receive authenticates, strips text, answers the tool immediately, and forwards
// in the background.
//
// ⚠️ THE TOOL IS ANSWERED BEFORE ATLAS IS. Delivery is the spool's job; making
// the tool wait on Atlas would put the daemon's network conditions on the
// editor's critical path and, when Atlas is slow, cause the exporter to time out
// and drop what we were about to make durable.
func (p *Proxy) receive(tr *clientevents.Transport) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !p.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		// ⚠️ DECODE BEFORE FORWARDING. The forwarder sends
		// `Content-Type: application/json` and no Content-Encoding, so a
		// compressed body passed through would reach Atlas as gzip bytes labelled
		// JSON: Atlas 400s, that classifies as REFUSED, and the batch is DROPPED.
		// Total silent telemetry loss, one `OTEL_EXPORTER_OTLP_COMPRESSION=gzip`
		// away. Decoding rather than refusing is right for an encoding we
		// understand — the tool is configured correctly and it is our transport
		// that cannot carry it — and StripText needs the plaintext anyway.
		switch enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); enc {
		case "", "identity":
		case "gzip":
			zr, zerr := gzip.NewReader(bytes.NewReader(body))
			if zerr != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			plain, zerr := io.ReadAll(io.LimitReader(zr, maxBody))
			zr.Close()
			if zerr != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			body = plain
		default:
			// Refused LOUDLY: the tool sees an error it can report, rather than
			// telemetry vanishing after a 202 we could not honour.
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		body = StripText(body)
		// Read on THIS goroutine: body is handed to the forwarder below and must
		// not be walked concurrently with it.
		ids := SessionIDs(body)

		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			if err := tr.Deliver(context.Background(), body); err == nil {
				p.noteForward(time.Now(), ids)
			}
		}()
		w.WriteHeader(http.StatusAccepted)
	}
}

// authorized checks the caller's credential against every place a tool can put
// it.
//
// ⚠️ THE TOOLS DO NOT SEND THIS PACKAGE'S OWN HEADER NAME, and assuming they did
// meant all three were 401'd live while every unit test passed — the tests used
// the name chosen here rather than the names telemetry.ClaudeEnv,
// CodexBlockBody and GeminiTelemetry actually emit:
//
//	Claude Code, Codex   x-keld-ingest-token: <secret>
//	Gemini               ?token=<secret>
//
// Gemini's OTLP SDK cannot send a custom header at all, which is why its token
// rides the URL — so the query form has to be understood whatever the headers
// do, and rewriting the tool writers would not have removed the need for it.
//
// ⚠️ An EMPTY configured secret rejects everything rather than accepting
// everything: ConstantTimeCompare("", "") is 1, so without the guard an
// unconfigured proxy would authenticate any local caller — fail-open on the one
// route that injects billable usage into the org.
func (p *Proxy) authorized(r *http.Request) bool {
	if p.secret == "" {
		return false
	}
	want := []byte(p.secret)
	for _, got := range []string{
		r.Header.Get("x-keld-telemetry-secret"),
		r.Header.Get("x-keld-ingest-token"),
		r.URL.Query().Get("token"),
	} {
		if got != "" && subtle.ConstantTimeCompare([]byte(got), want) == 1 {
			return true
		}
	}
	return false
}

// textKey reports whether an OTLP attribute key names free text from the user or
// the model.
//
// ⚠️ MATCHED BY SHAPE, NOT BY AN ALLOW-LIST OF TODAY'S THREE TOOLS. The daemon
// is now a conduit for OTLP it did not author, so the invariant — raw prompt
// text never leaves the machine — must be enforced here rather than trusted from
// three separate tools' defaults staying as they are. A key this misses is a
// leak.
//
// ⚠️ BUT OVER-MATCHING IS NOT FREE, AND THIS COMMENT USED TO SAY IT WAS: "a key
// it over-matches costs one dropped attribute." That was wrong, and it was wrong
// about the most important attribute on the wire. Claude Code sends `prompt.id`
// on every record; `strings.Contains(k, "prompt")` matched it and blanked it, and
// Atlas joins `Enrichment.corr_id` to `ToolEvent.prompt_id`. So every enrichment
// — every block, every facet — stopped joining to the telemetry it describes, and
// the failure was silent in exactly the way a dropped attribute is not: the rows
// arrived, attributed and counted, with the one field that relates them emptied.
// Measured on the dev Atlas: of one proxied session's 244 `tool_result` and 19
// `user_prompt` rows, ZERO had a non-empty prompt_id, while unproxied seed rows
// kept theirs. The symptom reported was "blocks show up but the activity panel is
// empty", which is that join returning nothing.
//
// The rule is therefore two-sided: match a text WORD, then subtract the shapes
// that carry an identifier or a measurement rather than prose. Both halves are
// needed — dropping the first leaks text, dropping the second breaks correlation.
func textKey(k string) bool {
	k = strings.ToLower(k)
	matched := false
	for _, s := range []string{"prompt", "completion", "message.content", "response.text",
		"input.text", "output.text", "user_text", "assistant_text"} {
		if strings.Contains(k, s) {
			matched = true
			break
		}
	}
	return matched && !identifierOrMeasure(k)
}

// identifierOrMeasure reports whether a key that named a text word in fact ends
// in an identifier or a quantity — `prompt.id`, `prompt_length`, `prompt_tokens`.
//
// Suffixes, not substrings: `prompt.id` is an id, while `user_prompt_text` merely
// contains one of these words and is prose. Anything not listed here stays
// stripped, so a shape nobody anticipated fails CLOSED — toward privacy, and at
// worst toward the one dropped attribute the original comment assumed.
func identifierOrMeasure(k string) bool {
	for _, s := range []string{
		".id", "_id", ".ids", "_ids", ".uuid", "_uuid",
		".length", "_length", ".len", "_len",
		".count", "_count", ".size", "_size",
		".tokens", "_tokens", ".hash", "_hash",
	} {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}

// StripText removes text-bearing attributes from an OTLP/JSON payload, leaving
// everything else byte-comparable. A payload it cannot parse is passed through
// unchanged — it is not ours to rewrite, and the alternative (dropping it) would
// lose telemetry to a schema change.
func StripText(body []byte) []byte {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return body
	}
	if !strip(v) {
		return body
	}
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return out
}

// strip walks the decoded payload, blanking the value of any attribute whose key
// is text-bearing. Reports whether anything changed.
func strip(v any) bool {
	changed := false
	switch t := v.(type) {
	case map[string]any:
		// The OTLP attribute shape: {"key": "...", "value": {...}}.
		if k, ok := t["key"].(string); ok && textKey(k) {
			if _, hasValue := t["value"]; hasValue {
				t["value"] = map[string]any{"stringValue": ""}
				return true
			}
		}
		for k, child := range t {
			if textKey(k) {
				switch child.(type) {
				case string:
					t[k] = ""
					changed = true
					continue
				}
			}
			if strip(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range t {
			if strip(child) {
				changed = true
			}
		}
	}
	return changed
}

// Contains reports whether body still holds s. For tests and diagnostics.
func Contains(body []byte, s string) bool { return bytes.Contains(body, []byte(s)) }
