//go:build darwin

package terminal

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// FindPTYMasterPath on macOS uses lsof to find the PTY master holder.
// Returns a path like /proc/<pid>/fd/<n> (Linux-style) or an error.
// On macOS this returns a description; actual reading uses /dev/pts approach.
func FindPTYMasterPath(pid int) (string, error) {
	// Use lsof to find what fd=1 of the process points to
	out, err := exec.Command("lsof", "-p", strconv.Itoa(pid), "-a", "-d", "1").Output()
	if err != nil {
		return "", fmt.Errorf("lsof failed: %w", err)
	}
	lines := strings.Split(string(out), "\n")
	var ptsDevice string
	for _, line := range lines[1:] { // skip header
		fields := strings.Fields(line)
		if len(fields) >= 9 {
			// lsof output: COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
			name := fields[len(fields)-1]
			if strings.HasPrefix(name, "/dev/tty") || strings.HasPrefix(name, "/dev/pts") {
				ptsDevice = name
				break
			}
		}
	}
	if ptsDevice == "" {
		return "", fmt.Errorf("stdout of pid %d is not a PTY", pid)
	}

	// Find who holds the master for this pts device
	out, err = exec.Command("lsof", ptsDevice).Output()
	if err != nil {
		return "", fmt.Errorf("lsof on %s failed: %w", ptsDevice, err)
	}
	for _, line := range strings.Split(string(out), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		masterPID, err := strconv.Atoi(fields[1])
		if err != nil || masterPID == pid {
			continue
		}
		fdNum := strings.TrimRight(fields[3], "rwu")
		// Return a path that reader.go can open
		return fmt.Sprintf("/dev/fd/%s/%s", strconv.Itoa(masterPID), fdNum), nil
	}
	return "", fmt.Errorf("PTY master not found for %s", ptsDevice)
}
