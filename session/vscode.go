package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const defaultVSCodeWatcherIdleFinalize = 2 * time.Second

var vscodeWatcherIdleFinalizeDuration = defaultVSCodeWatcherIdleFinalize

var editorStorageRootsFn = detectEditorStorageRoots

type LiveWatcher interface {
	Chan() <-chan ReasoningMsg
	DebugChan() <-chan string
	Stop()
}

type EditorStorageRoot struct {
	Name                 string
	WorkspaceStoragePath string
	GlobalStoragePath    string
}

type vscodeSessionData struct {
	Version         int                 `json:"version"`
	CreationDate    int64               `json:"creationDate"`
	CustomTitle     string              `json:"customTitle"`
	InitialLocation string              `json:"initialLocation"`
	SessionID       string              `json:"sessionId"`
	LastMessageDate int64               `json:"lastMessageDate"`
	Requests        []vscodeRequestData `json:"requests"`
}

type vscodeRequestData struct {
	RequestID string            `json:"requestId"`
	Timestamp int64             `json:"timestamp"`
	Message   vscodeMessageData `json:"message"`
	Response  []json.RawMessage `json:"response"`
}

type vscodeMessageData struct {
	Text  string                  `json:"text"`
	Parts []vscodeMessagePartData `json:"parts"`
}

type vscodeMessagePartData struct {
	Text string `json:"text"`
}

type vscodeObservedTurn struct {
	ID            string
	UserMessage   string
	ReasoningText string
	ContentText   string
	Timestamp     time.Time
}

type vscodeMutationEntry struct {
	Kind  int             `json:"kind"`
	Path  []interface{}   `json:"k"`
	Value json.RawMessage `json:"v"`
	Index *int            `json:"i"`
}

type vscodeResponsePart struct {
	Kind    string          `json:"kind"`
	Value   json.RawMessage `json:"value"`
	Content string          `json:"content"`
	Text    string          `json:"text"`
}

type VSCodeWatcher struct {
	filePath  string
	sessionID string
	format    SessionFormat
	ch        chan ReasoningMsg
	dbgCh     chan string
	done      chan struct{}

	mu              sync.Mutex
	lastTurns       []vscodeObservedTurn
	idleFinalize    *time.Timer
	pendingFinalize *vscodeObservedTurn
}

func detectEditorStorageRoots() ([]EditorStorageRoot, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot find home dir: %w", err)
	}

	base := ""
	switch runtime.GOOS {
	case "windows":
		base = os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support")
	default:
		base = filepath.Join(home, ".config")
	}

	variants := []struct {
		name string
		dir  string
	}{
		{name: "VS Code", dir: "Code"},
		{name: "VS Code Insiders", dir: "Code - Insiders"},
		{name: "Cursor", dir: "Cursor"},
		{name: "VSCodium", dir: "VSCodium"},
	}

	roots := make([]EditorStorageRoot, 0, len(variants))
	for _, variant := range variants {
		userDir := filepath.Join(base, variant.dir, "User")
		roots = append(roots, EditorStorageRoot{
			Name:                 variant.name,
			WorkspaceStoragePath: filepath.Join(userDir, "workspaceStorage"),
			GlobalStoragePath:    filepath.Join(userDir, "globalStorage"),
		})
	}

	return roots, nil
}

func detectVSCodeSessions() ([]SessionInfo, error) {
	roots, err := editorStorageRootsFn()
	if err != nil {
		return nil, err
	}

	var sessions []SessionInfo
	seen := make(map[string]struct{})
	for _, root := range roots {
		sessions = append(sessions, detectVSCodeWorkspaceSessions(root, seen)...)
		sessions = append(sessions, detectVSCodeLooseSessions(root, seen)...)
	}
	return sessions, nil
}

