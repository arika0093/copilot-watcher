package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func useSessionStatePath(t *testing.T, path string) {
	t.Helper()
	prev := sessionStatePathFn
	sessionStatePathFn = func() (string, error) {
		return path, nil
	}
	t.Cleanup(func() {
		sessionStatePathFn = prev
	})
}

func usePIDAlive(t *testing.T, fn func(int) bool) {
	t.Helper()
	prev := pidAliveFn
	pidAliveFn = fn
	t.Cleanup(func() {
		pidAliveFn = prev
	})
}

func useEditorStorageRoots(t *testing.T, roots []EditorStorageRoot) {
	t.Helper()
	prev := editorStorageRootsFn
	editorStorageRootsFn = func() ([]EditorStorageRoot, error) {
		return roots, nil
	}
	t.Cleanup(func() {
		editorStorageRootsFn = prev
	})
}

func writeSessionFixture(t *testing.T, stateDir, sessionID string, updatedAt time.Time, lockPIDs ...int) {
	t.Helper()
	sessionDir := filepath.Join(stateDir, sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "events.jsonl"), []byte("{\"type\":\"session.start\",\"data\":{},\"timestamp\":\"2026-04-01T00:00:00Z\"}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(events.jsonl) error = %v", err)
	}
	workspace := fmt.Sprintf("id: %s\ncwd: /work/%s\nsummary: %s summary\ncreated_at: %s\nupdated_at: %s\n",
		sessionID,
		sessionID,
		sessionID,
		updatedAt.Add(-time.Hour).Format(time.RFC3339),
		updatedAt.Format(time.RFC3339),
	)
	if err := os.WriteFile(filepath.Join(sessionDir, "workspace.yaml"), []byte(workspace), 0o644); err != nil {
		t.Fatalf("WriteFile(workspace.yaml) error = %v", err)
	}
	for _, pid := range lockPIDs {
		lockPath := filepath.Join(sessionDir, fmt.Sprintf("inuse.%d.lock", pid))
		if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
			t.Fatalf("WriteFile(lock) error = %v", err)
		}
	}
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	content := ""
	for _, line := range lines {
		content += line + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func TestReadWorkspaceConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.yaml")
	content := "id: session-1\ncwd: /repo\nsummary: sample\ncreated_at: 2026-04-01T10:00:00Z\nupdated_at: 2026-04-01T11:00:00Z\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ws, err := readWorkspaceConfig(path)
	if err != nil {
		t.Fatalf("readWorkspaceConfig() error = %v", err)
	}
	if ws.ID != "session-1" || ws.Cwd != "/repo" || ws.Summary != "sample" {
		t.Fatalf("readWorkspaceConfig() = %+v, want decoded workspace config", ws)
	}
	if ws.UpdatedAt.Format(time.RFC3339) != "2026-04-01T11:00:00Z" {
		t.Fatalf("UpdatedAt = %s, want %s", ws.UpdatedAt.Format(time.RFC3339), "2026-04-01T11:00:00Z")
	}
}

func TestLoadHistoryBuildsTurnsAndKeepsTrailingTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeLines(t, path,
		`{"type":"user.message","data":{"content":"first request","source":"cli"},"timestamp":"2026-04-01T10:00:00Z"}`,
		`{"type":"assistant.message","data":{"reasoningText":"First reasoning. ","content":"","messageId":"a1","interactionId":"i1","outputTokens":1},"timestamp":"2026-04-01T10:00:01Z"}`,
		`{"type":"assistant.message","data":{"reasoningText":"More reasoning.","content":"First response.","messageId":"a2","interactionId":"i1","outputTokens":2},"timestamp":"2026-04-01T10:00:02Z"}`,
		`{"type":"assistant.turn_end","data":{},"timestamp":"2026-04-01T10:00:03Z"}`,
		`not-json`,
		`{"type":"user.message","data":{"content":"second request","source":"cli"},"timestamp":"2026-04-01T10:01:00Z"}`,
		`{"type":"assistant.message","data":{"reasoningText":"","content":"Second response.","messageId":"a3","interactionId":"i2","outputTokens":1},"timestamp":"2026-04-01T10:01:01Z"}`,
	)

	turns, err := LoadHistory(path)
	if err != nil {
		t.Fatalf("LoadHistory() error = %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("len(turns) = %d, want %d", len(turns), 2)
	}
	if turns[0].UserMessage != "first request" || turns[0].ReasoningText != "First reasoning. More reasoning." || turns[0].Response != "First response." {
		t.Fatalf("turns[0] = %+v, want accumulated first turn", turns[0])
	}
	if turns[1].UserMessage != "second request" || turns[1].ReasoningText != "" || turns[1].Response != "Second response." {
		t.Fatalf("turns[1] = %+v, want trailing turn kept at EOF", turns[1])
	}
	if turns[1].Timestamp.Format(time.RFC3339) != "2026-04-01T10:01:00Z" {
		t.Fatalf("turns[1].Timestamp = %s, want %s", turns[1].Timestamp.Format(time.RFC3339), "2026-04-01T10:01:00Z")
	}
}

