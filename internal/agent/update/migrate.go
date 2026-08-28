package update

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// binaryNames are the two Go artifacts an update replaces.
func binaryNames() []string {
	if runtime.GOOS == "windows" {
		return []string{"keld.exe", "keld-agent.exe"}
	}
	return []string{"keld", "keld-agent"}
}

// MigrationTarget is where an update installs when the original destination is
// not writable — the macOS pkg case, whose payload lands in a root-owned
// /usr/local/keld. ~/.local/bin is user-writable, is already searched by
// daemon.sidecarBinPath()'s well-known list, and is where install.sh and
// onboard.command already put things.
func MigrationTarget(home string) string {
	return filepath.Join(home, ".local", "bin")
}

// StaleLink is a symlink on a PATH directory that points at an install other
// than the current one.
type StaleLink struct {
	Path   string // the link itself
	Points string // where it currently points
	Want   string // where it should point
}

// Fix is the exact command a human can run. An advisory that describes a
// problem without the command to fix it makes the reader do the work twice.
func (s StaleLink) Fix() string {
	return fmt.Sprintf("ln -sf %q %q", s.Want, s.Path)
}

// StaleLinks reports keld/keld-agent symlinks under roots that do NOT point
// into binDir.
//
// This exists for one specific, unavoidable situation. The macOS pkg's
// postinstall runs as root and creates /usr/local/bin/{keld,keld-agent} ->
// /usr/local/keld/…, and /usr/local/bin typically precedes ~/.local/bin on
// PATH. After the daemon migrates itself to ~/.local/bin it is running the new
// version while a human typing `keld` still gets the old one, and the daemon —
// unprivileged — cannot rewrite a root-owned link. So it says so. The
// alternative is an install that quietly disagrees with itself about which
// version it is.
//
// Only SYMLINKS are reported. A real binary elsewhere on PATH is a shadowing
// install, which is a different situation needing different advice.
func StaleLinks(roots []string, binDir string) []StaleLink {
	var out []StaleLink
	for _, root := range roots {
		if root == "" || root == binDir {
			continue
		}
		for _, name := range binaryNames() {
			p := filepath.Join(root, name)
			fi, err := os.Lstat(p)
			if err != nil || fi.Mode()&os.ModeSymlink == 0 {
				continue
			}
			dest, err := os.Readlink(p)
			if err != nil {
				continue
			}
			want := filepath.Join(binDir, name)
			if dest == want {
				continue
			}
			out = append(out, StaleLink{Path: p, Points: dest, Want: want})
		}
	}
	return out
}

// PathRoots are the well-known directories an installed keld may appear on,
// checked for stale links after a migration. Kept in sync with the macOS
// postinstall's own list and daemon.wellKnownSidecarDirs().
func PathRoots(home string) []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/usr/local/bin", "/opt/homebrew/bin", filepath.Join(home, ".local", "bin")}
	case "windows":
		return nil
	default:
		return []string{"/usr/local/bin", filepath.Join(home, ".local", "bin")}
	}
}