func detectVSCodeWorkspaceSessions(root EditorStorageRoot, seen map[string]struct{}) []SessionInfo {
	entries, err := os.ReadDir(root.WorkspaceStoragePath)
	if err != nil {
		return nil
	}

	var sessions []SessionInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		workspaceDir := filepath.Join(root.WorkspaceStoragePath, entry.Name())
		chatDir := filepath.Join(workspaceDir, "chatSessions")
		sessionFiles := listVSCodeSessionFiles(chatDir)
		if len(sessionFiles) == 0 {
			continue
		}

		cwd := readVSCodeWorkspacePath(workspaceDir)
		if cwd == "" {
			cwd = entry.Name()
		}

		for _, path := range sessionFiles {
			info, err := buildVSCodeSessionInfo(root, workspaceDir, entry.Name(), cwd, path)
			if err != nil {
				continue
			}
			key := info.SelectionKey()
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			sessions = append(sessions, info)
		}
	}

	return sessions
}

func detectVSCodeLooseSessions(root EditorStorageRoot, seen map[string]struct{}) []SessionInfo {
	type looseRoot struct {
		path string
		cwd  string
	}

	var candidates []looseRoot
	if root.GlobalStoragePath != "" {
		candidates = append(candidates,
			looseRoot{path: filepath.Join(root.GlobalStoragePath, "emptyWindowChatSessions"), cwd: "<empty window>"},
			looseRoot{path: filepath.Join(root.GlobalStoragePath, "github.copilot-chat", "sessions"), cwd: "<global session>"},
			looseRoot{path: filepath.Join(root.GlobalStoragePath, "github.copilot-chat", "history"), cwd: "<global history>"},
		)
	}

	var sessions []SessionInfo
	for _, candidate := range candidates {
		sessionFiles := listVSCodeSessionFiles(candidate.path)
		for _, path := range sessionFiles {
			info, err := buildVSCodeSessionInfo(root, candidate.path, filepath.Base(candidate.path), candidate.cwd, path)
			if err != nil {
				continue
			}
			key := info.SelectionKey()
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			sessions = append(sessions, info)
		}
	}

	return sessions
}

func listVSCodeSessionFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	type candidate struct {
		id    string
		path  string
		score int
	}
	chosen := make(map[string]candidate)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".json" && ext != ".jsonl" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ext)
		score := 1
		if ext == ".jsonl" {
			score = 2
		}
		path := filepath.Join(dir, entry.Name())
		current, ok := chosen[id]
		if !ok || score > current.score {
			chosen[id] = candidate{id: id, path: path, score: score}
		}
	}

	out := make([]string, 0, len(chosen))
	for _, entry := range chosen {
		out = append(out, entry.path)
	}
	sort.Strings(out)
	return out
}

func buildVSCodeSessionInfo(root EditorStorageRoot, storageRoot, workspaceID, cwd, sessionPath string) (SessionInfo, error) {
	data, err := readVSCodeSessionData(sessionPath)
	if err != nil {
		return SessionInfo{}, err
	}

	sessionID := data.SessionID
	if sessionID == "" {
		sessionID = strings.TrimSuffix(filepath.Base(sessionPath), filepath.Ext(sessionPath))
	}

	summary := strings.TrimSpace(data.CustomTitle)
	if summary == "" {
		summary = firstVSCodePrompt(data.Requests)
	}

	createdAt := unixMilliOrZero(data.CreationDate)
	updatedAt := unixMilliOrZero(data.LastMessageDate)
	if updatedAt.IsZero() {
		info, statErr := os.Stat(sessionPath)
		if statErr == nil {
			updatedAt = info.ModTime()
		}
	}

	format := SessionFormatVSCodeChatJSON
	if strings.EqualFold(filepath.Ext(sessionPath), ".jsonl") {
		format = SessionFormatVSCodeChatJSONL
	}

	return SessionInfo{
		SessionID:    sessionID,
		Source:       SessionSourceVSCode,
		SourceLabel:  root.Name,
		Format:       format,
		Cwd:          cwd,
		Summary:      summary,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
		HistoryPath:  sessionPath,
		LivePath:     sessionPath,
		WorkspaceID:  workspaceID,
		StorageRoot:  storageRoot,
		MetadataPath: filepath.Join(storageRoot, "workspace.json"),
	}, nil
}

