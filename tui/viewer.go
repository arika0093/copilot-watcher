package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/copilot-watcher/copilot-watcher/session"
	"github.com/copilot-watcher/copilot-watcher/terminal"
	"github.com/copilot-watcher/copilot-watcher/translator"
)

// ── Tab types ──────────────────────────────────────────────────────────────────

type TabID int

const (
	TabRealtime        TabID = iota // events.jsonl based
	TabLiveStream                   // terminal fd/PTY near-real-time
	TabHistorySessions              // per-session AI summary
	TabHistoryAll                   // combined summary of all sessions
	TabDebug                        // initialization log and debug messages
)

// ── Tea messages ───────────────────────────────────────────────────────────────

type ReasoningDetectedMsg session.ReasoningMsg
type TerminalChunkMsg terminal.TerminalMsg

// Real-time tab translation
type RTChunkMsg struct {
	Idx  int
	Text string
}
type RTDoneMsg struct{ Idx int }

// Live Stream tab translation
type TSChunkMsg struct {
	Idx  int
	Text string
}
type TSDoneMsg struct{ Idx int }
type tsDebounceMsg struct{ seq int }

// History: Sessions tab
type HSLoadedMsg struct {
	Entries []hsEntry
	Err     error
}

type hsNavDebounceMsg struct{ seq int }

// teeCheckMsg fires periodically to detect a newly-appeared tee stream file.
type teeCheckMsg struct{}

type HSChunkMsg struct {
	Idx  int
	Gen  int
	Text string
}
type HSDoneMsg struct {
	Idx int
	Gen int
}

// History: All tab
type HALoadedMsg struct {
	Reasoning string
	Err       error
}
type HAChunkMsg struct{ Text string }
type HADoneMsg struct{}

// Common
type HistoryLoadedMsg struct {
	Turns []session.Turn
	Err   error
}
type StatusMsg struct {
	Text string
	OK   bool
}
type InitStepMsg struct {
	Step string
	OK   bool
	Err  error
}
type BackToListMsg struct{}

// WatcherDebugMsg carries debug messages from the JSONL file watcher.
type WatcherDebugMsg struct{ Text string }

type hsEntry struct {
	Info      session.SessionInfo
	Reasoning string
}

// ── viewTurn: single thought + translation unit ────────────────────────────────

type viewTurn struct {
	turnNum     int
	label       string // for history tabs: session ID / "All Sessions"
	userMsg     string
	reasoning   string
	translation strings.Builder
	errMsg      string // non-empty when the translation API returned an error
	translating bool
	done        bool
	timestamp   time.Time
}

func (t *viewTurn) translationStr() string { return t.translation.String() }

// ── ViewerModel ────────────────────────────────────────────────────────────────

type ViewerModel struct {
	info        session.SessionInfo
	trans       *translator.Translator
	allSessions []session.SessionInfo

	activeTab TabID

	// Real-time tab (events.jsonl)
	rtTurns  []*viewTurn
	rtQ      []int
	rtCh     <-chan string
	rtCancel context.CancelFunc

	// Live Stream tab (terminal fd/PTY)
	tsTurns  []*viewTurn
	tsBuffer strings.Builder
	tsSeq    int
	tsCh     <-chan string
	tsCancel context.CancelFunc

	// History: Sessions tab
	hsTurns      []*viewTurn
	hsCursor     int // current session index (0=newest)
	hsGeneration int // incremented on each new translation to discard stale msgs
	hsCh         <-chan string
	hsCancel     context.CancelFunc
	hsCache      map[string]string // sessionID → completed translation cache
	hsNavSeq     int

	// History: All tab
	haEntry  *viewTurn
	haCh     <-chan string
	haCancel context.CancelFunc

	// Scroll state per tab (line index from top, 0 = newest)
	scroll map[TabID]int

	width        int
	height       int
	status       string
	statusOK     bool
	initLog      []InitStepMsg // kept for the startup init panel only
	debugLog     []string      // unified debug log (all events with timestamps)
	outputLang   string        // display-only; actual language is in trans
	outputFormat string        // display-only; actual format is in trans
	ready        bool

	spinnerTick int

	startTime   time.Time // when the viewer was created (used for tee-file freshness check)
	teeFilePath string    // resolved path to ~/.copilot-watcher-stream
	hasTmux     bool      // true if tmux is available on PATH

	watcher    *session.Watcher
	termReader *terminal.Reader
}

func NewViewerModel(info session.SessionInfo, trans *translator.Translator, allSessions []session.SessionInfo) ViewerModel {
	teeFilePath := ""
	if home, err := os.UserHomeDir(); err == nil {
		teeFilePath = home + "/.copilot-watcher-stream"
	}
	_, hasTmux := exec.LookPath("tmux")
	return ViewerModel{
		info:         info,
		trans:        trans,
		allSessions:  allSessions,
		activeTab:    TabRealtime,
		scroll:       map[TabID]int{},
		hsCache:      map[string]string{},
		status:       "Initializing...",
		statusOK:     false,
		outputLang:   trans.GetLanguage(),
		outputFormat: trans.GetFormat(),
		startTime:    time.Now(),
		teeFilePath:  teeFilePath,
		hasTmux:      hasTmux == nil,
	}
}

func (m ViewerModel) Init() tea.Cmd {
	return tea.Batch(
		loadHistoryCmd(m.info.EventsPath),
		func() tea.Msg {
			return InitStepMsg{Step: "Session selected: " + m.info.SessionID[:8], OK: true}
		},
		spinnerCmd(),
		teeCheckCmd(),
	)
}

// ── Async load commands ────────────────────────────────────────────────────────

func loadHistoryCmd(path string) tea.Cmd {
	return func() tea.Msg {
		turns, err := session.LoadHistory(path)
		return HistoryLoadedMsg{Turns: turns, Err: err}
	}
}

