package watch

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// codexSess is the session context (id + cwd) needed to synthesize prompt ids
// for a given Codex rollout file.
type codexSess struct {
	id  string
	cwd string
}

// codexExtractor is the stateful, path-aware promptExtractor for Codex rollout
// JSONL files. Codex's session id and cwd are only present on the file's
// session_meta line (the head); prompts arrive later and carry only a
// file-local ordinal. The extractor caches session context per file so
// prompts seen later in the same scan (or in later polls) can still be
// projected to a stable, globally-unique PromptID ("<sessionID>#<ordinal>").
type codexExtractor struct {
	mu   sync.Mutex
	sess map[string]codexSess
}

func newCodexExtractor() *codexExtractor {
	return &codexExtractor{sess: make(map[string]codexSess)}
}

type codexLine struct {
	Ordinal *uint64         `json:"ordinal"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type codexSessionMetaPayload struct {
	Id  string `json:"id"`
	Cwd string `json:"cwd"`
}

type codexEventMsgPayload struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// extract implements promptExtractor for Codex rollout lines. It never
// returns prompt text — only the ids and cwd needed to synthesize a pointer.
func (c *codexExtractor) extract(path string, line []byte) (promptRec, bool) {
	var ln codexLine
	if err := json.Unmarshal(line, &ln); err != nil {
		return promptRec{}, false
	}

	if ln.Type == "session_meta" {
		var payload codexSessionMetaPayload
		if err := json.Unmarshal(ln.Payload, &payload); err != nil || payload.Id == "" {
			return promptRec{}, false
		}
		c.mu.Lock()
		c.sess[path] = codexSess{id: payload.Id, cwd: payload.Cwd}
		c.mu.Unlock()
		return promptRec{}, false
	}

	if ln.Type != "event_msg" || ln.Ordinal == nil {
		return promptRec{}, false
	}
	var payload codexEventMsgPayload
	if err := json.Unmarshal(ln.Payload, &payload); err != nil {
		return promptRec{}, false
	}
	if payload.Type != "user_message" || payload.Message == "" {
		return promptRec{}, false
	}

	c.mu.Lock()
	s, ok := c.sess[path]
	c.mu.Unlock()
	if !ok {
		var found bool
		s, found = readCodexSessionHead(path)
		if !found {
			return promptRec{}, false
		}
		c.mu.Lock()
		c.sess[path] = s
		c.mu.Unlock()
	}

	return promptRec{
		PromptID:  fmt.Sprintf("%s#%d", s.id, *ln.Ordinal),
		Cwd:       s.cwd,
		SessionID: s.id,
	}, true
}

// readCodexSessionHead scans path from the start looking for the first
// session_meta line, for an incremental scan that started past the file's
// head and so never saw it via extract. Best-effort: any read/parse failure
// just yields "not found" so the caller treats the prompt as unresolvable.
func readCodexSessionHead(path string) (codexSess, bool) {
	f, err := os.Open(path)
	if err != nil {
		return codexSess{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var ln codexLine
		if err := json.Unmarshal(sc.Bytes(), &ln); err != nil {
			continue
		}
		if ln.Type != "session_meta" {
			continue
		}
		var payload codexSessionMetaPayload
		if err := json.Unmarshal(ln.Payload, &payload); err != nil || payload.Id == "" {
			continue
		}
		return codexSess{id: payload.Id, cwd: payload.Cwd}, true
	}
	return codexSess{}, false
}
