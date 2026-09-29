package promptlog

// The Gemini mirror. Gemini CLI's token-bearing event is `gemini_cli.api_response`
// — NOT `api_request`, which is the request side and carries no tokens — and its
// token attributes have their own spellings (`input_token_count` /
// `output_token_count` / `cached_content_token_count` / `thoughts_token_count`),
// which Atlas reads by name (services/api/app/services/gemini.py).
//
// Two conventions ride along and are deliberately NOT normalised here: the chat
// file's `tokens.input` already includes the cached prefix, and `tokens.thoughts`
// bills as output. Atlas folds both in. Normalising them here as well would
// subtract the prefix twice.
const eventGeminiAPIResponse = "gemini_cli.api_response"

// observeGeminiFile mirrors the model turns a chat file has gained since the last
// call.
//
// Which turns are new is freshGemini's decision, shared with the local count;
// the mirror keeps its own cursor (t.gemini) so the two never move each other.
//
// The cursor is a cost control, not the dedup mechanism. Atlas keys a Gemini row
// on a content HASH of its attributes, so what actually prevents a double count
// is that this payload is a pure function of the chat file: no wall clock, no
// counter, nothing from process state. A daemon that restarts and re-mirrors a
// whole session therefore produces byte-identical rows that upsert onto
// themselves — which is also why the cursor need not be persisted.
func (t *Telemetry) observeGeminiFile(source, path string) {
	s, fresh := freshGemini(&t.mu, t.gemini, path)
	if len(fresh) == 0 {
		return
	}

	res := geminiResource(source, s.ID)
	recs := make([]logRecord, 0, len(fresh))
	for _, r := range fresh {
		attrs := []kv{
			attr("event.name", eventGeminiAPIResponse),
			attr("event.timestamp", r.Timestamp),
			attr("session.id", s.ID),
			attr("model", r.Model),
			// message.id keeps two responses with identical tokens and instant
			// distinct under Atlas's content hash, which would otherwise collapse
			// them into one row.
			attr("message.id", r.RecordID),
			attrInt("input_token_count", r.Tokens.Input),
			attrInt("output_token_count", r.Tokens.Output),
			attrInt("cached_content_token_count", r.Tokens.Cached),
			attrInt("thoughts_token_count", r.Tokens.Thoughts),
			attrInt("tool_token_count", r.Tokens.Tool),
			attrInt("total_token_count", r.Tokens.Total),
		}
		// prompt_id is the id GEMINI'S OWN OTEL reports and the one Atlas joins
		// `Enrichment.corr_id` against, so a mirrored row lands beside the
		// enrichment the watcher published for the same prompt. A model turn
		// before any human turn has none rather than a guessed one.
		if r.PromptOrdinal >= 0 {
			attrs = append(attrs, attr("prompt_id", s.CorrID(r.PromptOrdinal)))
		}
		ns := timeNano(r.Timestamp)
		recs = append(recs, logRecord{
			TimeUnixNano:         ns,
			ObservedTimeUnixNano: ns,
			SeverityNumber:       9,
			SeverityText:         "INFO",
			Attributes:           pruneEmpty(attrs),
		})
	}
	t.postLogs(res, recs)
	// No metrics: Atlas prices Gemini entirely off this log record.
}

// geminiResource mirrors Gemini CLI's own resource block, which carries the
// session id at resource level.
func geminiResource(source, sessionID string) []kv {
	a := []kv{attr("service.name", "gemini-cli"), attr("tool", source)}
	if sessionID != "" {
		a = append(a, attr("session.id", sessionID))
	}
	return append(a, hostResource()...)
}