func loadHistorySessionsCmd(allSessions []session.SessionInfo) tea.Cmd {
	return func() tea.Msg {
		// Always reload all sessions for history tabs
		all, err := session.LoadAllSessions()
		if err != nil {
			return HSLoadedMsg{Err: err}
		}
		if len(all) == 0 {
			all = allSessions
		}
		var entries []hsEntry
		for _, s := range all {
			turns, err := session.LoadHistory(s.EventsPath)
			if err != nil || len(turns) == 0 {
				continue
			}
			var sb strings.Builder
			for _, t := range turns {
				if t.ReasoningText != "" {
					sb.WriteString(t.ReasoningText)
					sb.WriteString("\n---\n")
				}
			}
			if sb.Len() == 0 {
				continue
			}
			entries = append(entries, hsEntry{Info: s, Reasoning: sb.String()})
		}
		return HSLoadedMsg{Entries: entries}
	}
}

func loadHistoryAllCmd(allSessions []session.SessionInfo) tea.Cmd {
	return func() tea.Msg {
		all, err := session.LoadAllSessions()
		if err != nil {
			return HALoadedMsg{Err: err}
		}
		if len(all) == 0 {
			all = allSessions
		}
		var sb strings.Builder
		for _, s := range all {
			turns, err := session.LoadHistory(s.EventsPath)
			if err != nil || len(turns) == 0 {
				continue
			}
			sb.WriteString(fmt.Sprintf("=== Session %s (%s) ===\n", s.SessionID[:8], s.Cwd))
			for _, t := range turns {
				if t.ReasoningText != "" {
					sb.WriteString(t.ReasoningText)
					sb.WriteString("\n---\n")
				}
			}
		}
		return HALoadedMsg{Reasoning: sb.String()}
	}
}

// ── Channel wait commands ──────────────────────────────────────────────────────

func (m *ViewerModel) WatchChannels() tea.Cmd {
	var cmds []tea.Cmd
	if m.watcher != nil {
		cmds = append(cmds, waitForReasoning(m.watcher.Chan()))
		cmds = append(cmds, waitForWatcherDebug(m.watcher.DebugChan()))
	}
	if m.termReader != nil {
		cmds = append(cmds, waitForTerminal(m.termReader.Chan()))
	}
	return tea.Batch(cmds...)
}

func waitForWatcherDebug(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return WatcherDebugMsg{Text: msg}
	}
}

func waitForReasoning(ch <-chan session.ReasoningMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return ReasoningDetectedMsg(msg)
	}
}

func waitForTerminal(ch <-chan terminal.TerminalMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return TerminalChunkMsg(msg)
	}
}

func waitForRTChunk(idx int, ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		text, ok := <-ch
		if !ok {
			return RTDoneMsg{Idx: idx}
		}
		return RTChunkMsg{Idx: idx, Text: text}
	}
}

func waitForHSChunk(idx, gen int, ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		text, ok := <-ch
		if !ok {
			return HSDoneMsg{Idx: idx, Gen: gen}
		}
		return HSChunkMsg{Idx: idx, Gen: gen, Text: text}
	}
}

func waitForTSChunk(idx int, ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		text, ok := <-ch
		if !ok {
			return TSDoneMsg{Idx: idx}
		}
		return TSChunkMsg{Idx: idx, Text: text}
	}
}

func tsDebounceCmd(seq int) tea.Cmd {
	return tea.Tick(2*time.Second, func(_ time.Time) tea.Msg { return tsDebounceMsg{seq: seq} })
}

func hsNavDebounceCmd(seq int) tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(_ time.Time) tea.Msg { return hsNavDebounceMsg{seq: seq} })
}

func teeCheckCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(_ time.Time) tea.Msg { return teeCheckMsg{} })
}

func waitForHAChunk(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		text, ok := <-ch
		if !ok {
			return HADoneMsg{}
		}
		return HAChunkMsg{Text: text}
	}
}

// ── Translation starters ───────────────────────────────────────────────────────

