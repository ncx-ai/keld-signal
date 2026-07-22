// internal/config/envblock_test.go
package config

import (
	"strings"
	"testing"
)

func TestUpsertEnvBlockPreservesGeminiKey(t *testing.T) {
	current := "GEMINI_API_KEY=sk-abc\n"
	body := "CODEX_API_KEY=codex-xyz\n"
	out := UpsertEnvBlock(current, body)
	if !strings.Contains(out, "GEMINI_API_KEY=sk-abc") {
		t.Fatalf("expected GEMINI_API_KEY preserved, got:\n%s", out)
	}
	if !strings.Contains(out, "# >>> keld-managed (do not edit) >>>") {
		t.Fatalf("expected start marker present, got:\n%s", out)
	}
	if !strings.Contains(out, "# <<< keld-managed <<<") {
		t.Fatalf("expected end marker present, got:\n%s", out)
	}
	if !strings.Contains(out, body) {
		t.Fatalf("expected body in block, got:\n%s", out)
	}
}

func TestUpsertEnvBlockIdempotent(t *testing.T) {
	current := "GEMINI_API_KEY=sk-abc\n"
	body := "CODEX_API_KEY=codex-xyz\n"
	out1 := UpsertEnvBlock(current, body)
	out2 := UpsertEnvBlock(out1, body)
	if out1 != out2 {
		t.Fatalf("expected idempotent behavior:\nfirst:\n%s\nsecond:\n%s", out1, out2)
	}
	if strings.Count(out2, "# >>> keld-managed (do not edit) >>>") != 1 {
		t.Fatalf("expected single block after re-upsert:\n%s", out2)
	}
}

func TestUpsertEnvBlockReplaceBody(t *testing.T) {
	current := "GEMINI_API_KEY=sk-abc\n"
	body1 := "CODEX_API_KEY=codex-xyz\n"
	out1 := UpsertEnvBlock(current, body1)

	body2 := "CODEX_API_KEY=codex-new\n"
	out2 := UpsertEnvBlock(out1, body2)

	if !strings.Contains(out2, "GEMINI_API_KEY=sk-abc") {
		t.Fatalf("expected GEMINI_API_KEY preserved after replace, got:\n%s", out2)
	}
	if !strings.Contains(out2, "CODEX_API_KEY=codex-new") {
		t.Fatalf("expected new body in block, got:\n%s", out2)
	}
	if strings.Contains(out2, "CODEX_API_KEY=codex-xyz") {
		t.Fatalf("expected old body replaced, got:\n%s", out2)
	}
	if strings.Count(out2, "# >>> keld-managed (do not edit) >>>") != 1 {
		t.Fatalf("expected single block after replace:\n%s", out2)
	}
}

func TestRemoveEnvBlockStripsMarkersOnly(t *testing.T) {
	current := "GEMINI_API_KEY=sk-abc\n"
	body := "CODEX_API_KEY=codex-xyz\n"
	withBlock := UpsertEnvBlock(current, body)

	out := RemoveEnvBlock(withBlock)
	if !strings.Contains(out, "GEMINI_API_KEY=sk-abc") {
		t.Fatalf("expected GEMINI_API_KEY preserved after remove, got:\n%s", out)
	}
	if strings.Contains(out, "# >>> keld-managed (do not edit) >>>") {
		t.Fatalf("expected start marker removed, got:\n%s", out)
	}
	if strings.Contains(out, "# <<< keld-managed <<<") {
		t.Fatalf("expected end marker removed, got:\n%s", out)
	}
	if strings.Contains(out, "CODEX_API_KEY=codex-xyz") {
		t.Fatalf("expected block body removed, got:\n%s", out)
	}
}

func TestRemoveEnvBlockWhenAbsent(t *testing.T) {
	current := "GEMINI_API_KEY=sk-abc\n"
	out := RemoveEnvBlock(current)
	if out != current {
		t.Fatalf("expected no-op when block absent; got:\n%s", out)
	}
}

func TestUpsertEnvBlockToEmpty(t *testing.T) {
	body := "CODEX_API_KEY=codex-xyz\n"
	out := UpsertEnvBlock("", body)
	if !strings.Contains(out, "# >>> keld-managed (do not edit) >>>") {
		t.Fatalf("expected start marker in result, got:\n%s", out)
	}
	if !strings.Contains(out, body) {
		t.Fatalf("expected body in result, got:\n%s", out)
	}
	if !strings.Contains(out, "# <<< keld-managed <<<") {
		t.Fatalf("expected end marker in result, got:\n%s", out)
	}
}

func TestHasEnvBlock(t *testing.T) {
	current := "GEMINI_API_KEY=sk-abc\n"
	if HasEnvBlock(current) {
		t.Fatal("expected no block in plain content")
	}
	withBlock := UpsertEnvBlock(current, "CODEX_API_KEY=codex-xyz\n")
	if !HasEnvBlock(withBlock) {
		t.Fatalf("expected block to be detected, got:\n%s", withBlock)
	}
}
