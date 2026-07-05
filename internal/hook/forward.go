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
)

// forwardToAgent best-effort POSTs an enrich pointer to the local daemon. It is
// silent-skip toward the host tool: it never returns an error and never blocks
// it. POST transport errors / non-2xx responses are recorded in the finite-size
// debug log (endpoint + status + prompt_id only — never prompt text).
func forwardToAgent(source, sessionID, promptID, transcriptPath, cwd string) {
	info, err := agentcfg.Read()
	if err != nil || info == nil || info.Port == 0 || promptID == "" {
		return
	}
	payload := map[string]any{
		"source":      map[string]string{"id": source, "origin": "hook"},
		"correlation": map[string]string{"scheme": "prompt_id", "id": promptID, "session_id": sessionID},
		"pointer":     map[string]string{"transcript_path": transcriptPath, "prompt_id": promptID, "cwd": cwd},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	url := fmt.Sprintf("http://127.0.0.1:%d/enrich", info.Port)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-keld-agent-secret", info.Secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		debuglog.Append("forward: POST %s failed (prompt_id=%s): %v", url, promptID, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		debuglog.Append("forward: POST %s returned %d (prompt_id=%s)", url, resp.StatusCode, promptID)
	}
}
