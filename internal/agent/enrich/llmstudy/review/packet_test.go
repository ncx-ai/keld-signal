package review

import (
	"strings"
	"testing"
)

func TestAPacketFileParsesBackToItsThreeParts(t *testing.T) {
	// A window that contains its own fence and its own headings: the scorer has to recover the
	// evidence from a file like this to check a quote against it.
	p := Packet{
		ID:     "PKT-DEADBEEF",
		Record: "counts: turns=3 user_turns=1 tool_calls=1 corrections=0\nprojects: alpha",
		Window: "user: see below\n```\nfenced content\n```\n## a heading\nassistant: done",
		Output: "Working on alpha. The fenced example is in place.",
	}
	got, err := ParsePacket(renderPacket(p))
	if err != nil {
		t.Fatalf("ParsePacket: %v", err)
	}
	if got.ID != p.ID {
		t.Errorf("id = %q", got.ID)
	}
	if got.Record != p.Record {
		t.Errorf("record round-trip:\n got %q\nwant %q", got.Record, p.Record)
	}
	if got.Window != p.Window {
		t.Errorf("window round-trip:\n got %q\nwant %q", got.Window, p.Window)
	}
	if got.Output != p.Output {
		t.Errorf("statement round-trip:\n got %q\nwant %q", got.Output, p.Output)
	}
}

func TestRenderedPacketHoldsNothingButTheEvidenceAndTheStatement(t *testing.T) {
	body := renderPacket(Packet{ID: "PKT-1", Record: "counts: turns=1", Window: "user: hello", Output: "Saying hello."})
	for _, want := range []string{"# Review packet PKT-1", "counts: turns=1", "user: hello", "> Saying hello."} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q from:\n%s", want, body)
		}
	}
	// No coordinates, no kind, no beat number. The frame does say "session" and "window" —
	// "one work session", "the conversation window" — which name the KIND of thing being shown
	// and not which one, so they are not provenance and are not listed here.
	for _, forbidden := range []string{"window 0 of", "Beat ", "genuine", "planted", "clean_duplicate", "SUBJECT CHANGED"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("packet contains %q:\n%s", forbidden, body)
		}
	}
}

func TestPacketIDsAreStableAndDoNotOrderByKind(t *testing.T) {
	a := packetID(KindGenuine, "First session", 1, "")
	if a != packetID(KindGenuine, "First session", 1, "") {
		t.Fatal("packet ids are not stable across calls")
	}
	if a == packetID(KindCleanDuplicate, "First session", 1, "dup") {
		t.Fatal("a duplicate collides with its twin")
	}
	if a == packetID(KindGenuine, "First session", 2, "") {
		t.Fatal("two beats of one session collide")
	}
	if !strings.HasPrefix(a, "PKT-") || len(a) != len("PKT-")+8 {
		t.Fatalf("id %q is not the expected shape", a)
	}
}

func TestParsePacketRejectsSomethingThatIsNotAPacket(t *testing.T) {
	for _, bad := range []string{
		"no id line at all\n",
		"# Review packet PKT-1\n\n## The statement under review\n\n> only a statement\n",
		"# Review packet PKT-1\n\n```\nonly one fence\n```\n\n## The statement under review\n\n> x\n",
		"# Review packet PKT-1\n\n```\nunterminated\n",
	} {
		if _, err := ParsePacket(bad); err == nil {
			t.Errorf("parsed a malformed packet: %q", bad)
		}
	}
}
