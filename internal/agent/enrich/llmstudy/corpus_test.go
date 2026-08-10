//go:build llmstudy

package llmstudy

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// StratifiedTranscripts returns transcript paths spread ACROSS projects, round-robin, rather
// than the first N in sort order.
//
// Sort order was a silent monoculture. Path order puts every keld-atlas transcript first, and
// that project holds 43 of 59, so a 14-session sweep drew all 14 from one codebase — the
// results described one project's conventions while reading as a corpus. This session's own
// transcript sat at index 51 and was never sampled.
//
// Round-robin also front-loads project diversity, so a small budget still spans several
// projects instead of exhausting the largest one.
func StratifiedTranscripts() []string {
	root := filepath.Join(os.Getenv("HOME"), ".claude", "projects")
	byProject := map[string][]string{}
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			proj := filepath.Base(filepath.Dir(p))
			byProject[proj] = append(byProject[proj], p)
		}
		return nil
	})
	projects := make([]string, 0, len(byProject))
	for k := range byProject {
		sort.Strings(byProject[k])
		projects = append(projects, k)
	}
	// Largest project last, so a truncated budget does not fill up with its transcripts.
	sort.Slice(projects, func(i, j int) bool {
		if len(byProject[projects[i]]) != len(byProject[projects[j]]) {
			return len(byProject[projects[i]]) < len(byProject[projects[j]])
		}
		return projects[i] < projects[j]
	})

	var out []string
	for round := 0; ; round++ {
		added := false
		for _, proj := range projects {
			if round < len(byProject[proj]) {
				out = append(out, byProject[proj][round])
				added = true
			}
		}
		if !added {
			return out
		}
	}
}

// ThisSessionTranscript returns the transcript of the session that is running the harness, or
// "" when it cannot be identified.
//
// Worth having as a named case: it is the longest and most correction-dense transcript on this
// machine, and its report can be checked by the person who lived the session — the one reader
// who can judge a synopsis without trusting the harness.
func ThisSessionTranscript() string {
	if id := os.Getenv("KELD_STUDY_SESSION_ID"); id != "" {
		root := filepath.Join(os.Getenv("HOME"), ".claude", "projects")
		var found string
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, id+".jsonl") {
				found = p
			}
			return nil
		})
		return found
	}
	return ""
}
