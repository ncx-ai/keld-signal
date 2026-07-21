package watch

import "testing"

func TestParsePrompt(t *testing.T) {
	cases := []struct {
		name string
		line string
		ok   bool
		id   string
		cwd  string
	}{
		{"string content", `{"type":"user","promptId":"P1","cwd":"/w","sessionId":"S1","message":{"role":"user","content":"hello"}}`, true, "P1", "/w"},
		{"text block", `{"type":"user","promptId":"P2","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`, true, "P2", ""},
		{"tool_result block rejected", `{"type":"user","promptId":"P3","message":{"role":"user","content":[{"type":"tool_result","content":"out"}]}}`, false, "", ""},
		{"toolUseResult record rejected", `{"type":"user","promptId":"P3b","toolUseResult":{"ok":true},"message":{"role":"user","content":"result text"}}`, false, "", ""},
		{"sidechain rejected", `{"type":"user","promptId":"P3c","isSidechain":true,"message":{"role":"user","content":"subagent turn"}}`, false, "", ""},
		{"meta rejected", `{"type":"user","promptId":"P3d","isMeta":true,"message":{"role":"user","content":"injected caveat"}}`, false, "", ""},
		{"assistant rejected", `{"type":"assistant","promptId":"P4","message":{"role":"assistant","content":"x"}}`, false, "", ""},
		{"missing promptId rejected", `{"type":"user","message":{"role":"user","content":"hi"}}`, false, "", ""},
		{"empty string content rejected", `{"type":"user","promptId":"P5","message":{"role":"user","content":""}}`, false, "", ""},
		{"malformed rejected", `{not json`, false, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, ok := parsePrompt([]byte(c.line))
			if ok != c.ok {
				t.Fatalf("ok=%v want %v", ok, c.ok)
			}
			if ok && (rec.PromptID != c.id || rec.Cwd != c.cwd) {
				t.Fatalf("rec=%+v want id=%q cwd=%q", rec, c.id, c.cwd)
			}
		})
	}
}
