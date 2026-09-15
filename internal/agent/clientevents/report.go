package clientevents

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/creddetect"
)

// ReportSchema is the report bundle's own version, independent of the
// client-events envelope's: the bundle is a FILE a person keeps and may send us
// weeks later, so it has to say what shape it is.
const ReportSchema = 1

// maxReportLogLines is how many daemon log lines a bundle carries (AC-6). The
// NEWEST ones: a report is about what just went wrong.
const maxReportLogLines = 200

// maxReportBytes is the bundle's stated bound on disk. It is not a truncation
// point — nothing is silently cut to reach it — it is the number the test
// measures against, so a future field that blows it up fails here rather than
// in a support inbox.
const maxReportBytes = 64 * 1024

// logTimestampRe matches the instant at the head of a daemon log line. BOTH
// shapes this daemon writes are accepted — the Go standard logger's
// "2026/09/14 14:28:08 " (agent.err.log, what the service manager captures) and
// the debug log's RFC 3339 "2026-09-15T14:26:02Z " (agent.log). Measured: with
// only the first shape, every line of a real agent.log came back "<redacted>"
// INCLUDING its instant, which throws away the one thing about a redacted line
// that is still diagnostic. The prefix is kept VERBATIM: a clock reading cannot
// carry content.
var logTimestampRe = regexp.MustCompile(`^(?:\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?|\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})) `)

