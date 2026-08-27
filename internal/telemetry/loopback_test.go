package telemetry

import (
	"fmt"
	"strings"
	"testing"
)

// THE REGRESSION TEST FOR THE ORIGINAL BUG.
//
// ⚠️ A tool config that contains an Atlas ingest token is a tool that goes stale
// the moment that token rotates — and the user cannot fix it without restarting
// their editor, because tools read their config once at startup and keep it in
// memory. Measured: telemetry died for 40 minutes while `keld signal doctor`
// correctly reported no problems, because the stale copy lived inside a process
// it could not inspect.
//
// Gemini's exposure was the worst and the least obvious: its token rides in the
// URL query string, so a rotation leaves a dead credential sitting in a settings
// file rather than in a header.
func TestNoToolConfigCarriesAnAtlasCredential(t *testing.T) {
	const (
		loopback = "http://127.0.0.1:14318"
		local    = "LOCAL-STABLE-SECRET"
		atlasTok = "ATLAS-ORG-INGEST-TOKEN"
	)
	p := SetupParams{Endpoint: loopback, IngestToken: local, BinPath: "/usr/bin/keld"}

	outs := map[string]string{
		"claude": fmt.Sprint(ClaudeEnv(p)),
		"codex":  CodexBlockBody(p, "codex"),
		"gemini": fmt.Sprint(GeminiTelemetry(p)),
	}
	for name, out := range outs {
		if strings.Contains(out, "atlas.keld.co") {
			t.Errorf("%s points at Atlas directly:\n%s", name, out)
		}
		if strings.Contains(out, atlasTok) {
			t.Errorf("%s carries an Atlas credential:\n%s", name, out)
		}
		if !strings.Contains(out, "127.0.0.1:14318") {
			t.Errorf("%s does not point at the loopback proxy:\n%s", name, out)
		}
		if !strings.Contains(out, local) {
			t.Errorf("%s does not carry the local telemetry secret:\n%s", name, out)
		}
	}
}

// Whatever endpoint is handed in is what every writer emits — no writer may
// substitute its own. This is what keeps the three tools from drifting apart.
func TestEveryWriterHonoursTheEndpointItIsGiven(t *testing.T) {
	p := SetupParams{Endpoint: "http://127.0.0.1:15999", IngestToken: "s", BinPath: "/usr/bin/keld"}
	for name, out := range map[string]string{
		"claude": fmt.Sprint(ClaudeEnv(p)),
		"codex":  CodexBlockBody(p, "codex"),
		"gemini": fmt.Sprint(GeminiTelemetry(p)),
	} {
		if !strings.Contains(out, "15999") {
			t.Errorf("%s ignored the configured endpoint:\n%s", name, out)
		}
	}
}
