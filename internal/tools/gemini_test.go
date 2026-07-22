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
	p := SetupParams{Endpoint: "https://e", IngestToken: "tok", Actor: "me"}
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
// Apply must compute a Plan.ExtraFile that upserts the keld OTEL block into
// ~/.gemini/.env while never touching a pre-existing GEMINI_API_KEY line —
// and must NOT write that file itself; only the returned Plan carries the
// change, for the caller to commit under its own confirm/--dry-run gate.
func TestGeminiApplyWritesEnvBlockPreservingAPIKey(t *testing.T) {
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
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok", Actor: "me"}
	cur := "{\n  \"security\": {\n    \"auth\": {\n      \"selectedType\": \"oauth-personal\"\n    }\n  }\n}\n"

	plan := a.Apply(&cur, p, false)
	if plan.Conflict != "" {
		t.Fatalf("unexpected conflict: %s", plan.Conflict)
	}

	// Apply must be side-effect-free: the file on disk is untouched.
	onDisk, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading .env after Apply: %v", err)
	}
	if string(onDisk) != seeded {
		t.Fatalf("Apply wrote to ~/.gemini/.env directly; on-disk contents changed:\nwant: %q\ngot:  %q", seeded, string(onDisk))
	}

	// The change must instead be fully staged in plan.ExtraFile.
	ef := plan.ExtraFile
	if ef == nil {
		t.Fatal("expected plan.ExtraFile to be non-nil")
	}
	if ef.Path != envPath {
		t.Fatalf("ExtraFile.Path = %q, want %q", ef.Path, envPath)
	}
	if ef.Mode != 0o600 {
		t.Fatalf("ExtraFile.Mode = %o, want 0600", ef.Mode)
	}
	if ef.Delete {
		t.Fatal("ExtraFile.Delete should be false when upserting into an existing .env")
	}
	envText := ef.AfterText
	if !strings.Contains(envText, "GEMINI_API_KEY=real-secret-value") {
		t.Fatalf("ExtraFile.AfterText clobbered GEMINI_API_KEY:\n%s", envText)
	}
	if !strings.Contains(envText, "# >>> keld-managed (do not edit) >>>") {
		t.Fatalf("ExtraFile.AfterText missing keld block start marker:\n%s", envText)
	}
	if !strings.Contains(envText, "OTEL_EXPORTER_OTLP_HEADERS=x-keld-ingest-token=tok,x-keld-actor=me") {
		t.Fatalf("ExtraFile.AfterText missing OTEL headers line:\n%s", envText)
	}
	if !strings.Contains(envText, "OTEL_TRACES_EXPORTER=none") {
		t.Fatalf("ExtraFile.AfterText missing trace-off line:\n%s", envText)
	}

	// managed["env_created"] should be false: the file already existed.
	if created, _ := plan.Managed["env_created"].(bool); created {
		t.Fatal("expected managed[\"env_created\"]=false for a pre-existing .env")
	}
}

// TestGeminiApplyDoesNotWriteEnvFile proves Apply performs NO write of its
// own to ~/.gemini/.env even when the file is entirely absent: the returned
// Plan carries the would-be contents in ExtraFile, but the file itself must
// still not exist on disk afterward. Only a caller that later commits
// ExtraFile (as the real setup/uninstall flows do, gated by confirm/
// --dry-run) may bring it into existence.
func TestGeminiApplyDoesNotWriteEnvFile(t *testing.T) {
	home := sandboxGeminiHome(t)
	// Note: no ~/.gemini dir at all yet.
	envPath := filepath.Join(home, ".gemini", ".env")

	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok", Actor: "me"}

	plan := a.Apply(nil, p, false)
	if plan.Conflict != "" {
		t.Fatalf("unexpected conflict: %s", plan.Conflict)
	}

	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf("Apply must not create ~/.gemini/.env itself; stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Dir(envPath)); !os.IsNotExist(err) {
		t.Fatalf("Apply must not even create the ~/.gemini directory itself; stat err=%v", err)
	}

	ef := plan.ExtraFile
	if ef == nil {
		t.Fatal("expected plan.ExtraFile to be non-nil")
	}
	if ef.Path != envPath {
		t.Fatalf("ExtraFile.Path = %q, want %q", ef.Path, envPath)
	}
	if ef.Mode != 0o600 {
		t.Fatalf("ExtraFile.Mode = %o, want 0600", ef.Mode)
	}
	if !strings.Contains(ef.AfterText, "# >>> keld-managed (do not edit) >>>") {
		t.Fatalf("ExtraFile.AfterText missing keld block:\n%s", ef.AfterText)
	}
	if created, _ := plan.Managed["env_created"].(bool); !created {
		t.Fatal("expected managed[\"env_created\"]=true when .env is absent")
	}
}