func readVSCodeWorkspacePath(workspaceDir string) string {
	path := filepath.Join(workspaceDir, "workspace.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return ""
	}

	switch raw := decoded["folder"].(type) {
	case string:
		return decodeVSCodePath(raw)
	case map[string]interface{}:
		if uri, ok := raw["uri"].(string); ok {
			return decodeVSCodePath(uri)
		}
		if p, ok := raw["path"].(string); ok {
			return decodeVSCodePath(p)
		}
	}

	if workspace, ok := decoded["workspace"].(map[string]interface{}); ok {
		if folders, ok := workspace["folders"].([]interface{}); ok && len(folders) > 0 {
			if first, ok := folders[0].(map[string]interface{}); ok {
				if uri, ok := first["uri"].(string); ok {
					return decodeVSCodePath(uri)
				}
				if p, ok := first["path"].(string); ok {
					return decodeVSCodePath(p)
				}
			}
		}
	}

	if folders, ok := decoded["folders"].([]interface{}); ok && len(folders) > 0 {
		if first, ok := folders[0].(map[string]interface{}); ok {
			if uri, ok := first["uri"].(string); ok {
				return decodeVSCodePath(uri)
			}
			if p, ok := first["path"].(string); ok {
				return decodeVSCodePath(p)
			}
		}
	}

	return ""
}

func decodeVSCodePath(raw string) string {
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "file://") {
		u, err := url.Parse(raw)
		if err == nil {
			decodedPath, decodeErr := url.PathUnescape(u.Path)
			if decodeErr == nil && decodedPath != "" {
				if runtime.GOOS == "windows" && len(decodedPath) >= 3 && decodedPath[0] == '/' && decodedPath[2] == ':' {
					decodedPath = decodedPath[1:]
				}
				if u.Host != "" && runtime.GOOS == "windows" {
					return `\\` + u.Host + filepath.FromSlash(decodedPath)
				}
				return filepath.Clean(filepath.FromSlash(decodedPath))
			}
		}
	}
	return filepath.Clean(raw)
}

func firstVSCodePrompt(requests []vscodeRequestData) string {
	for _, request := range requests {
		text := strings.TrimSpace(request.Message.messageText())
		if text != "" {
			return text
		}
	}
	return ""
}

