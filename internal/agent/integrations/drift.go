package integrations

import (
	"regexp"
	"sort"
	"strings"

	"github.com/iancoleman/orderedmap"

	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/telemetry"
)

// keldTelemetry is what KELD'S OWN region of one tool's config says about where
// telemetry goes and what credential it carries. Nothing else in the file is
// read, and that boundary is the whole mechanism: keld may correct what it
// wrote and must never correct what the person wrote.
//
// `read` is a third answer and it is the refusal this package makes everywhere
// else (thin vs absent, known vs not-known): a config we could not parse, or one
// carrying no keld region at all, has NOT disagreed with us. Drift needs two
// readable sides, or it is not a comparison.
type keldTelemetry struct {
	endpoints []string
	secrets   []string
	read      bool
}

// telemetryDrifted reports whether keld's own block in `current` names a
// different endpoint, or carries a different credential, from the block keld
// would write right now (`want` — an adapter's own Plan.AfterText, so nothing
// here has to know how each tool composes its URLs).
//
// ⚠️ IT COMPARES VALUES, NEVER THE FILE, AND THAT IS NOT AN OPTIMISATION. The
// tools rewrite their own config files: measured 2026-09-18 on the maintainer's
// machine, Codex wrote `hooks.state` trust entries into config.toml at session
// start (observed 20:56) and Claude Code rewrote settings.json unprompted
// (observed 21:21). A whole-file comparison — a hash, or `plan.Changed` —
// answers yes to both of those, so the daemon would rewrite a healthy config
// every time a tool touched its own settings, and the person would watch keld
// and their editor take turns. The four values keld actually wrote are the only
// thing it is entitled to have an opinion about.
//
// ⚠️ AND THE 2026-09-18 INCIDENT IS WHY IT EXISTS AT ALL. ~/.keld/agent.json
// held telemetry secret 26908e20… while ~/.codex/config.toml and
// ~/.claude/settings.json both held a5629e92…, written by an older keld still on
// PATH; a probe POST to the running proxy with the tools' value returned 401.
// Both values were on disk, in front of the daemon, the whole time. The pane
// said `broken · otel` and the repair was left to a human who had to know to
// re-run setup.
func telemetryDrifted(adapterName, current, want string) bool {
	c := keldTelemetryIn(adapterName, current)
	w := keldTelemetryIn(adapterName, want)
	if !c.read || !w.read {
		// One side unreadable is "we cannot tell", never "it is wrong". A
		// config keld cannot parse is exactly the file it must not rewrite
		// unasked.
		return false
	}
	return !sameValues(c.endpoints, w.endpoints) || !sameValues(c.secrets, w.secrets)
}

// keldTelemetryIn routes one tool's config to the reader that knows where keld
// put its telemetry values in it. An adapter nothing here knows answers
// `read=false`, which can never produce a repair — a new tool arrives silent,
// not rewritten.
func keldTelemetryIn(adapterName, text string) keldTelemetry {
	switch adapterName {
	case "claude_code":
		return keldTelemetryInClaude(text)
	case "codex":
		return keldTelemetryInCodex(text)
	case "gemini":
		return keldTelemetryInGemini(text)
	default:
		return keldTelemetry{}
	}
}

// keldTelemetryInClaude reads the two `env` keys telemetry.ClaudeEnv writes.
// Claude Code's settings.json carries no marker comments — JSON has nowhere to
// put them — so keld's region is the KEYS it recorded, and these two are the
// only ones holding a destination or a credential. Every other key in that file,
// keld's own included, is left out: `CLAUDE_CODE_ENABLE_TELEMETRY` changing
// would be a different fault with a different repair.
func keldTelemetryInClaude(text string) keldTelemetry {
	obj, err := config.LoadJSON(text)
	if err != nil {
		return keldTelemetry{}
	}
	env := subMap(obj, "env")
	if env == nil {
		return keldTelemetry{}
	}
	var kt keldTelemetry
	if s, ok := stringAt(env, "OTEL_EXPORTER_OTLP_ENDPOINT"); ok {
		kt.endpoints = append(kt.endpoints, s)
		kt.read = true
	}
	if s, ok := stringAt(env, "OTEL_EXPORTER_OTLP_HEADERS"); ok {
		kt.secrets = append(kt.secrets, ingestTokensInHeaderList(s)...)
		kt.read = true
	}
	return kt
}

