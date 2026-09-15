package tools

import "os"

// ReadConfig reads an adapter's config file, returning nil when it is absent —
// the shape every Adapter method takes for `currentText`. An unreadable file is
// nil too: the adapters already treat "no text" as "not configured", and
// inventing an empty string instead would make a permissions error look like an
// empty config.
func ReadConfig(a Adapter) *string {
	data, err := os.ReadFile(a.ConfigPath())
	if err != nil {
		return nil
	}
	s := string(data)
	return &s
}

// ConfiguredOnDisk is THE DRIFT CHECK: the config on disk, read back now, is
// what this adapter would have written. `keld signal doctor` has run it since
// the CLI was ported (it is what produces "manifest records setup but config is
// not configured (drift)"), and the integrations route's `surfaces[].wired`
// asks exactly the same question — "read back, not remembered" (AC-1).
//
// It lives here, called by both, rather than being copied into the new package:
// two copies of this question are two ways for doctor and the pane to disagree
// about the same file, which is the defect AC-8 exists to prevent one level up.
//
// managed is the manifest's record for this tool, or nil when the manifest does
// not record it — which is itself a legitimate answer (not configured), not a
// missing input.
func ConfiguredOnDisk(a Adapter, managed map[string]any) bool {
	return ConfiguredAt(a, a.ConfigPath(), managed)
}

// ConfiguredAt is ConfiguredOnDisk against an EXPLICIT path: the one the
// manifest recorded at setup time, which is what `keld signal doctor` has
// always compared against. The two paths are the same file on every ordinary
// machine; they diverge only when HOME moved or a manifest was hand-edited,
// and doctor's finding is specifically "the manifest records setup and THAT
// file is not configured", so it keeps asking about the path it recorded.
func ConfiguredAt(a Adapter, path string, managed map[string]any) bool {
	var current *string
	if data, err := os.ReadFile(path); err == nil {
		s := string(data)
		current = &s
	}
	return a.Status(current, managed).Configured
}
