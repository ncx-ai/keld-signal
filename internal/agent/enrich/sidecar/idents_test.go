package sidecar

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// checkNameCounts is checkPathCounts' twin for the identifier inventories,
// which decode to enrich.NameCount rather than enrich.PathCount but share the
// same {Value, N} shape and the same ordering guarantee.
func checkNameCounts(t *testing.T, name string, got []enrich.NameCount, want []enrichAct) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %+v, want %d entries", name, got, len(want))
	}
	for i, w := range want {
		if got[i].Value != w.value || got[i].N != w.n {
			t.Errorf("%s entry %d = %+v, want %+v", name, i, got[i], w)
		}
	}
}

// Round-trip: the four identifier-shaped inventories arrive on the wire
// exactly as the acts/path inventories do (see acts_test.go, paths_test.go),
// and AnalyzeLabeled must carry all four plus their values and counts
// unchanged.
func TestAnalyzeLabeledCarriesTheIdentifierInventories(t *testing.T) {
	srv := analyzeServer(t, inventoryBody(map[string]any{
		"harness_tools":    acts("Bash", 30, "ToolSearch", 4),
		"programs":         acts("git", 9, "pnpm", 3),
		"external_systems": acts("github.com", 4, "api.anthropic.com", 2),
		"integrations":     acts("notion-fetch", 1, "notion-update-page", 1),
	}))
	defer srv.Close()

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	checkNameCounts(t, "HarnessTools", got.HarnessTools, []enrichAct{{"Bash", 30}, {"ToolSearch", 4}})
	checkNameCounts(t, "Programs", got.Programs, []enrichAct{{"git", 9}, {"pnpm", 3}})
	checkNameCounts(t, "ExternalSystems", got.ExternalSystems,
		[]enrichAct{{"github.com", 4}, {"api.anthropic.com", 2}})
	checkNameCounts(t, "Integrations", got.Integrations,
		[]enrichAct{{"notion-fetch", 1}, {"notion-update-page", 1}})
}

// The structural gate for programs: an exe containing a path separator, or one
// with a leading dot (the measured real defect, `.env.example` reaching the
// bashlex exe extraction), is dropped WITHOUT dropping the rest of the
// inventory.
func TestABadProgramEntryIsDroppedWithoutDroppingTheInventory(t *testing.T) {
	for _, bad := range []string{".env.example", "bin/git", "sub\\dir\\tool.exe"} {
		srv := analyzeServer(t, inventoryBody(map[string]any{
			"programs": acts("git", 9, bad, 4),
		}))
		got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
		srv.Close()
		if !ok {
			t.Fatalf("%q: AnalyzeLabeled reported failure", bad)
		}
		if len(got.Programs) != 1 || got.Programs[0].Value != "git" {
			t.Fatalf("%q: want only the well-shaped entry to survive, got %+v", bad, got.Programs)
		}
		b, _ := json.Marshal(got)
		if strings.Contains(string(b), bad) {
			t.Errorf("%q: a bad program entry reached the conversion output: %s", bad, b)
		}
	}
}

// The structural gate for harness_tools/integrations: an identifier-shaped
// value survives, anything holding whitespace, a path separator or other
// non-identifier punctuation does not — same per-entry drop.
func TestABadIdentifierEntryIsDroppedWithoutDroppingTheInventory(t *testing.T) {
	for _, bad := range []string{"two words", "mcp/notion", "notion:fetch", ""} {
		srv := analyzeServer(t, inventoryBody(map[string]any{
			"harness_tools": acts("Bash", 30, bad, 4),
		}))
		got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
		srv.Close()
		if !ok {
			t.Fatalf("%q: AnalyzeLabeled reported failure", bad)
		}
		if len(got.HarnessTools) != 1 || got.HarnessTools[0].Value != "Bash" {
			t.Fatalf("%q: want only the well-shaped entry to survive, got %+v", bad, got.HarnessTools)
		}
	}
}

