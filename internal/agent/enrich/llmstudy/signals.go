package llmstudy

import (
	"regexp"
	"strconv"
	"strings"
)

// Signals are structural properties of a conversational window, computed
// deterministically from the transcript with no model involved.
//
// Why these matter: routing wants to know how hard the work is, and the
// conversation's SHAPE carries that signal more reliably than its text. A user on
// their eighth turn of one problem, correcting the assistant, after fourteen tool
// calls and two failed test runs, is doing hard work — and none of that requires
// inference to observe.
//
// Privacy: every field is a COUNT or a length. No text is retained, so these are
// publishable without a masking gate, unlike labels derived from content.
type Signals struct {
	Turns          int `json:"turns"`           // turns in the window
	UserTurns      int `json:"user_turns"`      // how much the human is steering
	ToolCalls      int `json:"tool_calls"`      // total, expanding collapsed runs
	ToolVariety    int `json:"tool_variety"`    // distinct tools used
	ToolRuns       int `json:"tool_runs"`       // collapsed runs (a run of N counts once)
	CodeBlocks     int `json:"code_blocks"`     // fenced blocks the assistant produced
	CodeLines      int `json:"code_lines"`      // lines inside them
	Corrections    int `json:"corrections"`     // user turns that push back or redirect
	UserChars      int `json:"user_chars"`      // total human text volume
	AssistantChars int `json:"assistant_chars"` // total assistant prose volume
	TargetChars    int `json:"target_chars"`    // the turn being classified
}

// runSuffix parses the "(xN)" marker appendTurn adds when collapsing a tool run.
var runSuffix = regexp.MustCompile(`\(x(\d+)\)$`)

// codeMarker parses the "[code block, N lines]" marker elideCode leaves behind.
var codeMarker = regexp.MustCompile(`\[code block, (\d+) lines\]`)

// correctionMarkers flag a user turn that redirects, rejects, or repairs. A dense
// run of these is the clearest textual sign that the work is not going smoothly —
// and note the phrasing is checked, never stored.
var correctionMarkers = []string{
	"no,", "no.", "nope", "not quite", "not what", "that's wrong", "thats wrong",
	"incorrect", "actually", "instead", "revert", "undo", "roll back", "rollback",
	"you missed", "you forgot", "still broken", "still failing", "doesn't work",
	"does not work", "didn't work", "try again", "wrong", "mistake", "why did you",
}

// hasCorrection reports whether a user turn pushes back. Matching is
// case-insensitive on a lowered copy; the turn text itself is not retained.
func hasCorrection(s string) bool {
	l := strings.ToLower(s)
	for _, m := range correctionMarkers {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

// Extract computes the structural signals for a window.
func Extract(w Window) Signals {
	var s Signals
	tools := map[string]bool{}
	for _, t := range w.Turns {
		s.Turns++
		switch t.Role {
		case RoleUser:
			s.UserTurns++
			s.UserChars += len([]rune(t.Text))
			if hasCorrection(t.Text) {
				s.Corrections++
			}
		case RoleAssistant:
			s.AssistantChars += len([]rune(t.Text))
			for _, m := range codeMarker.FindAllStringSubmatch(t.Text, -1) {
				s.CodeBlocks++
				if n, err := strconv.Atoi(m[1]); err == nil {
					s.CodeLines += n
				}
			}
		case RoleTool:
			s.ToolRuns++
			n := 1
			if m := runSuffix.FindStringSubmatch(t.Text); m != nil {
				if v, err := strconv.Atoi(m[1]); err == nil {
					n = v
				}
			}
			s.ToolCalls += n
			tools[toolName(t.Text)] = true
		}
	}
	s.ToolVariety = len(tools)
	s.TargetChars = len([]rune(w.Target))
	return s
}
