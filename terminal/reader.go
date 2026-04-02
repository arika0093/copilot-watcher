package terminal

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// TerminalMsg carries raw text captured from the terminal
type TerminalMsg struct {
	Text string
}

// Reader reads raw terminal output from a Copilot CLI process.
// On Linux/macOS: reads from PTY master via /proc/*/fd (or lsof on macOS).
// On Windows: polls console screen buffer via DuplicateHandle.
// Fallback: tmux pipe-pane if $TMUX is set.
// If teeFilePath is set, tries that first before PTY discovery.
type Reader struct {
	pid         int
	teeFilePath string
	ch          chan TerminalMsg
	done        chan struct{}
	wg          sync.WaitGroup
}

// NewReader creates a Reader for the given PID.
func NewReader(pid int) *Reader {
	return &Reader{
		pid:  pid,
		ch:   make(chan TerminalMsg, 64),
		done: make(chan struct{}),
	}
}

// NewReaderWithTee creates a Reader that prefers reading from a tee file.
func NewReaderWithTee(pid int, teePath string) *Reader {
	return &Reader{
		pid:         pid,
		teeFilePath: teePath,
		ch:          make(chan TerminalMsg, 64),
		done:        make(chan struct{}),
	}
}

// Chan returns the channel for terminal messages.
func (r *Reader) Chan() <-chan TerminalMsg {
	return r.ch
}

// Stop signals the reader goroutine to exit.
func (r *Reader) Stop() {
	close(r.done)
	r.wg.Wait()
}

// Start launches the appropriate terminal reader goroutine.
// It checks the tee file first (if configured and recently modified),
// then falls back to tmux and PTY master discovery.
func (r *Reader) Start() error {
	// Try tee file first if configured and recently modified (within 30s)
	if r.teeFilePath != "" {
		if info, err := os.Stat(r.teeFilePath); err == nil {
			if time.Since(info.ModTime()) < 30*time.Second {
				r.wg.Add(1)
				go func() {
					defer r.wg.Done()
					r.ReadTeeFile(r.teeFilePath)
				}()
				return nil
			}
		}
	}

	// Prefer tmux pipe-pane if inside tmux (non-destructive)
	if isTmux() {
		pane, err := findTmuxPane(r.pid)
		if err == nil && pane != "" {
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				r.runTmux(pane)
			}()
			return nil
		}
	}

	// OS-specific PTY master reading
	masterPath, err := FindPTYMasterPath(r.pid)
	if err != nil {
		return fmt.Errorf("cannot find PTY master: %w", err)
	}

	if strings.HasPrefix(masterPath, "windows-console://") {
		return r.startWindows()
	}

	// Linux/macOS: open and read the PTY master fd
	f, err := os.Open(masterPath)
	if err != nil {
		return fmt.Errorf("opening PTY master %s: %w", masterPath, err)
	}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer f.Close()
		r.readPTY(f)
	}()
	return nil
}

// ReadTeeFile tail-follows a file (like `tail -f`) and sends chunks via the channel.
func (r *Reader) ReadTeeFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	// Seek to end so we only pick up new content
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return
	}

	buf := make([]byte, 4096)
	ansiRe := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b[()][A-B0-1]|\x1b=|\x1b>|\r`)
	for {
		select {
		case <-r.done:
			return
		default:
		}
		n, err := f.Read(buf)
		if n > 0 {
			raw := string(buf[:n])
			clean := ansiRe.ReplaceAllString(raw, "")
			clean = strings.TrimRight(clean, "\x00")
			if strings.TrimSpace(clean) != "" {
				select {
				case r.ch <- TerminalMsg{Text: clean}:
				default:
				}
			}
		}
		if err == io.EOF {
			// Wait and retry (tail -f behavior)
			select {
			case <-r.done:
				return
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		if err != nil {
			return
		}
	}
}

func (r *Reader) readPTY(f *os.File) {
	buf := make([]byte, 4096)
	ansiRe := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b[()][A-B0-1]|\x1b=|\x1b>|\r`)
	for {
		select {
		case <-r.done:
			return
		default:
		}
		n, err := f.Read(buf)
		if n > 0 {
			raw := string(buf[:n])
			clean := ansiRe.ReplaceAllString(raw, "")
			clean = strings.TrimRight(clean, "\x00")
			if strings.TrimSpace(clean) != "" {
				select {
				case r.ch <- TerminalMsg{Text: clean}:
				default:
				}
			}
		}
		if err == io.EOF {
			return
		}
	}
}

// isTmux checks if the current process is inside a tmux session.
func isTmux() bool {
	return os.Getenv("TMUX") != ""
}

// findTmuxPane finds the tmux pane running the given PID.
func findTmuxPane(pid int) (string, error) {
	out, err := exec.Command("tmux", "list-panes", "-a",
		"-F", "#{pane_id}:#{pane_pid}").Output()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.TrimSpace(parts[1]) == fmt.Sprintf("%d", pid) {
			return parts[0], nil
		}
	}
	return "", fmt.Errorf("pane not found for pid %d", pid)
}

// runTmux uses tmux pipe-pane to capture output non-destructively.
func (r *Reader) runTmux(pane string) {
	// Create a temp FIFO
	fifoPath := fmt.Sprintf("/tmp/copilot-watcher-%d.fifo", r.pid)
	os.Remove(fifoPath)
	if err := mkfifo(fifoPath); err != nil {
		return
	}
	defer os.Remove(fifoPath)

	// Start tmux pipe-pane
	cmd := exec.Command("tmux", "pipe-pane", "-o", "-t", pane,
		fmt.Sprintf("cat >> %s", fifoPath))
	if err := cmd.Run(); err != nil {
		return
	}
	defer exec.Command("tmux", "pipe-pane", "-o", "-t", pane, "").Run() //nolint:errcheck

	// Open the FIFO for reading
	f, err := os.Open(fifoPath)
	if err != nil {
		return
	}
	defer f.Close()
	r.readPTY(f)
}
