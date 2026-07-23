package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iancoleman/orderedmap"
)

func TestClaudeApplyStructural(t *testing.T) {
	a := &ClaudeAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok"}
	existing := "{\n  \"model\": \"x\"\n}\n"

	plan := a.Apply(&existing, p, false)

	if !plan.Changed {
		t.Fatal("expected plan.Changed == true")
	}
	if plan.Conflict != "" {
		t.Fatalf("expected no conflict, got %q", plan.Conflict)
	}

	// Parse AfterText and verify env keys
	obj := orderedmap.New()
	if err := json.Unmarshal([]byte(plan.AfterText), obj); err != nil {
		t.Fatalf("AfterText is not valid JSON: %v\n%s", err, plan.AfterText)
	}

	// Verify pre-existing key is preserved
	modelVal, ok := obj.Get("model")
	if !ok {
		t.Fatal("expected pre-existing key 'model' to be preserved")
	}
	if modelVal != "x" {
		t.Fatalf("expected model == 'x', got %v", modelVal)
	}

	// Verify env sub-object
	envVal, ok := obj.Get("env")
	if !ok {
		t.Fatal("expected 'env' key in AfterText")
	}
	envMap, ok := envVal.(*orderedmap.OrderedMap)
	if !ok {
		// Try value form
		if em, ok2 := envVal.(orderedmap.OrderedMap); ok2 {
			envMap = &em
		} else {
			t.Fatalf("expected 'env' to be a map, got %T", envVal)
		}
	}

	// OTEL_EXPORTER_OTLP_ENDPOINT must be present
	if _, ok := envMap.Get("OTEL_EXPORTER_OTLP_ENDPOINT"); !ok {
		t.Fatal("expected OTEL_EXPORTER_OTLP_ENDPOINT in env")
	}

	// CLAUDE_CODE_ENABLE_TELEMETRY must be first
	if len(envMap.Keys()) == 0 || envMap.Keys()[0] != "CLAUDE_CODE_ENABLE_TELEMETRY" {
		t.Fatalf("expected CLAUDE_CODE_ENABLE_TELEMETRY to be first env key, got %v", envMap.Keys())
	}

	// All 6 OTEL keys must be present in order
	wantKeys := []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY",
		"OTEL_LOGS_EXPORTER",
		"OTEL_METRICS_EXPORTER",
		"OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_HEADERS",
	}
	gotKeys := envMap.Keys()
	if len(gotKeys) < len(wantKeys) {
		t.Fatalf("expected at least %d env keys, got %d: %v", len(wantKeys), len(gotKeys), gotKeys)
	}
	for i, k := range wantKeys {
		if gotKeys[i] != k {
			t.Fatalf("env key[%d]: want %q, got %q", i, k, gotKeys[i])
		}
	}

	// AfterText must contain keld __hook
	if !strings.Contains(plan.AfterText, "keld __hook") {
		t.Fatal("expected AfterText to contain 'keld __hook'")
	}

	// hooks structure must have SessionStart and CwdChanged
	hooksVal, ok := obj.Get("hooks")
	if !ok {
		t.Fatal("expected 'hooks' key in AfterText")
	}
	hooksMap, ok := hooksVal.(*orderedmap.OrderedMap)
	if !ok {
		if hm, ok2 := hooksVal.(orderedmap.OrderedMap); ok2 {
			hooksMap = &hm
		} else {
			t.Fatalf("expected 'hooks' to be a map, got %T", hooksVal)
		}
	}
	if _, ok := hooksMap.Get("SessionStart"); !ok {
		t.Fatal("expected SessionStart in hooks")
	}
	if _, ok := hooksMap.Get("CwdChanged"); !ok {
		t.Fatal("expected CwdChanged in hooks")
	}

	// managed["created"] == false when applied over existing text
	if plan.Managed["created"] != false {
		t.Fatalf("expected managed['created'] == false, got %v", plan.Managed["created"])
	}

	// env_keys must be a slice of the 6 OTEL keys
	envKeys, ok := plan.Managed["env_keys"].([]string)
	if !ok {
		t.Fatalf("expected managed['env_keys'] to be []string, got %T", plan.Managed["env_keys"])
	}
	if len(envKeys) != 6 {
		t.Fatalf("expected 6 env keys in managed, got %d: %v", len(envKeys), envKeys)
	}

	// hook_substr must be present
	hookSubstr, ok := plan.Managed["hook_substr"].(string)
	if !ok || hookSubstr != "keld __hook" {
		t.Fatalf("expected managed['hook_substr'] == 'keld __hook', got %v", plan.Managed["hook_substr"])
	}
}

