package watch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"

	"github.com/ncx-ai/keld-signal/internal/debuglog"
)

// codexSess is the session context (id + cwd) read from a rollout's
// session_meta head line.
type codexSess struct {
	id  string
	cwd string
}

// codexTurn is the context of the turn currently being written: the id Codex
// itself uses for the human turn, and the cwd that turn ran in. A rollout
// writes one `turn_context` line per turn, immediately before the turn's
// prompt.
type codexTurn struct {
	id  string
	cwd string
}

// codexExtractor is the stateful, path-aware promptExtractor for Codex rollout
// JSONL files.
//
// ⚠️ IT USED TO KEY ON A FILE-LOCAL `ordinal` AND CAPTURED NOTHING, EVER.
// Measured over the captured fixtures in testdata/codex: 0.125.0 and 0.153.4
// write NO `ordinal` on any line, and 0.148–0.151 writes one on EVERY line
// including its human turns — so the id was unresolvable on two releases and
// meaningless on the third, and `keld signal doctor` had nothing to notice.
//
// The identity is `<session_meta.id>#<turn_id>`, which is the SAME id
// `keld __hook` builds from the UserPromptSubmit payload's `session_id` and
// `turn_id`. That is what lets the queue dedup the hook↔watcher overlap for
// Codex exactly as it does for Claude Code, rather than publishing each prompt
// twice under two different names.
//
// Two human-turn shapes are accepted because Codex writes two:
//   - `event_msg` / `user_message` — 0.125 and 0.153.4
//   - `event_msg` / `item_completed` with `item.type == "UserMessage"` —
//     0.148–0.151 writes ONLY this one (measured: 47 items, 0 user_message
//     lines in 1,920), so reading just the classic shape captures nothing at
//     all on those machines.
type codexExtractor struct {
	mu   sync.Mutex
	sess map[string]codexSess
	turn map[string]codexTurn

	// fallbacks counts prompts named by their own timestamp because no
	// turn_context preceded them. Measured on the reference corpus at 2 of
	// 1,848 turns on the newest rollouts and 320 of 4,076 (7.9%) across all
	// 287, which backfill reads — so it is a real rate, not a curiosity, and a
	// silent one would be indistinguishable from the ordinal bug returning.
	fallbacks atomic.Int64
}

func newCodexExtractor() *codexExtractor {
	return &codexExtractor{sess: make(map[string]codexSess), turn: make(map[string]codexTurn)}
}

// TurnIDFallbacks reports how many Codex prompts have been named by their own
// timestamp rather than a turn id.
func (c *codexExtractor) TurnIDFallbacks() int64 { return c.fallbacks.Load() }

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexSessionMetaPayload struct {
	Id  string `json:"id"`
	Cwd string `json:"cwd"`
}

// codexTurnContextPayload is the `turn_context` line. `turn_id` is absent on
// the 0.36-era rollouts (1,223 of 5,419 measured), which is what the timestamp
// fallback is for.
type codexTurnContextPayload struct {
	TurnID string `json:"turn_id"`
	Cwd    string `json:"cwd"`
}

// codexEventMsgPayload covers both human-turn shapes. `turn_id` is present on
// the item-model payload and absent on the classic one; `item` only on the
// former.
type codexEventMsgPayload struct {
	Type    string            `json:"type"`
	Message string            `json:"message"`
	TurnID  string            `json:"turn_id"`
	Item    *codexItemPayload `json:"item"`
}

type codexItemPayload struct {
	Type    string             `json:"type"`
	Content []codexItemContent `json:"content"`
}

type codexItemContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// codexHumanTurn reports whether an event_msg payload is a human turn, in
// either shape, and returns the turn id the line carries itself (empty for the
// classic shape, which carries none).
func codexHumanTurn(p codexEventMsgPayload) (turnID string, ok bool) {
	switch p.Type {
	case "user_message":
		return p.TurnID, p.Message != ""
	case "item_completed":
		if p.Item == nil || p.Item.Type != "UserMessage" {
			return "", false
		}
		for _, c := range p.Item.Content {
			if c.Text != "" {
				return p.TurnID, true
			}
		}
		return "", false
	}
	return "", false
}

