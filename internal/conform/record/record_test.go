package record

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// ⚠️ WHETHER THE HARNESS CAN RUN A TOOL WAS STATED IN TWO PLACES AND THEY DRIFTED
// IMMEDIATELY. `lib/tools.sh`'s `tool_supported` ran codex; `.conformance/
// last-tested.json` carried `harness: "pending"` for it. The canary reads the
// JSON, so every Codex release was reported "not in the harness yet" and
// dispatched nothing — the one trigger that exists to catch a tool release,
// silently off for the tool whose file format had already moved three times.
//
// The key is gone and the watch now asks tools.sh. These tests keep it gone.
const (
	recordPath = "../../../.conformance/last-tested.json"
	toolsPath  = "../../../scripts/conformance/lib/tools.sh"
)

type record struct {
	Tools map[string]struct {
		Package  string  `json:"package"`
		Version  *string `json:"version"`
		TestedOn *string `json:"tested_on"`
		ProvenBy *string `json:"proven_by"`
		Harness  *string `json:"harness"`
	} `json:"tools"`
}

func load(t *testing.T) (record, string) {
	t.Helper()
	b, err := os.ReadFile(recordPath)
	if err != nil {
		t.Skipf("record not readable: %v", err)
	}
	var r record
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("record is not valid JSON: %v", err)
	}
	s, err := os.ReadFile(toolsPath)
	if err != nil {
		t.Skipf("tools.sh not readable: %v", err)
	}
	return r, string(s)
}

// supportedIn parses tool_supported's case list — the one place the answer lives.
func supportedIn(sh string) map[string]bool {
	out := map[string]bool{}
	re := regexp.MustCompile(`(?s)tool_supported\(\)\s*\{.*?case.*?in(.*?)esac`)
	m := re.FindStringSubmatch(sh)
	if m == nil {
		return out
	}
	for _, line := range strings.Split(m[1], "\n") {
		line = strings.TrimSpace(line)
		i := strings.Index(line, ")")
		if i <= 0 || strings.HasPrefix(line, "*") {
			continue
		}
		for _, id := range strings.Split(line[:i], "|") {
			if id = strings.TrimSpace(id); id != "" && id != "*" {
				out[id] = true
			}
		}
	}
	return out
}

func TestTheRecordDoesNotRestateWhatTheHarnessDecides(t *testing.T) {
	r, _ := load(t)
	for id, e := range r.Tools {
		if e.Harness != nil {
			t.Errorf("%s carries a `harness` key. Whether the harness can run a tool is tools.sh's "+
				"tool_supported; a copy here drifted once already and turned the release canary off for codex.", id)
		}
	}
}

// A version can only be PROVEN by a chain, and a chain only runs a supported
// tool. So a proven version against an unsupported tool is a record of
// something that cannot have happened.
func TestAProvenVersionImpliesTheHarnessRunsIt(t *testing.T) {
	r, sh := load(t)
	sup := supportedIn(sh)
	if len(sup) == 0 {
		t.Fatal("parsed no ids out of tool_supported; this test would pass vacuously")
	}
	for id, e := range r.Tools {
		if e.Version == nil || *e.Version == "" {
			continue // never proven; nothing to check
		}
		if !sup[id] {
			t.Errorf("%s records a proven version (%s) but tools.sh will not run it: "+
				"run-chain.sh exits 2 on an unsupported tool, so that run cannot have happened", id, *e.Version)
		}
		if e.ProvenBy == nil || strings.TrimSpace(*e.ProvenBy) == "" {
			t.Errorf("%s records version %s with no proven_by; a version nobody can trace is not evidence", id, *e.Version)
		}
	}
}

// Every tool the harness can run should be in the record, or the canary never
// looks it up and its releases pass unnoticed — the same silence one level over.
func TestEveryHarnessToolIsWatched(t *testing.T) {
	r, sh := load(t)
	for id := range supportedIn(sh) {
		if _, ok := r.Tools[id]; !ok {
			t.Errorf("tools.sh runs %s but .conformance/last-tested.json does not list it: "+
				"the daily version watch iterates the record, so %s's releases would go unnoticed", id, id)
		}
	}
}