func TestLoadHistoryStartsNewTurnWhenNextUserMessageArrives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeLines(t, path,
		`{"type":"user.message","data":{"content":"first","source":"cli"},"timestamp":"2026-04-01T10:00:00Z"}`,
		`{"type":"assistant.message","data":{"reasoningText":"thinking","content":"","messageId":"a1","interactionId":"i1","outputTokens":1},"timestamp":"2026-04-01T10:00:01Z"}`,
		`{"type":"user.message","data":{"content":"second","source":"cli"},"timestamp":"2026-04-01T10:01:00Z"}`,
		`{"type":"assistant.message","data":{"reasoningText":"","content":"done","messageId":"a2","interactionId":"i2","outputTokens":1},"timestamp":"2026-04-01T10:01:01Z"}`,
		`{"type":"assistant.turn_end","data":{},"timestamp":"2026-04-01T10:01:02Z"}`,
	)

	turns, err := LoadHistory(path)
	if err != nil {
		t.Fatalf("LoadHistory() error = %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("len(turns) = %d, want %d", len(turns), 2)
	}
	if turns[0].UserMessage != "first" || turns[0].ReasoningText != "thinking" || turns[0].Response != "" {
		t.Fatalf("turns[0] = %+v, want interrupted first turn", turns[0])
	}
	if turns[1].UserMessage != "second" || turns[1].Response != "done" {
		t.Fatalf("turns[1] = %+v, want second finalized turn", turns[1])
	}
}

func TestDetectSortsActiveSessionsFirst(t *testing.T) {
	stateDir := t.TempDir()
	useSessionStatePath(t, stateDir)
	usePIDAlive(t, func(pid int) bool {
		return pid == 101 || pid == 301
	})
	useEditorStorageRoots(t, nil)

	writeSessionFixture(t, stateDir, "session-old-active", time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC), 101)
	writeSessionFixture(t, stateDir, "session-new-inactive", time.Date(2026, 4, 3, 9, 0, 0, 0, time.UTC), 201)
	writeSessionFixture(t, stateDir, "session-new-active", time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC), 301)
	writeSessionFixture(t, stateDir, "copilot-watcher-rt", time.Date(2026, 4, 4, 9, 0, 0, 0, time.UTC), 401)
	if err := os.MkdirAll(filepath.Join(stateDir, "session-no-events"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	sessions, err := Detect()
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("len(sessions) = %d, want %d", len(sessions), 3)
	}
	wantIDs := []string{"session-new-active", "session-old-active", "session-new-inactive"}
	for i, want := range wantIDs {
		if sessions[i].SessionID != want {
			t.Fatalf("sessions[%d].SessionID = %q, want %q", i, sessions[i].SessionID, want)
		}
	}
	if !sessions[0].Active || sessions[0].PID != 301 {
		t.Fatalf("sessions[0] = %+v, want active PID 301", sessions[0])
	}
	if !sessions[1].Active || sessions[1].PID != 101 {
		t.Fatalf("sessions[1] = %+v, want active PID 101", sessions[1])
	}
	if sessions[2].Active || sessions[2].PID != 0 {
		t.Fatalf("sessions[2] = %+v, want inactive session", sessions[2])
	}
}

func TestLoadAllSessionsSortsNewestFirst(t *testing.T) {
	stateDir := t.TempDir()
	useSessionStatePath(t, stateDir)
	useEditorStorageRoots(t, nil)

	writeSessionFixture(t, stateDir, "session-old", time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC))
	writeSessionFixture(t, stateDir, "session-new", time.Date(2026, 4, 3, 9, 0, 0, 0, time.UTC))
	writeSessionFixture(t, stateDir, "session-mid", time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC))
	writeSessionFixture(t, stateDir, "copilot-watcher-hist", time.Date(2026, 4, 4, 9, 0, 0, 0, time.UTC))
	if err := os.MkdirAll(filepath.Join(stateDir, "session-no-events"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	sessions, err := LoadAllSessions()
	if err != nil {
		t.Fatalf("LoadAllSessions() error = %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("len(sessions) = %d, want %d", len(sessions), 3)
	}
	wantIDs := []string{"session-new", "session-mid", "session-old"}
	for i, want := range wantIDs {
		if sessions[i].SessionID != want {
			t.Fatalf("sessions[%d].SessionID = %q, want %q", i, sessions[i].SessionID, want)
		}
	}
}
