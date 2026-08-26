package publish

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The window analysis returns more than the seven dimensions that publish:
// inventory.named_terms (proper nouns lifted from message TEXT — real person
// names have been observed in it) and the window's own start/end timestamps.
// None of that may cross to Atlas.
//
// Today the guarantee is STRUCTURAL: sidecar.AnalyzeResult does not model
// inventory at all, and it drops session/window_start/window_end on the way to
// enrich.Labeled, so publish.Enrichment has nowhere to put them. Structural is
// the right mechanism — a comment saying "don't forward this" is not a
// mechanism. But nothing FAILS if a later change gives Enrichment (or anything
// it embeds) somewhere to put them, and a privacy regression that announces
// itself only in a code review is one that ships.
//
// So: fill every field of the wire struct with non-zero data — reflectively, so
// a field added tomorrow is populated too and `omitempty` cannot hide it —
// marshal it, and assert the forbidden keys are absent from the bytes.
//
// EXTENDED for the dynamics block. /analyze's dynamics carry, per dimension, a
// `slice` and a `baseline` object whose `value` is the reference level ITSELF —
// on `term` (the one level read from message text) that slot is a name someone
// spoke — plus the comparison's three timestamps and the sizer's detail. What
// publishes is the derived six: status, reading, and the three shares plus
// `changed`. Everything else in the block is on this list, so giving Enrichment
// somewhere to put one FAILS here rather than in a review.
//
// "slice" and "baseline" are listed as whole quoted keys, which is why
// `"slice_start"` does not satisfy `"slice"`.
//
// EXTENDED AGAIN for the effort block. Two of six measured transcript signals
// survived and publish (authored_bytes/authoring_turns, fast_share/gaps/tempo);
// the other four are on this list, and so are the raw payload keys the diff
// magnitude is derived from:
//
//   - old_string / new_string / content / new_source are FILE CONTENTS. The one
//     function permitted to read them returns an int (sidecar/app/analysis/
//     magnitude.py's edit_bytes), so what crosses into Go is a length — but the
//     rule this list enforces is that no wire field can hold the string even if
//     that ever changed.
//   - token_weight / tokens were REFUTED (0.89% of dominant values flip) and are
//     still computed and stored on-device for the weighted rollup, which makes
//     "not published" the only thing keeping them off the wire. Likewise
//     out_bytes / out_lines (output volume, +0.552 against log volume — a
//     restated tool-call count), error_rate / n_errors / n_thrash / max_err_run
//     (a window statistic and a 4.8%-prevalence alert).
//
// ⚠️ `request_tokens` CAME OFF this list, the same way `named_terms` did below —
// it is a DIFFERENT computation from the REFUTED "token weight" candidate that
// key spelling was tested against here (0.89% dominant-value flips): the window's
// spend, priced into input-token equivalents (Effort.RequestTokens), plus
// gap_p50_s/gap_p90_s, the same inter-turn gap population fast_share already
// summarises as a share, read instead as a distribution. All three are asserted
// PRESENT below rather than forbidden. `tokens` (unpriced, per-line, over-counts
// a request) stays on the list — reviving one spelling is not licence for its
// sibling.
//
// EXTENDED AGAIN for the physical-acts inventory, and this is the case that tests
// the LIST itself rather than the payload. `physical_acts` publishes — it is the
// one key of /analyze's `inventory` block whose values come from tool names and
// shell argv against a closed 22-value table, never from message text. Its five
// siblings do not, and `inventory` and `named_terms` STAY on the list below
// precisely so that adding a publishable inventory key cannot be mistaken for
// permission to forward the block: a field named `inventory`, or one named
// `named_terms`, still fails here. What makes that a real guard rather than a
// coincidence of naming is that `physical_acts` is a TOP-LEVEL key on Enrichment
// (a sibling of `workstreams`/`dynamics`/`effort`), never nested under an
// `inventory` object — so nothing had to be removed from this list to let it
// through, and the presence check below proves the filler reaches it.
//
// EXTENDED AGAIN for the file-path inventories (`files`/`directories`/
// `components`) and `inventory_omitted`. The first three publish for the same
// structural reason `physical_acts` does — three more TOP-LEVEL keys, never
// nested under `inventory` — except their vocabulary is OPEN rather than
// closed: a file path is not a member of a lookup table, so what stands in for
// the vocabulary gate is the measured, and separately re-checked, invariant
// that `reconcile()` only ever hands back a workspace-relative value (see
// enrich.PathCount). `inventory_omitted` publishes too, but it can never carry
// a value — only a per-dimension COUNT of what a cut removed — so it needs no
// entry on the forbidden list below to stay safe; its presence is asserted the
// same way for the same reason: to prove the filler actually reaches it.
//
// EXTENDED AGAIN for the four IDENTIFIER-shaped inventories (`harness_tools`/
// `programs`/`external_systems`/`integrations`). Same structural reason as the
// file-path inventories — four more TOP-LEVEL keys, never nested under
// `inventory` — and the same OPEN vocabulary: a tool name, a program, a
// hostname or an MCP tool id is not a member of a lookup table either. What
// stands in for the vocabulary gate is a per-dimension structural rule
// (identifier shape for harness_tools/integrations, identifier shape plus a
// path-separator/leading-dot rejection for programs, a bare-IP-literal
// rejection for external_systems — see enrich.NameCount and
// sidecar.convertIdentifierInventory/convertProgramInventory/
// convertExternalSystemInventory). `inventory` STAYS on the list below: adding
// a publishable inventory key must never be mistaken for permission to forward
// the block itself.
//
// ⚠️ `named_terms` CAME OFF this list — one of two entries ever to do so; see
// `request_tokens` above for the other. Every earlier extension above added
// publishable keys while leaving the forbidden list untouched — that was the
// point, and it is why the list was a real guard rather than a naming
// coincidence. This one is different: the repo
// owner decided that named_terms should publish, knowing it is the sole
// inventory drawn from message TEXT and that it has been observed to carry real
// person names. It is now a TOP-LEVEL key like its eight siblings, gated on
// shape alone (sidecar.convertNamedTerms), with no person-name filter, because
// spaCy's ~1% measured precision on this corpus means a filter would create
// false assurance rather than remove names.
//
// `inventory` remaining forbidden still does real work: it keeps the BLOCK
// unforwardable even though all thirteen of its keys are now individually
// publishable, so a future change that forwards the whole object wholesale —
// picking up whatever fourteenth key the sidecar adds next — still fails here.
//
// ⚠️ `evidence` and `status` are INTENDED on the wire and were never on this
// list; nothing came off it for them. They are the workstream dimensions'
// observation count and attribution outcome, added at enrich.SchemaVersion 21
// so a sub-floor dimension can publish LABELLED instead of being deleted (924
// of 12,016 measured dimension-slots held real evidence and published nothing).
// Both are counts-and-closed-vocabulary, the same class as `physical_acts`'
// `n`. What stays forbidden is what the analysis knows and Atlas must not:
// `provenance` (which the sidecar computes per dimension and the Go side
// deliberately drops — the pass is attributed through `extractor_versions`, and
// a second unparsed attribution channel is what the field was) and `reason`
// (the dynamics per-side key, which sits beside a per-side `value` naming a
// reference level; the workstream outcome is `status` precisely so the two
// spellings cannot be confused, the same rule the session prior already
// follows).
//
// A field added for any of them fails HERE rather than in a review.
var forbiddenWireKeys = []string{
	"inventory", "window_start", "window_end",
	"slice", "baseline", "slice_start", "slice_end", "baseline_start",
	"slice_minutes", "baseline_minutes", "sizer", "sizer_detail",
	"reconcile_scope", "emerged", "decayed", "provenance", "reason",
	"old_string", "new_string", "new_source", "content", "edit_preview",
	"token_weight", "tokens",
	"out_bytes", "out_lines", "error_rate", "n_errors", "n_thrash", "max_err_run",
}

