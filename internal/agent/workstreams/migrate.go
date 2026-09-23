package workstreams

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// LegacyFileName is the pre-rename document, ~/.keld/state/projects.json. // vocab:keep
const LegacyFileName = "projects.json"

// LegacyBackupSuffix is what the legacy file is renamed with once migrated. It
// is never deleted: the one copy of a person's local setup is not ours to throw
// away on an upgrade.
const LegacyBackupSuffix = ".pre-rename"

// legacyDocumentV1 is the version-1 shape: `workstreams` held the GROUPS and
// `projects` the workstreams. The key `workstreams` therefore means a different
// thing in each version, which is why decoding goes by version and never by
// key presence.
type legacyDocumentV1 struct {
	Version     int                  `json:"version"`
	Groups      []Group              `json:"workstreams"`
	Workstreams []legacyWorkstreamV1 `json:"projects"`
}

// legacyWorkstreamV1 is a version-1 item: the group lived under `workstream`.
type legacyWorkstreamV1 struct {
	Workstream
	LegacyGroup string `json:"workstream,omitempty"`
}

// decodeDocument reads either version into the current shape.
func decodeDocument(b []byte) (Document, error) {
	var v struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return Document{}, err
	}
	if v.Version >= 2 {
		var d Document
		if err := json.Unmarshal(b, &d); err != nil {
			return Document{}, err
		}
		return d, nil
	}
	var old legacyDocumentV1
	if err := json.Unmarshal(b, &old); err != nil {
		return Document{}, err
	}
	d := Document{Version: CurrentVersion, Groups: old.Groups}
	for _, w := range old.Workstreams {
		ws := w.Workstream
		if ws.Group == "" {
			ws.Group = w.LegacyGroup
		}
		d.Workstreams = append(d.Workstreams, ws)
	}
	return d, nil
}

// readLegacySibling returns the decoded projects.json next to path when path is
// the current document's name and the legacy file exists; nil when there is
// none. It never writes.
func readLegacySibling(path string) (*Document, error) {
	if filepath.Base(path) != FileName {
		return nil, nil
	}
	legacy := filepath.Join(filepath.Dir(path), LegacyFileName)
	b, err := os.ReadFile(legacy)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d, err := decodeDocument(b)
	if err != nil {
		return nil, fmt.Errorf("legacy workstreams file %s: %w", legacy, err)
	}
	return &d, nil
}

// MigrateLegacy moves a pre-rename projects.json in stateDir to workstreams.json,
// once. It writes the new file first and only then renames the old one to
// projects.json.pre-rename, so a crash between the two leaves both copies
// rather than neither. It does nothing when the new file already exists (the
// legacy file is then left exactly where it is) or when there is no legacy
// file. It reports whether it migrated.
func MigrateLegacy(stateDir string) (bool, error) {
	cur := filepath.Join(stateDir, FileName)
	if _, err := os.Stat(cur); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	d, err := readLegacySibling(cur)
	if err != nil || d == nil {
		return false, err
	}
	if err := Save(cur, *d); err != nil {
		return false, err
	}
	legacy := filepath.Join(stateDir, LegacyFileName)
	if err := os.Rename(legacy, legacy+LegacyBackupSuffix); err != nil {
		return true, err
	}
	return true, nil
}