func unixMilliOrZero(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

func (m vscodeMessageData) messageText() string {
	if text := strings.TrimSpace(m.Text); text != "" {
		return text
	}
	var parts []string
	for _, part := range m.Parts {
		if text := strings.TrimSpace(part.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func readVSCodeSessionData(path string) (*vscodeSessionData, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return readVSCodeJSONSession(path)
	case ".jsonl":
		return readVSCodeJSONLSession(path)
	default:
		return nil, fmt.Errorf("unsupported session file: %s", path)
	}
}

func readVSCodeJSONSession(path string) (*vscodeSessionData, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var session vscodeSessionData
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

func readVSCodeJSONLSession(path string) (*vscodeSessionData, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var state interface{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	lineCount := 0

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		lineCount++

		var entry vscodeMutationEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, err
		}

		switch entry.Kind {
		case 0:
			if err := json.Unmarshal(entry.Value, &state); err != nil {
				return nil, err
			}
		case 1:
			value, err := decodeJSONValue(entry.Value)
			if err != nil {
				return nil, err
			}
			if err := applyVSCodeMutationSet(state, entry.Path, value); err != nil {
				return nil, err
			}
		case 2:
			values, err := decodeJSONValues(entry.Value)
			if err != nil {
				return nil, err
			}
			if err := applyVSCodeMutationPush(state, entry.Path, values, entry.Index); err != nil {
				return nil, err
			}
		case 3:
			if err := applyVSCodeMutationDelete(state, entry.Path); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unknown mutation kind %d", entry.Kind)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if lineCount == 0 {
		return nil, fmt.Errorf("empty mutation log")
	}

	serialized, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}

	var session vscodeSessionData
	if err := json.Unmarshal(serialized, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

func decodeJSONValue(raw json.RawMessage) (interface{}, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func decodeJSONValues(raw json.RawMessage) ([]interface{}, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var decoded []interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func applyVSCodeMutationSet(state interface{}, path []interface{}, value interface{}) error {
	parent, last, err := resolveVSCodeMutationTarget(state, path)
	if err != nil {
		return err
	}
	return assignVSCodeMutationValue(parent, last, value)
}

func applyVSCodeMutationDelete(state interface{}, path []interface{}) error {
	parent, last, err := resolveVSCodeMutationTarget(state, path)
	if err != nil {
		return err
	}

	switch target := parent.(type) {
	case map[string]interface{}:
		key, ok := last.(string)
		if !ok {
			return fmt.Errorf("delete target key is not a string")
		}
		delete(target, key)
		return nil
	case []interface{}:
		index, ok := last.(int)
		if !ok || index < 0 || index >= len(target) {
			return fmt.Errorf("delete target index is invalid")
		}
		target[index] = nil
		return nil
	default:
		return fmt.Errorf("delete target has unsupported type %T", parent)
	}
}

func applyVSCodeMutationPush(state interface{}, path []interface{}, values []interface{}, index *int) error {
	parent, last, err := resolveVSCodeMutationTarget(state, path)
	if err != nil {
		return err
	}

	switch target := parent.(type) {
	case map[string]interface{}:
		key, ok := last.(string)
		if !ok {
			return fmt.Errorf("push target key is not a string")
		}
		current, _ := target[key].([]interface{})
		if current == nil {
			current = []interface{}{}
		}
		if index != nil && *index >= 0 && *index <= len(current) {
			current = current[:*index]
		}
		if len(values) > 0 {
			current = append(current, values...)
		}
		target[key] = current
		return nil
	case []interface{}:
		segment, ok := last.(int)
		if !ok || segment < 0 {
			return fmt.Errorf("push target index is invalid")
		}
		if segment >= len(target) {
			return fmt.Errorf("push target index %d out of range", segment)
		}
		current, _ := target[segment].([]interface{})
		if current == nil {
			current = []interface{}{}
		}
		if index != nil && *index >= 0 && *index <= len(current) {
			current = current[:*index]
		}
		if len(values) > 0 {
			current = append(current, values...)
		}
		target[segment] = current
		return nil
	default:
		return fmt.Errorf("push target has unsupported type %T", parent)
	}
}

func resolveVSCodeMutationTarget(state interface{}, path []interface{}) (interface{}, interface{}, error) {
	if len(path) == 0 {
		return nil, nil, fmt.Errorf("mutation path is empty")
	}

	current := state
	for i := 0; i < len(path)-1; i++ {
		next, err := descendVSCodeMutationPath(current, path[i])
		if err != nil {
			return nil, nil, err
		}
		current = next
	}

	last, err := normalizeVSCodePathSegment(path[len(path)-1])
	if err != nil {
		return nil, nil, err
	}
	return current, last, nil
}

func descendVSCodeMutationPath(current interface{}, segment interface{}) (interface{}, error) {
	key, err := normalizeVSCodePathSegment(segment)
	if err != nil {
		return nil, err
	}

	switch target := current.(type) {
	case map[string]interface{}:
		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("object path segment is not a string")
		}
		return target[name], nil
	case []interface{}:
		index, ok := key.(int)
		if !ok || index < 0 || index >= len(target) {
			return nil, fmt.Errorf("array path segment is invalid")
		}
		return target[index], nil
	default:
		return nil, fmt.Errorf("cannot descend into %T", current)
	}
}

func assignVSCodeMutationValue(parent interface{}, last interface{}, value interface{}) error {
	switch target := parent.(type) {
	case map[string]interface{}:
		key, ok := last.(string)
		if !ok {
			return fmt.Errorf("object target key is not a string")
		}
		target[key] = value
		return nil
	case []interface{}:
		index, ok := last.(int)
		if !ok || index < 0 || index >= len(target) {
			return fmt.Errorf("array target index is invalid")
		}
		target[index] = value
		return nil
	default:
		return fmt.Errorf("set target has unsupported type %T", parent)
	}
}

func normalizeVSCodePathSegment(segment interface{}) (interface{}, error) {
	switch value := segment.(type) {
	case string:
		return value, nil
	case float64:
		return int(value), nil
	case int:
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported path segment type %T", segment)
	}
}

func LoadSessionHistory(info SessionInfo) ([]Turn, error) {
	switch info.Source {
	case SessionSourceVSCode:
		return loadVSCodeHistory(info.HistoryPath)
	case SessionSourceCLI:
		if info.HistoryPath != "" {
			return LoadHistory(info.HistoryPath)
		}
		return LoadHistory(info.EventsPath)
	default:
		if info.HistoryPath != "" {
			return loadVSCodeHistory(info.HistoryPath)
		}
		return LoadHistory(info.EventsPath)
	}
}

func loadVSCodeHistory(path string) ([]Turn, error) {
	data, err := readVSCodeSessionData(path)
	if err != nil {
		return nil, err
	}

	observed := observedTurnsFromVSCodeRequests(data.Requests, false)
	turns := make([]Turn, 0, len(observed))
	for _, turn := range observed {
		if turn.ReasoningText == "" && turn.ContentText == "" {
			continue
		}
		turns = append(turns, Turn{
			ID:            turn.ID,
			UserMessage:   turn.UserMessage,
			ReasoningText: turn.ReasoningText,
			Response:      turn.ContentText,
			Timestamp:     turn.Timestamp,
		})
	}
	return turns, nil
}

func observedTurnsFromVSCodeRequests(requests []vscodeRequestData, skipEmpty bool) []vscodeObservedTurn {
	turns := make([]vscodeObservedTurn, 0, len(requests))
	for i, request := range requests {
		userMsg := strings.TrimSpace(request.Message.messageText())
		reasoning, content := extractVSCodeResponseText(request.Response)
		if skipEmpty && userMsg == "" && reasoning == "" && content == "" {
			continue
		}
		id := strings.TrimSpace(request.RequestID)
		if id == "" {
			id = fmt.Sprintf("request-%d", i+1)
		}
		turns = append(turns, vscodeObservedTurn{
			ID:            id,
			UserMessage:   userMsg,
			ReasoningText: reasoning,
			ContentText:   content,
			Timestamp:     unixMilliOrZero(request.Timestamp),
		})
	}
	return turns
}

func extractVSCodeResponseText(parts []json.RawMessage) (string, string) {
	var reasoning strings.Builder
	var content strings.Builder

	for _, raw := range parts {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			continue
		}

		if raw[0] == '"' {
			var text string
			if err := json.Unmarshal(raw, &text); err == nil {
				content.WriteString(text)
			}
			continue
		}

		var part vscodeResponsePart
		if err := json.Unmarshal(raw, &part); err != nil {
			continue
		}

		text := extractVSCodeResponseValue(part)
		if text == "" {
			continue
		}

		switch part.Kind {
		case "thinking":
			reasoning.WriteString(text)
		case "", "markdownContent":
			content.WriteString(text)
		}
	}

	return reasoning.String(), content.String()
}

func extractVSCodeResponseValue(part vscodeResponsePart) string {
	if part.Content != "" {
		return part.Content
	}
	if part.Text != "" {
		return part.Text
	}
	if len(part.Value) == 0 || bytes.Equal(bytes.TrimSpace(part.Value), []byte("null")) {
		return ""
	}

	var text string
	if err := json.Unmarshal(part.Value, &text); err == nil {
		return text
	}

	var object map[string]interface{}
	if err := json.Unmarshal(part.Value, &object); err == nil {
		for _, key := range []string{"content", "text", "value"} {
			if raw, ok := object[key].(string); ok {
				return raw
			}
		}
	}

	return ""
}

func NewLiveWatcher(info SessionInfo) (LiveWatcher, error) {
	switch info.Source {
	case SessionSourceVSCode:
		if info.LivePath == "" {
			return nil, nil
		}
		watcher := NewVSCodeWatcher(info.SessionID, info.LivePath, info.Format)
		if err := watcher.Start(); err != nil {
			return nil, err
		}
		return watcher, nil
	default:
		path := info.LivePath
		if path == "" {
			path = info.EventsPath
		}
		if path == "" {
			return nil, nil
		}
		watcher := NewWatcher(info.SessionID, path)
		if err := watcher.Start(); err != nil {
			return nil, err
		}
		return watcher, nil
	}
}

func NewVSCodeWatcher(sessionID, filePath string, format SessionFormat) *VSCodeWatcher {
	return &VSCodeWatcher{
		filePath:  filePath,
		sessionID: sessionID,
		format:    format,
		ch:        make(chan ReasoningMsg, 32),
		dbgCh:     make(chan string, 64),
		done:      make(chan struct{}),
	}
}

func (w *VSCodeWatcher) Chan() <-chan ReasoningMsg { return w.ch }

func (w *VSCodeWatcher) DebugChan() <-chan string { return w.dbgCh }

func (w *VSCodeWatcher) Stop() {
	select {
	case <-w.done:
		return
	default:
		close(w.done)
	}
	w.stopIdleFinalize()
}

func (w *VSCodeWatcher) Start() error {
	initialTurns, err := loadVSCodeObservedTurns(w.filePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	w.lastTurns = initialTurns

	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := fw.Add(filepath.Dir(w.filePath)); err != nil {
		fw.Close()
		return err
	}

	go w.loop(fw)
	return nil
}

func (w *VSCodeWatcher) loop(fw *fsnotify.Watcher) {
	defer fw.Close()

	targetPath := filepath.Clean(w.filePath)
	flushCh := make(chan struct{}, 1)
	var debounceTimer *time.Timer

	resetDebounce := func() {
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
		debounceTimer = time.AfterFunc(debounceDuration, func() {
			select {
			case flushCh <- struct{}{}:
			default:
			}
		})
	}

	for {
		select {
		case <-w.done:
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return
		case evt, ok := <-fw.Events:
			if !ok {
				return
			}
			if filepath.Clean(evt.Name) != targetPath {
				continue
			}
			if evt.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			resetDebounce()
		case <-flushCh:
			w.flush()
		case err, ok := <-fw.Errors:
			if !ok || err == nil {
				continue
			}
			w.sendDbg(fmt.Sprintf("fsnotify error: %v", err))
		}
	}
}

func (w *VSCodeWatcher) flush() {
	currentTurns, err := loadVSCodeObservedTurns(w.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			w.sendDbg("session file disappeared while watching")
			return
		}
		w.sendDbg(fmt.Sprintf("session reload failed: %v", err))
		return
	}

	w.mu.Lock()
	previousTurns := append([]vscodeObservedTurn(nil), w.lastTurns...)
	w.lastTurns = append([]vscodeObservedTurn(nil), currentTurns...)
	w.mu.Unlock()

	if turnsReordered(previousTurns, currentTurns) {
		w.stopIdleFinalize()
		w.sendDbg("session order changed; live baseline reset")
		return
	}

	if len(currentTurns) > len(previousTurns) && len(previousTurns) > 0 {
		prevLast := previousTurns[len(previousTurns)-1]
		currPrevLast := currentTurns[len(previousTurns)-1]
		if sameObservedTurn(prevLast, currPrevLast) && hasObservedOutput(currPrevLast) {
			w.emitFinal(currPrevLast)
		}
	}

	emitted := false
	for i, curr := range currentTurns {
		if i >= len(previousTurns) {
			reasoningDelta, contentDelta, replacement := diffObservedTurn(vscodeObservedTurn{}, curr)
			if replacement {
				w.emitFinal(curr)
			} else if reasoningDelta != "" || contentDelta != "" {
				w.emitPartial(curr, reasoningDelta, contentDelta)
			}
			if hasObservedOutput(curr) {
				w.scheduleFinalize(curr)
			}
			emitted = emitted || replacement || reasoningDelta != "" || contentDelta != ""
			continue
		}

		prev := previousTurns[i]
		if !sameObservedTurn(prev, curr) {
			w.stopIdleFinalize()
			w.sendDbg("session turn identity changed; live baseline reset")
			return
		}

		reasoningDelta, contentDelta, replacement := diffObservedTurn(prev, curr)
		if replacement {
			w.emitFinal(curr)
			if hasObservedOutput(curr) {
				w.scheduleFinalize(curr)
			}
			emitted = true
			continue
		}
		if reasoningDelta == "" && contentDelta == "" {
			continue
		}
		w.emitPartial(curr, reasoningDelta, contentDelta)
		if hasObservedOutput(curr) {
			w.scheduleFinalize(curr)
		}
		emitted = true
	}

	if emitted {
		w.sendDbg(fmt.Sprintf("session file updated: %d turn(s)", len(currentTurns)))
	}
}

func loadVSCodeObservedTurns(path string) ([]vscodeObservedTurn, error) {
	data, err := readVSCodeSessionData(path)
	if err != nil {
		return nil, err
	}
	return observedTurnsFromVSCodeRequests(data.Requests, true), nil
}

func turnsReordered(previousTurns, currentTurns []vscodeObservedTurn) bool {
	limit := len(previousTurns)
	if len(currentTurns) < limit {
		limit = len(currentTurns)
	}
	for i := 0; i < limit; i++ {
		if !sameObservedTurn(previousTurns[i], currentTurns[i]) {
			return true
		}
	}
	return false
}

func sameObservedTurn(a, b vscodeObservedTurn) bool {
	if a.ID != "" && b.ID != "" {
		return a.ID == b.ID
	}
	return a.UserMessage == b.UserMessage && a.Timestamp.Equal(b.Timestamp)
}

func diffObservedTurn(previousTurn, currentTurn vscodeObservedTurn) (string, string, bool) {
	reasoningDelta, reasoningReplaced := diffObservedField(previousTurn.ReasoningText, currentTurn.ReasoningText)
	contentDelta, contentReplaced := diffObservedField(previousTurn.ContentText, currentTurn.ContentText)
	return reasoningDelta, contentDelta, reasoningReplaced || contentReplaced
}

func diffObservedField(previousValue, currentValue string) (string, bool) {
	switch {
	case currentValue == previousValue:
		return "", false
	case strings.HasPrefix(currentValue, previousValue):
		return currentValue[len(previousValue):], false
	default:
		return currentValue, true
	}
}

func hasObservedOutput(turn vscodeObservedTurn) bool {
	return turn.ReasoningText != "" || turn.ContentText != ""
}

func (w *VSCodeWatcher) emitPartial(turn vscodeObservedTurn, reasoningDelta, contentDelta string) {
	select {
	case <-w.done:
		return
	default:
	}

	select {
	case w.ch <- ReasoningMsg{
		SessionID:     w.sessionID,
		TurnID:        turn.ID,
		UserMessage:   turn.UserMessage,
		ReasoningText: reasoningDelta,
		ContentText:   contentDelta,
		Timestamp:     turn.Timestamp,
		Partial:       true,
	}:
	default:
	}
}

func (w *VSCodeWatcher) emitFinal(turn vscodeObservedTurn) {
	select {
	case <-w.done:
		return
	default:
	}

	select {
	case w.ch <- ReasoningMsg{
		SessionID:     w.sessionID,
		TurnID:        turn.ID,
		UserMessage:   turn.UserMessage,
		ReasoningText: turn.ReasoningText,
		ContentText:   turn.ContentText,
		Timestamp:     turn.Timestamp,
	}:
	default:
	}
}

func (w *VSCodeWatcher) scheduleFinalize(turn vscodeObservedTurn) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.idleFinalize != nil {
		w.idleFinalize.Stop()
	}
	copyTurn := turn
	w.pendingFinalize = &copyTurn
	w.idleFinalize = time.AfterFunc(vscodeWatcherIdleFinalizeDuration, w.finalizePending)
}

func (w *VSCodeWatcher) stopIdleFinalize() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.idleFinalize != nil {
		w.idleFinalize.Stop()
		w.idleFinalize = nil
	}
	w.pendingFinalize = nil
}

func (w *VSCodeWatcher) finalizePending() {
	w.mu.Lock()
	pending := w.pendingFinalize
	w.pendingFinalize = nil
	w.idleFinalize = nil
	w.mu.Unlock()

	if pending == nil {
		return
	}
	w.emitFinal(*pending)
}

func (w *VSCodeWatcher) sendDbg(msg string) {
	select {
	case <-w.done:
		return
	default:
	}

	select {
	case w.dbgCh <- msg:
	default:
	}
}
