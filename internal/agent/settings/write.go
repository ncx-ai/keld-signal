package settings

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// V3Patch is a partial update to the v3 Settings page's own keys, accepted by
// PUT /v1/settings (docs/v3/contracts.md). Every field is a pointer so an
// absent JSON key leaves the value already on disk untouched — the same
// "unset is distinguishable from false/empty" idiom Settings.SendToAtlas
// already uses, extended to every key this endpoint can write.
type V3Patch struct {
	SendToAtlas    *bool
	DevBlocks      *string
	ShowBreaks     *bool
	WorkstreamsOff *[]string
	Attribution    *bool
}

// WriteV3Settings merges p onto ~/.keld/agent-config.json.
//
// ⚠️ IT MERGES, the same way WriteInstallDefaults does and for the same
// reason: the file belongs to the operator, not to this struct, and may hold
// keys neither models (pii_regions, include_entity_text, ml_backend, ...). It
// is decoded into map[string]json.RawMessage rather than Settings so an
// unmodelled key survives the round trip and a modelled key that wasn't
// touched isn't re-serialised at its zero value.
//
// ⚠️ UNLIKE WriteInstallDefaults, this touches ONLY the keys p sets. A PUT
// that means to flip send_to_atlas alone must not also rewrite
// dev_blocks/show_breaks/workstreams_off/attribution back to whatever the
// file already held — harmless today because every key here already lives in
// the same file, but exactly the mistake that would silently reintroduce a
// stale value the day one of these keys gains an independent writer (a
// second route, a future remote override) that could race this one.
//
// dev_blocks is validated against the closed DevBlocksModes set HERE, at the
// write — mirroring WriteInstallDefaults' ml_backend check — because Load()
// must never reject an operator's file, so this is the one place a typo can
// be refused outright rather than silently read back as "" by DevBlocksMode.
//
// ⚠️ THE send_to_atlas/dev_blocks REFUSAL LIVES ELSEWHERE, DELIBERATELY.
// "a non-empty dev_blocks must never be written while Atlas is effectively
// on" needs env-var resolution (KELD_ATLAS, KELD_DEV_BLOCKS) on top of
// whatever this patch says, which is a question about the EFFECTIVE settings,
// not the file this function writes. That belongs to the caller who already
// has to answer it for the response body — ingress.SettingsRoute, which
// checks Settings.DevBlocksMode() on the merged effective view BEFORE calling
// this function. WriteV3Settings only ever writes what it is told.
func WriteV3Settings(p V3Patch) error {
	if p.DevBlocks != nil && !validDevBlocks(*p.DevBlocks) {
		return fmt.Errorf("unknown dev_blocks %q (want one of %v)", *p.DevBlocks, DevBlocksModes)
	}

	cfg := map[string]json.RawMessage{}
	if data, err := os.ReadFile(paths.AgentConfigPath()); err == nil {
		// A decode failure is deliberately ignored: an install must not be
		// abortable by a corrupt config, same rule WriteInstallDefaults follows.
		_ = json.Unmarshal(data, &cfg)
	}

	set := func(key string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		cfg[key] = b
		return nil
	}

	if p.SendToAtlas != nil {
		if err := set("send_to_atlas", *p.SendToAtlas); err != nil {
			return err
		}
	}
	if p.DevBlocks != nil {
		if err := set("dev_blocks", *p.DevBlocks); err != nil {
			return err
		}
	}
	if p.ShowBreaks != nil {
		if err := set("show_breaks", *p.ShowBreaks); err != nil {
			return err
		}
	}
	if p.WorkstreamsOff != nil {
		v := *p.WorkstreamsOff
		if v == nil {
			v = []string{}
		}
		if err := set("workstreams_off", v); err != nil {
			return err
		}
	}
	if p.Attribution != nil {
		if err := set("attribution", *p.Attribution); err != nil {
			return err
		}
	}

	// Indented + newline-terminated + sorted keys, same as WriteInstallDefaults:
	// this file is read and edited by humans, and MarshalIndent sorting map
	// keys is what makes repeated writes byte-comparable rather than merely
	// equivalent.
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	return writeConfigAtomic(out)
}