func TestClaudeApplyNilCreated(t *testing.T) {
	a := &ClaudeAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok"}

	plan := a.Apply(nil, p, false)

	if !plan.Changed {
		t.Fatal("expected plan.Changed == true for nil currentText")
	}
	if plan.Managed["created"] != true {
		t.Fatalf("expected managed['created'] == true when Apply(nil,...), got %v", plan.Managed["created"])
	}
}

func TestClaudeStatusConfigured(t *testing.T) {
	a := &ClaudeAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok"}
	existing := "{\n  \"model\": \"x\"\n}\n"

	plan := a.Apply(&existing, p, false)
	afterText := plan.AfterText

	st := a.Status(&afterText, nil)
	if !st.Configured {
		t.Fatalf("expected Status.Configured == true after Apply, got detail=%q, afterText=%s",
			st.Detail, afterText)
	}
	if st.Name != "claude_code" {
		t.Fatalf("expected Name == 'claude_code', got %q", st.Name)
	}
}

func TestClaudeRemoveRoundTrip(t *testing.T) {
	a := &ClaudeAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok"}
	existing := "{\n  \"model\": \"x\"\n}\n"

	applyPlan := a.Apply(&existing, p, false)
	afterText := applyPlan.AfterText

	// Status must be configured before remove
	st := a.Status(&afterText, nil)
	if !st.Configured {
		t.Fatal("expected configured before remove")
	}

	removePlan := a.Remove(&afterText, applyPlan.Managed)
	if !removePlan.Changed {
		t.Fatal("expected Remove to produce a change")
	}

	removedText := removePlan.AfterText
	st2 := a.Status(&removedText, nil)
	if st2.Configured {
		t.Fatalf("expected Status.Configured == false after Remove, got detail=%q, removedText=%s",
			st2.Detail, removedText)
	}
}

func TestClaudeName(t *testing.T) {
	a := &ClaudeAdapter{}
	if a.Name() != "claude_code" {
		t.Fatalf("expected Name() == 'claude_code', got %q", a.Name())
	}
	if a.DisplayName() != "Claude Code" {
		t.Fatalf("expected DisplayName() == 'Claude Code', got %q", a.DisplayName())
	}
}

func TestClaudeConfigPath(t *testing.T) {
	a := &ClaudeAdapter{}
	cp := a.ConfigPath()
	if !strings.HasSuffix(cp, "/.claude/settings.json") {
		t.Fatalf("expected ConfigPath to end with /.claude/settings.json, got %q", cp)
	}
}

// TestClaudeApplyDedupsHooksOnCommandChange: re-running setup after the hook
// command string changed (bare "keld" → pinned absolute path) must REPLACE the
// old keld hooks, not append duplicates. Result must equal a fresh pinned apply.
func TestClaudeApplyDedupsHooksOnCommandChange(t *testing.T) {
	a := &ClaudeAdapter{}
	pinned := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok", BinPath: "/usr/local/keld/keld"}

	// Older setup wrote BARE-command hooks.
	bare := a.Apply(strPtrLocal("{}"), SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok"}, false)
	// New setup re-applies with the pinned command over that config.
	got := a.Apply(&bare.AfterText, pinned, false)
	// A fresh pinned apply on empty config is the reference shape.
	want := a.Apply(strPtrLocal("{}"), pinned, false)

	if got.AfterText != want.AfterText {
		t.Fatalf("re-apply over stale bare hooks not idempotent (duplicate/leftover hooks):\n--got--\n%s\n--want--\n%s", got.AfterText, want.AfterText)
	}
	if strings.Contains(got.AfterText, `"keld __hook --source claude_code"`) {
		t.Fatalf("stale bare hooks should have been removed:\n%s", got.AfterText)
	}
}