func TestEnrichmentWireShapeCannotCarryAnalysisInternals(t *testing.T) {
	var e Enrichment
	fillNonZero(reflect.ValueOf(&e).Elem())

	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)

	// Guard the guard: if the filler silently stopped populating things, every
	// assertion below would pass vacuously.
	for _, present := range []string{`"workstreams"`, `"sensitivity_spans"`, `"custom"`, `"task_type"`,
		`"dynamics"`, `"reading"`, `"turnover"`,
		// The effort block and both of its halves, so the absence checks below
		// cannot pass merely because the filler stopped reaching it.
		`"effort"`, `"authored_bytes"`, `"authoring_turns"`, `"authored_status"`,
		`"fast_share"`, `"gaps"`, `"tempo"`, `"tempo_status"`,
		`"request_tokens"`, `"gap_p50_s"`, `"gap_p90_s"`,
		// The acts inventory and both fields of an entry, so "inventory" and
		// "named_terms" staying forbidden below is a real result about a payload
		// that DOES carry an inventory key, not a vacuous pass over one that
		// carries none.
		`"physical_acts"`, `"n":1`,
		// The three file-path inventories and the cut-visibility map beside them,
		// so a payload that carries none of them could not make the checks below
		// pass vacuously.
		`"files"`, `"directories"`, `"components"`, `"inventory_omitted"`,
		// The four identifier-shaped inventories, so a payload that carries none
		// of them could not make the checks below pass vacuously either.
		`"harness_tools"`, `"programs"`, `"external_systems"`, `"integrations"`,
		// And the last four, over the levels the analysis had always extracted and
		// never published. All thirteen inventory keys are now individually
		// publishable, which is exactly why `"inventory"` itself stays forbidden
		// below: the BLOCK must remain unforwardable so a FOURTEENTH key cannot
		// ride along wholesale.
		`"file_types"`, `"shell_verbs"`, `"subagents"`, `"mcp_servers"`,
		// The session prior and all three contrast measures, so `"reason"`
		// staying forbidden below is a real result about a payload that DOES
		// carry a prior block. The prior states its attribution outcome as
		// `status`, never `reason` — a second meaning for a key already used by
		// the dynamics per-side objects (which do not publish at all) is a
		// reader's error waiting to happen, and this list is what enforces it.
		`"prior"`, `"agrees"`, `"departure"`, `"novel"`, `"status"`,
		// The workstream dimensions' own two published fields, so `"provenance"`
		// and `"reason"` staying forbidden below is a real result about a payload
		// whose workstreams DO carry an evidence count and an attribution
		// outcome, not a vacuous pass over one that carries neither.
		`"evidence"`} {
		if !strings.Contains(got, present) {
			t.Fatalf("filler did not populate %s; the absence checks below would be vacuous:\n%s",
				present, got)
		}
	}

	for _, k := range forbiddenWireKeys {
		if strings.Contains(got, `"`+k+`"`) {
			t.Errorf("published enrichment carries %q — analysis internals must stay on-device "+
				"(named terms can be real person names; window bounds are local metadata):\n%s",
				k, got)
		}
	}
	// Guard the guard, the other way round: the entries whose REMOVAL would be the
	// regression must still be on the list. Adding a publishable key must not have
	// widened what the list permits, and deleting an entry to make a future failure
	// go away is exactly the change this catches.
	//
	// `inventory` is here because all thirteen of its keys now publish individually
	// and the BLOCK must still not; `tokens` and `token_weight` are here because
	// `request_tokens` came OFF the list (see the note above) and the prose beside
	// it — "reviving one spelling is not licence for its sibling" — was the only
	// thing holding them. This file's own standard is that a change like that fails
	// HERE rather than in a review, so the two refuted spellings are now held by
	// the same mechanism `inventory` is.
	for _, required := range []string{"inventory", "tokens", "token_weight"} {
		if !slices.Contains(forbiddenWireKeys, required) {
			t.Errorf("%q was removed from forbiddenWireKeys: `physical_acts` publishes "+
				"because its provenance is a closed table, and `request_tokens` publishes "+
				"because it is a different computation — neither is licence to forward "+
				"the rest", required)
		}
	}
}

// fillNonZero recursively sets every field to a non-zero value, so `omitempty`
// cannot keep a newly-added field out of the marshalled bytes. Depth-bounded
// because a self-referential type would otherwise recurse forever; nothing in
// the wire shape is anywhere near it.
func fillNonZero(v reflect.Value) { fill(v, 0) }

func fill(v reflect.Value, depth int) {
	if depth > 6 || !v.CanSet() {
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	case reflect.Ptr:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), depth+1)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			fill(v.Field(i), depth+1)
		}
	case reflect.Slice:
		el := reflect.New(v.Type().Elem()).Elem()
		fill(el, depth+1)
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), el))
	case reflect.Map:
		k := reflect.New(v.Type().Key()).Elem()
		fill(k, depth+1)
		val := reflect.New(v.Type().Elem()).Elem()
		fill(val, depth+1)
		m := reflect.MakeMap(v.Type())
		m.SetMapIndex(k, val)
		v.Set(m)
	case reflect.Interface:
		v.Set(reflect.ValueOf("x"))
	}
}