// The structural gate for external_systems: a bare IP literal, v4 or v6, is
// dropped without dropping the rest of the inventory. LOOPBACK is already
// filtered sidecar-side (workstreams.payload); this is the client-side
// decode-boundary defence-in-depth for any OTHER address.
func TestABareIPServiceEntryIsDroppedWithoutDroppingTheInventory(t *testing.T) {
	for name, bad := range map[string]string{
		"IPv4":          "203.0.113.5",
		"IPv4 RFC1918":  "10.0.0.1",
		"IPv6":          "2001:db8::1",
		"IPv6 loopback": "::1",
	} {
		srv := analyzeServer(t, inventoryBody(map[string]any{
			"external_systems": acts("github.com", 4, bad, 2),
		}))
		got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
		srv.Close()
		if !ok {
			t.Fatalf("%s (%q): AnalyzeLabeled reported failure", name, bad)
		}
		if len(got.ExternalSystems) != 1 || got.ExternalSystems[0].Value != "github.com" {
			t.Fatalf("%s (%q): want only the hostname to survive, got %+v", name, bad, got.ExternalSystems)
		}
		b, _ := json.Marshal(got)
		if strings.Contains(string(b), bad) {
			t.Errorf("%s: a bare IP literal reached the conversion output: %s", name, b)
		}
	}
}

// ⚠️ THE DELIBERATE DECISION, PINNED: a corporate hostname and an
// RFC1918-LOOKING hostname STRING (as opposed to a literal address) both
// survive. This is not an oversight to be "tightened" later — see
// convertExternalSystemInventory's comment for the argument: hostnames come
// from the same tool-call-input provenance `files`/`branch` already publish,
// and filtering anything that merely LOOKS internal would defeat the dimension
// for every enterprise user while this project's own corpus, having none of
// these shapes, could never catch the regression. A later change that makes
// this test fail must argue with this comment, not silently walk past it.
func TestCorporateAndRFC1918LookingHostnamesSurviveTheExternalSystemsGate(t *testing.T) {
	for _, keep := range []string{
		"jenkins.corp.internal",
		"gitlab.acme.com",
		"10.0.0.1.corp.internal", // RFC1918-looking, but a HOSTNAME, not an IP literal
	} {
		srv := analyzeServer(t, inventoryBody(map[string]any{
			"external_systems": acts(keep, 3),
		}))
		got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
		srv.Close()
		if !ok {
			t.Fatalf("%q: AnalyzeLabeled reported failure", keep)
		}
		if len(got.ExternalSystems) != 1 || got.ExternalSystems[0].Value != keep {
			t.Errorf("%q: a deliberately-kept hostname was dropped, got %+v", keep, got.ExternalSystems)
		}
	}
}

// Nil, not an empty slice, for the same reason the acts/path inventories are:
// a sidecar too old to publish the level (or a window that used nothing at
// that level) must not read as "we looked and the hour used nothing".
func TestNoIdentifierInventoriesIsAbsentNotAnEmptyList(t *testing.T) {
	for name, inv := range map[string]map[string]any{
		"no inventory block at all":       nil,
		"an inventory without these keys": {"physical_acts": acts("read", 2)},
		"the keys present but empty": {
			"harness_tools": []map[string]any{}, "programs": []map[string]any{},
			"external_systems": []map[string]any{}, "integrations": []map[string]any{},
		},
		"every entry rejected by its gate": {
			"harness_tools": acts("two words", 4), "programs": acts(".env.example", 4),
			"external_systems": acts("10.0.0.1", 4), "integrations": acts("bad id!", 4),
		},
	} {
		body := inventoryBody(inv)
		if inv == nil {
			delete(body, "inventory")
		}
		srv := analyzeServer(t, body)
		got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
		srv.Close()
		if !ok {
			t.Fatalf("%s: AnalyzeLabeled reported failure", name)
		}
		if got.HarnessTools != nil || got.Programs != nil || got.ExternalSystems != nil || got.Integrations != nil {
			t.Errorf("%s: want nil, got tools=%+v programs=%+v systems=%+v integrations=%+v",
				name, got.HarnessTools, got.Programs, got.ExternalSystems, got.Integrations)
		}
	}
}

