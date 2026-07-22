// Package tools provides tests for the GeminiAdapter.
package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sandboxGeminiHome points $HOME at a fresh temp dir for the duration of the
// test so GeminiAdapter's real side effect (reading/writing ~/.gemini/.env)
// never touches the developer's actual home directory. Returns the temp home.
func sandboxGeminiHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestGeminiApplySetsTelemetry(t *testing.T) {
	sandboxGeminiHome(t)
	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://e", IngestToken: "tok", Actor: "me"}
	cur := "{\n  \"theme\": \"dark\"\n}\n"
	plan := a.Apply(&cur, p, false)
	if !plan.Changed || !strings.Contains(plan.AfterText, "otlpEndpoint") || !strings.Contains(plan.AfterText, "\"theme\"") {
		t.Fatalf("telemetry not merged into existing config:\n%s", plan.AfterText)
	}
	st := a.Status(&plan.AfterText, nil)
	if !st.Configured {
		t.Fatalf("expected configured, detail=%s", st.Detail)
	}
}

func TestGeminiApplyRemoveRoundTrip(t *testing.T) {
	sandboxGeminiHome(t)
	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://otel.example.com", IngestToken: "secret", Actor: "alice"}

	// Start with a config that has an extra key.
	original := "{\n  \"theme\": \"dark\"\n}\n"

	// Apply: should set telemetry.
	applyPlan := a.Apply(&original, p, false)
	if !applyPlan.Changed {
		t.Fatal("Apply: expected Changed=true")
	}
	if applyPlan.Conflict != "" {
		t.Fatalf("Apply: unexpected conflict: %s", applyPlan.Conflict)
	}
	if !strings.Contains(applyPlan.AfterText, "otlpEndpoint") {
		t.Fatalf("Apply: AfterText missing otlpEndpoint:\n%s", applyPlan.AfterText)
	}
	if !strings.Contains(applyPlan.AfterText, "\"theme\"") {
		t.Fatalf("Apply: AfterText missing original 'theme' key:\n%s", applyPlan.AfterText)
	}

	// Status after apply: Configured must be true.
	stAfterApply := a.Status(&applyPlan.AfterText, applyPlan.Managed)
	if !stAfterApply.Configured {
		t.Fatalf("Status after Apply: expected Configured=true, detail=%s", stAfterApply.Detail)
	}

	// Remove: strip telemetry + hook + .env block back out.
	removePlan := a.Remove(&applyPlan.AfterText, applyPlan.Managed)
	if !removePlan.Changed {
		t.Fatal("Remove: expected Changed=true")
	}
	if removePlan.Conflict != "" {
		t.Fatalf("Remove: unexpected conflict: %s", removePlan.Conflict)
	}

	// Status after remove: Configured must be false.
	stAfterRemove := a.Status(&removePlan.AfterText, nil)
	if stAfterRemove.Configured {
		t.Fatalf("Status after Remove: expected Configured=false, detail=%s", stAfterRemove.Detail)
	}

	// The surviving config should still contain the original "theme" key.
	if !strings.Contains(removePlan.AfterText, "\"theme\"") {
		t.Fatalf("Remove: AfterText lost original 'theme' key:\n%s", removePlan.AfterText)
	}
}

func TestGeminiApplyNilCurrentText(t *testing.T) {
	sandboxGeminiHome(t)
	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://otel.example.com", IngestToken: "tok2", Actor: "bob"}

	plan := a.Apply(nil, p, false)
	if !plan.Changed {
		t.Fatal("Apply on nil currentText: expected Changed=true")
	}
	if !strings.Contains(plan.AfterText, "otlpEndpoint") {
		t.Fatalf("Apply on nil currentText: AfterText missing otlpEndpoint:\n%s", plan.AfterText)
	}

	// managed["created"] should be true when currentText was nil.
	created, ok := plan.Managed["created"]
	if !ok || created != true {
		t.Fatalf("Apply on nil currentText: expected managed[\"created\"]=true, got %v", plan.Managed)
	}
}

