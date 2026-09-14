package daemon

import "testing"

// THE STORY, at the seam that actually broke: a block goes to the block route.
//
// ⚠️ **THE CONNECTOR WAS HANDED THE ENRICHMENT PUBLISHER, AND EVERY REPUBLISHED
// BLOCK WAS REFUSED FOREVER.** `atlas.Live{Blocks: pub}` was given the same
// `pub` the enrichment worker uses, which posts to `/v1/enrichments`. So the
// republish sweep posted BLOCK payloads at the ENRICHMENT route and Atlas
// rejected them — correctly — with 422. Measured on a real machine: 41 captured
// blocks that Atlas accepts on `/v1/signal/blocks` were refused indefinitely on
// the wrong one, and the health strip read "Atlas batch refused" the whole time.
//
// Nothing caught it because the LIVE emitter builds its own publisher on
// signalBlocksEndpoint in blocks.go and worked perfectly. The path people watch
// was fine; only the recovery path was pointed at the wrong door.
//
// This test is deliberately about the two derivations rather than about wiring:
// it fails the moment anyone makes them equal, which is the only way the defect
// can come back.
func TestBlockAndEnrichmentEndpointsAreNotTheSameRoute(t *testing.T) {
	for _, ingest := range []string{
		"http://localhost:8000",
		"http://localhost:8000/",
		"https://atlas.keld.co",
		"https://atlas.keld.co/v1/ingest",
	} {
		blocks := signalBlocksEndpoint(ingest)
		enrich := enrichEndpoint(ingest)

		if blocks == enrich {
			t.Fatalf("for ingest %q both routes resolve to %q — a block posted there is a 422", ingest, blocks)
		}
		if got, want := blocks, "/v1/signal/blocks"; !hasSuffix(got, want) {
			t.Fatalf("signalBlocksEndpoint(%q) = %q, want it to end in %q", ingest, got, want)
		}
		if got, want := enrich, "/v1/enrichments"; !hasSuffix(got, want) {
			t.Fatalf("enrichEndpoint(%q) = %q, want it to end in %q", ingest, got, want)
		}
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