// codexEndpoint / codexIngestToken read the two value shapes
// telemetry.CodexBlockBody writes, INSIDE keld's marker block and nowhere else.
var (
	codexEndpoint    = regexp.MustCompile(`endpoint\s*=\s*"([^"]*)"`)
	codexIngestToken = regexp.MustCompile(`"x-keld-ingest-token"\s*=\s*"([^"]*)"`)
)

// keldTelemetryInCodex reads only what lies between config.KeldTOMLStart and
// config.KeldTOMLEnd. Those markers are a frozen backward-compat contract and
// they are what makes "keld's own block" a thing keld can point at; a person's
// own `[otel]` table, or any other exporter they keep beside it, sits outside
// them and is never read here.
func keldTelemetryInCodex(text string) keldTelemetry {
	block, ok := keldTOMLBlock(text)
	if !ok {
		return keldTelemetry{}
	}
	kt := keldTelemetry{read: true}
	for _, m := range codexEndpoint.FindAllStringSubmatch(block, -1) {
		kt.endpoints = append(kt.endpoints, m[1])
	}
	for _, m := range codexIngestToken.FindAllStringSubmatch(block, -1) {
		kt.secrets = append(kt.secrets, m[1])
	}
	return kt
}

// keldTOMLBlock returns the body between keld's markers, and whether there was
// one. It is the inverse of config.StripKeldBlock and reads the same two frozen
// constants, so the two cannot disagree about where the block is.
func keldTOMLBlock(text string) (string, bool) {
	if !config.HasKeldBlock(text) {
		return "", false
	}
	var body []string
	inside := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.TrimSpace(line) == config.KeldTOMLStart:
			inside = true
		case inside && strings.TrimSpace(line) == config.KeldTOMLEnd:
			inside = false
		case inside:
			body = append(body, line)
		}
	}
	return strings.Join(body, "\n"), true
}

// keldTelemetryInGemini reads `telemetry.otlpEndpoint`, which carries BOTH
// values: telemetry.endpointWithToken puts the credential in the URL's path as
// "<base>/t/<token>" because Gemini cannot carry an auth header.
//
// ⚠️ A value with no "/t/" segment is reported as an endpoint and NO secret, so
// it drifts against anything current. That is deliberate: the query-string form
// an older keld wrote (`<base>?token=…`) is the shape measured on gemini-cli
// 0.37.1 to send every export to a path of "/" with a token of
// "SECRET/v1/logs" — a machine still holding it needs the repair, and splitting
// it apart here would declare it equal and leave it broken.
func keldTelemetryInGemini(text string) keldTelemetry {
	obj, err := config.LoadJSON(text)
	if err != nil {
		return keldTelemetry{}
	}
	tel := subMap(obj, "telemetry")
	if tel == nil {
		return keldTelemetry{}
	}
	s, ok := stringAt(tel, "otlpEndpoint")
	if !ok {
		return keldTelemetry{}
	}
	kt := keldTelemetry{read: true}
	if i := strings.LastIndex(s, telemetry.GeminiTokenPath); i >= 0 {
		kt.endpoints = append(kt.endpoints, s[:i])
		kt.secrets = append(kt.secrets, s[i+len(telemetry.GeminiTokenPath):])
		return kt
	}
	kt.endpoints = append(kt.endpoints, s)
	return kt
}

// ingestTokensInHeaderList pulls keld's own credential out of an
// OTEL_EXPORTER_OTLP_HEADERS list. Other headers in that value are somebody
// else's and are not read: keld writes exactly one.
func ingestTokensInHeaderList(v string) []string {
	var out []string
	for _, pair := range strings.Split(v, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "x-keld-ingest-token") {
			out = append(out, strings.TrimSpace(val))
		}
	}
	return out
}

// subMap reads a nested object out of a decoded JSON document. orderedmap stores
// sub-maps as a VALUE after unmarshal and as a POINTER after a Set, so both
// forms occur in the same file depending on who wrote it last — the same pair
// hasOTLPEndpoint already handles one package over.
func subMap(obj *orderedmap.OrderedMap, key string) *orderedmap.OrderedMap {
	v, ok := obj.Get(key)
	if !ok {
		return nil
	}
	switch m := v.(type) {
	case *orderedmap.OrderedMap:
		return m
	case orderedmap.OrderedMap:
		return &m
	}
	return nil
}

func stringAt(obj *orderedmap.OrderedMap, key string) (string, bool) {
	v, ok := obj.Get(key)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// sameValues compares two value lists as SETS, because a comparison that
// depended on the order the adapter happened to emit its exporters in would
// call a healthy config drifted the day one of them was reordered.
func sameValues(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
