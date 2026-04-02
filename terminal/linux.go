//go:build linux

package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// FindPTYMasterPath returns the /proc/<master_pid>/fd/<n> path for the PTY master
// corresponding to the PTY slave used by the given PID's stdout.
// Returns empty string if not found or not applicable.
func FindPTYMasterPath(pid int) (string, error) {
	// Step 1: Find what pid's stdout (fd=1) points to
	stdoutLink := fmt.Sprintf("/proc/%d/fd/1", pid)
	target, err := os.Readlink(stdoutLink)
	if err != nil {
		return "", fmt.Errorf("readlink %s: %w", stdoutLink, err)
	}
	if !strings.HasPrefix(target, "/dev/pts/") {
		return "", fmt.Errorf("stdout is %s, not a PTY slave", target)
	}
	ptsNumStr := strings.TrimPrefix(target, "/dev/pts/")
	ptsNum, err := strconv.Atoi(ptsNumStr)
	if err != nil {
		return "", fmt.Errorf("parsing pts number from %s: %w", target, err)
	}

	// Step 2: Walk parent chain to find who holds the PTY master
	current := pid
	for depth := 0; depth < 10; depth++ {
		ppid, err := getParentPID(current)
		if err != nil || ppid <= 1 {
			break
		}
		masterPath, err := findPTXMasterForPTS(ppid, ptsNum)
		if err == nil && masterPath != "" {
			return masterPath, nil
		}
		current = ppid
	}

	// Step 3: Scan ALL /proc entries as fallback (handles containers / non-parent holders)
	procEntries, err := os.ReadDir("/proc")
	if err == nil {
		for _, e := range procEntries {
			candidatePID, err := strconv.Atoi(e.Name())
			if err != nil || candidatePID <= 1 {
				continue
			}
			masterPath, err := findPTXMasterForPTS(candidatePID, ptsNum)
			if err == nil && masterPath != "" {
				return masterPath, nil
			}
		}
	}

	return "", fmt.Errorf("PTY master not found for pts/%d", ptsNum)
}

func getParentPID(pid int) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PPid:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return strconv.Atoi(parts[1])
			}
		}
	}
	return 0, fmt.Errorf("PPid not found")
}

// findPTXMasterForPTS scans /proc/<pid>/fd/ for a ptmx fd that corresponds to pts/<n>
func findPTXMasterForPTS(pid, ptsNum int) (string, error) {
	fdDir := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		fdPath := filepath.Join(fdDir, entry.Name())
		target, err := os.Readlink(fdPath)
		if err != nil {
			continue
		}
		if target != "/dev/pts/ptmx" && target != "/dev/ptmx" {
			continue
		}
		// Open this ptmx fd to check its pts number
		f, err := os.Open(fdPath)
		if err != nil {
			continue
		}
		n, err := tiocgptn(f)
		f.Close()
		if err == nil && n == ptsNum {
			return fdPath, nil
		}
	}
	return "", fmt.Errorf("no ptmx matching pts/%d found in pid %d", ptsNum, pid)
}

// tiocgptn calls ioctl TIOCGPTN to get the pts number of a ptmx fd
func tiocgptn(f *os.File) (int, error) {
	var n uint32
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		f.Fd(),
		syscall.TIOCGPTN,
		uintptr(unsafe.Pointer(&n)),
	)
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}
