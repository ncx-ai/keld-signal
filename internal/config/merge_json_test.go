// internal/config/merge_json_test.go
package config

import (
	"strings"
	"testing"

	"github.com/iancoleman/orderedmap"
)

func TestDumpJSONFormatAndOrder(t *testing.T) {
	o := orderedmap.New()
	o.Set("b", "1")
	o.Set("a", "2")
	got := DumpJSON(o)
	want := "{\n  \"b\": \"1\",\n  \"a\": \"2\"\n}\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestLoadJSONInvalid(t *testing.T) {
	_, err := LoadJSON("{not json")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "existing config is not valid JSON") {
		t.Fatalf("error message %q missing expected prefix", err.Error())
	}
	o, err := LoadJSON("   ")
	if err != nil || len(o.Keys()) != 0 {
		t.Fatalf("blank should be empty map, got %v %v", o, err)
	}
}

func TestMergeEnvPreservesExistingOrder(t *testing.T) {
	o, _ := LoadJSON(`{"env":{"EXISTING":"x"}}`)
	env := orderedmap.New()
	env.Set("NEW", "y")
	keys := MergeEnv(o, env)
	if len(keys) != 1 || keys[0] != "NEW" {
		t.Fatalf("keys %v", keys)
	}
	if DumpJSON(o) != "{\n  \"env\": {\n    \"EXISTING\": \"x\",\n    \"NEW\": \"y\"\n  }\n}\n" {
		t.Fatalf("merge output:\n%s", DumpJSON(o))
	}
}

func TestMergeEnvJSONUpsertsExistingKey(t *testing.T) {
	o, _ := LoadJSON(`{"env":{"EXISTING":"old"}}`)
	env := orderedmap.New()
	env.Set("EXISTING", "new")
	keys := MergeEnv(o, env)
	if len(keys) != 1 || keys[0] != "EXISTING" {
		t.Fatalf("keys %v", keys)
	}
	want := "{\n  \"env\": {\n    \"EXISTING\": \"new\"\n  }\n}\n"
	if got := DumpJSON(o); got != want {
		t.Fatalf("upsert output got %q want %q", got, want)
	}
}

func TestDumpJSONDeepNestingNoEscape(t *testing.T) {
	// Regression: value-form OrderedMaps from LoadJSON nested 3+ levels deep
	// must still have HTML escaping disabled, matching Python json.dumps.
	o, _ := LoadJSON(`{"a":{"b":{"c":"x&y<z>"}}}`)
	got := DumpJSON(o)
	if !strings.Contains(got, "x&y<z>") {
		t.Fatalf("deep-nested special chars were escaped, dump:\n%s", got)
	}
}

func TestDumpJSONTopLevelNoEscape(t *testing.T) {
	o := orderedmap.New()
	o.Set("k", "a&b<c>d")
	got := DumpJSON(o)
	if !strings.Contains(got, "a&b<c>d") {
		t.Fatalf("top-level special chars were escaped, dump:\n%s", got)
	}
}

func TestHasHookWithCommandJSONHandlesAmpersand(t *testing.T) {
	// FIX 1 regression: marshalCompact must not HTML-escape '&'. Before the
	// no-escape encoder, "a&b" became "a&b" and this search returned false.
	o := orderedmap.New()
	m := "startup"
	AddClaudeHook(o, "SessionStart", &m, "echo a&b && keld __hook --source claude_code")
	if !HasHookWithCommand(o, "a&b") {
		t.Fatalf("expected substring with '&' to be found, dump:\n%s", DumpJSON(o))
	}
}

func TestRemoveSectionKeysDeletesEmptySection(t *testing.T) {
	o, _ := LoadJSON(`{"env":{"A":"1"}}`)
	RemoveSectionKeys(o, "env", []string{"A"})
	if DumpJSON(o) != "{}\n" {
		t.Fatalf("expected empty obj, got %s", DumpJSON(o))
	}
}

func TestClaudeHookAddIdempotentAndRemove(t *testing.T) {
	o := orderedmap.New()
	m := "startup"
	AddClaudeHook(o, "SessionStart", &m, "keld __hook --source claude_code")
	AddClaudeHook(o, "SessionStart", &m, "keld __hook --source claude_code") // dup → no-op
	AddClaudeHook(o, "CwdChanged", nil, "keld __hook --source claude_code")
	if !HasHookWithCommand(o, "keld __hook") {
		t.Fatal("expected hook present")
	}
	RemoveHooksByCommand(o, "keld __hook")
	if HasHookWithCommand(o, "keld __hook") {
		t.Fatal("expected hooks removed")
	}
	if len(o.Keys()) != 0 {
		t.Fatalf("hooks key should be pruned, keys=%v", o.Keys())
	}
}

// TestRemoveHooksByCommandMultipleAllKeldEvents guards the delete-during-range
// bug: orderedmap.Keys() is the live slice and Delete() reslices it, so removing
// several all-keld events in one pass must not skip any (it previously left the
// event after each deleted one behind).
func TestRemoveHooksByCommandMultipleAllKeldEvents(t *testing.T) {
	const in = `{"hooks":{` +
		`"A":[{"hooks":[{"type":"command","command":"keld __hook --source x"}]}],` +
		`"B":[{"hooks":[{"type":"command","command":"keld __hook --source x"}]}],` +
		`"C":[{"hooks":[{"type":"command","command":"keld __hook --source x"}]}],` +
		`"D":[{"hooks":[{"type":"command","command":"keld __hook --source x"}]}]}}`
	obj, err := LoadJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	RemoveHooksByCommand(obj, "keld __hook")
	out := DumpJSON(obj)
	if strings.Contains(out, "keld __hook") {
		t.Fatalf("all keld hooks should be removed across every event; leftover:\n%s", out)
	}
	// With every event emptied, the hooks object itself should be gone.
	if strings.Contains(out, "\"hooks\"") {
		t.Fatalf("empty hooks map should be dropped:\n%s", out)
	}
}
