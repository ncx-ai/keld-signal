package review

import (
	"strings"
	"testing"
)

// fixtureCorpus is a three-session corpus with the shapes the series mutations need: a session long
// enough to shuffle and to drop a middle from, a second session to contaminate from, and a marked
// subject change in an interior beat.
func fixtureCorpus() Corpus {
	rec := func(turns int, project, subjects string) string {
		return "counts: turns=" + itoa(turns) + " user_turns=1 tool_calls=0 corrections=0\n" +
			"projects: " + project + "\nrecurring subjects: " + subjects
	}
	return Corpus{Sessions: []Session{
		{
			Title: "Alpha session", Domain: "Software",
			Items: []Item{
				{SessionTitle: "Alpha session", SessionDomain: "Software", Ordinal: 1, MarkedSubjectChanged: true,
					Record: rec(1, "alpha", "widgetd"), Window: "user: start on widgetd", Output: "Starting work on widgetd, the intake service."},
				{SessionTitle: "Alpha session", SessionDomain: "Software", Ordinal: 2,
					Record: rec(10, "alpha", "widgetd, parser.go"), Window: "user: parser next", Output: "Rewriting the widgetd parser in parser.go."},
				{SessionTitle: "Alpha session", SessionDomain: "Software", Ordinal: 3, MarkedSubjectChanged: true,
					Record: rec(20, "alpha", "widgetd, cache.go"), Window: "user: cache it", Output: "Turning to the widgetd cache in cache.go."},
				{SessionTitle: "Alpha session", SessionDomain: "Software", Ordinal: 4,
					Record: rec(30, "alpha", "widgetd, cache.go, bench"), Window: "user: bench it", Output: "Benchmarking the widgetd cache against the old path."},
				{SessionTitle: "Alpha session", SessionDomain: "Software", Ordinal: 5,
					Record: rec(40, "alpha", "widgetd, release"), Window: "user: ship", Output: "Preparing the widgetd release notes, still unfinished."},
			},
		},
		{
			Title: "Beta session", Domain: "Accounting",
			Items: []Item{
				{SessionTitle: "Beta session", SessionDomain: "Accounting", Ordinal: 1, MarkedSubjectChanged: true,
					Record: rec(1, "beta", "quarterly"), Window: "user: close the quarter", Output: "Closing the quarter for the Larkspur entity."},
				{SessionTitle: "Beta session", SessionDomain: "Accounting", Ordinal: 2,
					Record: rec(12, "beta", "quarterly, depreciation"), Window: "user: depreciation", Output: "Applying depreciation to the Larkspur fixed assets, then clearing the cache of stale rates."},
				{SessionTitle: "Beta session", SessionDomain: "Accounting", Ordinal: 3,
					Record: rec(18, "beta", "quarterly, ledger"), Window: "user: ledger", Output: "Tying the Larkspur ledger back to the quarterly statements."},
			},
		},
	}}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

func TestTheSeriesRecordIsTheSessionTotalsAndAUnionOfSubjects(t *testing.T) {
	all, err := BuildSeries(fixtureCorpus())
	if err != nil {
		t.Fatalf("BuildSeries: %v", err)
	}
	alpha := all[0]
	block := alpha.Record.Block()
	// The counts are the LAST block's, verbatim: they are cumulative, so the last one is the
	// session's.
	if !strings.Contains(block, "counts: turns=40") {
		t.Errorf("record does not carry the session's final counts:\n%s", block)
	}
	// Exactly ONE counts line. Several would leak the chronology in numbers, which is what the
	// derivation exists to avoid. Counted as whole lines rather than by substring: "turns=1" is
	// also inside "user_turns=1", and a substring check here would be this branch's own recurring
	// defect — a test measuring ordinary text rather than the thing it names.
	if n := countLinesWithPrefix(block, "counts:"); n != 1 {
		t.Errorf("record carries %d counts lines, want 1:\n%s", n, block)
	}
	// Subjects are the union, alphabetised — every term counted anywhere, in an order that says
	// nothing about when.
	for _, want := range []string{"bench", "cache.go", "parser.go", "release", "widgetd"} {
		if !strings.Contains(block, want) {
			t.Errorf("union of recurring subjects is missing %q:\n%s", want, block)
		}
	}
	if got := alpha.Record.Subjects; !sortedFold(got) {
		t.Errorf("subjects are not alphabetised: %v", got)
	}
	if alpha.Record.DerivedFrom != 5 {
		t.Errorf("record derived from %d blocks, want 5", alpha.Record.DerivedFrom)
	}
}

func countLinesWithPrefix(block, prefix string) int {
	n := 0
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			n++
		}
	}
	return n
}

