// Package promptlog is the TRANSCRIPT-FIRST USAGE MIRROR: it reads what a tool
// wrote to its own transcript on disk and posts the usage OTLP that tool would
// have posted, host-side, from the daemon.
//
// It began as a Cowork-only workaround — Cowork's agent sandbox egress allowlist
// excludes atlas.keld.co, so its natively-configured OTEL export dies at the
// firewall — and that narrow job is the reason to widen it. A tool reads its
// telemetry configuration ONCE, at startup, so every change Keld makes to it is
// invisible until a human restarts the editor; a transcript needs no
// configuration, no credential in the tool, and no restart. The evidence that
// this reading is enough: this machine's own ledger holds **1,073 delivered
// blocks (966 Claude Code, 107 Codex) covering 22,746 requests and $3,732.92 of
// estimated spend, all derived from transcripts rather than from OTLP**.
//
// Three mirrors, one per tool family, each emitting THAT TOOL'S OWN native OTLP
// shape so Atlas needs no new parser:
//
//	claude_code, cowork  claude_code.user_prompt / claude_code.api_request
//	codex                codex.sse_event (event.kind = response.completed)
//	gemini               gemini_cli.api_response
//
// # What never crosses
//
// No prompt, no response, no thinking, no tool input, no tool result, no span
// and no offset. The mirror reads a transcript in order to report NUMBERS —
// token counts, a model name, identifiers and instants. `privacy_test.go` puts a
// canary in every text-bearing field each tool writes and asserts none of them
// reaches the wire.
//
// # The dedup contract
//
// Atlas stores one tool event per `(event_ts, dedup_key)` and upserts, so a
// mirrored row and a row the tool sent itself must agree on both halves or the
// same work is counted twice. Every identifier a mirrored record carries is read
// off the transcript line; nothing on the priced record comes from process state,
// because a daemon restart renumbers process state and the key then names a
// different row for work that happened once. See `dedup_test.go`, which replicas
// all three of Atlas's key rules.
package promptlog

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ncx-ai/keld-signal/internal/debuglog"
)

// Capture sources this package knows how to mirror. `cowork` and `claude_code`
// share one transcript format and therefore one mirror.
const (
	sourceClaudeCode = "claude_code"
	sourceCowork     = "cowork"
	sourceCodex      = "codex"
	// ⚠️ THE WATCHER CALLS IT `gemini_cli`, AND THIS SAID `gemini`. That one
	// letter-for-letter difference made the whole Gemini mirror dead code: the
	// hook fires `observeDoc(source, path)` with the watcher's own id
	// (`roots.go` builds `Root{SourceID: "gemini_cli"}`, and `isDocumentSource`
	// matches that), `eligible` looked it up in a set holding "gemini", missed,
	// and returned in silence. Conformance chain A for gemini_cli: transcript
	// PASS, pointer PASS, publish PASS, telemetry 0 — with `tokens: {input 42,
	// output 3}` sitting in the chat file.
	//
	// This tool genuinely wears two names and AGENTS.md says so: the watcher,
	// the reader and the conformance id say `gemini_cli`, while the hook keld
	// writes says `--source gemini`. `resolve` already registers under both for
	// exactly this reason. So does this, rather than picking a winner and
	// leaving the other spelling to fail quietly somewhere else.
	sourceGemini     = "gemini_cli"
	sourceGeminiHook = "gemini"
)

// Telemetry emits OTLP logs + metrics for eligible captured sources.
type Telemetry struct {
	logsURL    func() string
	metricsURL func() string
	token      func() string
	ids        *identityCache
	client     *http.Client

	mu         sync.Mutex
	sources    map[string]bool
	seq        map[string]int64  // per-session event.sequence counter (unpriced events only)
	lastPrompt map[string]string // per-session last user prompt id, for prompt.id linkage
	// promptSeen is every (session, promptId) this mirror has already emitted a
	// user_prompt for. See firstSightOfPrompt. Bounded: cleared when it reaches
	// promptSeenCap, which costs at most one double-count per clear.
	promptSeen map[string]struct{}
	// onDrop is told about an observation this mirror could not deliver, with
	// a closed reason. See OnDrop.
	onDrop  func(reason string)
	dropped map[string]int64
	lastReq map[string]string // per-transcript last assistant requestId (Claude Code)
	codex   map[string]*codexState
	gemini  map[string]int // per-chat count of model turns already mirrored
}

