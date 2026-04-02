package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ReasoningMsg carries a new reasoning text detected from events.jsonl
type ReasoningMsg struct {
	SessionID     string
	UserMessage   string
	ReasoningText string
	Timestamp     time.Time
}

// Watcher tails events.jsonl and emits ReasoningMsg via a channel.
type Watcher struct {
	eventsPath string
	sessionID  string
	ch         chan ReasoningMsg
	dbgCh      chan string
	done       chan struct{}
}

// NewWatcher creates a Watcher for the given events.jsonl path.
func NewWatcher(sessionID, eventsPath string) *Watcher {
	return &Watcher{
		eventsPath: eventsPath,
		sessionID:  sessionID,
		ch:         make(chan ReasoningMsg, 32),
		dbgCh:      make(chan string, 64),
		done:       make(chan struct{}),
	}
}

// Chan returns the channel on which new ReasoningMsg values are sent.
func (w *Watcher) Chan() <-chan ReasoningMsg { return w.ch }

// DebugChan returns the channel on which debug/trace messages are sent.
func (w *Watcher) DebugChan() <-chan string { return w.dbgCh }

// Stop shuts down the watcher goroutine.
func (w *Watcher) Stop() {
	close(w.done)
}

// Start begins watching events.jsonl. Seeks to end of file first (skips history).
// Call LoadHistory separately for past turns.
func (w *Watcher) Start() error {
	f, err := os.Open(w.eventsPath)
	if err != nil {
		return err
	}

	// Seek to end so we only see new events
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		f.Close()
		return err
	}
	if err := watcher.Add(w.eventsPath); err != nil {
		f.Close()
		watcher.Close()
		return err
	}

	go w.readLoop(f, watcher)
	return nil
}

func (w *Watcher) sendDbg(msg string) {
	select {
	case w.dbgCh <- msg:
	default:
	}
}

func (w *Watcher) readLoop(f *os.File, fw *fsnotify.Watcher) {
	defer f.Close()
	defer fw.Close()

	reader := bufio.NewReader(f)
	var pendingUser string
	lineCount := 0

	for {
		select {
		case <-w.done:
			return
		case evt, ok := <-fw.Events:
			if !ok {
				return
			}
			if evt.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			// Read all newly appended lines
			linesRead := 0
			for {
				line, err := reader.ReadBytes('\n')
				if len(line) == 0 {
					break
				}
				linesRead++
				lineCount++
				w.processLine(line, &pendingUser)
				if err == io.EOF {
					break
				}
			}
			if linesRead > 0 {
				w.sendDbg(fmt.Sprintf("events.jsonl updated: +%d line(s) (total %d)", linesRead, lineCount))
			}
		case err, ok := <-fw.Errors:
			if !ok || err == nil {
				continue
			}
			w.sendDbg(fmt.Sprintf("fsnotify error: %v", err))
		}
	}
}

func (w *Watcher) processLine(line []byte, pendingUser *string) {
	var evt SessionEvent
	if err := json.Unmarshal(line, &evt); err != nil {
		return
	}
	switch evt.Type {
	case "user.message":
		d, err := ParseUserMessage(evt)
		if err == nil {
			*pendingUser = d.Content
			w.sendDbg(fmt.Sprintf("user.message: %d chars", len(d.Content)))
		}
	case "assistant.message":
		d, err := ParseAssistantMessage(evt)
		if err != nil {
			return
		}
		// Use reasoningText if present; fall back to content (response text)
		text := d.ReasoningText
		isReasoning := text != ""
		if text == "" {
			text = d.Content
		}
		if text == "" {
			return
		}
		kind := "content"
		if isReasoning {
			kind = "reasoningText"
		}
		w.sendDbg(fmt.Sprintf("assistant.message (%s): %d chars → queuing translation", kind, len(text)))
		select {
		case w.ch <- ReasoningMsg{
			SessionID:     w.sessionID,
			UserMessage:   *pendingUser,
			ReasoningText: text,
			Timestamp:     evt.Timestamp,
		}:
		default:
		}
	case "assistant.turn_end":
		w.sendDbg("assistant.turn_end received")
	}
}
