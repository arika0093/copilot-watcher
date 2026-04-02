package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const sessionStateDir = ".copilot/session-state"

// Detect scans ~/.copilot/session-state for active Copilot CLI sessions.
// A session is considered active if its inuse.<pid>.lock file exists and the PID is alive.
func Detect() ([]SessionInfo, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot find home dir: %w", err)
	}
	stateDir := filepath.Join(home, sessionStateDir)

	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading session state dir: %w", err)
	}

	var sessions []SessionInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Skip copilot-watcher's own internal translation sessions
		if strings.HasPrefix(entry.Name(), "copilot-watcher-") {
			continue
		}
		sessionDir := filepath.Join(stateDir, entry.Name())
		info, err := parseSession(sessionDir, entry.Name())
		if err != nil || info == nil {
			continue
		}
		sessions = append(sessions, *info)
	}

	// Sort by most recently updated first
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	return sessions, nil
}

func parseSession(sessionDir, sessionID string) (*SessionInfo, error) {
	// Find inuse.<pid>.lock file
	lockFiles, err := filepath.Glob(filepath.Join(sessionDir, "inuse.*.lock"))
	if err != nil || len(lockFiles) == 0 {
		return nil, nil // no active lock = inactive session
	}

	// Parse PID from first lock file
	lockBase := filepath.Base(lockFiles[0])
	parts := strings.Split(lockBase, ".")
	if len(parts) < 3 {
		return nil, nil
	}
	pid, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, nil
	}

	// Verify PID is alive
	if !isPIDAlive(pid) {
		return nil, nil
	}

	// Read workspace.yaml
	wsPath := filepath.Join(sessionDir, "workspace.yaml")
	ws, err := readWorkspaceConfig(wsPath)
	if err != nil {
		// Use defaults if workspace.yaml is unreadable
		ws = &WorkspaceConfig{ID: sessionID, Cwd: "unknown"}
	}

	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	if _, err := os.Stat(eventsPath); err != nil {
		return nil, nil
	}

	return &SessionInfo{
		SessionID:  sessionID,
		PID:        pid,
		Cwd:        ws.Cwd,
		Summary:    ws.Summary,
		CreatedAt:  ws.CreatedAt,
		UpdatedAt:  ws.UpdatedAt,
		EventsPath: eventsPath,
	}, nil
}

func readWorkspaceConfig(path string) (*WorkspaceConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ws WorkspaceConfig
	if err := yaml.Unmarshal(data, &ws); err != nil {
		return nil, err
	}
	return &ws, nil
}

func isPIDAlive(pid int) bool {
	if runtime.GOOS == "windows" {
		return isPIDAliveWindows(pid)
	}
	// Linux/macOS: check /proc/<pid>/stat or use kill(0)
	if runtime.GOOS == "linux" {
		_, err := os.Stat(fmt.Sprintf("/proc/%d/stat", pid))
		return err == nil
	}
	// macOS: use os.FindProcess + signal 0
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(os.Signal(nil))
	return err == nil
}

// LoadAllSessions returns all sessions regardless of active status, sorted newest first.
func LoadAllSessions() ([]SessionInfo, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot find home dir: %w", err)
	}
	stateDir := filepath.Join(home, sessionStateDir)
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sessions []SessionInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Skip copilot-watcher's own internal translation sessions
		if strings.HasPrefix(entry.Name(), "copilot-watcher-") {
			continue
		}
		sessionDir := filepath.Join(stateDir, entry.Name())
		sessionID := entry.Name()
		eventsPath := filepath.Join(sessionDir, "events.jsonl")
		if _, err := os.Stat(eventsPath); err != nil {
			continue
		}
		wsPath := filepath.Join(sessionDir, "workspace.yaml")
		ws, err := readWorkspaceConfig(wsPath)
		if err != nil {
			ws = &WorkspaceConfig{ID: sessionID, Cwd: "unknown"}
		}
		sessions = append(sessions, SessionInfo{
			SessionID:  sessionID,
			PID:        0,
			Cwd:        ws.Cwd,
			Summary:    ws.Summary,
			CreatedAt:  ws.CreatedAt,
			UpdatedAt:  ws.UpdatedAt,
			EventsPath: eventsPath,
		})
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	return sessions, nil
}

// LoadHistory reads all past turns from events.jsonl synchronously.
// Returns turns with ReasoningText populated (where available).
func LoadHistory(eventsPath string) ([]Turn, error) {
	f, err := os.Open(eventsPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var turns []Turn
	var currentUser string
	var currentReasoning string
	var currentContent string
	var currentTimestamp time.Time

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer
	for scanner.Scan() {
		line := scanner.Bytes()
		var evt SessionEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}
		switch evt.Type {
		case "user.message":
			// Start of new turn
			if currentUser != "" && currentReasoning != "" {
				turns = append(turns, Turn{
					UserMessage:   currentUser,
					ReasoningText: currentReasoning,
					Response:      currentContent,
					Timestamp:     currentTimestamp,
				})
			}
			d, err := ParseUserMessage(evt)
			if err == nil {
				currentUser = d.Content
				currentReasoning = ""
				currentContent = ""
				currentTimestamp = evt.Timestamp
			}
		case "assistant.message":
			d, err := ParseAssistantMessage(evt)
			if err == nil {
				if d.ReasoningText != "" {
					currentReasoning = d.ReasoningText
				}
				if d.Content != "" {
					currentContent = d.Content
				}
			}
		case "assistant.turn_end":
			// Finalize turn when reasoning was found
			if currentUser != "" && currentReasoning != "" {
				turns = append(turns, Turn{
					UserMessage:   currentUser,
					ReasoningText: currentReasoning,
					Response:      currentContent,
					Timestamp:     currentTimestamp,
				})
				currentUser = ""
				currentReasoning = ""
				currentContent = ""
			}
		}
	}
	return turns, scanner.Err()
}