// New builds a Telemetry. logsURL/metricsURL are the full OTLP endpoints; token is
// read live (re-auth swaps picked up); sources is the set of capture sources to
// mirror.
func New(logsURL, metricsURL string, token func() string, sources map[string]bool) *Telemetry {
	return NewPending(func() string { return logsURL }, func() string { return metricsURL }, token, sources)
}

// NewPending is New for a daemon that starts its WATCHER before it is paired.
// The two endpoints are resolved per POST, so the watcher's observe hook can be
// wired from the first second on an unpaired machine.
//
// ⚠️ **AN OBSERVATION MADE WHILE UNPAIRED IS LOST, and that is stated rather
// than hidden.** This path has no spool — it mirrors a transcript's events as
// OTLP, fire-and-forget — so while the endpoints answer "" the post is skipped.
// The cost is bounded: the default source set is {cowork}, Claude Code emits its
// own OTEL through the telemetry proxy (which DOES spool), and a person is
// unpaired only until they finish signing in. Giving this path a spool of its
// own would be a new durable queue, which WS1 deliberately does not add.
func NewPending(logsURL, metricsURL func() string, token func() string, sources map[string]bool) *Telemetry {
	return &Telemetry{
		logsURL:    logsURL,
		metricsURL: metricsURL,
		token:      token,
		ids:        newIdentityCache(),
		client:     &http.Client{Timeout: 5 * time.Second},
		sources:    copySources(sources),
		seq:        map[string]int64{},
		lastPrompt: map[string]string{},
		promptSeen: map[string]struct{}{},
		dropped:    map[string]int64{},
		lastReq:    map[string]string{},
		codex:      map[string]*codexState{},
		gemini:     map[string]int{},
	}
}

// SetSources replaces the set of capture sources this mirror emits for.
//
// This is the seam WS3 wires: the per-tool `tool_otlp` switch decides whether a
// tool posts its own OTLP or the daemon mirrors its transcript, and it is
// resolved at the ONE call site in daemon.go — deliberately not read here, so
// this package stays a pure function of "which sources, which transcript" and
// can be tested without a settings poll. Per source it is on or off; there is no
// half state, because half a source is how the same request gets counted twice.
func (t *Telemetry) SetSources(sources map[string]bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.sources = copySources(sources)
	t.mu.Unlock()
}

func copySources(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		if v {
			out[k] = true
		}
	}
	return out
}

// SourcesFromEnv returns the set of capture sources to mirror.
// Default {"cowork"}. KELD_WATCH_TELEMETRY=off disables (empty set);
// KELD_WATCH_TELEMETRY_SOURCES=a,b overrides the list.
//
// The default stays {cowork} here on purpose. Widening the MECHANISM to three
// tools and widening the POLICY that turns it on are separate changes: the
// policy rides the per-tool `tool_otlp` setting (WS3) through SetSources, and a
// default flipped in this function would enable mirroring on every machine the
// moment the binary shipped, beside tools still posting their own OTLP.
func SourcesFromEnv() map[string]bool {
	switch strings.ToLower(os.Getenv("KELD_WATCH_TELEMETRY")) {
	case "off", "0", "false":
		return map[string]bool{}
	}
	if v := os.Getenv("KELD_WATCH_TELEMETRY_SOURCES"); v != "" {
		out := map[string]bool{}
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out[s] = true
			}
		}
		return out
	}
	return map[string]bool{sourceCowork: true}
}

// SourcesFor is the POLICY the comment above promises: which sources this
// machine mirrors, given the position of the `tool_otlp` switch.
//
// ⚠️ THE TWO PATHS ARE COMPLEMENTS — NEVER BOTH, AND NEVER NEITHER — AND
// "NEITHER" IS WHAT SHIPPED FOR ONE COMMIT. The switch stops writing an OTEL
// block into a tool's config, so with it off the tool sends nothing; this
// mirror's default set was `{cowork}`, so it did not cover that tool either.
// Two changes each correct on their own left Claude Code and Codex emitting NO
// usage at all, and nothing said so: the pane's otel lane is not expected while
// the switch is off, so the row read `working`. The conformance chain is what
// caught it, as `telemetry: 0 OTLP forwarded`.
//
// Cowork is mirrored either way and is not part of the complement: its sandbox
// blocks its own egress to Atlas by design, so host-side mirroring is the only
// path it has ever had.
func SourcesFor(toolOTLP bool) map[string]bool {
	if v, explicit := sourcesFromEnvExplicit(); explicit {
		return v
	}
	out := map[string]bool{sourceCowork: true}
	if !toolOTLP {
		out[sourceClaudeCode] = true
		out[sourceCodex] = true
		// Both spellings: whichever half of the product hands us a source, it
		// is in the set. See sourceGemini.
		out[sourceGemini] = true
		out[sourceGeminiHook] = true
	}
	return out
}

