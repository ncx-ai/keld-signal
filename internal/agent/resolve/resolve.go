// Package resolve turns an enrich request into prompt text — either inline text
// supplied by the producer, or text read from a tool's transcript on disk.
package resolve

// TranscriptReader reads one prompt's text from a tool transcript.
type TranscriptReader interface {
	Source() string
	Read(transcriptPath, promptID string) (text string, ok bool)
}

var readers = map[string]TranscriptReader{}

func register(r TranscriptReader) { readers[r.Source()] = r }

func init() { register(NewClaudeReader()) }

// Resolve returns the prompt text. Inline text (when present) wins; otherwise it
// dispatches to the registered reader for source. Returns ok=false to skip.
func Resolve(source, transcriptPath, promptID, inline string) (string, bool) {
	if inline != "" {
		return inline, true
	}
	r, ok := readers[source]
	if !ok {
		return "", false
	}
	return r.Read(transcriptPath, promptID)
}
