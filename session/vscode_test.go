package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeVSCodeWorkspace(t *testing.T, root EditorStorageRoot, workspaceID, folder string) string {
	t.Helper()
	workspaceDir := filepath.Join(root.WorkspaceStoragePath, workspaceID)
	chatDir := filepath.Join(workspaceDir, "chatSessions")
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	workspaceJSON, err := json.Marshal(map[string]string{"folder": folder})
	if err != nil {
		t.Fatalf("Marshal(workspace.json) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "workspace.json"), workspaceJSON, 0o644); err != nil {
		t.Fatalf("WriteFile(workspace.json) error = %v", err)
	}
	return chatDir
}

func writeVSCodeJSONSession(t *testing.T, path, sessionID, title string, createdAt, updatedAt int64) {
	t.Helper()
	payload := map[string]interface{}{
		"version":         3,
		"creationDate":    createdAt,
		"customTitle":     title,
		"initialLocation": "panel",
		"lastMessageDate": updatedAt,
		"sessionId":       sessionID,
		"requests": []map[string]interface{}{
			{
				"requestId": "req-1",
				"timestamp": updatedAt,
				"message": map[string]interface{}{
					"text": "how does this work?",
					"parts": []map[string]interface{}{
						{"text": "how does this work?"},
					},
				},
				"response": []map[string]interface{}{
					{"id": "p1", "kind": "thinking", "value": "first thought. "},
					{"id": "p2", "kind": nil, "value": "first answer"},
				},
				"result":       map[string]interface{}{},
				"isCanceled":   false,
				"variableData": map[string]interface{}{"variables": []interface{}{}},
			},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal(vscode session) error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

func writeVSCodeJSONSessionWithResponse(t *testing.T, path, sessionID string, createdAt, updatedAt int64, reasoning, response string) {
	t.Helper()
	parts := make([]map[string]interface{}, 0, 2)
	if reasoning != "" {
		parts = append(parts, map[string]interface{}{"id": "p1", "kind": "thinking", "value": reasoning})
	}
	if response != "" {
		parts = append(parts, map[string]interface{}{"id": "p2", "kind": nil, "value": response})
	}
	payload := map[string]interface{}{
		"version":         3,
		"creationDate":    createdAt,
		"customTitle":     "Live title",
		"initialLocation": "panel",
		"lastMessageDate": updatedAt,
		"sessionId":       sessionID,
		"requests": []map[string]interface{}{
			{
				"requestId": "req-live-1",
				"timestamp": updatedAt,
				"message": map[string]interface{}{
					"text": "stream this",
					"parts": []map[string]interface{}{
						{"text": "stream this"},
					},
				},
				"response": parts,
				"result":   map[string]interface{}{},
			},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal(live session) error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

func writeVSCodeJSONLSession(t *testing.T, path string) {
	t.Helper()
	request := map[string]interface{}{
		"requestId": "req-log-1",
		"timestamp": 2000,
		"message": map[string]interface{}{
			"text": "show me the logs",
			"parts": []map[string]interface{}{
				{"text": "show me the logs"},
			},
		},
		"response": []map[string]interface{}{
			{"id": "l1", "kind": "thinking", "value": "trace path. "},
			{"id": "l2", "kind": nil, "value": "done"},
		},
		"result": map[string]interface{}{},
	}
	lines := []map[string]interface{}{
		{
			"kind": 0,
			"v": map[string]interface{}{
				"version":         3,
				"creationDate":    1000,
				"customTitle":     nil,
				"initialLocation": "panel",
				"lastMessageDate": 1000,
				"sessionId":       "log-session",
				"requests":        []interface{}{},
			},
		},
		{"kind": 1, "k": []interface{}{"customTitle"}, "v": "Mutation title"},
		{"kind": 1, "k": []interface{}{"lastMessageDate"}, "v": 2000},
		{"kind": 2, "k": []interface{}{"requests"}, "v": []interface{}{request}},
	}

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(%s) error = %v", path, err)
	}
	defer file.Close()

	enc := json.NewEncoder(file)
	for _, line := range lines {
		if err := enc.Encode(line); err != nil {
			t.Fatalf("Encode(line) error = %v", err)
		}
	}
}

func TestLoadSessionHistoryReadsVSCodeJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	writeVSCodeJSONSession(t, path, "json-session", "JSON title", 1000, 2000)

	turns, err := LoadSessionHistory(SessionInfo{
		Source:      SessionSourceVSCode,
		HistoryPath: path,
	})
	if err != nil {
		t.Fatalf("LoadSessionHistory() error = %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("len(turns) = %d, want %d", len(turns), 1)
	}
	if turns[0].ID != "req-1" {
		t.Fatalf("turns[0].ID = %q, want %q", turns[0].ID, "req-1")
	}
	if turns[0].UserMessage != "how does this work?" {
		t.Fatalf("turns[0].UserMessage = %q, want %q", turns[0].UserMessage, "how does this work?")
	}
	if turns[0].ReasoningText != "first thought. " {
		t.Fatalf("turns[0].ReasoningText = %q, want %q", turns[0].ReasoningText, "first thought. ")
	}
	if turns[0].Response != "first answer" {
		t.Fatalf("turns[0].Response = %q, want %q", turns[0].Response, "first answer")
	}
	if turns[0].Timestamp != time.UnixMilli(2000) {
		t.Fatalf("turns[0].Timestamp = %v, want %v", turns[0].Timestamp, time.UnixMilli(2000))
	}
}

func TestLoadSessionHistoryReadsVSCodeJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeVSCodeJSONLSession(t, path)

	turns, err := LoadSessionHistory(SessionInfo{
		Source:      SessionSourceVSCode,
		HistoryPath: path,
	})
	if err != nil {
		t.Fatalf("LoadSessionHistory() error = %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("len(turns) = %d, want %d", len(turns), 1)
	}
	if turns[0].ID != "req-log-1" {
		t.Fatalf("turns[0].ID = %q, want %q", turns[0].ID, "req-log-1")
	}
	if turns[0].UserMessage != "show me the logs" {
		t.Fatalf("turns[0].UserMessage = %q, want %q", turns[0].UserMessage, "show me the logs")
	}
	if turns[0].ReasoningText != "trace path. " {
		t.Fatalf("turns[0].ReasoningText = %q, want %q", turns[0].ReasoningText, "trace path. ")
	}
	if turns[0].Response != "done" {
		t.Fatalf("turns[0].Response = %q, want %q", turns[0].Response, "done")
	}
}

func TestDetectIncludesVSCodeSessionsAndPrefersJSONL(t *testing.T) {
	tempRoot := t.TempDir()
	useSessionStatePath(t, filepath.Join(tempRoot, "copilot", "session-state"))
	useEditorStorageRoots(t, []EditorStorageRoot{
		{
			Name:                 "VS Code",
			WorkspaceStoragePath: filepath.Join(tempRoot, "Code", "User", "workspaceStorage"),
			GlobalStoragePath:    filepath.Join(tempRoot, "Code", "User", "globalStorage"),
		},
	})

	root := EditorStorageRoot{
		Name:                 "VS Code",
		WorkspaceStoragePath: filepath.Join(tempRoot, "Code", "User", "workspaceStorage"),
		GlobalStoragePath:    filepath.Join(tempRoot, "Code", "User", "globalStorage"),
	}
	chatDir := writeVSCodeWorkspace(t, root, "workspace-a", filepath.Join(tempRoot, "demo-project"))
	writeVSCodeJSONSession(t, filepath.Join(chatDir, "shared-session.json"), "shared-session", "Legacy title", 1000, 1500)
	writeVSCodeJSONLSession(t, filepath.Join(chatDir, "shared-session.jsonl"))

	sessions, err := Detect()
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want %d", len(sessions), 1)
	}

	sessionInfo := sessions[0]
	if sessionInfo.Source != SessionSourceVSCode {
		t.Fatalf("sessions[0].Source = %q, want %q", sessionInfo.Source, SessionSourceVSCode)
	}
	if sessionInfo.Format != SessionFormatVSCodeChatJSONL {
		t.Fatalf("sessions[0].Format = %q, want %q", sessionInfo.Format, SessionFormatVSCodeChatJSONL)
	}
	if filepath.Ext(sessionInfo.HistoryPath) != ".jsonl" {
		t.Fatalf("sessions[0].HistoryPath = %q, want .jsonl path", sessionInfo.HistoryPath)
	}
	if sessionInfo.Cwd != filepath.Join(tempRoot, "demo-project") {
		t.Fatalf("sessions[0].Cwd = %q, want %q", sessionInfo.Cwd, filepath.Join(tempRoot, "demo-project"))
	}
	if sessionInfo.DisplaySource() != "VS Code" {
		t.Fatalf("sessions[0].DisplaySource() = %q, want %q", sessionInfo.DisplaySource(), "VS Code")
	}
}

func TestVSCodeWatcherEmitsPartialAndFinalMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live-session.json")
	writeVSCodeJSONSessionWithResponse(t, path, "live-session", 1000, 1000, "", "")

	prevIdle := vscodeWatcherIdleFinalizeDuration
	vscodeWatcherIdleFinalizeDuration = 75 * time.Millisecond
	t.Cleanup(func() {
		vscodeWatcherIdleFinalizeDuration = prevIdle
	})

	watcher := NewVSCodeWatcher("live-session", path, SessionFormatVSCodeChatJSON)
	if err := watcher.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer watcher.Stop()

	writeVSCodeJSONSessionWithResponse(t, path, "live-session", 1000, 2000, "live thought. ", "live answer")

	var partial ReasoningMsg
	select {
	case partial = <-watcher.Chan():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for partial message")
	}
	if !partial.Partial {
		t.Fatalf("partial.Partial = %v, want true", partial.Partial)
	}
	if partial.TurnID != "req-live-1" {
		t.Fatalf("partial.TurnID = %q, want %q", partial.TurnID, "req-live-1")
	}
	if partial.ReasoningText != "live thought. " {
		t.Fatalf("partial.ReasoningText = %q, want %q", partial.ReasoningText, "live thought. ")
	}
	if partial.ContentText != "live answer" {
		t.Fatalf("partial.ContentText = %q, want %q", partial.ContentText, "live answer")
	}

	var final ReasoningMsg
	select {
	case final = <-watcher.Chan():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for final message")
	}
	if final.Partial {
		t.Fatalf("final.Partial = %v, want false", final.Partial)
	}
	if final.ReasoningText != "live thought. " {
		t.Fatalf("final.ReasoningText = %q, want %q", final.ReasoningText, "live thought. ")
	}
	if final.ContentText != "live answer" {
		t.Fatalf("final.ContentText = %q, want %q", final.ContentText, "live answer")
	}
}