func TestGeminiMeta(t *testing.T) {
	a := &GeminiAdapter{}
	if a.Name() != "gemini" {
		t.Fatalf("Name()=%q, want %q", a.Name(), "gemini")
	}
	if a.DisplayName() != "Gemini CLI" {
		t.Fatalf("DisplayName()=%q, want %q", a.DisplayName(), "Gemini CLI")
	}
	cp := a.ConfigPath()
	if !strings.HasSuffix(cp, "/.gemini/settings.json") {
		t.Fatalf("ConfigPath()=%q should end with /.gemini/settings.json", cp)
	}
}

// TestGeminiApplyWiresBeforeAgentHook covers the brief's core scenario: a
// settings.json containing only security.auth (the real-world shape of a
// freshly-installed Gemini CLI) gets both the fixed-endpoint telemetry block
// and a hooks.BeforeAgent entry running keld's hook command, with the
// existing security.auth key preserved.
func TestGeminiApplyWiresBeforeAgentHook(t *testing.T) {
	sandboxGeminiHome(t)
	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok", Actor: "me"}

	cur := "{\n  \"security\": {\n    \"auth\": {\n      \"selectedType\": \"oauth-personal\"\n    }\n  }\n}\n"
	plan := a.Apply(&cur, p, false)

	if plan.Conflict != "" {
		t.Fatalf("unexpected conflict: %s", plan.Conflict)
	}
	if !strings.Contains(plan.AfterText, "\"selectedType\": \"oauth-personal\"") {
		t.Fatalf("security.auth not preserved:\n%s", plan.AfterText)
	}
	// Fixed base endpoint: no /v1/logs path, no ?token= query param.
	if !strings.Contains(plan.AfterText, "\"otlpEndpoint\": \"https://atlas.keld.co\"") {
		t.Fatalf("expected fixed base otlpEndpoint:\n%s", plan.AfterText)
	}
	if strings.Contains(plan.AfterText, "/v1/logs") || strings.Contains(plan.AfterText, "?token=") {
		t.Fatalf("otlpEndpoint must not carry a path or token query param:\n%s", plan.AfterText)
	}
	if !strings.Contains(plan.AfterText, "\"BeforeAgent\"") {
		t.Fatalf("expected hooks.BeforeAgent entry:\n%s", plan.AfterText)
	}
	if !strings.Contains(plan.AfterText, "keld __hook --source gemini") {
		t.Fatalf("expected keld hook command:\n%s", plan.AfterText)
	}
	// Shape must mirror Claude's hook convention: an array of
	// {hooks:[{type:"command", command}]} entries under the event name,
	// verified against Gemini v0.51.0's own hook-config parser (see
	// task-3-report.md for how this was confirmed live).
	if !strings.Contains(plan.AfterText, "\"type\": \"command\"") {
		t.Fatalf("expected type:command inner hook shape:\n%s", plan.AfterText)
	}
}

