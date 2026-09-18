package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/integrations"
)

// A seam that is declared but never bound is the defect this pins: the default
// answers "cannot tell" forever, every Codex row reads approval_required, and
// nothing fails. Assert the binding is live by giving it a config it must read.
func TestCodexTrustReaderIsBoundToTheRealImplementation(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	body := "[[hooks.UserPromptSubmit]]\nhooks = [ { type = \"command\", command = 'keld __hook --source codex' } ]\n\n" +
		"[hooks.state.\"" + cfg + ":user_prompt_submit:0:0\"]\nenabled = true\ntrusted_hash = \"sha256:deadbeef\"\n" +
		"[hooks.state.\"" + cfg + ":stop:0:0\"]\nenabled = true\ntrusted_hash = \"sha256:deadbeef\"\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, known := integrations.CodexHooksTrusted(raw, "keld __hook")
	if !known {
		t.Fatal("CodexHooksTrusted still answers 'cannot tell' on a config that carries hooks.state; the seam is not bound")
	}
}