// TestGeminiRemoveStripsBothArtifacts covers the brief's Remove scenario: a
// settings.json with security.auth plus keld's blocks, and a .env with
// GEMINI_API_KEY plus keld's block — Remove must compute a Plan.ExtraFile
// that strips only keld's parts of each, leaving security.auth and
// GEMINI_API_KEY intact, again without writing anything itself.
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
	// Simulate the caller committing Apply's staged ExtraFile (confirmed,
	// non-dry-run) so Remove has something on disk to read back and strip.
	commitExtraFile(t, applyPlan.ExtraFile)

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

	// Remove must not have written to disk itself: the on-disk .env should
	// still be exactly what Apply's commit left (i.e. unchanged by Remove).
	onDiskAfterRemove, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading .env after Remove: %v", err)
	}
	if !strings.Contains(string(onDiskAfterRemove), "keld-managed") {
		t.Fatalf("Remove must not touch disk itself, but the committed keld block is missing:\n%s", onDiskAfterRemove)
	}

	// The staged ExtraFile is where the strip actually lives.
	ef := removePlan.ExtraFile
	if ef == nil {
		t.Fatal("expected removePlan.ExtraFile to be non-nil")
	}
	if ef.Path != envPath {
		t.Fatalf("ExtraFile.Path = %q, want %q", ef.Path, envPath)
	}
	if ef.Delete {
		t.Fatal("ExtraFile.Delete should be false: the .env pre-existed (GEMINI_API_KEY), so it must survive stripped rather than be deleted")
	}
	if ef.Mode != 0o600 {
		t.Fatalf("ExtraFile.Mode = %o, want 0600", ef.Mode)
	}
	envText := ef.AfterText
	if !strings.Contains(envText, "GEMINI_API_KEY=real-secret-value") {
		t.Fatalf("Remove: GEMINI_API_KEY lost from ExtraFile.AfterText:\n%s", envText)
	}
	if strings.Contains(envText, "keld-managed") {
		t.Fatalf("Remove: keld block not stripped from ExtraFile.AfterText:\n%s", envText)
	}
	if strings.Contains(envText, "OTEL_EXPORTER_OTLP_HEADERS") || strings.Contains(envText, "OTEL_TRACES_EXPORTER") {
		t.Fatalf("Remove: OTEL env lines not stripped from ExtraFile.AfterText:\n%s", envText)
	}

	// Now commit the remove plan too, and confirm final on-disk state and Status.
	commitExtraFile(t, ef)
	finalEnv, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading .env after committing Remove: %v", err)
	}
	if !strings.Contains(string(finalEnv), "GEMINI_API_KEY=real-secret-value") {
		t.Fatalf("Remove: GEMINI_API_KEY lost from .env:\n%s", finalEnv)
	}
	if strings.Contains(string(finalEnv), "keld-managed") {
		t.Fatalf("Remove: keld block not stripped from .env:\n%s", finalEnv)
	}

	st := a.Status(&removePlan.AfterText, nil)
	if st.Configured {
		t.Fatalf("Status after Remove: expected Configured=false, detail=%s", st.Detail)
	}
}

// TestGeminiRemoveDeletesFreshlyCreatedEnvFile covers the "we created the
// .env file, and after removal it's now empty" case: Remove's Plan.ExtraFile
// should carry Delete=true rather than an empty AfterText, so the caller
// removes the file rather than leaving an empty husk behind.
func TestGeminiRemoveDeletesFreshlyCreatedEnvFile(t *testing.T) {
	home := sandboxGeminiHome(t)
	envPath := filepath.Join(home, ".gemini", ".env")

	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok", Actor: "me"}

	applyPlan := a.Apply(nil, p, false)
	if applyPlan.Conflict != "" {
		t.Fatalf("Apply: unexpected conflict: %s", applyPlan.Conflict)
	}
	// Simulate the caller committing Apply's ExtraFile.
	commitExtraFile(t, applyPlan.ExtraFile)
	if _, err := os.Stat(envPath); err != nil {
		t.Fatalf(".env should exist after committing Apply's ExtraFile: %v", err)
	}

	removePlan := a.Remove(&applyPlan.AfterText, applyPlan.Managed)
	if removePlan.Conflict != "" {
		t.Fatalf("Remove: unexpected conflict: %s", removePlan.Conflict)
	}

	ef := removePlan.ExtraFile
	if ef == nil {
		t.Fatal("expected removePlan.ExtraFile to be non-nil")
	}
	if !ef.Delete {
		t.Fatalf("expected ExtraFile.Delete=true for a freshly-created, now-empty .env, got AfterText=%q", ef.AfterText)
	}
	if ef.Path != envPath {
		t.Fatalf("ExtraFile.Path = %q, want %q", ef.Path, envPath)
	}

	// Remove itself must not have deleted the file (no side effects).
	if _, err := os.Stat(envPath); err != nil {
		t.Fatalf("Remove must not delete .env itself before commit: %v", err)
	}

	// Commit the remove plan (as the real caller would) and confirm deletion.
	commitExtraFile(t, ef)
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf(".env should have been deleted after committing Remove's ExtraFile, stat err=%v", err)
	}
}

// TestGeminiApplyIdempotent covers the brief's idempotency requirement:
// applying twice in a row must produce byte-identical settings.json output
// and must not duplicate the .env block.
func TestGeminiApplyIdempotent(t *testing.T) {
	sandboxGeminiHome(t)

	a := &GeminiAdapter{}
	p := SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok", Actor: "me"}
	cur := "{\n  \"security\": {\n    \"auth\": {\n      \"selectedType\": \"oauth-personal\"\n    }\n  }\n}\n"

	first := a.Apply(&cur, p, false)
	if first.Conflict != "" {
		t.Fatalf("first Apply: unexpected conflict: %s", first.Conflict)
	}
	// Simulate the caller committing the first Apply's ExtraFile before the
	// second Apply runs, exactly as a real re-run of `keld setup` would.
	commitExtraFile(t, first.ExtraFile)

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
	// The .env block is already present and unchanged, so the second Apply
	// must report no ExtraFile change to write.
	if second.ExtraFile != nil {
		t.Fatalf("second Apply: expected ExtraFile=nil (no change), got %+v", second.ExtraFile)
	}

	envText := first.ExtraFile.AfterText
	if n := strings.Count(envText, "# >>> keld-managed (do not edit) >>>"); n != 1 {
		t.Fatalf(".env block duplicated: found %d start markers in:\n%s", n, envText)
	}
	if n := strings.Count(envText, "OTEL_TRACES_EXPORTER=none"); n != 1 {
		t.Fatalf(".env OTEL line duplicated: found %d occurrences in:\n%s", n, envText)
	}
}