func sortedFold(in []string) bool {
	for i := 1; i < len(in); i++ {
		if strings.ToLower(in[i-1]) > strings.ToLower(in[i]) {
			return false
		}
	}
	return true
}

func TestASeriesPacketParsesBackToItsRecordAndItsBeats(t *testing.T) {
	p := SeriesPacket{
		ID:     "SER-DEADBEEF",
		Record: "counts: turns=3 user_turns=1 tool_calls=1 corrections=0\nprojects: alpha",
		Beats:  []string{"First, the intake service.", "Then the parser, with a ``` fence in it.", "Finally the cache."},
	}
	got, err := ParseSeriesPacket(renderSeriesPacket(p))
	if err != nil {
		t.Fatalf("ParseSeriesPacket: %v", err)
	}
	if got.ID != p.ID {
		t.Errorf("id = %q", got.ID)
	}
	if got.Record != p.Record {
		t.Errorf("record round-trip:\n got %q\nwant %q", got.Record, p.Record)
	}
	if len(got.Beats) != len(p.Beats) {
		t.Fatalf("got %d beats, want %d", len(got.Beats), len(p.Beats))
	}
	for i := range p.Beats {
		if got.Beats[i] != p.Beats[i] {
			t.Errorf("beat %d round-trip:\n got %q\nwant %q", i+1, got.Beats[i], p.Beats[i])
		}
	}
}

func TestASeriesPacketIsNumberedByPositionAndCarriesNoProvenance(t *testing.T) {
	all, _ := BuildSeries(fixtureCorpus())
	// A shuffled series: the real beat 5 is shown third. If anything in the file said "5" for it,
	// the order-shuffle plant would be answerable from the packet.
	m := SeriesMutation{ID: "t1", Class: OrderShuffle, Session: "Alpha session",
		Order: []int{1, 2, 5, 3, 4}, Note: "fixture"}
	p, err := ApplySeries(fixtureCorpus(), all, m)
	if err != nil {
		t.Fatalf("ApplySeries: %v", err)
	}
	body := renderSeriesPacket(seriesPacketOf("SER-1", p.Series))
	for i := 1; i <= len(p.Series.Beats); i++ {
		if !strings.Contains(body, "### Beat "+itoa(i)) {
			t.Errorf("packet is missing positional heading for beat %d", i)
		}
	}
	for _, forbidden := range []string{
		"Alpha session", "Software", "window ", "SUBJECT CHANGED",
		"series_clean", "series_planted", "series_clean_duplicate", "order_shuffle",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("packet contains provenance %q:\n%s", forbidden, body)
		}
	}
	// And no transcript: `followable` asks whether the work can be reconstructed without one.
	if strings.Contains(body, "user: start on widgetd") {
		t.Error("packet contains a conversation window, which the metric forbids")
	}
}

func TestSeriesPacketIDsAreStableAndDoNotOrderByKind(t *testing.T) {
	a := seriesPacketID(KindSeriesClean, "Alpha session", "")
	if a != seriesPacketID(KindSeriesClean, "Alpha session", "") {
		t.Fatal("series packet ids are not stable across calls")
	}
	if a == seriesPacketID(KindSeriesDuplicate, "Alpha session", "dup") {
		t.Fatal("a duplicate collides with its twin")
	}
	if a == seriesPacketID(KindSeriesPlanted, "Alpha session", "s01") {
		t.Fatal("a plant collides with the clean series it was cut from")
	}
	if !strings.HasPrefix(a, "SER-") || len(a) != len("SER-")+8 {
		t.Fatalf("id %q is not the expected shape", a)
	}
}

func TestParseSeriesPacketRejectsSomethingThatIsNotOne(t *testing.T) {
	for _, bad := range []string{
		"no id line at all\n",
		"# Series review packet SER-1\n\n### Beat 1\n\n> only beats, no record\n",
		"# Series review packet SER-1\n\n```\nrecord\n```\n\n### Beat 1\n\n> a single beat is not a timeline\n",
		"# Series review packet SER-1\n\n```\nunterminated\n",
	} {
		if _, err := ParseSeriesPacket(bad); err == nil {
			t.Errorf("parsed a malformed series packet: %q", bad)
		}
	}
}
