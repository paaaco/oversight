// Package procs holds the small amount of process inspection the tool needs.
package procs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Alive reports whether pid still exists, via kill(pid, 0).
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// parent returns (ppid, comm) for pid using ps, or ok=false.
func parent(pid int) (ppid int, comm string, ok bool) {
	out, err := exec.Command("ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, "", false
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return 0, "", false
	}
	ppid, err = strconv.Atoi(fields[0])
	if err != nil {
		return 0, "", false
	}
	return ppid, strings.Join(fields[1:], " "), true
}

// ClaudePID walks up from this process to the nearest ancestor whose
// executable is named claude. Hooks run under a shell, so the direct parent
// is usually sh. Falls back to the direct parent when nothing matches.
func ClaudePID() int {
	pid := os.Getpid()
	for i := 0; i < 12; i++ {
		ppid, comm, ok := parent(pid)
		if !ok || ppid <= 1 {
			break
		}
		if isClaude(comm) {
			return ppid
		}
		pid = ppid
	}
	return os.Getppid()
}

func isClaude(comm string) bool {
	base := filepath.Base(comm)
	return base == "claude" || strings.HasPrefix(base, "claude-")
}