// named_terms DECODES, alongside the other eight. This test previously asserted
// the exact opposite — that named_terms stayed structurally unforwardable while
// the rest widened — and it is rewritten rather than deleted so the reversal is
// visible in the history of this file rather than only in a commit message.
//
// The value used is deliberately still "Federico": a real person name, the same
// one the old guard was written around. What changed is not the risk but the
// decision about it (see sidecar.InventoryBlock). A test asserting a person name
// now reaches the caller is the honest expression of that, and if someone later
// finds this uncomfortable, this is exactly the right place for them to find it.
func TestNamedTermsDecodesAlongsideTheIdentifierInventories(t *testing.T) {
	srv := analyzeServer(t, inventoryBody(map[string]any{
		"harness_tools":    acts("Bash", 30),
		"programs":         acts("git", 9),
		"external_systems": acts("github.com", 4),
		"integrations":     acts("notion-fetch", 1),
		"named_terms":      acts("Federico", 2),
	}))
	defer srv.Close()
	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if len(got.NamedTerms) != 1 || got.NamedTerms[0].Value != "Federico" || got.NamedTerms[0].N != 2 {
		t.Fatalf("named_terms did not round-trip: %+v", got.NamedTerms)
	}
}

// The shape gate is a BOUND, not a filter on meaning: it drops what the
// sidecar's own normalisation could not have produced, and keeps everything
// else — including multi-word terms, which a bare-identifier gate would have
// silently swallowed. Per-entry, so one bad value costs one value.
func TestTheNamedTermsGateBoundsShapeAndKeepsMultiWordTerms(t *testing.T) {
	long := strings.Repeat("x", 129)
	srv := analyzeServer(t, inventoryBody(map[string]any{
		"named_terms": []map[string]any{
			{"value": "Developer Preview", "n": 5}, // multi-word: MUST survive
			{"value": "Together.ai", "n": 3},
			{"value": "line\nbreak", "n": 2}, // could not come from terms.py
			{"value": long, "n": 1},          // over termMaxLen
			{"value": "", "n": 9},
			{"value": "ACME", "n": 7},
		},
	}))
	defer srv.Close()
	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	var kept []string
	for _, nt := range got.NamedTerms {
		kept = append(kept, nt.Value)
	}
	want := []string{"Developer Preview", "Together.ai", "ACME"}
	if len(kept) != len(want) {
		t.Fatalf("want %v, got %v", want, kept)
	}
	for i, w := range want {
		if kept[i] != w {
			t.Errorf("entry %d: want %q, got %q", i, w, kept[i])
		}
	}
}

// Round-trip for the LAST FOUR inventories — the levels the analysis had always
// extracted and never published. Same shape and same ordering guarantee as the
// four above; `shell_verbs` is the one whose values are deliberately multi-word.
func TestAnalyzeLabeledCarriesTheLastFourInventories(t *testing.T) {
	srv := analyzeServer(t, inventoryBody(map[string]any{
		"file_types":  acts(".tsx", 12, ".py", 5),
		"shell_verbs": acts("git rebase", 7, "pnpm test", 2),
		"subagents":   acts("general-purpose", 4, "Explore", 1),
		"mcp_servers": acts("notion", 5, "linear", 1),
	}))
	defer srv.Close()

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	checkNameCounts(t, "FileTypes", got.FileTypes, []enrichAct{{".tsx", 12}, {".py", 5}})
	checkNameCounts(t, "ShellVerbs", got.ShellVerbs,
		[]enrichAct{{"git rebase", 7}, {"pnpm test", 2}})
	checkNameCounts(t, "Subagents", got.Subagents,
		[]enrichAct{{"general-purpose", 4}, {"Explore", 1}})
	checkNameCounts(t, "McpServers", got.McpServers, []enrichAct{{"notion", 5}, {"linear", 1}})
}