// TestGeminiApplyWritesEnvBlockPreservingAPIKey covers the second artifact:
// Apply must upsert the keld OTEL block into ~/.gemini/.env while never
// touching a pre-existing GEMINI_API_KEY line.
func TestGeminiApplyWritesEnvBlockPreservingAPIKey(t *testing.T) {
	home := sandboxGeminiHome(t)
	geminiDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	envPath := filepath.Join(geminiDir, ".env")
	if err := os.WriteFile(envPath, []byte("GEMINI_API_KEY=real-secret-value\n"), 0o600); err != nil {
		t.Fatalf("seed .env: %v", err)
	}

	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok", Actor: "me"}
	cur := "{\n  \"security\": {\n    \"auth\": {\n      \"selectedType\": \"oauth-personal\"\n    }\n  }\n}\n"

	plan := a.Apply(&cur, p, false)
	if plan.Conflict != "" {
		t.Fatalf("unexpected conflict: %s", plan.Conflict)
	}

	got, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading .env after Apply: %v", err)
	}
	envText := string(got)

	if !strings.Contains(envText, "GEMINI_API_KEY=real-secret-value") {
		t.Fatalf("Apply clobbered GEMINI_API_KEY:\n%s", envText)
	}
	if !strings.Contains(envText, "# >>> keld-managed (do not edit) >>>") {
		t.Fatalf(".env missing keld block start marker:\n%s", envText)
	}
	if !strings.Contains(envText, "OTEL_EXPORTER_OTLP_HEADERS=x-keld-ingest-token=tok,x-keld-actor=me") {
		t.Fatalf(".env missing OTEL headers line:\n%s", envText)
	}
	if !strings.Contains(envText, "OTEL_TRACES_EXPORTER=none") {
		t.Fatalf(".env missing trace-off line:\n%s", envText)
	}

	// Mode must be 0600 (the file holds a secret).
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatalf("Stat .env: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf(".env mode = %o, want 0600", perm)
	}

	// managed["env_created"] should be false: the file already existed.
	if created, _ := plan.Managed["env_created"].(bool); created {
		t.Fatal("expected managed[\"env_created\"]=false for a pre-existing .env")
	}
}

// TestGeminiApplyCreatesEnvFileWhenAbsent covers the "no .env yet" path: Apply
// must create the file fresh at 0600.
func TestGeminiApplyCreatesEnvFileWhenAbsent(t *testing.T) {
	home := sandboxGeminiHome(t)
	// Note: no ~/.gemini dir at all yet — Apply must create it.
	envPath := filepath.Join(home, ".gemini", ".env")

	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok", Actor: "me"}

	plan := a.Apply(nil, p, false)
	if plan.Conflict != "" {
		t.Fatalf("unexpected conflict: %s", plan.Conflict)
	}

	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatalf(".env was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf(".env mode = %o, want 0600", perm)
	}
	if created, _ := plan.Managed["env_created"].(bool); !created {
		t.Fatal("expected managed[\"env_created\"]=true for a freshly-created .env")
	}
}

// TestGeminiRemoveStripsBothArtifacts covers the brief's Remove scenario: a
// settings.json with security.auth plus keld's blocks, and a .env with
// GEMINI_API_KEY plus keld's block — Remove must strip only keld's parts of
// each, leaving security.auth and GEMINI_API_KEY intact.
func TestGeminiRemoveStripsBothArtifacts(t *testing.T) {
	home := sandboxGeminiHome(t)
	geminiDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	envPath := filepath.Join(geminiDir, ".env")
	if err := os.WriteFile(envPath, []byte("GEMINI_API_KEY=real-secret-value\n"), 0o600); err != nil {
		t.Fatalf("seed .env: %v", err)
	}

	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok", Actor: "me"}
	cur := "{\n  \"security\": {\n    \"auth\": {\n      \"selectedType\": \"oauth-personal\"\n    }\n  }\n}\n"

	applyPlan := a.Apply(&cur, p, false)
	if applyPlan.Conflict != "" {
		t.Fatalf("Apply: unexpected conflict: %s", applyPlan.Conflict)
	}

	removePlan := a.Remove(&applyPlan.AfterText, applyPlan.Managed)
	if removePlan.Conflict != "" {
		t.Fatalf("Remove: unexpected conflict: %s", removePlan.Conflict)
	}
	if !removePlan.Changed {
		t.Fatal("Remove: expected Changed=true")
	}

	// settings.json: security.auth survives; telemetry + hook are gone.
	if !strings.Contains(removePlan.AfterText, "\"selectedType\": \"oauth-personal\"") {
		t.Fatalf("Remove: security.auth lost:\n%s", removePlan.AfterText)
	}
	if strings.Contains(removePlan.AfterText, "telemetry") {
		t.Fatalf("Remove: telemetry block not stripped:\n%s", removePlan.AfterText)
	}
	if strings.Contains(removePlan.AfterText, "BeforeAgent") || strings.Contains(removePlan.AfterText, "keld __hook") {
		t.Fatalf("Remove: BeforeAgent hook not stripped:\n%s", removePlan.AfterText)
	}

	// .env: GEMINI_API_KEY survives; keld block is gone.
	envAfter, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading .env after Remove: %v", err)
	}
	envText := string(envAfter)
	if !strings.Contains(envText, "GEMINI_API_KEY=real-secret-value") {
		t.Fatalf("Remove: GEMINI_API_KEY lost from .env:\n%s", envText)
	}
	if strings.Contains(envText, "keld-managed") {
		t.Fatalf("Remove: keld block not stripped from .env:\n%s", envText)
	}
	if strings.Contains(envText, "OTEL_EXPORTER_OTLP_HEADERS") || strings.Contains(envText, "OTEL_TRACES_EXPORTER") {
		t.Fatalf("Remove: OTEL env lines not stripped:\n%s", envText)
	}

	// Status after remove must report not configured.
	st := a.Status(&removePlan.AfterText, nil)
	if st.Configured {
		t.Fatalf("Status after Remove: expected Configured=false, detail=%s", st.Detail)
	}
}

