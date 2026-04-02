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

const (
	copilotDirName      = ".copilot"
	sessionStateDirName = "session-state"
)

func copilotDirPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot find home dir: %w", err)
	}
	return filepath.Join(home, copilotDirName), nil
}

func sessionStatePath() (string, error) {
	rootDir, err := copilotDirPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(rootDir, sessionStateDirName), nil
}

// Detect scans ~/.copilot/session-state for all Copilot CLI sessions.
// Sessions with a live process are marked Active=true; others are included too
// so the user can browse history even when Copilot CLI is not running.
func Detect() ([]SessionInfo, error) {
	stateDir, err := sessionStatePath()
	if err != nil {
		return nil, err
	}

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

	// Sort: active first, then by most recently updated
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].Active != sessions[j].Active {
			return sessions[i].Active
		}
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	return sessions, nil
}

func parseSession(sessionDir, sessionID string) (*SessionInfo, error) {
	// Must have events.jsonl to be useful
	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	if _, err := os.Stat(eventsPath); err != nil {
		return nil, nil
	}

	ws, err := readWorkspaceConfig(filepath.Join(sessionDir, "workspace.yaml"))
	if err != nil {
		ws = &WorkspaceConfig{ID: sessionID, Cwd: "unknown"}
	}

	info := &SessionInfo{
		SessionID:  sessionID,
		Cwd:        ws.Cwd,
		Summary:    ws.Summary,
		CreatedAt:  ws.CreatedAt,
		UpdatedAt:  ws.UpdatedAt,
		EventsPath: eventsPath,
	}

	// Check whether a live process holds a lock on this session
	lockFiles, _ := filepath.Glob(filepath.Join(sessionDir, "inuse.*.lock"))
	for _, lf := range lockFiles {
		parts := strings.Split(filepath.Base(lf), ".")
		if len(parts) < 3 {
			continue
		}
		pid, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		if isPIDAlive(pid) {
			info.Active = true
			info.PID = pid
			break
		}
	}

	return info, nil
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
	stateDir, err := sessionStatePath()
	if err != nil {
		return nil, err
	}
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
// Returns turns with reasoning and/or response content populated (where available).
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
			if currentUser != "" && (currentReasoning != "" || currentContent != "") {
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
					currentReasoning += d.ReasoningText // accumulate (multiple msgs per turn)
				}
				if d.Content != "" {
					currentContent += d.Content
				}
			}
		case "assistant.turn_end":
			// Finalize turn when any assistant output was found
			if currentUser != "" && (currentReasoning != "" || currentContent != "") {
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
