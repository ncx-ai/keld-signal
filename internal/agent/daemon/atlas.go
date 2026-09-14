package daemon

import (
	"log"

	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/atlas"
)

// atlasClient constructs THE ONE connector this daemon will have, from the
// `send_to_atlas` setting (docs/v3/contracts.md, D4).
//
// ⚠️ **When the toggle is off, the live client is never CONSTRUCTED — not
// merely never called.** atlas.Off is an empty struct with no transport, no
// credential and no address, so a machine that promised local-only cannot phone
// home even through a code path someone forgets to guard. That is the whole
// reason this is a package boundary rather than an `if enabled` at each call
// site: there is exactly one branch, here, and it is covered by a test that
// drives the daemon with a dialer which fails on any connection.
//
// The daemon logs the choice once at startup, because "is this machine sending
// anything to Atlas?" is the first question anyone debugging a quiet fleet asks,
// and until v3 the only way to answer it was to read the config file.
func atlasClient(set settings.Settings, pub *publish.Publisher, sc *settings.Client) atlas.Client {
	if !set.AtlasEnabled() {
		log.Printf("keld-agent: Send to Atlas is OFF (send_to_atlas=false or KELD_ATLAS=0) — " +
			"focus blocks, projects and the ledger stay on this machine; nothing is published")
		return atlas.Off{}
	}
	return &atlas.Live{Blocks: pub, Settings_: sc}
}

// devBlocksMode resolves the developer block granularity and REFUSES it while
// Atlas is on.
//
// ⚠️ A one-minute block is not a fact about anyone's work. The refusal lives in
// settings.DevBlocksMode so no code path can read an unsafe value, and this
// wrapper exists only to say so out loud: a developer who set KELD_DEV_BLOCKS
// and forgot to turn Atlas off would otherwise see production blocks and
// conclude the setting did nothing.
func devBlocksMode(set settings.Settings) string {
	mode, refused := set.DevBlocksMode()
	if refused {
		log.Printf("keld-agent: developer block granularity (%q) IGNORED because Send to Atlas is on — "+
			"turn it off first; dev blocks must never reach the org's numbers", set.DevBlocks)
	}
	if mode != "" {
		log.Printf("keld-agent: DEVELOPER block granularity %q — blocks are not the shipped 20-minute unit "+
			"and are never published", mode)
	}
	return mode
}
