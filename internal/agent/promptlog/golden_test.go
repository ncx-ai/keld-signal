package promptlog

import (
	"bufio"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A1 of docs/superpowers/specs/2026-09-29-per-request-usage-proposal.html:
// WHAT ATLAS RECEIVES MUST NOT MOVE while the local per-request count is built
// on this reader. These golden files are every OTLP body the mirror posts for
// the real captured fixtures, frozen BEFORE that work began. A diff here means
// a change reached the Atlas feed; if it was meant to, regenerate with
//
//	go test ./internal/agent/promptlog -run TestAtlasPayloadsAreUnchanged -update-golden
//
// and say so in the commit.

var updateGolden = flag.Bool("update-golden", false, "rewrite the A1 golden payloads")

// normalise removes the only machine-dependent values in a body: the OS and CPU
// architecture hostResource stamps on every resource.
func normalise(body string) string {
	body = strings.ReplaceAll(body, `"stringValue":"`+runtime.GOOS+`"`, `"stringValue":"<os>"`)
	return strings.ReplaceAll(body, `"stringValue":"`+runtime.GOARCH+`"`, `"stringValue":"<arch>"`)
}

// posted is every body the sink received, in order, one "path body" per line.
func posted(c *capSink) string {
	var b strings.Builder
	for _, path := range []string{"/v1/logs", "/v1/metrics"} {
		for _, body := range c.bodies(path) {
			b.WriteString(path + " " + normalise(body) + "\n")
		}
	}
	return b.String()
}

func feedLines(t *testing.T, tel *Telemetry, source, fixture, asPath string) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		tel.Observe(source, asPath, append([]byte(nil), sc.Bytes()...))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestAtlasPayloadsAreUnchanged(t *testing.T) {
	all := map[string]bool{sourceClaudeCode: true, sourceCowork: true, sourceCodex: true, sourceGemini: true, sourceGeminiHook: true}
	cases := []struct {
		name string
		run  func(t *testing.T, tel *Telemetry)
	}{
		{"claude_code", func(t *testing.T, tel *Telemetry) {
			feedLines(t, tel, sourceClaudeCode, "claude_code_session.jsonl", filepath.Join("testdata", "claude_code_session.jsonl"))
		}},
		{"cowork", func(t *testing.T, tel *Telemetry) {
			feedLines(t, tel, sourceCowork, "claude_code_session.jsonl", coworkPath(t))
		}},
		{"codex_rollout", func(t *testing.T, tel *Telemetry) {
			feedLines(t, tel, sourceCodex, "codex_rollout.jsonl", filepath.Join("testdata", "codex_rollout.jsonl"))
		}},
		{"codex_reemission", func(t *testing.T, tel *Telemetry) {
			feedLines(t, tel, sourceCodex, "codex_reemission.jsonl", filepath.Join("testdata", "codex_reemission.jsonl"))
		}},
		{"gemini", func(t *testing.T, tel *Telemetry) {
			tel.ObserveFile(sourceGemini, filepath.Join("testdata", "gemini_session.json"))
			// A second look at the unchanged file must post nothing more.
			tel.ObserveFile(sourceGemini, filepath.Join("testdata", "gemini_session.json"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, srv := newCapSink()
			defer srv.Close()
			tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", all)
			tc.run(t, tel)
			got := posted(c)
			if got == "" {
				t.Fatal("the fixture posted nothing; the golden file would pin an empty feed")
			}
			golden := filepath.Join("testdata", "golden", tc.name+".golden")
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("no golden file %s — run with -update-golden once, BEFORE changing the reader: %v", golden, err)
			}
			if got != string(want) {
				t.Fatalf("what Atlas receives changed for %s (A1).\nwant:\n%s\ngot:\n%s", tc.name, want, got)
			}
		})
	}
}

// The set of tools mirrored to Atlas must not move either: widening it would
// double count a tool that also sends its own telemetry (A1, R2).
func TestAtlasMirroredSourcesAreUnchanged(t *testing.T) {
	t.Setenv("KELD_WATCH_TELEMETRY", "")
	t.Setenv("KELD_WATCH_TELEMETRY_SOURCES", "")
	got := map[bool]map[string]bool{true: SourcesFor(true), false: SourcesFor(false)}
	want := map[bool]map[string]bool{
		true:  {sourceCowork: true},
		false: {sourceCowork: true, sourceClaudeCode: true, sourceCodex: true, sourceGemini: true, sourceGeminiHook: true},
	}
	for k := range want {
		if len(got[k]) != len(want[k]) {
			t.Fatalf("tool_otlp=%v: mirrored sources %v, want %v", k, got[k], want[k])
		}
		for s := range want[k] {
			if !got[k][s] {
				t.Fatalf("tool_otlp=%v: mirrored sources %v, want %v", k, got[k], want[k])
			}
		}
	}
}
