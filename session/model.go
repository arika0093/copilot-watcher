package session

import (
	"encoding/json"
	"time"
)

type SessionSource string

const (
	SessionSourceCLI    SessionSource = "cli"
	SessionSourceVSCode SessionSource = "vscode"
)

type SessionFormat string

const (
	SessionFormatCLIEventsJSONL    SessionFormat = "cli-events-jsonl"
	SessionFormatVSCodeChatJSON    SessionFormat = "vscode-chat-json"
	SessionFormatVSCodeChatJSONL   SessionFormat = "vscode-chat-jsonl"
	SessionFormatVSCodeLegacyState SessionFormat = "vscode-legacy-state"
)

// SessionEvent represents a single event in events.jsonl
type SessionEvent struct {
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	ID        string          `json:"id"`
	Timestamp time.Time       `json:"timestamp"`
	ParentID  *string         `json:"parentId"`
}

// AssistantMessageData is the data payload for type=="assistant.message"
type AssistantMessageData struct {
	MessageID     string `json:"messageId"`
	Content       string `json:"content"`
	InteractionID string `json:"interactionId"`
	ReasoningText string `json:"reasoningText"`
	OutputTokens  int    `json:"outputTokens"`
}

// UserMessageData is the data payload for type=="user.message"
type UserMessageData struct {
	Content string `json:"content"`
	Source  string `json:"source"`
}

// SessionStartData is the data payload for type=="session.start"
type SessionStartData struct {
	SessionID      string    `json:"sessionId"`
	StartTime      time.Time `json:"startTime"`
	CopilotVersion string    `json:"copilotVersion"`
}

// WorkspaceConfig represents workspace.yaml content
type WorkspaceConfig struct {
	ID        string    `json:"id" yaml:"id"`
	Cwd       string    `json:"cwd" yaml:"cwd"`
	Summary   string    `json:"summary" yaml:"summary"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt time.Time `json:"updated_at" yaml:"updated_at"`
}

// SessionInfo represents a detected Copilot CLI session (active or inactive)
type SessionInfo struct {
	SessionID    string
	Source       SessionSource
	SourceLabel  string
	Format       SessionFormat
	Active       bool // true if the session process is currently running
	PID          int  // non-zero only when Active==true
	Cwd          string
	Summary      string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	EventsPath   string
	HistoryPath  string
	LivePath     string
	WorkspaceID  string
	StorageRoot  string
	MetadataPath string
}

// Turn represents a single user+assistant exchange
type Turn struct {
	ID            string
	UserMessage   string
	ReasoningText string
	Response      string
	Translation   string // AI translation (populated later)
	Timestamp     time.Time
}

func (s SessionInfo) DisplaySource() string {
	if s.SourceLabel != "" {
		return s.SourceLabel
	}
	switch s.Source {
	case SessionSourceCLI:
		return "CLI"
	case SessionSourceVSCode:
		return "VS Code"
	default:
		return "Unknown"
	}
}

func (s SessionInfo) SelectionKey() string {
	return string(s.Source) + "|" + s.SessionID + "|" + s.HistoryPath + "|" + s.EventsPath
}

func (s SessionInfo) SupportsLive() bool {
	return s.LivePath != ""
}

// ParseAssistantMessage decodes AssistantMessageData from a raw SessionEvent
func ParseAssistantMessage(e SessionEvent) (*AssistantMessageData, error) {
	var d AssistantMessageData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// ParseUserMessage decodes UserMessageData from a raw SessionEvent
func ParseUserMessage(e SessionEvent) (*UserMessageData, error) {
	var d UserMessageData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		return nil, err
	}
	return &d, nil
}