// ⚠️ THE GATE THAT MAKES `shell_verbs` DIFFERENT FROM ITS THREE SIBLINGS.
// identifierShape would have been the obvious reuse and it is WRONG here: a verb
// is a COMMAND, and the whole reason this dimension beats `programs` is that it
// keeps the subcommand — `git rebase` says something `git` does not. A
// bare-identifier gate drops every multi-word value, i.e. exactly the class the
// dimension exists to carry, and it would have done so SILENTLY.
func TestShellVerbsKeepMultiWordCommandsThatIdentifierShapeWouldDrop(t *testing.T) {
	for _, v := range []string{"git rebase", "pnpm test", "docker compose up",
		"cargo build --release", "uv run pytest"} {
		got := convertShellVerbInventory([]InventoryItem{{Value: v, N: 3}})
		if len(got) != 1 || got[0].Value != v {
			t.Errorf("convertShellVerbInventory(%q) = %+v, want it kept whole — dropping a "+
				"multi-word command discards the whole point of this dimension", v, got)
		}
		if len(identifierShape.FindString(v)) != 0 {
			t.Errorf("premise broken: %q now matches identifierShape, so this test no longer "+
				"demonstrates why shell_verbs needs its own gate", v)
		}
	}
}

// PER-ENTRY, like every sibling gate: one unusable value costs exactly that
// value and the rest of the list survives. The rejections are the two the
// sidecar's own extraction could not have produced — a path separator (a
// filename is not a command, the same defect convertProgramInventory closes for
// `programs`) and a value long enough to be a command LINE, which is what `sh -c
// "…"` puts in one argument.
func TestShellVerbsDropOneBadEntryAndKeepTheList(t *testing.T) {
	long := "sh -c " + strings.Repeat("x", shellVerbMaxLen)
	got := convertShellVerbInventory([]InventoryItem{
		{Value: "git rebase", N: 7},
		{Value: "./scripts/build.sh", N: 4}, // a path, not a command
		{Value: "", N: 2},
		{Value: long, N: 1},
		{Value: "pnpm test", N: 2},
	})
	checkNameCounts(t, "ShellVerbs", got, []enrichAct{{"git rebase", 7}, {"pnpm test", 2}})
}

// The other three take identifierShape unchanged, and the same per-entry rule
// applies: a value the sidecar's normalisation could not have produced is
// dropped alone.
func TestTheThreeSingleTokenInventoriesDropOneBadEntryAndKeepTheList(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []InventoryItem
		want []enrichAct
	}{
		{"file_types", []InventoryItem{
			{Value: ".tsx", N: 12}, {Value: "src/app/page.tsx", N: 3}, {Value: ".py", N: 5},
		}, []enrichAct{{".tsx", 12}, {".py", 5}}},
		{"subagents", []InventoryItem{
			{Value: "general-purpose", N: 4}, {Value: "an agent with spaces", N: 2},
			{Value: "Explore", N: 1},
		}, []enrichAct{{"general-purpose", 4}, {"Explore", 1}}},
		{"mcp_servers", []InventoryItem{
			{Value: "notion", N: 5}, {Value: "", N: 9}, {Value: "linear", N: 1},
		}, []enrichAct{{"notion", 5}, {"linear", 1}}},
	} {
		checkNameCounts(t, tc.name, convertIdentifierInventory(tc.in), tc.want)
	}
}

// Nil in, nil out — not an empty slice — for all four, so a sidecar too old to
// emit the key publishes NO key rather than an empty list, which would read as
// "we looked and the hour used nothing".
func TestTheLastFourInventoriesAreNilRatherThanEmpty(t *testing.T) {
	if got := convertShellVerbInventory(nil); got != nil {
		t.Errorf("convertShellVerbInventory(nil) = %+v, want nil", got)
	}
	if got := convertShellVerbInventory([]InventoryItem{{Value: "/bin/sh", N: 1}}); got != nil {
		t.Errorf("an all-rejected list must be nil, not empty: %+v", got)
	}
}