// TestGeminiRemoveDeletesFreshlyCreatedEnvFile covers the "we created the
// .env file, and after removal it's now empty" case: the file itself should
// be deleted rather than left behind as an empty husk.
func TestGeminiRemoveDeletesFreshlyCreatedEnvFile(t *testing.T) {
	home := sandboxGeminiHome(t)
	envPath := filepath.Join(home, ".gemini", ".env")

	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok", Actor: "me"}

	applyPlan := a.Apply(nil, p, false)
	if applyPlan.Conflict != "" {
		t.Fatalf("Apply: unexpected conflict: %s", applyPlan.Conflict)
	}
	if _, err := os.Stat(envPath); err != nil {
		t.Fatalf(".env should exist after Apply: %v", err)
	}

	removePlan := a.Remove(&applyPlan.AfterText, applyPlan.Managed)
	if removePlan.Conflict != "" {
		t.Fatalf("Remove: unexpected conflict: %s", removePlan.Conflict)
	}

	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf(".env should have been deleted (keld created it fresh and it's now empty), stat err=%v", err)
	}
}

// TestGeminiApplyIdempotent covers the brief's idempotency requirement:
// applying twice in a row must produce byte-identical settings.json output
// and must not duplicate the .env block.
func TestGeminiApplyIdempotent(t *testing.T) {
	home := sandboxGeminiHome(t)
	envPath := filepath.Join(home, ".gemini", ".env")

	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok", Actor: "me"}
	cur := "{\n  \"security\": {\n    \"auth\": {\n      \"selectedType\": \"oauth-personal\"\n    }\n  }\n}\n"

	first := a.Apply(&cur, p, false)
	if first.Conflict != "" {
		t.Fatalf("first Apply: unexpected conflict: %s", first.Conflict)
	}
	second := a.Apply(&first.AfterText, p, false)
	if second.Conflict != "" {
		t.Fatalf("second Apply: unexpected conflict: %s", second.Conflict)
	}

	if second.AfterText != first.AfterText {
		t.Fatalf("Apply is not idempotent on settings.json:\n--first--\n%s\n--second--\n%s", first.AfterText, second.AfterText)
	}
	if second.Changed {
		t.Fatal("second Apply: expected Changed=false (already configured)")
	}

	envData, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading .env: %v", err)
	}
	envText := string(envData)
	if n := strings.Count(envText, "# >>> keld-managed (do not edit) >>>"); n != 1 {
		t.Fatalf(".env block duplicated: found %d start markers in:\n%s", n, envText)
	}
	if n := strings.Count(envText, "OTEL_TRACES_EXPORTER=none"); n != 1 {
		t.Fatalf(".env OTEL line duplicated: found %d occurrences in:\n%s", n, envText)
	}
}
