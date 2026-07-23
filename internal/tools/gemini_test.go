// Package tools provides tests for the GeminiAdapter.
package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sandboxGeminiHome points $HOME at a fresh temp dir for the duration of the
// test so any manual writes tests perform to simulate the caller committing
// a Plan.ExtraFile never touch the developer's actual home directory. Returns
// the temp home.
func sandboxGeminiHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// commitExtraFile writes (or deletes) an ExtraFile to disk exactly the way
// the real caller (internal/cli/setup.go, internal/cli/uninstall.go) does
// once it has passed its own confirm/--dry-run gate. Tests use this to
// simulate "the user confirmed" between an Apply/Remove call and a
// subsequent one that needs to observe the committed on-disk state (e.g.
// Remove reading back what Apply staged). A nil ef is a no-op.
func commitExtraFile(t *testing.T, ef *ExtraFile) {
	t.Helper()
	if ef == nil {
		return
	}
	if ef.Delete {
		if err := os.Remove(ef.Path); err != nil && !os.IsNotExist(err) {
			t.Fatalf("commitExtraFile: delete %s: %v", ef.Path, err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(ef.Path), 0o700); err != nil {
		t.Fatalf("commitExtraFile: MkdirAll: %v", err)
	}
	mode := ef.Mode
	if mode == 0 {
		mode = 0o600
	}
	if err := os.WriteFile(ef.Path, []byte(ef.AfterText), mode); err != nil {
		t.Fatalf("commitExtraFile: WriteFile %s: %v", ef.Path, err)
	}
}

func TestGeminiApplySetsTelemetry(t *testing.T) {
	sandboxGeminiHome(t)
	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://e", IngestToken: "tok"}
	cur := "{\n  \"theme\": \"dark\"\n}\n"
	plan := a.Apply(&cur, p, false)
	if !plan.Changed || !strings.Contains(plan.AfterText, "otlpEndpoint") || !strings.Contains(plan.AfterText, "\"theme\"") {
		t.Fatalf("telemetry not merged into existing config:\n%s", plan.AfterText)
	}
	// Status reads .env off disk; commit the staged ExtraFile first, exactly
	// as the real caller would after confirm, so Status sees it.
	commitExtraFile(t, plan.ExtraFile)
	st := a.Status(&plan.AfterText, nil)
	if !st.Configured {
		t.Fatalf("expected configured, detail=%s", st.Detail)
	}
}

func TestGeminiApplyRemoveRoundTrip(t *testing.T) {
	sandboxGeminiHome(t)
	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://otel.example.com", IngestToken: "secret"}

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
	commitExtraFile(t, applyPlan.ExtraFile)

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
	commitExtraFile(t, removePlan.ExtraFile)

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
	p := SetupParams{Endpoint: "https://otel.example.com", IngestToken: "tok2"}

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
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok"}

	cur := "{\n  \"security\": {\n    \"auth\": {\n      \"selectedType\": \"oauth-personal\"\n    }\n  }\n}\n"
	plan := a.Apply(&cur, p, false)

	if plan.Conflict != "" {
		t.Fatalf("unexpected conflict: %s", plan.Conflict)
	}
	if !strings.Contains(plan.AfterText, "\"selectedType\": \"oauth-personal\"") {
		t.Fatalf("security.auth not preserved:\n%s", plan.AfterText)
	}
	// Token rides in the endpoint query (gemini can't carry an auth header in an
	// untrusted workspace); still no baked-in /v1/logs path (the SDK appends it).
	if !strings.Contains(plan.AfterText, "\"otlpEndpoint\": \"https://atlas.keld.co?token=tok\"") {
		t.Fatalf("expected token-in-query otlpEndpoint:\n%s", plan.AfterText)
	}
	if strings.Contains(plan.AfterText, "/v1/logs") {
		t.Fatalf("otlpEndpoint must not carry a signal path:\n%s", plan.AfterText)
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

// legacyEnvBlock is a ~/.gemini/.env as keld <= v0.11.0 wrote it: the user's
// own GEMINI_API_KEY plus a keld-managed OTEL header block (the block that, on
// this version, we clean up rather than write).
const legacyEnvBlock = "GEMINI_API_KEY=real-secret-value\n" +
	"# >>> keld-managed (do not edit) >>>\n" +
	"OTEL_EXPORTER_OTLP_HEADERS=x-keld-ingest-token=old-token\n" +
	"# <<< keld-managed <<<\n"

// TestGeminiApplyLeavesCleanEnvUntouched: keld no longer writes a .env block.
// When ~/.gemini/.env has no legacy keld block, Apply must produce NO ExtraFile
// and never touch the file (especially the user's GEMINI_API_KEY).
func TestGeminiApplyLeavesCleanEnvUntouched(t *testing.T) {
	home := sandboxGeminiHome(t)
	geminiDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	envPath := filepath.Join(geminiDir, ".env")
	const seeded = "GEMINI_API_KEY=real-secret-value\n"
	if err := os.WriteFile(envPath, []byte(seeded), 0o600); err != nil {
		t.Fatalf("seed .env: %v", err)
	}

	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok"}
	cur := "{\n  \"security\": {\n    \"auth\": {\n      \"selectedType\": \"oauth-personal\"\n    }\n  }\n}\n"

	plan := a.Apply(&cur, p, false)
	if plan.Conflict != "" {
		t.Fatalf("unexpected conflict: %s", plan.Conflict)
	}

	// No legacy keld block present, so there is nothing to clean up.
	if plan.ExtraFile != nil {
		t.Fatalf("expected no ExtraFile for a .env with no keld block, got %+v", plan.ExtraFile)
	}
	// And the file on disk is untouched.
	onDisk, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading .env after Apply: %v", err)
	}
	if string(onDisk) != seeded {
		t.Fatalf("Apply touched ~/.gemini/.env:\nwant: %q\ngot:  %q", seeded, string(onDisk))
	}
	// Token rides in settings.json's otlpEndpoint, not the .env.
	if !strings.Contains(plan.AfterText, "?token=tok") {
		t.Fatalf("expected token in otlpEndpoint query:\n%s", plan.AfterText)
	}
}

// TestGeminiApplyCleansLegacyEnvBlock: an upgrading install has a v0.11.0 keld
// block in ~/.gemini/.env. Apply must stage an ExtraFile that strips it while
// preserving GEMINI_API_KEY — and must not write the file itself.
func TestGeminiApplyCleansLegacyEnvBlock(t *testing.T) {
	home := sandboxGeminiHome(t)
	geminiDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	envPath := filepath.Join(geminiDir, ".env")
	if err := os.WriteFile(envPath, []byte(legacyEnvBlock), 0o600); err != nil {
		t.Fatalf("seed .env: %v", err)
	}

	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok"}
	cur := "{}\n"

	plan := a.Apply(&cur, p, false)
	if plan.Conflict != "" {
		t.Fatalf("unexpected conflict: %s", plan.Conflict)
	}

	// Apply is side-effect free: on-disk .env still has the legacy block.
	onDisk, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading .env after Apply: %v", err)
	}
	if string(onDisk) != legacyEnvBlock {
		t.Fatalf("Apply wrote to .env directly:\n%s", string(onDisk))
	}

	ef := plan.ExtraFile
	if ef == nil {
		t.Fatal("expected plan.ExtraFile staging the legacy-block cleanup")
	}
	if ef.Path != envPath {
		t.Fatalf("ExtraFile.Path = %q, want %q", ef.Path, envPath)
	}
	if ef.Delete {
		t.Fatal("ExtraFile.Delete should be false: GEMINI_API_KEY must survive")
	}
	if ef.Mode != 0o600 {
		t.Fatalf("ExtraFile.Mode = %o, want 0600", ef.Mode)
	}
	if !strings.Contains(ef.AfterText, "GEMINI_API_KEY=real-secret-value") {
		t.Fatalf("cleanup dropped GEMINI_API_KEY:\n%s", ef.AfterText)
	}
	if strings.Contains(ef.AfterText, "keld-managed") || strings.Contains(ef.AfterText, "OTEL_EXPORTER_OTLP_HEADERS") {
		t.Fatalf("legacy keld block not stripped:\n%s", ef.AfterText)
	}

	// Committing it leaves just the API key on disk.
	commitExtraFile(t, ef)
	final, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading .env after commit: %v", err)
	}
	if strings.Contains(string(final), "keld-managed") {
		t.Fatalf("keld block still present after cleanup:\n%s", string(final))
	}
}

// TestGeminiApplyDoesNotCreateEnvFile: when ~/.gemini/.env is absent, keld
// writes nothing there (the token lives in settings.json now), so Apply must
// produce no ExtraFile and not create the file or directory.
func TestGeminiApplyDoesNotCreateEnvFile(t *testing.T) {
	home := sandboxGeminiHome(t)
	envPath := filepath.Join(home, ".gemini", ".env")

	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok"}

	plan := a.Apply(nil, p, false)
	if plan.Conflict != "" {
		t.Fatalf("unexpected conflict: %s", plan.Conflict)
	}
	if plan.ExtraFile != nil {
		t.Fatalf("expected no ExtraFile when .env absent, got %+v", plan.ExtraFile)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf("Apply must not create ~/.gemini/.env; stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Dir(envPath)); !os.IsNotExist(err) {
		t.Fatalf("Apply must not create the ~/.gemini directory; stat err=%v", err)
	}
}

// TestGeminiRemoveStripsSettingsAndLegacyEnv: Remove strips keld's telemetry +
// hook from settings.json, and stages a cleanup of any lingering legacy keld
// .env block — preserving security.auth and GEMINI_API_KEY.
func TestGeminiRemoveStripsSettingsAndLegacyEnv(t *testing.T) {
	home := sandboxGeminiHome(t)
	geminiDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	envPath := filepath.Join(geminiDir, ".env")
	if err := os.WriteFile(envPath, []byte(legacyEnvBlock), 0o600); err != nil {
		t.Fatalf("seed .env: %v", err)
	}

	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok"}
	cur := "{\n  \"security\": {\n    \"auth\": {\n      \"selectedType\": \"oauth-personal\"\n    }\n  }\n}\n"

	// Configure settings via Apply, then Remove.
	applyPlan := a.Apply(&cur, p, false)
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

	// .env cleanup staged: legacy block stripped, GEMINI_API_KEY preserved.
	ef := removePlan.ExtraFile
	if ef == nil {
		t.Fatal("expected removePlan.ExtraFile staging the legacy .env cleanup")
	}
	if ef.Delete {
		t.Fatal("ExtraFile.Delete should be false: GEMINI_API_KEY must survive")
	}
	if !strings.Contains(ef.AfterText, "GEMINI_API_KEY=real-secret-value") {
		t.Fatalf("Remove: GEMINI_API_KEY lost:\n%s", ef.AfterText)
	}
	if strings.Contains(ef.AfterText, "keld-managed") || strings.Contains(ef.AfterText, "OTEL_EXPORTER_OTLP_HEADERS") {
		t.Fatalf("Remove: legacy keld block not stripped:\n%s", ef.AfterText)
	}

	st := a.Status(&removePlan.AfterText, nil)
	if st.Configured {
		t.Fatalf("Status after Remove: expected Configured=false, detail=%s", st.Detail)
	}
}

// TestGeminiRemoveDeletesLegacyOnlyEnvFile: a .env that contains ONLY a legacy
// keld block (no other lines) should be deleted, not left as an empty husk.
func TestGeminiRemoveDeletesLegacyOnlyEnvFile(t *testing.T) {
	home := sandboxGeminiHome(t)
	geminiDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	envPath := filepath.Join(geminiDir, ".env")
	const blockOnly = "# >>> keld-managed (do not edit) >>>\n" +
		"OTEL_EXPORTER_OTLP_HEADERS=x-keld-ingest-token=old-token\n" +
		"# <<< keld-managed <<<\n"
	if err := os.WriteFile(envPath, []byte(blockOnly), 0o600); err != nil {
		t.Fatalf("seed .env: %v", err)
	}

	a := &GeminiAdapter{}
	removePlan := a.Remove(strPtrLocal("{}"), map[string]any{})
	ef := removePlan.ExtraFile
	if ef == nil {
		t.Fatal("expected removePlan.ExtraFile")
	}
	if !ef.Delete {
		t.Fatalf("expected ExtraFile.Delete=true for a keld-block-only .env, got AfterText=%q", ef.AfterText)
	}
	if ef.Path != envPath {
		t.Fatalf("ExtraFile.Path = %q, want %q", ef.Path, envPath)
	}

	// Commit and confirm deletion.
	commitExtraFile(t, ef)
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf(".env should be deleted after commit, stat err=%v", err)
	}
}

// TestGeminiApplyIdempotent: applying twice must produce byte-identical
// settings.json output, and (with a clean .env) stage no ExtraFile either time.
func TestGeminiApplyIdempotent(t *testing.T) {
	sandboxGeminiHome(t)

	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok"}
	cur := "{\n  \"security\": {\n    \"auth\": {\n      \"selectedType\": \"oauth-personal\"\n    }\n  }\n}\n"

	first := a.Apply(&cur, p, false)
	if first.Conflict != "" {
		t.Fatalf("first Apply: unexpected conflict: %s", first.Conflict)
	}
	// No .env on disk → no cleanup ExtraFile.
	if first.ExtraFile != nil {
		t.Fatalf("first Apply: expected ExtraFile=nil (no legacy .env), got %+v", first.ExtraFile)
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
	if second.ExtraFile != nil {
		t.Fatalf("second Apply: expected ExtraFile=nil, got %+v", second.ExtraFile)
	}
}

// TestGeminiApplyPinsHookBinary: when SetupParams.BinPath is set, the hook
// command embeds that absolute path so a different keld earlier on PATH can't
// hijack it.
func TestGeminiApplyPinsHookBinary(t *testing.T) {
	sandboxGeminiHome(t)
	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok", BinPath: "/usr/local/keld/keld"}
	plan := a.Apply(nil, p, false)
	if !strings.Contains(plan.AfterText, "/usr/local/keld/keld __hook --source gemini") {
		t.Fatalf("hook command not pinned to abs path:\n%s", plan.AfterText)
	}
}

// strPtrLocal returns a pointer to s (test helper).
func strPtrLocal(s string) *string { return &s }
