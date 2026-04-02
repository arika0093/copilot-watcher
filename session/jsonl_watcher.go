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

// debounceDuration is how long the watcher waits for file writes to settle
// before processing new lines. This prevents partial-line reads when
// events.jsonl is written incrementally.
const debounceDuration = 300 * time.Millisecond

// ReasoningMsg carries a new reasoning text detected from events.jsonl
type ReasoningMsg struct {
	SessionID     string
	UserMessage   string
	ReasoningText string
	ContentText   string // non-reasoning AI response content
	Timestamp     time.Time
	Partial       bool // true when this is a streaming snippet, not a complete turn
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
	var partial []byte // incomplete line buffered across reads
	var pendingUser string
	var pendingReasoning string
	var pendingContent string
	var pendingTS time.Time
	lineCount := 0

	// flushCh is signalled by the debounce timer to process pending file data.
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

	flush := func() {
		linesRead := 0
		for {
			chunk, err := reader.ReadBytes('\n')
			// Prepend any previously buffered partial data.
			if len(partial) > 0 {
				chunk = append(partial, chunk...)
				partial = nil
			}
			if len(chunk) == 0 {
				break
			}
			if err == io.EOF {
				// Incomplete line — buffer it and wait for more data.
				partial = append([]byte(nil), chunk...)
				break
			}
			linesRead++
			lineCount++
			w.processLine(chunk, &pendingUser, &pendingReasoning, &pendingContent, &pendingTS)
		}
		if linesRead > 0 {
			w.sendDbg(fmt.Sprintf("events.jsonl flushed: +%d line(s) (total %d)", linesRead, lineCount))
		}
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
			if evt.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			resetDebounce()
		case <-flushCh:
			flush()
		case err, ok := <-fw.Errors:
			if !ok || err == nil {
				continue
			}
			w.sendDbg(fmt.Sprintf("fsnotify error: %v", err))
		}
	}
}

func (w *Watcher) processLine(line []byte, pendingUser *string, pendingReasoning *string, pendingContent *string, pendingTS *time.Time) {
	var evt SessionEvent
	if err := json.Unmarshal(line, &evt); err != nil {
		return
	}
	switch evt.Type {
	case "user.message":
		d, err := ParseUserMessage(evt)
		if err == nil {
			*pendingUser = d.Content
			*pendingReasoning = ""
			*pendingContent = ""
			*pendingTS = evt.Timestamp
			w.sendDbg(fmt.Sprintf("user.message: %d chars", len(d.Content)))
		}
	case "assistant.message":
		d, err := ParseAssistantMessage(evt)
		if err != nil {
			return
		}
		if d.ReasoningText != "" {
			// Emit partial msg with just the new snippet before accumulating
			ts := *pendingTS
			if ts.IsZero() {
				ts = time.Now()
			}
			select {
			case w.ch <- ReasoningMsg{
				SessionID:     w.sessionID,
				UserMessage:   *pendingUser,
				ReasoningText: d.ReasoningText,
				Timestamp:     ts,
				Partial:       true,
			}:
			default:
			}
			*pendingReasoning += d.ReasoningText
			w.sendDbg(fmt.Sprintf("assistant.message (reasoningText): %d chars accumulated", len(*pendingReasoning)))
		}
		if d.Content != "" {
			*pendingContent += d.Content
			w.sendDbg(fmt.Sprintf("assistant.message (content): %d chars accumulated", len(*pendingContent)))
		}
	case "assistant.turn_end":
		w.sendDbg("assistant.turn_end received")
		if *pendingUser != "" && (*pendingReasoning != "" || *pendingContent != "") {
			ts := *pendingTS
			if ts.IsZero() {
				ts = time.Now()
			}
			select {
			case w.ch <- ReasoningMsg{
				SessionID:     w.sessionID,
				UserMessage:   *pendingUser,
				ReasoningText: *pendingReasoning,
				ContentText:   *pendingContent,
				Timestamp:     ts,
			}:
				w.sendDbg(fmt.Sprintf("turn emitted (reasoning=%d, content=%d chars)", len(*pendingReasoning), len(*pendingContent)))
			default:
			}
			*pendingReasoning = ""
			*pendingContent = ""
		}
	}
}