// sourcesFromEnvExplicit reports the env override and whether one was given, so
// SourcesFor can tell "the operator asked for this set" from "nobody said".
func sourcesFromEnvExplicit() (map[string]bool, bool) {
	switch strings.ToLower(os.Getenv("KELD_WATCH_TELEMETRY")) {
	case "off", "0", "false":
		return map[string]bool{}, true
	}
	if v := os.Getenv("KELD_WATCH_TELEMETRY_SOURCES"); v != "" {
		return SourcesFromEnv(), true
	}
	return nil, false
}

// eligible reports whether this source is mirrored and a token exists to post
// with.
func (t *Telemetry) eligible(source string) bool {
	if t == nil || t.token == nil || t.token() == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sources[source]
}

// Observe parses one transcript LINE and emits the matching telemetry for an
// eligible source. Best-effort: any parse/POST failure is logged and swallowed.
//
// Gemini is absent from the switch by construction: its session is one JSON
// document that is rewritten whole on every turn, so there are no appended lines
// to observe. It arrives through ObserveFile instead.
func (t *Telemetry) Observe(source, transcriptPath string, line []byte) {
	if !t.eligible(source) {
		return
	}
	switch source {
	case sourceCodex:
		t.observeCodexLine(transcriptPath, line)
	case sourceClaudeCode, sourceCowork:
		t.observeClaudeLine(source, transcriptPath, line)
	}
}

// ObserveFile mirrors a whole transcript FILE, for a source whose session is one
// document rather than a stream of appended lines (Gemini). The watcher calls it
// once per poll in which the file advanced; the mirror keeps its own per-file
// cursor so an unchanged re-read costs nothing.
func (t *Telemetry) ObserveFile(source, transcriptPath string) {
	if !t.eligible(source) {
		return
	}
	if source == sourceGemini || source == sourceGeminiHook {
		t.observeGeminiFile(source, transcriptPath)
	}
}

func (t *Telemetry) postLogs(res []kv, recs []logRecord) {
	if len(recs) == 0 {
		return
	}
	body, err := logsPayload(res, recs)
	if err != nil {
		return
	}
	t.doPost(t.logsURL(), body)
}

func (t *Telemetry) postMetricList(res []kv, metrics []metric) {
	if len(metrics) == 0 {
		return
	}
	body, err := metricsPayload(res, metrics)
	if err != nil {
		return
	}
	t.doPost(t.metricsURL(), body)
}

// Drop reasons. Closed set: a client event carries one of these and nothing
// else, and docs/durability.md names the same losses.
const (
	DropNotPaired   = "not_paired"  // no Atlas endpoint yet: nothing to post to
	DropUnreachable = "unreachable" // the POST never got an answer
	DropRejected    = "rejected"    // Atlas answered 4xx: this record will never land
	DropUnavailable = "unavailable" // Atlas answered 5xx: it might have, and this mirror does not retry
)

// OnDrop installs the hook told about every observation this mirror could not
// deliver, with a reason from the Drop* set.
//
// ⚠️ THIS MIRROR HAS NO SPOOL, AND UNTIL THIS THE LOSS WAS A SENTENCE IN A DOC.
// Every other lane holds what it cannot send — the proxy spools, the block
// emitter keeps its cursor, the enrich worker re-spools the pointer — and this
// one posts once and forgets, by design: a batch here is one record, and a
// spool that re-sent it would need the same restart-safe dedup the api_request
// row already carries and user_prompt does not. docs/durability.md says so.
// But a loss that is documented and not counted is invisible on every machine
// it happens on; this hook is what turns it into a number the daemon can
// report. It fires per dropped record; the daemon decides how loudly.
func (t *Telemetry) OnDrop(fn func(reason string)) {
	t.mu.Lock()
	t.onDrop = fn
	t.mu.Unlock()
}

