package teleproxy

import (
	"os"
	"testing"
)

func TestSessionIDsFromRealPayload(t *testing.T) {
	body, err := os.ReadFile("testdata/claude_code_logs.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got := SessionIDs(body)
	if len(got) != 1 || got[0] != "39b953bc-6e27-45f5-9b80-820a08c984a3" {
		t.Fatalf("SessionIDs = %v, want the one real session id", got)
	}
}

func TestSessionIDsAcceptsTheKeysTheToolsActuallySend(t *testing.T) {
	cases := map[string]string{
		`{"a":[{"key":"session.id","value":{"stringValue":"s1"}}]}`:      "s1",
		`{"a":[{"key":"session_id","value":{"stringValue":"s2"}}]}`:      "s2",
		`{"a":[{"key":"conversation.id","value":{"stringValue":"s3"}}]}`: "s3",
		`{"a":[{"key":"thread_id","value":{"stringValue":"s4"}}]}`:       "s4",
	}
	for body, want := range cases {
		got := SessionIDs([]byte(body))
		if len(got) != 1 || got[0] != want {
			t.Errorf("SessionIDs(%s) = %v, want [%s]", body, got, want)
		}
	}
}

func TestSessionIDsIsBoundedAndIgnoresJunk(t *testing.T) {
	if got := SessionIDs([]byte("not json")); got != nil {
		t.Errorf("unparseable body: got %v, want nil", got)
	}
	// An empty value is not an id.
	if got := SessionIDs([]byte(`{"a":[{"key":"session.id","value":{"stringValue":""}}]}`)); got != nil {
		t.Errorf("empty session id: got %v, want nil", got)
	}
	// A hostile payload cannot make the proxy accumulate without bound.
	big := `{"a":[`
	for i := 0; i < maxSessionsPerBatch*10; i++ {
		if i > 0 {
			big += ","
		}
		big += `{"key":"session.id","value":{"stringValue":"s` + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `"}}`
	}
	big += `]}`
	if got := SessionIDs([]byte(big)); len(got) > maxSessionsPerBatch {
		t.Errorf("got %d ids, want <= %d", len(got), maxSessionsPerBatch)
	}
}