func (m *ViewerModel) startNextRT() tea.Cmd {
	for len(m.rtQ) > 0 {
		idx := m.rtQ[0]
		m.rtQ = m.rtQ[1:]
		if idx >= len(m.rtTurns) {
			continue
		}
		t := m.rtTurns[idx]
		if t.done || t.translating || t.reasoning == "" {
			continue
		}
		if m.rtCancel != nil {
			m.rtCancel()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		m.rtCancel = cancel
		ch, err := m.trans.Translate(ctx, t.reasoning)
		if err != nil {
			t.translation.WriteString(fmt.Sprintf("Translation error: %v", err))
			t.done = true
			return m.startNextRT()
		}
		t.translating = true
		m.rtCh = ch
		return tea.Batch(waitForRTChunk(idx, ch), spinnerCmd())
	}
	return nil
}

// startHSForCursor starts translation for the session at hsCursor.
// Cancels any in-progress translation, resets the turn, and starts fresh.
func (m *ViewerModel) startHSForCursor() tea.Cmd {
	if m.hsCursor >= len(m.hsTurns) {
		return nil
	}
	t := m.hsTurns[m.hsCursor]
	if t.done {
		return nil // already fully translated; show cached result
	}
	// Cancel any in-flight translation
	if m.hsCancel != nil {
		m.hsCancel()
		m.hsCancel = nil
	}
	// Reset partial state for this turn
	t.translating = false
	t.translation.Reset()
	if t.reasoning == "" {
		return nil
	}
	m.hsGeneration++ // invalidate any stale chunk messages
	gen := m.hsGeneration
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	m.hsCancel = cancel
	ch, err := m.trans.SummarizeSession(ctx, t.label, t.reasoning)
	if err != nil {
		t.translation.WriteString(fmt.Sprintf("エラー: %v", err))
		t.done = true
		return nil
	}
	t.translating = true
	m.hsCh = ch
	return tea.Batch(waitForHSChunk(m.hsCursor, gen, ch), spinnerCmd())
}

func (m *ViewerModel) startHA() tea.Cmd {
	if m.haEntry == nil || m.haEntry.done || m.haEntry.translating {
		return nil
	}
	if m.haCancel != nil {
		m.haCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	m.haCancel = cancel
	ch, err := m.trans.SummarizeAll(ctx, m.haEntry.reasoning)
	if err != nil {
		m.haEntry.translation.WriteString(fmt.Sprintf("エラー: %v", err))
		m.haEntry.done = true
		return nil
	}
	m.haEntry.translating = true
	m.haCh = ch
	return tea.Batch(waitForHAChunk(ch), spinnerCmd())
}

// startTS flushes the terminal buffer into a new Live Stream turn and translates it.
func (m *ViewerModel) startTS() tea.Cmd {
	raw := strings.TrimSpace(m.tsBuffer.String())
	m.tsBuffer.Reset()
	if raw == "" {
		return nil
	}
	idx := len(m.tsTurns)
	vt := &viewTurn{
		turnNum:   idx + 1,
		label:     "Live Stream",
		reasoning: raw,
		timestamp: time.Now(),
	}
	m.tsTurns = append(m.tsTurns, vt)
	if m.tsCancel != nil {
		m.tsCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	m.tsCancel = cancel
	ch, err := m.trans.Translate(ctx, raw)
	if err != nil {
		vt.translation.WriteString(fmt.Sprintf("Translation error: %v", err))
		vt.done = true
		return nil
	}
	vt.translating = true
	m.tsCh = ch
	return tea.Batch(waitForTSChunk(idx, ch), spinnerCmd())
}

// ── Update ─────────────────────────────────────────────────────────────────────

func (m ViewerModel) Update(msg tea.Msg) (ViewerModel, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case spinnerTickMsg:
		m.spinnerTick = (m.spinnerTick + 1) % len(SpinnerFrames)
		anyActive := false
		for _, t := range m.rtTurns {
			if t.translating {
				anyActive = true
				break
			}
		}
		if !anyActive {
			for _, t := range m.tsTurns {
				if t.translating {
					anyActive = true
					break
				}
			}
		}
		if !anyActive && m.hsCursor < len(m.hsTurns) && m.hsTurns[m.hsCursor].translating {
			anyActive = true
		}
		if !anyActive && m.haEntry != nil && m.haEntry.translating {
			anyActive = true
		}
		if !m.ready || anyActive {
			return m, spinnerCmd()
		}

	case InitStepMsg:
		m.initLog = append(m.initLog, msg)
		// Always add to unified debug log with timestamp
		if msg.Err != nil {
			m.debugLog = append(m.debugLog, dbgf("INIT ✗ %s: %v", msg.Step, msg.Err))
		} else {
			m.debugLog = append(m.debugLog, dbgf("INIT ✓ %s", msg.Step))
		}
		if !m.ready {
			// During initialization: also update header status
			if msg.Err != nil {
				m.status = fmt.Sprintf("Error: %v", msg.Err)
				m.statusOK = false
			} else {
				m.status = msg.Step
				m.statusOK = true
			}
		}

	case WatcherDebugMsg:
		m.debugLog = append(m.debugLog, dbgf("WATCHER %s", msg.Text))
		if m.watcher != nil {
			return m, waitForWatcherDebug(m.watcher.DebugChan())
		}

	case StatusMsg:
		m.status = msg.Text
		m.statusOK = msg.OK
		if msg.OK && msg.Text == "Monitoring" {
			m.ready = true
			m.debugLog = append(m.debugLog, dbgf("STATUS ready — monitoring session %s", m.info.SessionID[:8]))
		}

	case HistoryLoadedMsg:
		n := len(msg.Turns)
		if msg.Err != nil {
			n = 0
		}
		m.debugLog = append(m.debugLog, dbgf("HISTORY loaded %d turns from events.jsonl", n))
		return m, func() tea.Msg {
			return InitStepMsg{Step: fmt.Sprintf("Session has %d history turns (tabs [3]/[4] to view)", n), OK: true}
		}

	case ReasoningDetectedMsg:
		vt := &viewTurn{
			turnNum:   len(m.rtTurns) + 1,
			userMsg:   msg.UserMessage,
			reasoning: msg.ReasoningText,
			timestamp: msg.Timestamp,
		}
		m.rtTurns = append(m.rtTurns, vt)
		idx := len(m.rtTurns) - 1
		m.rtQ = append([]int{idx}, m.rtQ...) // live turn: priority
		m.debugLog = append(m.debugLog, dbgf("RT turn #%d detected (%d chars), queuing translation", idx+1, len(msg.ReasoningText)))
		var cmds []tea.Cmd
		if m.watcher != nil {
			cmds = append(cmds, waitForReasoning(m.watcher.Chan()))
		}
		cmds = append(cmds, m.startNextRT())
		return m, tea.Batch(cmds...)

	case RTChunkMsg:
		if msg.Idx < len(m.rtTurns) {
			t := m.rtTurns[msg.Idx]
			if strings.HasPrefix(msg.Text, translator.StreamErrPrefix) {
				t.errMsg = strings.TrimPrefix(msg.Text, translator.StreamErrPrefix)
				m.debugLog = append(m.debugLog, dbgf("RT turn #%d API error: %s", msg.Idx+1, t.errMsg))
			} else {
				t.translation.WriteString(msg.Text)
			}
		}
		return m, waitForRTChunk(msg.Idx, m.rtCh)

	case RTDoneMsg:
		if msg.Idx < len(m.rtTurns) {
			t := m.rtTurns[msg.Idx]
			t.translating = false
			t.done = true
			if t.errMsg != "" {
				m.debugLog = append(m.debugLog, dbgf("RT turn #%d completed with error", msg.Idx+1))
			} else {
				m.debugLog = append(m.debugLog, dbgf("RT turn #%d translation complete (%d chars)", msg.Idx+1, len(t.translationStr())))
			}
		}
		return m, m.startNextRT()

	case TerminalChunkMsg:
		// Accumulate text for Live Stream tab; debounce to batch translate
		m.tsBuffer.WriteString(msg.Text)
		m.tsSeq++
		seq := m.tsSeq
		var cmds []tea.Cmd
		if m.termReader != nil {
			cmds = append(cmds, waitForTerminal(m.termReader.Chan()))
		}
		cmds = append(cmds, tsDebounceCmd(seq))
		return m, tea.Batch(cmds...)

	case tsDebounceMsg:
		// Only translate if no newer chunks arrived since this debounce was scheduled
		if msg.seq == m.tsSeq {
			m.debugLog = append(m.debugLog, dbgf("LIVE debounce fired (seq=%d), buffer=%d bytes → translating", msg.seq, m.tsBuffer.Len()))
			return m, m.startTS()
		}

	case TSChunkMsg:
		if msg.Idx < len(m.tsTurns) {
			t := m.tsTurns[msg.Idx]
			if strings.HasPrefix(msg.Text, translator.StreamErrPrefix) {
				t.errMsg = strings.TrimPrefix(msg.Text, translator.StreamErrPrefix)
			} else {
				t.translation.WriteString(msg.Text)
			}
		}
		return m, waitForTSChunk(msg.Idx, m.tsCh)

	case TSDoneMsg:
		if msg.Idx < len(m.tsTurns) {
			t := m.tsTurns[msg.Idx]
			t.translating = false
			t.done = true
			if t.errMsg != "" {
				m.debugLog = append(m.debugLog, dbgf("LIVE turn #%d API error: %s", msg.Idx+1, t.errMsg))
			} else {
				m.debugLog = append(m.debugLog, dbgf("LIVE turn #%d translation complete (%d chars)", msg.Idx+1, len(t.translationStr())))
			}
		}

	// History: Sessions tab
	case HSLoadedMsg:
		// Preserve completed translations from cache
		prevCache := map[string]string{}
		for _, t := range m.hsTurns {
			if t.done && t.label != "" {
				prevCache[t.label] = t.translationStr()
			}
		}
		for k, v := range prevCache {
			m.hsCache[k] = v
		}

		m.hsTurns = nil
		if msg.Err != nil {
			vt := &viewTurn{turnNum: 1, label: "Error", reasoning: msg.Err.Error()}
			vt.translation.WriteString(fmt.Sprintf("Session load error: %v", msg.Err))
			vt.done = true
			m.hsTurns = []*viewTurn{vt}
			m.debugLog = append(m.debugLog, dbgf("SESSIONS load error: %v", msg.Err))
		} else {
			cached := 0
			for i, entry := range msg.Entries {
				label := entry.Info.SessionID[:8] + "  " + entry.Info.Cwd
				vt := &viewTurn{
					turnNum:   i + 1,
					label:     label,
					reasoning: entry.Reasoning,
					timestamp: entry.Info.UpdatedAt,
				}
				if c, ok := m.hsCache[label]; ok {
					vt.translation.WriteString(c)
					vt.done = true
					cached++
				}
				m.hsTurns = append(m.hsTurns, vt)
			}
			m.debugLog = append(m.debugLog, dbgf("SESSIONS loaded %d sessions (%d from cache)", len(msg.Entries), cached))
		}
		m.hsCursor = 0
		if m.trans != nil {
			return m, m.startHSForCursor()
		}
		return m, nil

	case HSChunkMsg:
		if msg.Gen != m.hsGeneration {
			return m, nil
		}
		if msg.Idx < len(m.hsTurns) {
			t := m.hsTurns[msg.Idx]
			if strings.HasPrefix(msg.Text, translator.StreamErrPrefix) {
				t.errMsg = strings.TrimPrefix(msg.Text, translator.StreamErrPrefix)
			} else {
				t.translation.WriteString(msg.Text)
			}
		}
		return m, waitForHSChunk(msg.Idx, msg.Gen, m.hsCh)

	case HSDoneMsg:
		if msg.Gen != m.hsGeneration {
			return m, nil
		}
		if msg.Idx < len(m.hsTurns) {
			t := m.hsTurns[msg.Idx]
			t.translating = false
			t.done = true
			m.hsCache[t.label] = t.translationStr()
			if t.errMsg != "" {
				m.debugLog = append(m.debugLog, dbgf("SESSIONS summary #%d API error: %s", msg.Idx+1, t.errMsg))
			} else {
				m.debugLog = append(m.debugLog, dbgf("SESSIONS summary #%d done (%d chars)", msg.Idx+1, len(t.translationStr())))
			}
		}

	// History: All tab
	case HALoadedMsg:
		if msg.Err != nil {
			vt := &viewTurn{turnNum: 1, label: "All Sessions"}
			vt.translation.WriteString(fmt.Sprintf("Load error: %v", msg.Err))
			vt.done = true
			m.haEntry = vt
			m.debugLog = append(m.debugLog, dbgf("ALL-SESSIONS load error: %v", msg.Err))
		} else {
			m.haEntry = &viewTurn{
				turnNum:   1,
				label:     "All Sessions",
				reasoning: msg.Reasoning,
			}
			m.debugLog = append(m.debugLog, dbgf("ALL-SESSIONS loaded %d chars of reasoning", len(msg.Reasoning)))
		}
		if m.trans != nil && m.haEntry != nil && !m.haEntry.done {
			return m, m.startHA()
		}
		return m, nil

	case HAChunkMsg:
		if m.haEntry != nil {
			if strings.HasPrefix(msg.Text, translator.StreamErrPrefix) {
				m.haEntry.errMsg = strings.TrimPrefix(msg.Text, translator.StreamErrPrefix)
			} else {
				m.haEntry.translation.WriteString(msg.Text)
			}
		}
		return m, waitForHAChunk(m.haCh)

	case HADoneMsg:
		if m.haEntry != nil {
			m.haEntry.translating = false
			m.haEntry.done = true
			if m.haEntry.errMsg != "" {
				m.debugLog = append(m.debugLog, dbgf("ALL-SESSIONS API error: %s", m.haEntry.errMsg))
			} else {
				m.debugLog = append(m.debugLog, dbgf("ALL-SESSIONS summary done (%d chars)", len(m.haEntry.translationStr())))
			}
		}

	case LangChangedMsg:
		m.outputLang = msg.Lang
		m.debugLog = append(m.debugLog, dbgf("SETTINGS language → %s", msg.Lang))

	case FormatChangedMsg:
		m.outputFormat = msg.Format
		m.debugLog = append(m.debugLog, dbgf("SETTINGS format → %s", msg.Format))

	case SDKLogMsg:
		m.debugLog = append(m.debugLog, dbgf("SDK %s", msg.Text))

	case hsNavDebounceMsg:
		if msg.seq == m.hsNavSeq {
			return m, m.startHSForCursor()
		}

	case teeCheckMsg:
		if m.termReader == nil && m.teeFilePath != "" {
			info, err := os.Stat(m.teeFilePath)
			if err == nil && info.ModTime().After(m.startTime.Add(-30*time.Second)) {
				r := terminal.NewReaderWithTee(m.info.PID, m.teeFilePath)
				if startErr := r.Start(); startErr == nil {
					m.termReader = r
					m.debugLog = append(m.debugLog, dbgf("TEE file appeared, started live stream from %s", m.teeFilePath))
					return m, waitForTerminal(r.Chan())
				}
			}
			// File not ready yet, check again later
			return m, teeCheckCmd()
		}

	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if m.activeTab == TabHistorySessions {
				if m.hsCursor > 0 {
					m.hsCursor--
					m.hsNavSeq++
					seq := m.hsNavSeq
					return m, hsNavDebounceCmd(seq)
				}
			} else {
				if m.scroll[m.activeTab] > 0 {
					m.scroll[m.activeTab]--
				}
			}
		case tea.MouseButtonWheelDown:
			if m.activeTab == TabHistorySessions {
				if m.hsCursor < len(m.hsTurns)-1 {
					m.hsCursor++
					m.hsNavSeq++
					seq := m.hsNavSeq
					return m, hsNavDebounceCmd(seq)
				}
			} else {
				m.scroll[m.activeTab]++
			}
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "b":
			return m, func() tea.Msg { return BackToListMsg{} }
		case "1":
			m.activeTab = TabRealtime
			m.debugLog = append(m.debugLog, dbgf("TAB switched to Real-time"))
		case "2":
			m.activeTab = TabLiveStream
			m.debugLog = append(m.debugLog, dbgf("TAB switched to Live Stream"))
		case "3":
			// Always reload sessions when switching to this tab; cancel any pending nav
			m.activeTab = TabHistorySessions
			if m.hsCancel != nil {
				m.hsCancel()
				m.hsCancel = nil
			}
			m.hsNavSeq++
			m.debugLog = append(m.debugLog, dbgf("TAB switched to History: Sessions → reloading"))
			return m, tea.Batch(
				func() tea.Msg { return InitStepMsg{Step: "Refreshing session history...", OK: true} },
				loadHistorySessionsCmd(m.allSessions),
			)
		case "4":
			// Always reload all-sessions summary when switching to this tab
			m.activeTab = TabHistoryAll
			if m.haCancel != nil {
				m.haCancel()
				m.haCancel = nil
			}
			m.haEntry = nil
			m.debugLog = append(m.debugLog, dbgf("TAB switched to History: All → reloading"))
			return m, tea.Batch(
				func() tea.Msg { return InitStepMsg{Step: "Refreshing all-sessions summary...", OK: true} },
				loadHistoryAllCmd(m.allSessions),
			)
		case "5":
			m.activeTab = TabDebug
		case "up", "k":
			if m.activeTab == TabHistorySessions {
				if m.hsCursor > 0 {
					m.hsCursor--
					m.hsNavSeq++
					seq := m.hsNavSeq
					return m, hsNavDebounceCmd(seq)
				}
			} else {
				if m.scroll[m.activeTab] > 0 {
					m.scroll[m.activeTab]--
				}
			}
		case "down", "j":
			if m.activeTab == TabHistorySessions {
				if m.hsCursor < len(m.hsTurns)-1 {
					m.hsCursor++
					m.hsNavSeq++
					seq := m.hsNavSeq
					return m, hsNavDebounceCmd(seq)
				}
			} else {
				m.scroll[m.activeTab]++
			}
		case "G":
			m.scroll[m.activeTab] = 999999
		case "g":
			m.scroll[m.activeTab] = 0
		}
	}
	return m, nil
}

// ── View ───────────────────────────────────────────────────────────────────────

func (m ViewerModel) View() string {
	w := m.width
	if w <= 0 {
		w = 100
	}
	h := m.height
	if h <= 0 {
		h = 40
	}

	var sb strings.Builder

	// ── Header ────────────────────────────────────────────────────────────────
	sid := m.info.SessionID
	if len(sid) > 8 {
		sid = sid[:8]
	}
	cwd := m.info.Cwd
	maxCwd := w - 50
	if maxCwd < 10 {
		maxCwd = 10
	}
	if lipgloss.Width(cwd) > maxCwd {
		cwd = "…" + string([]rune(cwd)[len([]rune(cwd))-maxCwd+1:])
	}
	leftStr := fmt.Sprintf("  copilot-watcher  │  %s  %s  │  %s / %s  ", sid, cwd, m.outputLang, fmtShort(m.outputFormat))
	dot := StatusDot(m.statusOK)
	statusStr := m.status
	if lipgloss.Width(statusStr) > 30 {
		statusStr = string([]rune(statusStr)[:27]) + "…"
	}
	rightStr := fmt.Sprintf("  %s %s  ", dot, statusStr)
	pad := w - lipgloss.Width(leftStr) - lipgloss.Width(rightStr)
	if pad < 0 {
		pad = 0
	}
	header := HeaderStyle.Width(w).Render(leftStr + strings.Repeat(" ", pad) + rightStr)
	sb.WriteString(header + "\n")

	// ── Tab bar ───────────────────────────────────────────────────────────────
	tabBar := m.renderTabBar(w)
	sb.WriteString(tabBar + "\n")

	// ── Init log (shown only during initialization) ────────────────────────────
	if !m.ready {
		var logLines []string
		start := 0
		if len(m.initLog) > 4 {
			start = len(m.initLog) - 4
		}
		for _, step := range m.initLog[start:] {
			var line string
			if step.Err != nil {
				line = "  " + ErrorStyle.Render("✗") + " " + TextStyle.Render(fmt.Sprintf("%s: %v", step.Step, step.Err))
			} else {
				line = "  " + ActiveStyle.Render("✓") + " " + TextStyle.Render(step.Step)
			}
			logLines = append(logLines, line)
		}
		spin := WarnStyle.Render(SpinnerFrames[m.spinnerTick])
		logLines = append(logLines, fmt.Sprintf("  %s  %s", spin, WarnStyle.Render("Starting watchers...")))
		sb.WriteString(renderPanel(" Initialization ", strings.Join(logLines, "\n"), w))
		sb.WriteString("\n")
		sb.WriteString(HelpStyle.Render("  [esc] back   [q] quit"))
		return sb.String()
	}

	// ── Main content panel ────────────────────────────────────────────────────
	// height budget: header(1) + tabbar(1) + panelBorderTop(1) + panelBorderBot(1) + help(1) = 5
	contentH := h - 5
	if contentH < 3 {
		contentH = 3
	}

	allLines := m.buildTabLines(w)

	offset := m.scroll[m.activeTab]
	maxOffset := len(allLines) - contentH
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	end := offset + contentH
	if end > len(allLines) {
		end = len(allLines)
	}
	var visible []string
	if len(allLines) > 0 {
		visible = allLines[offset:end]
	}

	content := strings.Join(visible, "\n")
	panelTitle := m.buildPanelTitle()
	sb.WriteString(renderPanel(panelTitle, content, w))
	sb.WriteString("\n")

	// ── Help bar ──────────────────────────────────────────────────────────────
	scrollInfo := ""
	if len(allLines) > contentH {
		scrollInfo = fmt.Sprintf("   [%d/%d lines]", offset+1, len(allLines))
	}
	help := HelpStyle.Render(fmt.Sprintf(
		"  [esc/b] back   [1-5] tab   [↑↓/kj] scroll   [g/G] top/bottom   [q] quit%s",
		scrollInfo,
	))
	sb.WriteString(help)
	return sb.String()
}

func (m ViewerModel) renderTabBar(w int) string {
	type tabDef struct {
		id    TabID
		label string
	}
	tabs := []tabDef{
		{TabRealtime, "[1] Real-time"},
		{TabLiveStream, "[2] Live Stream"},
		{TabHistorySessions, "[3] History: Sessions"},
		{TabHistoryAll, "[4] History: All"},
		{TabDebug, "[5] Debug"},
	}
	var parts []string
	for _, td := range tabs {
		extra := ""
		if td.id == TabRealtime && len(m.rtTurns) > 0 {
			extra = " ●"
		}
		if td.id == TabLiveStream && len(m.tsTurns) > 0 {
			extra = " ●"
		}
		label := td.label + extra
		if td.id == m.activeTab {
			parts = append(parts, TabActiveStyle.Render("❯ "+label))
		} else {
			parts = append(parts, TabInactiveStyle.Render("  "+label))
		}
	}
	bar := "  " + strings.Join(parts, "   ")
	padded := bar + strings.Repeat(" ", max(0, w-lipgloss.Width(bar)))
	return DimStyle.Render(padded)
}

func (m ViewerModel) buildPanelTitle() string {
	var title string
	switch m.activeTab {
	case TabRealtime:
		title = " AI Thought Translator"
		for _, t := range m.rtTurns {
			if t.translating {
				title += " " + WarnStyle.Render(SpinnerFrames[m.spinnerTick]+" translating")
				break
			}
		}
	case TabLiveStream:
		title = " Live Stream (Terminal)"
		for _, t := range m.tsTurns {
			if t.translating {
				title += " " + WarnStyle.Render(SpinnerFrames[m.spinnerTick]+" translating")
				break
			}
		}
		if m.tsBuffer.Len() > 0 {
			title += " " + MutedStyle.Render(fmt.Sprintf(" [buffering %d bytes…]", m.tsBuffer.Len()))
		}
	case TabHistorySessions:
		title = " History: Sessions"
		if m.hsCursor < len(m.hsTurns) && m.hsTurns[m.hsCursor].translating {
			title += " " + WarnStyle.Render(SpinnerFrames[m.spinnerTick]+" summarizing")
		}
	case TabHistoryAll:
		title = " History: All Sessions"
		if m.haEntry != nil && m.haEntry.translating {
			title += " " + WarnStyle.Render(SpinnerFrames[m.spinnerTick]+" summarizing")
		}
	case TabDebug:
		title = " Debug / Init Log"
	}
	return title + " "
}

// buildTabLines builds all displayable lines for the active tab (newest first).
func (m ViewerModel) buildTabLines(w int) []string {
	switch m.activeTab {
	case TabRealtime:
		return m.buildRTLines(w)
	case TabLiveStream:
		return m.buildTSLines(w)
	case TabHistorySessions:
		return m.buildHSLines(w)
	case TabHistoryAll:
		return m.buildHALines(w)
	case TabDebug:
		return m.buildDebugLines(w)
	}
	return nil
}

func (m ViewerModel) buildRTLines(w int) []string {
	if len(m.rtTurns) == 0 {
		return []string{
			"",
			WarnStyle.Render("  Waiting for Copilot CLI to produce reasoning output..."),
			MutedStyle.Render("  (New turns will appear here as Copilot processes requests)"),
			"",
		}
	}
	var lines []string
	// Newest first
	for i := len(m.rtTurns) - 1; i >= 0; i-- {
		if i < len(m.rtTurns)-1 {
			lines = append(lines, MutedStyle.Render("  "+strings.Repeat("─", max(0, w-8))))
		}
		lines = append(lines, buildTurnBlock(m.rtTurns[i], w, m.spinnerTick)...)
	}
	return lines
}

func (m ViewerModel) buildTSLines(w int) []string {
	if m.termReader == nil {
		teePathDisplay := "~/.copilot-watcher-stream"
		if m.teeFilePath != "" {
			teePathDisplay = m.teeFilePath
		}
		lines := []string{
			"",
			WarnStyle.Render("  Live Stream: PTY master not accessible in this environment."),
			"",
		}
		// tmux option (shown first, if available)
		if m.hasTmux {
			lines = append(lines,
				ActiveStyle.Render("  Option 1 — tmux (recommended, non-destructive):"),
				TextStyle.Render("    tmux pipe-pane -o 'cat >> "+teePathDisplay+"'"),
				MutedStyle.Render("    (Run this in any tmux window where copilot is running)"),
				"",
				MutedStyle.Render("  Option 2 — tee wrapper:"),
			)
		} else {
			lines = append(lines,
				MutedStyle.Render("  Option 1 — tee wrapper:"),
			)
		}
		lines = append(lines,
			TextStyle.Render(fmt.Sprintf("    copilot [args] 2>&1 | tee %s", teePathDisplay)),
			"",
			MutedStyle.Render("  Or add a shell alias:"),
			TextStyle.Render(fmt.Sprintf("    alias copilot='copilot 2>&1 | tee %s'", teePathDisplay)),
			"",
			MutedStyle.Render(fmt.Sprintf("  Waiting for %s to appear...", teePathDisplay)),
			"",
		)
		return lines
	}
	if len(m.tsTurns) == 0 {
		return []string{
			"",
			WarnStyle.Render("  Listening to terminal output..."),
			MutedStyle.Render("  Translations will appear after 2s of silence in the terminal."),
			"",
		}
	}
	var lines []string
	for i := len(m.tsTurns) - 1; i >= 0; i-- {
		if i < len(m.tsTurns)-1 {
			lines = append(lines, MutedStyle.Render("  "+strings.Repeat("─", max(0, w-8))))
		}
		lines = append(lines, buildTurnBlock(m.tsTurns[i], w, m.spinnerTick)...)
	}
	return lines
}

func (m ViewerModel) buildHSLines(w int) []string {
	if len(m.hsTurns) == 0 {
		return []string{"", WarnStyle.Render("  Loading sessions..."), ""}
	}
	cur := m.hsCursor
	if cur >= len(m.hsTurns) {
		cur = len(m.hsTurns) - 1
	}

	// Navigation indicator bar
	navParts := []string{fmt.Sprintf("  Session %d / %d", cur+1, len(m.hsTurns))}
	if cur == 0 {
		navParts = append(navParts, MutedStyle.Render("(newest)"))
	} else if cur == len(m.hsTurns)-1 {
		navParts = append(navParts, MutedStyle.Render("(oldest)"))
	}
	if cur > 0 {
		navParts = append(navParts, ActiveStyle.Render("[↑] newer"))
	}
	if cur < len(m.hsTurns)-1 {
		navParts = append(navParts, MutedStyle.Render("[↓] older"))
	}
	navLine := DimStyle.Render(strings.Join(navParts, "   "))

	lines := []string{navLine, ""}
	lines = append(lines, buildHistoryBlock(m.hsTurns[cur], w, m.spinnerTick)...)
	return lines
}

func (m ViewerModel) buildHALines(w int) []string {
	if m.haEntry == nil {
		return []string{"", WarnStyle.Render("  Loading all-sessions summary..."), ""}
	}
	return buildHistoryBlock(m.haEntry, w, m.spinnerTick)
}

func (m ViewerModel) buildDebugLines(w int) []string {
	textMaxW := w - 6
	if textMaxW < 20 {
		textMaxW = 20
	}
	var lines []string
	if len(m.debugLog) == 0 {
		return []string{"", MutedStyle.Render("  No debug messages yet."), ""}
	}
	// Newest first: iterate in reverse
	for i := len(m.debugLog) - 1; i >= 0; i-- {
		for _, wrapped := range wordWrapDisplay(m.debugLog[i], textMaxW) {
			lines = append(lines, DimStyle.Render("  "+wrapped))
		}
	}
	return lines
}

// ── Turn block builders ────────────────────────────────────────────────────────

// buildTurnBlock builds lines for a real-time turn (with turn number and user message).
func buildTurnBlock(t *viewTurn, w int, spinTick int) []string {
	var lines []string
	// text max width = w - 2(borders) - 2(padding) - 2("  " prefix) = w-6
	textMaxW := w - 6
	if textMaxW < 20 {
		textMaxW = 20
	}

	// Header line
	age := ""
	if !t.timestamp.IsZero() {
		age = MutedStyle.Render("  " + fmtAge(t.timestamp))
	}
	lines = append(lines, ReasonStyle.Render(fmt.Sprintf("  Turn #%d", t.turnNum))+age)

	// User message (compact, one line, truncated by rune)
	if t.userMsg != "" {
		um := t.userMsg
		// truncate to textMaxW display cols
		if lipgloss.Width(um) > textMaxW-6 {
			runes := []rune(um)
			maxR := textMaxW - 9 // "  Q: \"...\"" overhead
			if maxR < 5 {
				maxR = 5
			}
			truncW := 0
			end := 0
			for end < len(runes) {
				rw := lipgloss.Width(string(runes[end]))
				if truncW+rw > maxR {
					break
				}
				truncW += rw
				end++
			}
			um = string(runes[:end]) + "…"
		}
		lines = append(lines, MutedStyle.Render("  Q: ")+UserMsgStyle.Render(`"`+um+`"`))
	}

	lines = append(lines, "")
	lines = append(lines, buildTranslationLines(t, textMaxW, spinTick)...)
	lines = append(lines, "")
	return lines
}

// buildHistoryBlock builds lines for a history entry (session or all-sessions).
func buildHistoryBlock(t *viewTurn, w int, spinTick int) []string {
	var lines []string
	textMaxW := w - 6
	if textMaxW < 20 {
		textMaxW = 20
	}

	// Header
	age := ""
	if !t.timestamp.IsZero() {
		age = MutedStyle.Render("  " + fmtAge(t.timestamp))
	}
	label := t.label
	if label == "" {
		label = fmt.Sprintf("Session #%d", t.turnNum)
	}
	lines = append(lines, ReasonStyle.Render("  "+label)+age)
	lines = append(lines, "")
	lines = append(lines, buildTranslationLines(t, textMaxW, spinTick)...)
	lines = append(lines, "")
	return lines
}

// buildTranslationLines renders the translation content (or status) for a turn.
func buildTranslationLines(t *viewTurn, textMaxW int, spinTick int) []string {
	var lines []string

	// Show API error state prominently.
	if t.errMsg != "" {
		lines = append(lines, ErrorStyle.Render("  ✗ API Error: "+t.errMsg))
		lines = append(lines, MutedStyle.Render("  (The Copilot API returned an error for this turn.)"))
		return lines
	}

	trans := t.translationStr()
	if trans == "" {
		if t.translating {
			spin := WarnStyle.Render(SpinnerFrames[spinTick])
			lines = append(lines, fmt.Sprintf("  %s  %s", spin, WarnStyle.Render("Translating...")))
		} else if !t.done {
			lines = append(lines, MutedStyle.Render("  (Pending translation)"))
		}
		return lines
	}

	for _, line := range strings.Split(trans, "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			lines = append(lines, "")
			continue
		}
		if lipgloss.Width(line) > textMaxW {
			for _, wrapped := range wordWrapDisplay(line, textMaxW) {
				lines = append(lines, TransStyle.Render("  "+wrapped))
			}
		} else {
			lines = append(lines, TransStyle.Render("  "+line))
		}
	}
	if t.translating {
		spin := WarnStyle.Render(SpinnerFrames[spinTick])
		lines = append(lines, fmt.Sprintf("  %s", spin))
	}
	return lines
}