// logComponentRe matches ONE short label token our own logger writes after the
// timestamp — "keld-agent: ", "sidecar: ", "teleproxy: ".
//
// ⚠️ IT IS SPLIT OFF RATHER THAN COUNTED, AND THE DIFFERENCE IS THE WHOLE
// USEFULNESS OF THE BUNDLE. The free-text gate allows three words, and the
// daemon's real lines spend one of them on this label — measured on a real
// machine, "keld-agent: listening on 127.0.0.1:52928" is four words and comes
// back "<redacted>", so a strict reading redacts the entire log and nobody
// asks for a second bundle. A single token of at most 24 characters from a
// restricted charset, ending in a colon, is a label this codebase emits, not
// content: it cannot hold a sentence, and the gate below is applied unchanged
// to everything after it. Exactly one label is split, never a chain — at most
// two short tokens, because this daemon writes both "attrib: " and
// "ingest signal: ".
var logComponentRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.\-]{0,23}(?: [A-Za-z][A-Za-z0-9_.\-]{0,23})?: `)

// bareTokenPrefixes are vendor credential prefixes that are dropped on sight.
//
// ⚠️ THIS IS A BACKSTOP FOR A MEASURED HOLE, NOT A SECOND CREDENTIAL DETECTOR.
// creddetect (the vendored gitleaks rules) is the detector and runs first. Its
// generic-api-key rule has a KEYWORD pre-filter, so — measured on 2026-09-15 —
// `auth: sk-ant-PLANTEDKEY…` scores a hit and `sk-ant-PLANTEDKEY…` alone on a
// line scores nothing. A log line is exactly where a token appears with no
// keyword beside it, so the keyword pre-filter that makes creddetect precise on
// prompts makes it blind here. Prefix plus a length floor, and the whole line
// goes — never a mask, because a masked credential line still publishes its
// surroundings and the secret's shape.
var bareTokenPrefixes = []string{
	"sk-", "ghp_", "gho_", "ghs_", "ghu_", "github_pat_",
	"AKIA", "ASIA", "xox", "glpat-", "AIza", "ya29.",
}

// minBareTokenLen keeps a prefix from matching an ordinary word: "sk-1" is not
// a credential, and a real one is long.
const minBareTokenLen = 20

// BundleInput is everything the daemon hands the builder. Every value is
// already a primitive or a list of them, and NOTHING here reads a transcript,
// a prompt or a spool row — the bundle is about the machine, not the work.
type BundleInput struct {
	Source      string
	State       string
	ToolVersion string

	AgentVersion   string
	SidecarVersion string
	OS             string
	Arch           string
	InstallID      string

	// DoctorFindings are finding IDS, not sentences. A finding is NAMED, not
	// quoted: doctor's prose is written around whatever it found, which is the
	// one part of it that could carry a path or a tool's own output. An id that
	// still reads as prose is redacted like any other string.
	DoctorFindings []string

	// Counters are the per-source lane counts, flattened as "<source>.<lane>".
	Counters map[string]int

	// Log is the daemon's log, oldest first. The caller reads it with
	// TailLines; the builder scrubs and caps it.
	Log []string
}

// Bundle is what is written to ~/.keld/reports/ and summarised into the event.
type Bundle struct {
	Schema      int       `json:"schema"`
	GeneratedAt time.Time `json:"generated_at"`

	Source      string `json:"source"`
	State       string `json:"state"`
	ToolVersion string `json:"tool_version"`

	// Versions holds agent, sidecar and tool. ⚠️ An empty string means "could
	// not tell", never "same" — internal/version.Skew's own refusal, and the
	// reason the skew check exists at all.
	Versions map[string]string `json:"versions"`

	OS        string `json:"os"`
	Arch      string `json:"arch"`
	InstallID string `json:"install_id"`

	DoctorFindings []string       `json:"doctor_findings"`
	Counters       map[string]int `json:"counters"`

	Log []string `json:"log"`
	// The three counters are what stop a scrubbed log and a short one from
	// looking alike — omittedNotice's rule, one level up.
	LogKept     int `json:"log_kept"`
	LogRedacted int `json:"log_redacted"`
	LogDropped  int `json:"log_dropped"`
}

// BuildBundle assembles and scrubs a bundle. It never fails: a report a person
// asked for must produce something, and every field that could not be filled
// says so by being empty rather than by aborting.
func BuildBundle(now time.Time, in BundleInput) Bundle {
	b := Bundle{
		Schema:      ReportSchema,
		GeneratedAt: now.UTC(),
		Source:      in.Source,
		State:       in.State,
		ToolVersion: in.ToolVersion,
		Versions: map[string]string{
			"agent":   in.AgentVersion,
			"sidecar": in.SidecarVersion,
			"tool":    in.ToolVersion,
		},
		OS:             in.OS,
		Arch:           in.Arch,
		InstallID:      in.InstallID,
		DoctorFindings: []string{},
		Counters:       map[string]int{},
		Log:            []string{},
	}

	for _, f := range in.DoctorFindings {
		b.DoctorFindings = append(b.DoctorFindings, redactString(f))
	}
	for k, v := range in.Counters {
		b.Counters[redactString(k)] = v
	}

	// Newest first: cap, then scrub, so the cap is on lines a person would
	// want rather than on lines that were going to be dropped anyway.
	lines := in.Log
	if len(lines) > maxReportLogLines {
		lines = lines[len(lines)-maxReportLogLines:]
	}
	for _, line := range lines {
		out, kept, redacted := scrubLogLine(line)
		if !kept {
			b.LogDropped++
			continue
		}
		if redacted {
			b.LogRedacted++
		}
		b.Log = append(b.Log, out)
	}
	b.LogKept = len(b.Log)
	return b
}

// scrubLogLine reduces one daemon log line to something publishable.
//
// The order is the argument:
//
//  1. a credential anywhere in the line ⇒ the WHOLE line goes. Masking would
//     leave the surroundings and the secret's shape.
//  2. the timestamp and the one component label are kept verbatim — a clock
//     reading and a short fixed label our own logger writes carry nothing.
//  3. absolute paths are replaced surgically with <path>, RedactError's rule,
//     so "waiting for <path>" stays a readable fact.
//  4. what is left goes through the SAME free-text gate a published field value
//     gets. A short operational message survives ("listening on 127.0.0.1:…");
//     anything still reading as prose becomes "<redacted>".
//
// ⚠️ STEP 4 IS THE ONE THAT COSTS SOMETHING, AND THE COST IS MEASURED RATHER
// THAN GUESSED AT: on a real agent.log, 87 of 88 lines come back
// "<timestamp> <component>: <redacted>". You cannot tell a logged prompt from a
// logged message by looking at it, so there is no rule that keeps our prose and
// drops theirs; the choice is between a log that carries free text and a log
// that carries none, and a bundle is sent to us by a person who cannot audit it
// first. What survives is still diagnostic — when, from which component, how
// many, and beside them the doctor findings, versions and counters — and what
// did not is declared in the three counters rather than hidden.
//
// The change that would make the message half publishable is STRUCTURED
// LOGGING, not a looser cap: a line whose message is a compiled-in key
// ("attrib.job_quarantined") plus typed fields is provably not user content,
// and the same gate would then pass it. That is a daemon-wide change and is
// deliberately not smuggled in here.
func scrubLogLine(line string) (out string, kept bool, redacted bool) {
	line = strings.TrimRight(line, "\r\n\t ")
	if strings.TrimSpace(line) == "" {
		return "", false, false
	}
	if len(creddetect.Detect(line)) > 0 || hasBareToken(line) {
		return "", false, false
	}

	prefix := logTimestampRe.FindString(line)
	rest := line[len(prefix):]
	if c := logComponentRe.FindString(rest); c != "" {
		prefix += c
		rest = rest[len(c):]
	}

	rest = wsCollapseRE.ReplaceAllString(rest, " ")
	rest = pathRe.ReplaceAllString(rest, "${1}<path>")
	if !safeErrMessage(rest) {
		rest = "<redacted>"
		redacted = true
	}

	result := prefix + rest
	if runes := []rune(result); len(runes) > maxErrLen {
		result = string(runes[:maxErrLen]) + "…"
	}
	return result, true, redacted
}

// hasBareToken reports whether any whitespace-delimited token in the line
// starts with a vendor credential prefix and is long enough to be one.
func hasBareToken(line string) bool {
	for _, tok := range strings.Fields(line) {
		tok = strings.Trim(tok, `"'`+"`,;()[]{}<>")
		if len(tok) < minBareTokenLen {
			continue
		}
		for _, p := range bareTokenPrefixes {
			if strings.HasPrefix(tok, p) {
				return true
			}
		}
	}
	return false
}

// EventFields is the bundle reduced to a client event's fields.
//
// ⚠️ THE LOG LINES ARE NOT HERE, AND THAT IS SPEC GAP 4'S ANSWER. The question
// was whether 200 redacted lines fit a batch; they do not need to, because
// redactFields DROPS every non-primitive value — a []string cannot ride an
// event even if someone added one. So the lines live in the file the person
// keeps, and the event carries the counts and the file's name. Measured: the
// field object is under 1 KB against a 200-line worst-case bundle of ~40 KB.
func (b Bundle) EventFields() map[string]any {
	f := map[string]any{
		"schema":          b.Schema,
		"state":           b.State,
		"tool_version":    b.ToolVersion,
		"agent_version":   b.Versions["agent"],
		"sidecar_version": b.Versions["sidecar"],
		"findings_n":      len(b.DoctorFindings),
		"log_kept":        b.LogKept,
		"log_redacted":    b.LogRedacted,
		"log_dropped":     b.LogDropped,
		"report":          b.FileName(),
	}
	for k, v := range b.Counters {
		f["n_"+k] = v
	}
	return f
}

// FileName is the bundle's file name: sortable instant, then the source.
func (b Bundle) FileName() string {
	source := b.Source
	if source == "" {
		source = "unknown"
	}
	return b.GeneratedAt.UTC().Format("20060102T150405Z") + "-" + source + ".json"
}

// WriteBundle writes the bundle under dir and returns its path. The directory
// and the file are user-only, like everything else under ~/.keld.
func WriteBundle(dir string, b Bundle) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	raw, err := json.MarshalIndent(b, "", " ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, b.FileName())
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// TailLines returns the last n lines of a file, oldest first.
//
// An unreadable or absent file answers EMPTY, never an error: a machine whose
// service manager writes no log still has a problem worth reporting, and
// refusing to build a bundle because one input is missing is the failure this
// whole surface exists to avoid.
func TailLines(path string, n int) []string {
	if n <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	ring := make([]string, 0, n)
	sc := bufio.NewScanner(f)
	// A pathological single line (a pasted blob) must not be able to allocate
	// without bound; over the cap the scanner stops and we keep what we have.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if len(ring) == n {
			ring = append(ring[:0], ring[1:]...)
		}
		ring = append(ring, sc.Text())
	}
	return ring
}
