package update

import (
	"os"
	"path/filepath"
	"runtime"
)

// Dest is where this machine's artifacts actually live, resolved from the
// running process rather than guessed.
type Dest struct {
	BinDir        string // holds keld and keld-agent
	SidecarDir    string // the PARENT of the sidecar (flat or nested layout)
	SidecarNested bool   // true when the sidecar is <SidecarDir>/keld-agent-sidecar/<bin>
	HasSidecar    bool
	Writable      bool   // BinDir is writable by this process
	Migrated      bool   // BinDir is not the original install dir (macOS pkg case)
	OrigBinDir    string // the install dir we could not write to, when Migrated
}

// Writable reports whether this process can create a file in dir. A probe
// rather than a permission-bit calculation: the answer depends on ownership,
// ACLs, read-only mounts and the effective uid, and only the kernel knows all
// of those.
func Writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".keld-write-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// StageDir creates a staging directory INSIDE dir.
//
// Inside, deliberately: the commit is then a same-filesystem rename rather
// than a cross-device copy of the sidecar's ~15,000 files — the same reason
// scripts/install.sh stages under $DEST. There is no /tmp fallback, because
// falling back would trade an atomic rename for a copy that can fail halfway
// and leave the install in a state neither old nor new.
func StageDir(dir string) (string, error) {
	return os.MkdirTemp(dir, ".keld-update.*")
}

func sidecarBinName() string {
	if runtime.GOOS == "windows" {
		return "keld-agent-sidecar.exe"
	}
	return "keld-agent-sidecar"
}

// DetectSidecarLayout reports which of the two shipped layouts dir holds:
//
//	flat:   dir/keld-agent-sidecar[.exe]                      (Windows Inno)
//	nested: dir/keld-agent-sidecar/keld-agent-sidecar[.exe]   (macOS pkg, install.sh)
//
// The layout already on disk is preserved by an update: an install that
// changed shape mid-life would break sidecarBinPath()'s resolution order for
// anything still pointing at the old one.
func DetectSidecarLayout(dir string) (nested, ok bool) {
	name := sidecarBinName()
	if isRegular(filepath.Join(dir, name)) {
		return false, true
	}
	if isRegular(filepath.Join(dir, "keld-agent-sidecar", name)) {
		return true, true
	}
	return false, false
}

func isRegular(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