// Dropped is how many records were lost, by reason, since this mirror started.
func (t *Telemetry) Dropped() map[string]int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]int64, len(t.dropped))
	for k, v := range t.dropped {
		out[k] = v
	}
	return out
}

func (t *Telemetry) noteDrop(reason string) {
	t.mu.Lock()
	t.dropped[reason]++
	fn := t.onDrop
	t.mu.Unlock()
	if fn != nil {
		fn(reason)
	}
}

func (t *Telemetry) doPost(url string, body []byte) {
	// Not paired yet: no address to post to. See NewPending for why this is a
	// skip rather than a spool.
	if strings.TrimSpace(url) == "" {
		debuglog.Append("promptlog: not paired yet — skipping one OTLP post")
		t.noteDrop(DropNotPaired)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		debuglog.Append("promptlog: request build failed: %v", err)
		return
	}
	req.Header.Set("content-type", "application/json")
	// Auth is the ingest token only; x-keld-actor is deprecated (never sent).
	req.Header.Set("x-keld-ingest-token", t.token())
	resp, err := t.client.Do(req)
	if err != nil {
		debuglog.Append("promptlog: POST %s failed: %v", url, err)
		t.noteDrop(DropUnreachable)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	debuglog.Append("promptlog: POST %s -> HTTP %d", url, resp.StatusCode)
	switch {
	case resp.StatusCode >= 500:
		t.noteDrop(DropUnavailable)
	case resp.StatusCode >= 400:
		t.noteDrop(DropRejected)
	}
}

func (t *Telemetry) nextSeq(session string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq[session]++
	return t.seq[session]
}

// promptSeenCap bounds promptSeen. 4,096 prompts is weeks of one person's work;
// a daemon runs for days, not months.
const promptSeenCap = 4096

// firstSightOfPrompt reports whether (session, promptId) has NOT been emitted
// yet, and records it.
//
// ⚠️ A CLAUDE CODE HUMAN TURN IS SEVERAL USER LINES SHARING ONE promptId, AND
// EMITTING PER LINE COUNTED THE SAME PROMPT TWICE. The meta line, the text line,
// and any continuation the turn produces all carry the same `promptId` (AGENTS.md
// measured one spanning 7 lines across 8 minutes), and two of them routinely pass
// the genuine-prompt filter: measured 2026-09-21 in Atlas, 19 user_prompt rows for
// 16 distinct prompt.ids on one machine — the Prompts KPI over-counting by 19%.
// The watcher already dedups on promptId (queue.Offer answers Duplicate); the
// mirror had no such memory, so every line it was handed became a row.
//
// Atlas could not collapse them either: user_prompt's dedup key is
// `session.id:event.sequence`, a fresh per-line counter, and the unique index is
// (event_ts, dedup_key) besides. Deduping at the source is the only place it works.
func (t *Telemetry) firstSightOfPrompt(session, promptID string) bool {
	key := session + "\x00" + promptID
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, seen := t.promptSeen[key]; seen {
		return false
	}
	if len(t.promptSeen) >= promptSeenCap {
		t.promptSeen = map[string]struct{}{}
	}
	t.promptSeen[key] = struct{}{}
	return true
}

func (t *Telemetry) setLastPrompt(session, promptID string) {
	t.mu.Lock()
	t.lastPrompt[session] = promptID
	t.mu.Unlock()
}

func (t *Telemetry) lastPromptID(session string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastPrompt[session]
}

// hostResource is the machine half of the resource attributes, identical for
// every tool: os.type and host.arch are what the tools' own SDKs report and they
// are constant per machine, so including them cannot make a payload
// non-deterministic.
func hostResource() []kv {
	return []kv{attr("os.type", runtime.GOOS), attr("host.arch", runtime.GOARCH)}
}

func identityAttrs(id Identity) []kv {
	var a []kv
	if id.Email != "" {
		a = append(a, attr("user.email", id.Email))
	}
	if id.AccountUUID != "" {
		a = append(a, attr("user.account_uuid", id.AccountUUID))
	}
	if id.OrgID != "" {
		a = append(a, attr("organization.id", id.OrgID))
	}
	return a
}

// timeNano converts an RFC3339 timestamp to a UnixNano decimal string, or "" if
// unparseable.
func timeNano(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(t.UnixNano(), 10)
}
