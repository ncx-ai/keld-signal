package llmstudy

import (
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

func TestSessionSchemaEnumsMatchVocabularyAndBoundsTopics(t *testing.T) {
	s := SessionSchema()
	props := s["properties"].(map[string]any)

	sess := props["session"].(map[string]any)
	sp := sess["properties"].(map[string]any)
	dom := sp["domain"].(map[string]any)["enum"].([]string)
	if len(dom) != len(enrich.DomainDefs) {
		t.Fatalf("session domain enum has %d, vocabulary has %d", len(dom), len(enrich.DomainDefs))
	}
	if sess["additionalProperties"] != false {
		t.Error("session object must be strict")
	}
	topics := props["topics"].(map[string]any)
	if topics["maxItems"] != maxTopics {
		t.Errorf("topics maxItems = %v, want %d", topics["maxItems"], maxTopics)
	}
}

func TestSessionPromptShowsBothViewsAndForbidsParaphrase(t *testing.T) {
	w := mineFixture(t, 8)[1]
	p := SessionPrompt(w)
	if !strings.Contains(p, Render(w)) {
		t.Error("prompt omits the recent window")
	}
	if !strings.Contains(p, "SESSION SO FAR") {
		t.Error("prompt omits the session digest heading")
	}
	if !strings.Contains(p, "VERBATIM") {
		t.Error("prompt must forbid paraphrase, since paraphrases are discarded")
	}
}

func sessionReply(topics, summary string) string {
	return chatReply(`{"session":{"domain":"software","function_guess":"eng","activity_type":"generate"},` +
		`"topics":` + topics + `,"summary":"` + summary + `"}`)
}

func TestClassifySessionVerifiesTopicsAgainstBothViews(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep past millisecond granularity: a loopback handler can answer in
		// under 1ms, which truncates LatencyMS to 0 and makes the assertion below
		// flaky rather than wrong.
		time.Sleep(2 * time.Millisecond)
		// "settings poll" is in the fixture; "blockchain sharding" is not.
		io.WriteString(w, sessionReply(`["settings poll","blockchain sharding"]`, "wiring retry into the poll"))
	}))
	defer srv.Close()

	got := NewLlama(srv.URL).ClassifySession(mineFixture(t, 8)[1])
	if !got.Valid {
		t.Fatalf("Valid=false, Err=%q", got.Err)
	}
	if len(got.Topics) != 1 || !strings.EqualFold(got.Topics[0], "settings poll") {
		t.Errorf("Topics = %v, want only the verified term", got.Topics)
	}
	if len(got.Dropped) != 1 || got.Dropped[0] != "blockchain sharding" {
		t.Errorf("Dropped = %v, want the unverifiable term", got.Dropped)
	}
	if len(got.RawTopics) != 2 {
		t.Errorf("RawTopics = %v, want both as emitted (needed for pass rate)", got.RawTopics)
	}
	for _, f := range sessionFacets {
		if got.Session[f] == "" {
			t.Errorf("session facet %s not populated", f)
		}
	}
	if got.Summary == "" {
		t.Error("summary not captured")
	}
	if got.LatencyMS <= 0 {
		t.Errorf("LatencyMS = %d, want > 0", got.LatencyMS)
	}
}

func TestClassifySessionRejectsOffVocabularySessionLabel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(`{"session":{"domain":"astrology","function_guess":"eng","activity_type":"generate"},"topics":[],"summary":"x"}`))
	}))
	defer srv.Close()

	got := NewLlama(srv.URL).ClassifySession(mineFixture(t, 8)[1])
	if got.Valid {
		t.Fatal("an off-vocabulary session label must invalidate the answer")
	}
	if !strings.Contains(got.Err, "astrology") {
		t.Errorf("Err should name the offending label, got %q", got.Err)
	}
}

func TestTopicPassRate(t *testing.T) {
	r := SessionRun{Answers: []SessionAnswer{
		{Valid: true, RawTopics: []string{"a", "b", "c"}, Topics: []string{"a", "b"}},
		{Valid: true, RawTopics: []string{"d"}, Topics: []string{"d"}},
		{Valid: false, RawTopics: []string{"x", "y", "z"}}, // must not count
	}}
	if got := TopicPassRate(r); math.Abs(got-0.75) > 1e-9 {
		t.Fatalf("TopicPassRate = %v, want 0.75 (3 of 4 across valid answers)", got)
	}
}

func TestTopicPassRateEmptyIsZeroNotNaN(t *testing.T) {
	if got := TopicPassRate(SessionRun{}); got != 0 {
		t.Fatalf("TopicPassRate = %v, want 0", got)
	}
}

// The session digest must lead with the session's opening prompt and exclude tool
// lines, which are noise at session scale.
func TestSessionDigestOpensWithFirstPromptAndSkipsTools(t *testing.T) {
	w := mineFixture(t, 2)[1] // K=2 so the digest covers turns the window drops
	if len(w.Digest) == 0 {
		t.Fatal("digest is empty")
	}
	if w.Digest[0].Role != RoleUser || !strings.HasPrefix(w.Digest[0].Text, "add retry to the settings poll") {
		t.Errorf("digest must open with the session's first user prompt, got %+v", w.Digest[0])
	}
	for _, d := range w.Digest {
		if d.Role == RoleTool {
			t.Errorf("digest must exclude tool lines, got %+v", d)
		}
	}
	if len(w.Digest) > digestTurns {
		t.Errorf("digest has %d turns, cap is %d", len(w.Digest), digestTurns)
	}
}

// The digest is a session-scale summary, so its per-turn budget is much tighter
// than the recent window's.
func TestSessionDigestClipsHarderThanRecentWindow(t *testing.T) {
	if digestClip >= DefaultMineOpts().PerTurnChars {
		t.Fatalf("digestClip %d must be tighter than PerTurnChars %d",
			digestClip, DefaultMineOpts().PerTurnChars)
	}
	for _, w := range mineFixture(t, 8) {
		for _, d := range w.Digest {
			if len([]rune(d.Text)) > digestClip {
				t.Errorf("digest turn exceeds clip: %d runes", len([]rune(d.Text)))
			}
		}
	}
}
