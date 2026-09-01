// Package hardware collects a coarse, best-effort snapshot of the machine's
// CPU/memory/OS for the daemon's one-time agent.hardware client-event.
//
// Best-effort by design and coarse by design: this exists to answer "is the
// verifier viable on this class of machine", not to fingerprint the person
// running it. Collect NEVER fails — a missing value is an empty string or a
// zero, never an error, because hardware reporting must not be able to take
// the daemon down. Every field comes from a single shell-out or a single
// small file read; a failure on either yields the zero value silently.
package hardware

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// Info is the coarse hardware snapshot. Every field is best-effort: an
// unavailable value is the zero value, never an error.
type Info struct {
	CPUModel     string `json:"cpu_model"`
	LogicalCores int    `json:"logical_cores"`
	MemTotalGB   int    `json:"mem_total_gb"`
	OSVersion    string `json:"os_version"`
}

// Collect returns a best-effort hardware snapshot for this host. LogicalCores
// is runtime.NumCPU() on every platform — it needs no exec or file read and
// can never fail. CPUModel/MemTotalGB/OSVersion are resolved per OS; Windows
// leaves CPUModel and OSVersion empty this iteration (the daemon's event
// envelope still stamps os/arch on every event regardless).
func Collect() Info {
	info := Info{LogicalCores: runtime.NumCPU()}
	switch runtime.GOOS {
	case "darwin":
		collectDarwin(&info)
	case "linux":
		collectLinux(&info)
	}
	return info
}

// sysctlOutput runs `sysctl -n <key>` and returns its trimmed stdout, or ""
// on any failure (binary absent, key unknown, non-zero exit).
func sysctlOutput(key string) string {
	out, err := exec.Command("sysctl", "-n", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func collectDarwin(info *Info) {
	info.CPUModel = sysctlOutput("machdep.cpu.brand_string")

	if raw := sysctlOutput("hw.memsize"); raw != "" {
		if bytes, err := strconv.ParseInt(raw, 10, 64); err == nil && bytes > 0 {
			info.MemTotalGB = int(bytes / (1024 * 1024 * 1024))
		}
	}

	if out, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
		info.OSVersion = strings.TrimSpace(string(out))
	}
}

func collectLinux(info *Info) {
	if v, ok := firstFieldAfterColon("/proc/cpuinfo", "model name"); ok {
		info.CPUModel = v
	}

	if line, ok := grepLine("/proc/meminfo", "MemTotal:"); ok {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil && kb > 0 {
				info.MemTotalGB = int(kb / (1024 * 1024))
			}
		}
	}

	if v, ok := osReleaseField("/etc/os-release", "PRETTY_NAME"); ok {
		info.OSVersion = v
	}
}

// grepLine returns the first line of path starting with prefix, or ok=false
// if the file cannot be opened or no such line exists.
func grepLine(path, prefix string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, prefix) {
			return line, true
		}
	}
	return "", false
}

// firstFieldAfterColon reads path (a "key : value" file, e.g. /proc/cpuinfo)
// and returns the trimmed value of the first line whose key matches prefix.
func firstFieldAfterColon(path, prefix string) (string, bool) {
	line, ok := grepLine(path, prefix)
	if !ok {
		return "", false
	}
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", false
	}
	return strings.TrimSpace(line[idx+1:]), true
}

// osReleaseField reads an /etc/os-release-shaped file (KEY=VALUE lines, value
// optionally double-quoted) and returns the value for key.
func osReleaseField(path, key string) (string, bool) {
	line, ok := grepLine(path, key+"=")
	if !ok {
		return "", false
	}
	v := strings.TrimPrefix(line, key+"=")
	v = strings.Trim(v, `"`)
	return v, true
}