// extract implements promptExtractor for Codex rollout lines. It never
// returns prompt text — only the ids and cwd needed to synthesize a pointer.
func (c *codexExtractor) extract(path string, line []byte) (promptRec, bool) {
	var ln codexLine
	if err := json.Unmarshal(line, &ln); err != nil {
		return promptRec{}, false
	}

	switch ln.Type {
	case "session_meta":
		var payload codexSessionMetaPayload
		if err := json.Unmarshal(ln.Payload, &payload); err != nil || payload.Id == "" {
			return promptRec{}, false
		}
		c.mu.Lock()
		c.sess[path] = codexSess{id: payload.Id, cwd: payload.Cwd}
		c.mu.Unlock()
		return promptRec{}, false

	case "turn_context":
		var payload codexTurnContextPayload
		if err := json.Unmarshal(ln.Payload, &payload); err != nil {
			return promptRec{}, false
		}
		c.mu.Lock()
		c.turn[path] = codexTurn{id: payload.TurnID, cwd: payload.Cwd}
		c.mu.Unlock()
		return promptRec{}, false

	case "event_msg":
		// handled below
	default:
		return promptRec{}, false
	}

	var payload codexEventMsgPayload
	if err := json.Unmarshal(ln.Payload, &payload); err != nil {
		return promptRec{}, false
	}
	lineTurnID, isHuman := codexHumanTurn(payload)
	if !isHuman {
		return promptRec{}, false
	}

	c.mu.Lock()
	s, haveSess := c.sess[path]
	turn := c.turn[path]
	c.mu.Unlock()

	if !haveSess {
		var found bool
		s, turn, found = readCodexHeadState(path, line)
		if !found {
			return promptRec{}, false
		}
		c.mu.Lock()
		c.sess[path] = s
		if turn.id != "" {
			c.turn[path] = turn
		}
		c.mu.Unlock()
	}

	// The line's own turn id wins where it has one (the item shape carries it
	// and it is measured identical to the preceding turn_context's on every
	// turn of the 0.151 fixture); otherwise the pending turn_context supplies
	// it. With neither, the prompt is named by its own instant rather than
	// dropped — a prompt with an awkward id is still a prompt.
	turnKey := lineTurnID
	if turnKey == "" {
		turnKey = turn.id
	}
	if turnKey == "" {
		turnKey = ln.Timestamp
		if turnKey == "" {
			return promptRec{}, false
		}
		n := c.fallbacks.Add(1)
		debuglog.Append("watch/codex: no turn_context for a prompt in %s; naming it by timestamp (%d so far)", path, n)
	}

	cwd := turn.cwd
	if cwd == "" {
		cwd = s.cwd
	}

	return promptRec{
		PromptID:  s.id + "#" + turnKey,
		Cwd:       cwd,
		SessionID: s.id,
	}, true
}

// readCodexHeadState scans path from the start for the session_meta line and
// for the last turn_context BEFORE the given line, for an incremental scan
// that started past the file's head and so never saw either through extract.
//
// The turn_context half matters: without it the first prompt after a daemon
// restart mid-session would fall back to a timestamp id while the hook named
// the same prompt by its turn id, and the queue would publish it twice under
// two names. Best-effort — any read or parse failure yields "not found" and
// the caller treats the prompt as unresolvable.
func readCodexHeadState(path string, target []byte) (codexSess, codexTurn, bool) {
	f, err := os.Open(path)
	if err != nil {
		return codexSess{}, codexTurn{}, false
	}
	defer f.Close()

	want := bytes.TrimRight(target, "\r\n")
	var sess codexSess
	var turn codexTurn
	var found bool

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		raw := bytes.TrimRight(sc.Bytes(), "\r\n")
		if len(want) > 0 && bytes.Equal(raw, want) {
			// Stop at the prompt itself: a later turn_context describes a
			// later turn and would misname this one.
			break
		}
		var ln codexLine
		if err := json.Unmarshal(raw, &ln); err != nil {
			continue
		}
		switch ln.Type {
		case "session_meta":
			var payload codexSessionMetaPayload
			if err := json.Unmarshal(ln.Payload, &payload); err == nil && payload.Id != "" {
				sess = codexSess{id: payload.Id, cwd: payload.Cwd}
				found = true
			}
		case "turn_context":
			var payload codexTurnContextPayload
			if err := json.Unmarshal(ln.Payload, &payload); err == nil {
				turn = codexTurn{id: payload.TurnID, cwd: payload.Cwd}
			}
		}
	}
	return sess, turn, found
}
