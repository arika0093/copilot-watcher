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

var (
	sessionStatePathFn = sessionStatePath
	pidAliveFn         = isPIDAlive
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

// Detect scans all supported local session stores and returns the merged list.
func Detect() ([]SessionInfo, error) {
	var sessions []SessionInfo
	var detectErr error

	cliSessions, err := detectCLISessions()
	if err != nil {
		detectErr = err
	}
	sessions = append(sessions, cliSessions...)

	vscodeSessions, err := detectVSCodeSessions()
	if err != nil && detectErr == nil {
		detectErr = err
	}
	sessions = append(sessions, vscodeSessions...)

	// Sort: active first, then by most recently updated
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].Active != sessions[j].Active {
			return sessions[i].Active
		}
		if sessions[i].UpdatedAt.Equal(sessions[j].UpdatedAt) {
			return sessions[i].SelectionKey() < sessions[j].SelectionKey()
		}
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	if len(sessions) > 0 {
		return sessions, nil
	}
	return sessions, detectErr
}

func detectCLISessions() ([]SessionInfo, error) {
	stateDir, err := sessionStatePathFn()
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
		info, err := parseCLISession(sessionDir, entry.Name())
		if err != nil || info == nil {
			continue
		}
		sessions = append(sessions, *info)
	}
	return sessions, nil
}

func parseCLISession(sessionDir, sessionID string) (*SessionInfo, error) {
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
		SessionID:    sessionID,
		Source:       SessionSourceCLI,
		SourceLabel:  "CLI",
		Format:       SessionFormatCLIEventsJSONL,
		Cwd:          ws.Cwd,
		Summary:      ws.Summary,
		CreatedAt:    ws.CreatedAt,
		UpdatedAt:    ws.UpdatedAt,
		EventsPath:   eventsPath,
		HistoryPath:  eventsPath,
		LivePath:     eventsPath,
		WorkspaceID:  sessionID,
		StorageRoot:  sessionDir,
		MetadataPath: filepath.Join(sessionDir, "workspace.yaml"),
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
		if pidAliveFn(pid) {
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
	sessions, err := Detect()
	if err != nil && len(sessions) == 0 {
		return nil, err
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].UpdatedAt.Equal(sessions[j].UpdatedAt) {
			return sessions[i].SelectionKey() < sessions[j].SelectionKey()
		}
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
					ID:            "",
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
					ID:            "",
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
	if currentUser != "" && (currentReasoning != "" || currentContent != "") {
		turns = append(turns, Turn{
			ID:            "",
			UserMessage:   currentUser,
			ReasoningText: currentReasoning,
			Response:      currentContent,
			Timestamp:     currentTimestamp,
		})
	}
	return turns, scanner.Err()
}
