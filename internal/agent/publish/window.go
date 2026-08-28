package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/retry"
)

// WindowEnrichment is the wire shape of a TICK-emitted characterisation: the
// hour of work that no prompt's look-back reaches, published on its own row.
//
// IT IS ITS OWN STRUCT, NOT AN Enrichment WITH MOST FIELDS ZERO, and that is the
// whole reason it exists. Enrichment declares task_type/domain/sensitivity/
// activity_type/personal/function_guess/subcategory WITHOUT omitempty, and they
// are structs, so a zero value serialises as `{"value":"","confidence":0}` — not
// as an absence. Atlas's EnrichmentIn parses that into a LabeledIn and stores
// `task_type = ”`, so every tick row would arrive claiming a sensitivity
// classification of the empty string. A tick reads no prompt text and computes
// none of those facets; "nobody looked" and "we looked and found nothing" are
// different facts and this repo's whole discipline is not letting them render
// alike. A separate struct makes the first one unrepresentable-as-the-second
// rather than merely discouraged.
//
// What it DOES carry is the blocks a prompt's window carries, converted by
// exactly the same functions under exactly the same vocabulary/structural gates
// (see sidecar.TickCharacterised), plus the bounds that say where it applies. No new
// content channel: TestEnrichmentWireShapeCannotCarryAnalysisInternals' rule
// holds here for the same structural reason it holds there — there is no field a
// reference-level value or a transcript fragment could occupy.
type WindowEnrichment struct {
	Source      Source      `json:"source"`
	Correlation Correlation `json:"correlation"`
	Actor       string      `json:"actor,omitempty"`
	// Window is where this row's characterisation applies. Mandatory: a prompt
	// row is located by its prompt and this one has no prompt, so without the
	// bounds the row says nothing about anything.
	Window enrich.WindowRef `json:"window"`
	// AnalysisFacets is the deterministic analysis: workstreams, dynamics,
	// effort, the thirteen inventories, the cut-visibility map and the session
	// prior. EMBEDDED rather than restated, and shared with BlockEnrichment,
	// because a tick window and a v2 block carry exactly the same set — see
	// AnalysisFacets for why keeping two copies of that list is the defect
	// this avoids. Anonymous and untagged, so every field inlines and this
	// row's wire shape is unchanged by the extraction.
	//
	// A tick-emitted window is not a lesser window, which is why it carries all
	// of it — including the session prior, a CONTRAST reported beside
	// `workstreams` and never a value supplied in its place.
	AnalysisFacets
	// PipelineStatus is always enrich.PipelineStatusWindow. It rides here so a
	// reader can tell WHY there is no task_type on this row (there was never a
	// prompt) rather than inferring it from an absence.
	PipelineStatus    string            `json:"pipeline_status"`
	ExtractorVersions map[string]string `json:"extractor_versions"`
	SchemaVersion     int               `json:"schema_version"`
	TS                string            `json:"ts"`
}

// BuildWindow maps one tick-emitted window characterisation into the wire shape.
//
// The correlation is the load-bearing decision, and it is made against measured
// evidence rather than preference. Atlas keys enrichments
// `UNIQUE(org_id, source_id, corr_scheme, corr_id)` and inserts with
// `ON CONFLICT DO UPDATE` across every column (keld-atlas
// services/api/app/services/enrichments.py, models.py's uq_enrichment_corr). So
// the design spec's option (a) — attach the tick to the most recent prompt at or
// before the slice — does not dedup: it OVERWRITES that prompt's enrichment,
// replacing its task_type, sensitivity and domain with the nothing a tick
// computed. Several ticks sharing one anchor would then also overwrite each
// other. Under enrich.WindowCorrScheme the key cannot collide with any prompt
// row, and WindowCorrID is a deterministic function of the session and the
// window's end, so a re-published window upserts itself — which is the
// idempotency the design wanted from (a) and could not have had.
//
// SHIPPING INERT, SAID PLAINLY: every Atlas consumer joins
// `Enrichment.corr_id == ToolEvent.prompt_id`, so nothing reads a window row
// until Atlas learns to join by time and identity. The row is accepted and
// stored whole (EnrichmentIn ignores unknown fields and the raw body is
// persisted), so no characterisation is lost in the meantime — but it joins to
// nothing, which is why the daemon's ticker is off by default and announces
// itself when switched on.
func BuildWindow(w enrich.WindowCharacterisation, actor string, now time.Time) WindowEnrichment {
	return WindowEnrichment{
		Source: Source{ID: w.Source},
		Correlation: Correlation{
			Scheme:    enrich.WindowCorrScheme,
			ID:        WindowCorrID(w.SessionID, w.Ref.End),
			SessionID: w.SessionID,
		},
		Actor:             actor,
		Window:            w.Ref,
		AnalysisFacets:    facetsOf(w.Analysis),
		PipelineStatus:    enrich.PipelineStatusWindow,
		ExtractorVersions: windowExtractorVersions(),
		SchemaVersion:     enrich.SchemaVersion,
		TS:                now.UTC().Format(time.RFC3339),
	}
}

// WindowCorrID is a tick row's correlation id: the session and the window's own
// end instant. DETERMINISTIC, because that is what makes a re-publish an upsert
// rather than a duplicate under Atlas's uq_enrichment_corr — a daemon that
// restarts mid-batch, or one whose cursor was lost, must not leave two rows for
// one window.
//
// The END instant, not the start: the cursor advances through ends, and windows
// within a session are disjoint and chronological, so an end is unique per
// session by construction. Normalised to UTC so two spellings of one instant
// cannot become two ids. An unparseable end falls back to the raw string, which
// keeps distinct windows distinct — collapsing them onto a shared placeholder
// would make them overwrite each other, the exact failure this whole scheme
// exists to avoid.
func WindowCorrID(sessionID, end string) string {
	if t, err := time.Parse(time.RFC3339Nano, end); err == nil {
		end = t.UTC().Format(time.RFC3339Nano)
	}
	return sessionID + "@" + end
}

// windowExtractorVersions attributes a tick row to the pass that produced it. A
// tick runs exactly one thing — the deterministic window analysis — so the map
// has one entry, and it is the SAME key and version a prompt row's workstreams
// carry, because it is the same analysis over the same kind of window. A reader
// comparing the two must not have to learn a second name for one producer.
func windowExtractorVersions() map[string]string {
	var e enrich.WorkstreamsExtractor
	return map[string]string{e.Name(): e.Version()}
}

// SendWindow POSTs one window enrichment to the same endpoint prompt rows go to.
// Same route, same token, same idempotency — only the correlation scheme differs.
func (p *Publisher) SendWindow(e WindowEnrichment) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-keld-ingest-token", p.Token())

	client := p.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return &retry.StatusError{Code: resp.StatusCode}
	}
	return nil
}
