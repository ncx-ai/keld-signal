package daemon

import (
	"github.com/ncx-ai/keld-signal/internal/agent/integrations"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// The integrations package cannot import `tools` — `tools` reads the catalogue
// to know which adapters exist, so the dependency runs the other way. The trust
// reader is therefore a variable there and is bound here, once, at init.
//
// Its default answers (false, false) — *cannot tell* — so before this binding
// exists nobody is told to approve hooks on the strength of an unwritten
// function. Removing this file does not fail a build; it silently returns every
// Codex machine to "cannot tell", which is why the test below exists.
func init() {
	integrations.CodexHooksTrusted = tools.CodexHooksTrusted
}