// ── renderPanel ────────────────────────────────────────────────────────────────

// renderPanel draws a btop-style rounded-border panel.
// Uses lipgloss.Width() for ANSI-aware, CJK-correct measurements.
func renderPanel(title, content string, width int) string {
	if width <= 4 {
		return content
	}
	inner := width - 2 // space between │ chars

	// Top: ╭─ TITLE ──────╮
	titleW := lipgloss.Width(title)
	rightDashes := inner - titleW - 2 // "╭─" costs 2
	if rightDashes < 0 {
		rightDashes = 0
	}
	topLine := borderStr("╭─") + title + borderStr(strings.Repeat("─", rightDashes)+"╮")

	// Content lines
	available := inner - 2 // 1 space padding each side
	var body strings.Builder
	for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		lineW := lipgloss.Width(line)
		pad := available - lineW
		if pad < 0 {
			pad = 0
		}
		body.WriteString(borderStr("│") + " " + line + strings.Repeat(" ", pad) + " " + borderStr("│") + "\n")
	}

	// Bottom: ╰──────────────╯
	bottomLine := borderStr("╰" + strings.Repeat("─", inner) + "╯")
	return topLine + "\n" + body.String() + bottomLine
}

func borderStr(s string) string {
	return lipgloss.NewStyle().Foreground(clrBorder).Render(s)
}

// ── wordWrapDisplay ────────────────────────────────────────────────────────────

// wordWrapDisplay splits s into lines of at most maxCols display columns.
// Correctly handles CJK full-width characters (2 columns each).
func wordWrapDisplay(s string, maxCols int) []string {
	if maxCols <= 0 {
		return []string{s}
	}
	if lipgloss.Width(s) <= maxCols {
		return []string{s}
	}
	var result []string
	var cur []rune
	curW := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if curW+rw > maxCols && len(cur) > 0 {
			result = append(result, string(cur))
			cur = cur[:0]
			curW = 0
		}
		cur = append(cur, r)
		curW += rw
	}
	if len(cur) > 0 {
		result = append(result, string(cur))
	}
	if len(result) == 0 {
		return []string{""}
	}
	return result
}

// dbgf formats a timestamped debug log entry.
func dbgf(format string, args ...any) string {
	return fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

// fmtShort returns a short display string for an output format code.
func fmtShort(format string) string {
	switch format {
	case "bullets":
		return "bullets"
	case "numbered":
		return "numbered"
	case "prose":
		return "prose"
	default:
		if format == "" {
			return "bullets"
		}
		if len(format) > 12 {
			return format[:12] + "…"
		}
		return format
	}
}
