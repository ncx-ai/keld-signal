package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/debuglog"
	"github.com/ncx-ai/keld-signal/internal/spool"
)

// forwardToAgent best-effort delivers an enrich pointer to the local daemon:
// the fast path is an HTTP POST; on any miss (no daemon info, transport error,
// or non-2xx) the pointer is spooled to disk so the daemon can drain it later.
// Silent-skip toward the host tool: never returns an error, never blocks it, and
// never records prompt text.
func forwardToAgent(source, sessionID, promptID, transcriptPath, cwd string) {
	if promptID == "" {
		return
	}
	p := spool.Pointer{
		Source:      spool.Source{ID: source, Origin: "hook"},
		Correlation: spool.Correlation{Scheme: "prompt_id", ID: promptID, SessionID: sessionID},
		Pointer:     &spool.Ptr{TranscriptPath: transcriptPath, PromptID: promptID, Cwd: cwd},
	}
	if postToAgent(p) {
		return
	}
	if err := spool.Write(p); err != nil {
		debuglog.Append("forward: spool write failed (prompt_id=%s): %v", promptID, err)
	} else {
		debuglog.Append("forward: daemon unreachable, spooled pointer (prompt_id=%s)", promptID)
	}
}

// postToAgent POSTs the pointer to the daemon. Returns true only on a 2xx.
func postToAgent(p spool.Pointer) bool {
	info, err := agentcfg.Read()
	if err != nil || info == nil || info.Port == 0 {
		return false
	}
	body, err := json.Marshal(p)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	url := fmt.Sprintf("http://127.0.0.1:%d/enrich", info.Port)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-keld-agent-secret", info.Secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		debuglog.Append("forward: POST %s failed (prompt_id=%s): %v", url, p.Correlation.ID, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		debuglog.Append("forward: POST %s returned %d (prompt_id=%s)", url, resp.StatusCode, p.Correlation.ID)
		return false
	}
	return true
}
