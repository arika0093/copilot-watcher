package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/copilot-watcher/copilot-watcher/session"
	"github.com/copilot-watcher/copilot-watcher/translator"
)

// ── Tab types ──────────────────────────────────────────────────────────────────

type TabID int

const (
	TabRealtime        TabID = iota // events.jsonl based
	TabHistorySessions              // per-session AI summary
	TabHistoryAll                   // summary of the current session as a whole
	TabDebug                        // initialization log and debug messages
)

// ── Tea messages ───────────────────────────────────────────────────────────────

type ReasoningDetectedMsg session.ReasoningMsg

// Real-time tab translation
type RTChunkMsg struct {
	Idx  int
	Gen  int
	Text string
}
type RTDoneMsg struct {
	Idx int
	Gen int
}

// History: Sessions tab
type HSLoadedMsg struct {
	Turns []session.Turn
	Err   error
}

type HSChunkMsg struct {
	Idx  int
	Gen  int
	Text string
}
type HSDoneMsg struct {
	Idx int
	Gen int
}
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

// ── viewTurn: single thought + translation unit ────────────────────────────────

type viewTurn struct {
	turnNum     int
	turnID      string
	label       string // for history tabs: session identifier / session summary label
	userMsg     string
	reasoning   string // AI internal reasoning text
	response    string // AI response content (non-reasoning)
	isReasoning bool   // true when this turn has reasoning text
	translation strings.Builder
	errMsg      string // non-empty when the translation API returned an error
	translating bool
	done        bool
	liveOpen    bool
	// pendingUpdate is set in streaming mode when content is updated while a
	// translation is already in progress. The running translation is allowed to
	// finish; then a new one starts automatically with the accumulated content.
	pendingUpdate bool
	timestamp     time.Time
}

func (t *viewTurn) translationStr() string { return t.translation.String() }

func hasRTSummaryContent(t *viewTurn) bool {
	if t == nil {
		return false
	}
	return strings.TrimSpace(t.reasoning) != "" || strings.TrimSpace(t.response) != ""
}

func selectedTurnIndex(total, cursor int) int {
	if total == 0 {
		return -1
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= total {
		cursor = total - 1
	}
	idx := total - 1 - cursor
	if idx < 0 {
		return 0
	}
	return idx
}

func findTurnByID(turns []*viewTurn, turnID string) int {
	if turnID == "" {
		return -1
	}
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].turnID == turnID {
			return i
		}
	}
	return -1
}

// ── ViewerModel ────────────────────────────────────────────────────────────────

type ViewerModel struct {
	info  session.SessionInfo
	trans *translator.Translator

	activeTab TabID

	// Real-time tab (events.jsonl)
	rtTurns      []*viewTurn
	rtCursor     int // 0 = newest, increments toward older
	rtQ          []int
	rtGeneration int
	rtCh         <-chan string
	rtCancel     context.CancelFunc
	rtStreamMode bool // false = turn-to-turn mode, true = streaming (live) mode

	// History: Turns tab (per-request of the current session)
	hsTurns      []*viewTurn
	hsCursor     int   // 0 = newest, increments toward older
	hsQ          []int // translation queue (indices), newest first
	hsGeneration int   // incremented per new translation to discard stale msgs
	hsCh         <-chan string
	hsCancel     context.CancelFunc

	// History: All tab
	haEntry  *viewTurn
	haCh     <-chan string
	haCancel context.CancelFunc

	// Scroll state per tab as distance from the latest line at the bottom.
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

	watcher session.LiveWatcher
}

func NewViewerModel(info session.SessionInfo, trans *translator.Translator) ViewerModel {
	return ViewerModel{
		info:         info,
		trans:        trans,
		activeTab:    TabRealtime,
		scroll:       map[TabID]int{},
		status:       "Initializing...",
		statusOK:     false,
		outputLang:   trans.GetLanguage(),
		outputFormat: trans.GetFormat(),
		rtStreamMode: false,
	}
}

func (m ViewerModel) contentHeight() int {
	h := m.height
	if h <= 0 {
		h = 40
	}
	contentH := h - m.headerHeight() - 4
	if contentH < 3 {
		contentH = 3
	}
	return contentH
}

func (m ViewerModel) headerHeight() int {
	return len(m.buildHeaderLines(m.width))
}

func (m ViewerModel) panelContentWidth() int {
	w := m.width
	if w <= 0 {
		w = 100
	}
	panelContentW := w - 4
	if panelContentW < 20 {
		panelContentW = 20
	}
	return panelContentW
}

func (m ViewerModel) maxScrollOffset() int {
	allLines := m.buildTabLines(m.panelContentWidth())
	maxOffset := len(allLines) - m.contentHeight()
	if maxOffset < 0 {
		return 0
	}
	return maxOffset
}

func (m *ViewerModel) scrollOlder() {
	maxOffset := m.maxScrollOffset()
	if m.scroll[m.activeTab] < maxOffset {
		m.scroll[m.activeTab]++
	}
}

func (m *ViewerModel) scrollNewer() {
	if m.scroll[m.activeTab] > 0 {
		m.scroll[m.activeTab]--
	}
}

func (m *ViewerModel) scrollToOldest() {
	m.scroll[m.activeTab] = m.maxScrollOffset()
}

func (m *ViewerModel) scrollToLatest() {
	m.scroll[m.activeTab] = 0
}

func (m ViewerModel) Init() tea.Cmd {
	return tea.Batch(
		loadHistoryCmd(m.info),
		func() tea.Msg {
			return InitStepMsg{Step: "Session selected: " + m.info.SessionID[:8], OK: true}
		},
		spinnerCmd(),
	)
}

// ── Async load commands ────────────────────────────────────────────────────────

func loadHistoryCmd(info session.SessionInfo) tea.Cmd {
	return func() tea.Msg {
		turns, err := session.LoadSessionHistory(info)
		return HistoryLoadedMsg{Turns: turns, Err: err}
	}
}

// loadHistorySessionsCmd loads individual turns from the current session only.
func loadHistorySessionsCmd(info session.SessionInfo) tea.Cmd {
	return func() tea.Msg {
		turns, err := session.LoadSessionHistory(info)
		if err != nil {
			return HSLoadedMsg{Err: err}
		}
		return HSLoadedMsg{Turns: turns}
	}
}

func loadSessionAllCmd(info session.SessionInfo) tea.Cmd {
	return func() tea.Msg {
		turns, err := session.LoadSessionHistory(info)
		if err != nil {
			return HALoadedMsg{Err: err}
		}
		var sb strings.Builder
		for i, t := range turns {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(fmt.Sprintf("=== Request %d ===\n", i+1))
			if t.UserMessage != "" {
				sb.WriteString("User request:\n")
				sb.WriteString(t.UserMessage)
				sb.WriteString("\n\n")
			}
			if t.ReasoningText != "" {
				sb.WriteString("AI internal reasoning:\n")
				sb.WriteString(t.ReasoningText)
				sb.WriteString("\n\n")
			}
			if t.Response != "" {
				sb.WriteString("AI response:\n")
				sb.WriteString(t.Response)
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

func waitForRTChunk(idx, gen int, ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		text, ok := <-ch
		if !ok {
			return RTDoneMsg{Idx: idx, Gen: gen}
		}
		return RTChunkMsg{Idx: idx, Gen: gen, Text: text}
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
		if t.done || t.translating {
			continue
		}
		if !hasRTSummaryContent(t) {
			if !t.liveOpen {
				t.done = true
			}
			continue
		}
		if m.rtCancel != nil {
			m.rtCancel()
		}
		m.rtGeneration++
		gen := m.rtGeneration
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		m.rtCancel = cancel
		ch, err := m.trans.TranslateLiveRequest(ctx, t.userMsg, t.reasoning, t.response)
		if err != nil {
			t.errMsg = err.Error()
			t.translating = false
			t.done = !t.liveOpen
			return m.startNextRT()
		}
		t.translating = true
		m.rtCh = ch
		return tea.Batch(waitForRTChunk(idx, gen, ch), spinnerCmd())
	}
	return nil
}

// startHSAtCursor translates only the currently visible Requests turn (on-demand).
func (m *ViewerModel) startHSAtCursor() tea.Cmd {
	if len(m.hsTurns) == 0 {
		return nil
	}
	cursor := m.hsCursor
	if cursor >= len(m.hsTurns) {
		cursor = len(m.hsTurns) - 1
	}
	idx := len(m.hsTurns) - 1 - cursor
	if idx < 0 {
		idx = 0
	}
	t := m.hsTurns[idx]
	if t.done || t.translating || t.reasoning == "" || !t.isReasoning {
		return nil
	}
	if m.hsCancel != nil {
		m.hsCancel()
	}
	m.hsGeneration++
	gen := m.hsGeneration
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	m.hsCancel = cancel
	ch, err := m.trans.SummarizeRequest(ctx, t.userMsg, t.reasoning, t.response)
	if err != nil {
		t.translation.WriteString(fmt.Sprintf("Translation error: %v", err))
		t.done = true
		return nil
	}
	t.translating = true
	m.hsCh = ch
	return tea.Batch(waitForHSChunk(idx, gen, ch), spinnerCmd())
}

// startNextHS translates the next queued turn in the History/Turns tab.
// Uses the histSession (independent from the RT session) for per-turn translation.
func (m *ViewerModel) startNextHS() tea.Cmd {
	for len(m.hsQ) > 0 {
		idx := m.hsQ[0]
		m.hsQ = m.hsQ[1:]
		if idx >= len(m.hsTurns) {
			continue
		}
		t := m.hsTurns[idx]
		if t.done || t.translating || t.reasoning == "" {
			continue
		}
		if m.hsCancel != nil {
			m.hsCancel()
		}
		m.hsGeneration++
		gen := m.hsGeneration
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		m.hsCancel = cancel
		ch, err := m.trans.TranslateTurn(ctx, t.reasoning)
		if err != nil {
			t.translation.WriteString(fmt.Sprintf("Translation error: %v", err))
			t.done = true
			return m.startNextHS()
		}
		t.translating = true
		m.hsCh = ch
		return tea.Batch(waitForHSChunk(idx, gen, ch), spinnerCmd())
	}
	return nil
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
	ch, err := m.trans.SummarizeSession(ctx, m.haEntry.label, m.haEntry.reasoning)
	if err != nil {
		m.haEntry.translation.WriteString(fmt.Sprintf("Translation error: %v", err))
		m.haEntry.done = true
		return nil
	}
	m.haEntry.translating = true
	m.haCh = ch
	return tea.Batch(waitForHAChunk(ch), spinnerCmd())
}

func newLiveTurn(turnNum int, msg ReasoningDetectedMsg, liveOpen bool) *viewTurn {
	vt := &viewTurn{
		turnNum:     turnNum,
		turnID:      msg.TurnID,
		userMsg:     msg.UserMessage,
		reasoning:   msg.ReasoningText,
		response:    msg.ContentText,
		isReasoning: msg.ReasoningText != "",
		done:        !liveOpen && msg.ReasoningText == "" && msg.ContentText == "",
		liveOpen:    liveOpen,
		timestamp:   msg.Timestamp,
	}
	return vt
}

func applyPartialToTurn(t *viewTurn, msg ReasoningDetectedMsg) bool {
	reasoningChanged := false
	if msg.TurnID != "" {
		t.turnID = msg.TurnID
	}
	if msg.UserMessage != "" {
		t.userMsg = msg.UserMessage
	}
	if !msg.Timestamp.IsZero() {
		t.timestamp = msg.Timestamp
	}
	if msg.ReasoningText != "" {
		t.reasoning += msg.ReasoningText
		reasoningChanged = true
	}
	if msg.ContentText != "" {
		t.response += msg.ContentText
	}
	t.isReasoning = t.reasoning != ""
	t.liveOpen = true
	t.done = false
	return reasoningChanged
}

func applyFinalToTurn(t *viewTurn, msg ReasoningDetectedMsg) bool {
	reasoningChanged := false
	if msg.TurnID != "" {
		t.turnID = msg.TurnID
	}
	if msg.UserMessage != "" {
		t.userMsg = msg.UserMessage
	}
	if !msg.Timestamp.IsZero() {
		t.timestamp = msg.Timestamp
	}
	if msg.ReasoningText != "" && msg.ReasoningText != t.reasoning {
		t.reasoning = msg.ReasoningText
		reasoningChanged = true
	}
	if msg.ContentText != "" {
		t.response = msg.ContentText
	}
	t.isReasoning = t.reasoning != ""
	t.liveOpen = false
	return reasoningChanged
}

func (m *ViewerModel) findRTTurn(msg ReasoningDetectedMsg) int {
	if idx := findTurnByID(m.rtTurns, msg.TurnID); idx >= 0 {
		return idx
	}
	for i := len(m.rtTurns) - 1; i >= 0; i-- {
		if m.rtTurns[i].userMsg == msg.UserMessage && m.rtTurns[i].liveOpen {
			return i
		}
	}
	return -1
}

func (m *ViewerModel) upsertHistoryTurn(msg ReasoningDetectedMsg) (int, bool) {
	if idx := findTurnByID(m.hsTurns, msg.TurnID); idx >= 0 {
		t := m.hsTurns[idx]
		applyFinalToTurn(t, msg)
		if !t.isReasoning {
			t.done = true
		}
		return idx, false
	}

	vt := newLiveTurn(len(m.hsTurns)+1, msg, false)
	if !vt.isReasoning {
		vt.done = true
	}
	m.hsTurns = append(m.hsTurns, vt)
	return len(m.hsTurns) - 1, true
}

// ── Update ─────────────────────────────────────────────────────────────────────

func (m ViewerModel) Update(msg tea.Msg) (ViewerModel, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.activeTab == TabRealtime {
			m.ensureRTCursorVisible()
		}

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
			for _, t := range m.hsTurns {
				if t.translating {
					anyActive = true
					break
				}
			}
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
		m.debugLog = append(m.debugLog, dbgf("HISTORY loaded %d turns from %s", n, m.info.DisplaySource()))
		if msg.Err == nil && len(msg.Turns) > 0 {
			// Populate Requests tab (hsTurns) from history.
			// RT tab (rtTurns) is only populated from live watcher events.
			m.hsTurns = nil
			m.hsQ = nil
			for i, t := range msg.Turns {
				vt := &viewTurn{
					turnNum:     i + 1,
					turnID:      t.ID,
					userMsg:     t.UserMessage,
					reasoning:   t.ReasoningText,
					response:    t.Response,
					isReasoning: t.ReasoningText != "",
					timestamp:   t.Timestamp,
				}
				if !vt.isReasoning {
					vt.done = true
				}
				m.hsTurns = append(m.hsTurns, vt)
			}
		}
		return m, func() tea.Msg {
			return InitStepMsg{Step: fmt.Sprintf("Session has %d history turns (tab [2] to view)", n), OK: true}
		}

	case ReasoningDetectedMsg:
		if msg.Partial {
			if !m.rtStreamMode {
				// In turn mode: ignore partial msgs entirely
				if m.watcher != nil {
					return m, waitForReasoning(m.watcher.Chan())
				}
				return m, nil
			}
			// Stream mode: find or create open partial turn
			var cmds []tea.Cmd
			if m.watcher != nil {
				cmds = append(cmds, waitForReasoning(m.watcher.Chan()))
			}
			foundIdx := m.findRTTurn(msg)
			if foundIdx >= 0 {
				t := m.rtTurns[foundIdx]
				contentAdded := msg.ReasoningText != "" || msg.ContentText != ""
				applyPartialToTurn(t, msg)
				if contentAdded {
					if t.translating {
						// Let the running translation finish; restart after it completes.
						t.pendingUpdate = true
					} else {
						t.errMsg = ""
						t.done = false
						t.translation.Reset()
						m.rtQ = append([]int{foundIdx}, m.rtQ...)
						cmds = append(cmds, m.startNextRT())
					}
				}
			} else {
				vt := newLiveTurn(len(m.rtTurns)+1, msg, true)
				m.rtTurns = append(m.rtTurns, vt)
				idx := len(m.rtTurns) - 1
				if hasRTSummaryContent(vt) {
					m.rtQ = append([]int{idx}, m.rtQ...)
					cmds = append(cmds, m.startNextRT())
				}
			}
			if m.activeTab == TabRealtime {
				m.ensureRTCursorVisible()
			}
			m.debugLog = append(m.debugLog, dbgf("RT partial update detected (reasoning=%d content=%d chars)", len(msg.ReasoningText), len(msg.ContentText)))
			return m, tea.Batch(cmds...)
		}
		// Partial=false: final turn_end msg
		if m.rtStreamMode {
			var cmds []tea.Cmd
			if m.watcher != nil {
				cmds = append(cmds, waitForReasoning(m.watcher.Chan()))
			}
			foundIdx := m.findRTTurn(msg)
			if foundIdx >= 0 {
				t := m.rtTurns[foundIdx]
				contentChanged := (msg.ReasoningText != "" && msg.ReasoningText != t.reasoning) ||
					(msg.ContentText != "" && msg.ContentText != t.response)
				applyFinalToTurn(t, msg) // sets liveOpen=false
				if t.translating {
					if contentChanged {
						// Let the running translation finish; restart after it completes.
						t.pendingUpdate = true
					}
					// else: current translation already covers the final content; let it finish
				} else if hasRTSummaryContent(t) && (contentChanged || t.translation.Len() == 0 || t.errMsg != "") {
					t.translation.Reset()
					t.errMsg = ""
					t.done = false
					m.rtQ = append([]int{foundIdx}, m.rtQ...)
					cmds = append(cmds, m.startNextRT())
				} else if !hasRTSummaryContent(t) {
					t.done = true
				}
				m.debugLog = append(m.debugLog, dbgf("RT stream turn #%d finalized", foundIdx+1))
			} else {
				vt := newLiveTurn(len(m.rtTurns)+1, msg, false)
				if !hasRTSummaryContent(vt) {
					vt.done = true
				}
				m.rtTurns = append(m.rtTurns, vt)
				if hasRTSummaryContent(vt) {
					idx := len(m.rtTurns) - 1
					m.rtQ = append([]int{idx}, m.rtQ...)
					cmds = append(cmds, m.startNextRT())
				}
				m.debugLog = append(m.debugLog, dbgf("RT stream turn created on final event (%d chars)", len(msg.ReasoningText)))
			}
			hsIdx, _ := m.upsertHistoryTurn(msg)
			hsVt := m.hsTurns[hsIdx]
			if hsVt.isReasoning {
				hsVt.done = false
				m.hsQ = append([]int{hsIdx}, m.hsQ...)
			} else {
				hsVt.done = true
			}
			if m.activeTab == TabHistorySessions && m.hsCursor == 0 {
				cmds = append(cmds, m.startHSAtCursor())
			}
			if m.activeTab == TabRealtime {
				m.ensureRTCursorVisible()
			}
			return m, tea.Batch(cmds...)
		}
		// Turn mode (default): existing behavior
		vt := newLiveTurn(len(m.rtTurns)+1, msg, false)
		m.rtTurns = append(m.rtTurns, vt)
		idx := len(m.rtTurns) - 1
		if hasRTSummaryContent(vt) {
			m.rtQ = append([]int{idx}, m.rtQ...) // live turn: priority
		} else {
			vt.done = true // content-only: no translation needed
		}
		m.debugLog = append(m.debugLog, dbgf("RT turn #%d detected (reasoning=%d content=%d chars)", idx+1, len(msg.ReasoningText), len(msg.ContentText)))

		// Also add to History/Turns tab so all session turns are visible there
		hsIdx, _ := m.upsertHistoryTurn(msg)
		hsVt := m.hsTurns[hsIdx]
		if hsVt.isReasoning {
			m.hsQ = append([]int{hsIdx}, m.hsQ...)
		} else {
			hsVt.done = true
		}

		var cmds []tea.Cmd
		if m.watcher != nil {
			cmds = append(cmds, waitForReasoning(m.watcher.Chan()))
		}
		cmds = append(cmds, m.startNextRT())
		// For HS tab: only translate if currently viewing this new turn
		if m.activeTab == TabHistorySessions && m.hsCursor == 0 {
			cmds = append(cmds, m.startHSAtCursor())
		}
		if m.activeTab == TabRealtime {
			m.ensureRTCursorVisible()
		}
		return m, tea.Batch(cmds...)

	case RTChunkMsg:
		if msg.Gen != m.rtGeneration {
			return m, nil
		}
		if msg.Idx < len(m.rtTurns) {
			t := m.rtTurns[msg.Idx]
			if strings.HasPrefix(msg.Text, translator.StreamErrPrefix) {
				t.errMsg = strings.TrimPrefix(msg.Text, translator.StreamErrPrefix)
				m.debugLog = append(m.debugLog, dbgf("RT turn #%d API error: %s", msg.Idx+1, t.errMsg))
			} else {
				t.translation.WriteString(msg.Text)
			}
		}
		return m, waitForRTChunk(msg.Idx, msg.Gen, m.rtCh)

	case RTDoneMsg:
		if msg.Gen != m.rtGeneration {
			return m, nil
		}
		if msg.Idx < len(m.rtTurns) {
			t := m.rtTurns[msg.Idx]
			t.translating = false
			if t.pendingUpdate {
				// New content arrived while this translation was running; restart now.
				t.pendingUpdate = false
				t.translation.Reset()
				t.errMsg = ""
				m.rtQ = append([]int{msg.Idx}, m.rtQ...)
			} else if !t.liveOpen {
				t.done = true
			}
			if t.errMsg != "" {
				m.debugLog = append(m.debugLog, dbgf("RT turn #%d completed with error", msg.Idx+1))
			} else {
				m.debugLog = append(m.debugLog, dbgf("RT turn #%d translation complete (%d chars)", msg.Idx+1, len(t.translationStr())))
			}
		}
		return m, m.startNextRT()

	// History: Turns tab
	case HSLoadedMsg:
		m.hsTurns = nil
		m.hsQ = nil
		if msg.Err != nil {
			vt := &viewTurn{turnNum: 1, reasoning: msg.Err.Error()}
			vt.translation.WriteString(fmt.Sprintf("Load error: %v", msg.Err))
			vt.done = true
			m.hsTurns = []*viewTurn{vt}
			m.debugLog = append(m.debugLog, dbgf("HISTORY TURNS load error: %v", msg.Err))
		} else {
			for i, t := range msg.Turns {
				vt := &viewTurn{
					turnNum:     i + 1,
					turnID:      t.ID,
					userMsg:     t.UserMessage,
					reasoning:   t.ReasoningText,
					response:    t.Response,
					isReasoning: t.ReasoningText != "",
					timestamp:   t.Timestamp,
				}
				if !vt.isReasoning {
					vt.done = true
				}
				m.hsTurns = append(m.hsTurns, vt)
			}
			m.debugLog = append(m.debugLog, dbgf("HISTORY TURNS loaded %d turns", len(msg.Turns)))
		}
		// Translation starts on-demand when user views the Requests tab.
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
			if t.errMsg != "" {
				m.debugLog = append(m.debugLog, dbgf("HISTORY TURNS #%d API error: %s", msg.Idx+1, t.errMsg))
			} else {
				m.debugLog = append(m.debugLog, dbgf("HISTORY TURNS #%d done (%d chars)", msg.Idx+1, len(t.translationStr())))
			}
		}
		return m, m.startNextHS()

	// History: All tab
	case HALoadedMsg:
		if msg.Err != nil {
			vt := &viewTurn{turnNum: 1, label: sessionSummaryLabel(m.info)}
			vt.translation.WriteString(fmt.Sprintf("Load error: %v", msg.Err))
			vt.done = true
			m.haEntry = vt
			m.debugLog = append(m.debugLog, dbgf("SESSION-ALL load error: %v", msg.Err))
		} else {
			m.haEntry = &viewTurn{
				turnNum:   1,
				label:     sessionSummaryLabel(m.info),
				reasoning: msg.Reasoning,
			}
			if msg.Reasoning == "" {
				m.haEntry.translation.WriteString("No requests found for this session yet.")
				m.haEntry.done = true
			}
			m.debugLog = append(m.debugLog, dbgf("SESSION-ALL loaded %d chars of session context", len(msg.Reasoning)))
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
				m.debugLog = append(m.debugLog, dbgf("SESSION-ALL API error: %s", m.haEntry.errMsg))
			} else {
				m.debugLog = append(m.debugLog, dbgf("SESSION-ALL summary done (%d chars)", len(m.haEntry.translationStr())))
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

	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.scrollOlder()
		case tea.MouseButtonWheelDown:
			m.scrollNewer()
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "b":
			return m, func() tea.Msg { return BackToListMsg{} }
		case "1":
			m.activeTab = TabRealtime
			m.ensureRTCursorVisible()
			m.debugLog = append(m.debugLog, dbgf("TAB switched to Live"))
		case "2":
			m.activeTab = TabHistorySessions
			m.debugLog = append(m.debugLog, dbgf("TAB switched to Requests"))
			if m.trans != nil {
				return m, m.startHSAtCursor()
			}
		case "3":
			// Always reload the current-session summary when switching to this tab.
			m.activeTab = TabHistoryAll
			if m.haCancel != nil {
				m.haCancel()
				m.haCancel = nil
			}
			m.haEntry = nil
			m.debugLog = append(m.debugLog, dbgf("TAB switched to Session All → reloading"))
			return m, tea.Batch(
				func() tea.Msg { return InitStepMsg{Step: "Refreshing current-session summary...", OK: true} },
				loadSessionAllCmd(m.info),
			)
		case "4":
			m.activeTab = TabDebug
		case "m":
			if m.activeTab == TabRealtime {
				m.rtStreamMode = !m.rtStreamMode
				m.scrollToLatest()
				if m.rtStreamMode {
					m.debugLog = append(m.debugLog, dbgf("RT mode switched to streaming (live)"))
				} else {
					m.debugLog = append(m.debugLog, dbgf("RT mode switched to turn-to-turn"))
				}
			}
		case "up", "k":
			m.scrollOlder()
		case "down", "j":
			m.scrollNewer()
		case "left":
			switch m.activeTab {
			case TabRealtime:
				if m.rtCursor < len(m.rtTurns)-1 {
					m.rtCursor++
					m.ensureRTCursorVisible()
				}
			case TabHistorySessions:
				if m.hsCursor < len(m.hsTurns)-1 {
					m.hsCursor++
					m.scrollToLatest()
					if m.trans != nil {
						return m, m.startHSAtCursor()
					}
				}
			}
		case "right":
			switch m.activeTab {
			case TabRealtime:
				if m.rtCursor > 0 {
					m.rtCursor--
					m.ensureRTCursorVisible()
				}
			case TabHistorySessions:
				if m.hsCursor > 0 {
					m.hsCursor--
					m.scrollToLatest()
					if m.trans != nil {
						return m, m.startHSAtCursor()
					}
				}
			}
		case "G":
			m.scrollToLatest()
		case "g":
			m.scrollToOldest()
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

	var sb strings.Builder

	// ── Header ────────────────────────────────────────────────────────────────
	headerLines := m.buildHeaderLines(w)
	sb.WriteString(strings.Join(headerLines, "\n"))
	sb.WriteString("\n")

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
		sb.WriteString(HelpStyle.Render("  [q] back   [esc] quit"))
		return sb.String()
	}

	// ── Main content panel ────────────────────────────────────────────────────
	contentH := m.contentHeight()
	panelContentW := m.panelContentWidth()
	allLines := m.buildTabLines(panelContentW)
	visible, _, start, end := visibleLinesFromBottom(allLines, contentH, m.scroll[m.activeTab])
	content := strings.Join(visible, "\n")
	panelTitle := m.buildPanelTitle()
	sb.WriteString(renderPanel(panelTitle, content, w))
	sb.WriteString("\n")

	// ── Help bar ──────────────────────────────────────────────────────────────
	scrollInfo := ""
	if len(allLines) > contentH {
		scrollInfo = fmt.Sprintf("   [lines %d-%d/%d]", start+1, end, len(allLines))
	}
	help := HelpStyle.Render(fmt.Sprintf(
		"  [q] back   [1-4] tab   [↑↓/kj] scroll   [g/G] top/bottom   [esc] quit%s",
		scrollInfo,
	))
	sb.WriteString(help)
	sb.WriteString("\n")

	// ── Tab bar ───────────────────────────────────────────────────────────────
	tabBar := m.renderTabBar(w)
	sb.WriteString(tabBar)
	return sb.String()
}

func (m ViewerModel) buildHeaderLines(w int) []string {
	if w <= 0 {
		w = 100
	}
	innerW := w - 2
	if innerW < 10 {
		innerW = 10
	}

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

	leftStr := fmt.Sprintf("copilot-watcher  │  %s  %s  │  %s  │  %s / %s", sid, cwd, m.info.DisplaySource(), m.outputLang, fmtShort(m.outputFormat))
	statusStr := m.status
	if lipgloss.Width(statusStr) > 30 {
		statusStr = string([]rune(statusStr)[:27]) + "…"
	}
	rightStr := fmt.Sprintf("%s %s", StatusDot(m.statusOK), statusStr)

	if lipgloss.Width(leftStr)+lipgloss.Width(rightStr)+1 <= innerW {
		pad := innerW - lipgloss.Width(leftStr) - lipgloss.Width(rightStr)
		return []string{HeaderStyle.Width(w).Render(leftStr + strings.Repeat(" ", pad) + rightStr)}
	}

	leftLines := wordWrapDisplay(leftStr, innerW)
	if len(leftLines) == 0 {
		leftLines = []string{"copilot-watcher"}
	}

	rendered := make([]string, 0, len(leftLines)+1)
	for i := 0; i < len(leftLines)-1; i++ {
		rendered = append(rendered, HeaderStyle.Width(w).Render(leftLines[i]))
	}

	last := leftLines[len(leftLines)-1]
	if lipgloss.Width(last)+lipgloss.Width(rightStr)+1 <= innerW {
		pad := innerW - lipgloss.Width(last) - lipgloss.Width(rightStr)
		rendered = append(rendered, HeaderStyle.Width(w).Render(last+strings.Repeat(" ", pad)+rightStr))
		return rendered
	}

	rendered = append(rendered, HeaderStyle.Width(w).Render(last))
	rendered = append(rendered, HeaderStyle.Width(w).Render(rightStr))
	return rendered
}

func (m ViewerModel) renderTabBar(w int) string {
	type tabDef struct {
		id    TabID
		label string
	}
	tabs := []tabDef{
		{TabRealtime, "[1] Live"},
		{TabHistorySessions, "[2] Requests"},
		{TabHistoryAll, "[3] Session"},
		{TabDebug, "[4] Debug"},
	}
	var parts []string
	for _, td := range tabs {
		extra := ""
		if td.id == TabRealtime && len(m.rtTurns) > 0 {
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
		title = " Live"
		for _, t := range m.rtTurns {
			if t.translating {
				title += " · " + WarnStyle.Render(SpinnerFrames[m.spinnerTick]+" translating")
				break
			}
		}
	case TabHistorySessions:
		title = " Requests"
		anyTranslating := false
		for _, t := range m.hsTurns {
			if t.translating {
				anyTranslating = true
				break
			}
		}
		if anyTranslating {
			title += " · " + WarnStyle.Render(SpinnerFrames[m.spinnerTick]+" translating")
		}
	case TabHistoryAll:
		title = " Session All"
		if m.haEntry != nil && m.haEntry.translating {
			title += " · " + WarnStyle.Render(SpinnerFrames[m.spinnerTick]+" summarizing")
		}
	case TabDebug:
		title = " Debug / Init Log"
	}
	return title + " "
}

// buildTabLines builds display lines for the active tab in reading order.
func (m ViewerModel) buildTabLines(w int) []string {
	switch m.activeTab {
	case TabRealtime:
		return m.buildRTLines(w)
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
	lines, _, _ := m.buildRTLinesWithSelection(w)
	return lines
}

func (m ViewerModel) buildRTLinesWithSelection(w int) ([]string, int, int) {
	if len(m.rtTurns) == 0 {
		modeLine := MutedStyle.Render("  Mode: [m] turn-to-turn")
		if m.rtStreamMode {
			modeLine = MutedStyle.Render("  Mode: [m] streaming (live)")
		}
		waitingLabel := fmt.Sprintf("  Waiting for %s session updates...", m.info.DisplaySource())
		if m.info.Source == session.SessionSourceCLI {
			waitingLabel = "  Waiting for Copilot CLI to produce reasoning output..."
		}
		return []string{
			"",
			WarnStyle.Render(waitingLabel),
			modeLine,
			MutedStyle.Render("  Press [m] to switch modes while waiting."),
			MutedStyle.Render("  (New reasoning or response updates will appear here while the session is running)"),
			"",
		}, -1, -1
	}
	cursor := m.rtCursor
	idx := selectedTurnIndex(len(m.rtTurns), cursor)
	t := m.rtTurns[idx]
	total := len(m.rtTurns)

	lines := buildNavBar(cursor, total, "Reasoning", t.timestamp, w)
	if m.rtStreamMode {
		lines = append(lines, MutedStyle.Render("  Mode: [m] streaming (live)"))
	} else {
		lines = append(lines, MutedStyle.Render("  Mode: [m] turn-to-turn"))
	}
	lines = append(lines, "")

	dividerW := w - 8
	if dividerW < 12 {
		dividerW = 12
	}

	selectedStart := -1
	selectedEnd := -1
	for i, turn := range m.rtTurns {
		if i > 0 {
			lines = append(lines, MutedStyle.Render("  "+strings.Repeat("─", dividerW)))
			lines = append(lines, "")
		}
		blockStart := len(lines)
		lines = append(lines, buildStackedTurnHeader(i+1, total, i == idx, turn.timestamp, w)...)
		lines = append(lines, buildTurnBlock(turn, w, m.spinnerTick, true)...)
		blockEnd := len(lines)
		if i == idx {
			selectedStart = blockStart
			selectedEnd = blockEnd
		}
	}
	return lines, selectedStart, selectedEnd
}

func (m ViewerModel) buildHSLines(w int) []string {
	if len(m.hsTurns) == 0 {
		return []string{"", WarnStyle.Render("  No requests found for this session yet."), ""}
	}
	cursor := m.hsCursor
	idx := selectedTurnIndex(len(m.hsTurns), cursor)
	t := m.hsTurns[idx]
	total := len(m.hsTurns)

	lines := buildNavBar(cursor, total, "Request", t.timestamp, w)
	lines = append(lines, "")
	lines = append(lines, buildTurnBlock(t, w, m.spinnerTick, false)...)
	return lines
}

func (m ViewerModel) buildHALines(w int) []string {
	if m.haEntry == nil {
		return []string{"", WarnStyle.Render("  Loading current-session summary..."), ""}
	}
	return buildHistoryBlock(m.haEntry, w, m.spinnerTick)
}

func sessionSummaryLabel(info session.SessionInfo) string {
	if strings.TrimSpace(info.Summary) != "" {
		return info.Summary
	}
	sid := info.SessionID
	if len(sid) > 8 {
		sid = sid[:8]
	}
	return fmt.Sprintf("Session %s", sid)
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
	for i := 0; i < len(m.debugLog); i++ {
		for _, wrapped := range wordWrapDisplay(m.debugLog[i], textMaxW) {
			lines = append(lines, DimStyle.Render("  "+wrapped))
		}
	}
	return lines
}

func visibleLinesFromBottom(allLines []string, height, offset int) ([]string, int, int, int) {
	if height <= 0 {
		return nil, 0, 0, 0
	}
	maxOffset := len(allLines) - height
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset < 0 {
		offset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	end := len(allLines) - offset
	if end < 0 {
		end = 0
	}
	start := end - height
	if start < 0 {
		start = 0
	}
	visible := append([]string(nil), allLines[start:end]...)
	if pad := height - len(visible); pad > 0 {
		padded := make([]string, 0, height)
		for i := 0; i < pad; i++ {
			padded = append(padded, "")
		}
		visible = append(padded, visible...)
	}
	return visible, offset, start, end
}

func clampBottomOffsetForRange(totalLines, height, offset, rangeStart, rangeEnd int) int {
	maxOffset := totalLines - height
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset < 0 {
		offset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	if totalLines == 0 || rangeStart < 0 || rangeEnd <= rangeStart {
		return offset
	}

	if rangeEnd-rangeStart > height {
		offset = totalLines - (rangeStart + height)
	} else {
		end := totalLines - offset
		start := end - height
		if start < 0 {
			start = 0
		}
		if rangeStart < start {
			offset = totalLines - (rangeStart + height)
		} else if rangeEnd > end {
			offset = totalLines - rangeEnd
		}
	}

	if offset < 0 {
		offset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	return offset
}

func (m *ViewerModel) ensureRTCursorVisible() {
	if len(m.rtTurns) == 0 {
		return
	}
	lines, selectedStart, selectedEnd := m.buildRTLinesWithSelection(m.panelContentWidth())
	m.scroll[TabRealtime] = clampBottomOffsetForRange(len(lines), m.contentHeight(), m.scroll[TabRealtime], selectedStart, selectedEnd)
}

// ── Turn block builders ────────────────────────────────────────────────────────

func buildStackedTurnHeader(turnNum, total int, selected bool, ts time.Time, w int) []string {
	left := fmt.Sprintf("  Request %d / %d", turnNum, total)
	if selected {
		left = SelectedStyle.Render("  ▶ Request " + fmt.Sprintf("%d / %d", turnNum, total))
	} else {
		left = DimStyle.Render(left)
	}

	tsStr := timeAgo(ts)
	if tsStr == "" {
		return []string{left}
	}
	right := MutedStyle.Render(tsStr)
	if pad := w - lipgloss.Width(left) - lipgloss.Width(right); pad >= 3 {
		return []string{left + strings.Repeat(" ", pad) + right}
	}
	return []string{left}
}

// buildNavBar builds the navigation indicator line for single-turn views.
// The timestamp ts is shown right-aligned if non-zero.
func buildNavBar(cursor, total int, kind string, ts time.Time, w int) []string {
	leftStr := DimStyle.Render(fmt.Sprintf("  %s %d / %d", kind, cursor+1, total))
	var navParts []string
	if cursor < total-1 {
		navParts = append(navParts, MutedStyle.Render("[<] older"))
	}
	if cursor > 0 {
		navParts = append(navParts, ActiveStyle.Render("[>] newer"))
	}
	navStr := ""
	if len(navParts) > 0 {
		navStr = "   " + strings.Join(navParts, "   ")
	}
	tsStr := timeAgo(ts)
	fullLeft := leftStr + navStr
	if tsStr == "" {
		return []string{fullLeft}
	}

	tsRendered := MutedStyle.Render(tsStr)
	fullLeftWidth := lipgloss.Width(fullLeft)
	tsWidth := lipgloss.Width(tsRendered)
	if pad := w - fullLeftWidth - tsWidth; pad >= 3 {
		return []string{fullLeft + strings.Repeat(" ", pad) + tsRendered}
	}

	leftOnlyWidth := lipgloss.Width(leftStr)
	if pad := w - leftOnlyWidth - tsWidth; pad >= 3 {
		return []string{leftStr + strings.Repeat(" ", pad) + tsRendered}
	}

	return []string{fullLeft}
}

// timeAgo formats a time as a human-readable "X ago" string.
func timeAgo(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}

// buildTurnBlock builds lines for one turn. Reasoning turns get a translated
// block; content-only turns can optionally show the raw response line.
func buildTurnBlock(t *viewTurn, w int, spinTick int, showResponse bool) []string {
	textMaxW := w - 6
	if textMaxW < 20 {
		textMaxW = 20
	}

	var lines []string

	// User message (compact, one line)
	if t.userMsg != "" {
		um := truncateDisplay(t.userMsg, textMaxW-9)
		lines = append(lines, MutedStyle.Render("  Q: ")+UserMsgStyle.Render(`"`+um+`"`))
	}

	lines = append(lines, "")

	hasTranslatedBlock := t.isReasoning || t.translation.Len() > 0 || t.translating || t.errMsg != "" || !t.done
	if hasTranslatedBlock {
		// Render the translated/summarized block when available or pending.
		lines = append(lines, buildReasoningLines(t, textMaxW, spinTick)...)
		if showResponse && t.response != "" {
			lines = append(lines, "")
			lines = append(lines, MutedStyle.Render("  Response"))
			lines = append(lines, buildResponseLines(t, textMaxW)...)
		}
	} else {
		// Content-only turn: compact response display
		if showResponse {
			lines = append(lines, buildResponseLines(t, textMaxW)...)
		} else {
			lines = append(lines, MutedStyle.Render("  (No reasoning summary available)"))
		}
	}
	lines = append(lines, "")
	return lines
}

// buildReasoningLines renders the translated reasoning text (gray, markdown-aware).
func buildReasoningLines(t *viewTurn, textMaxW int, spinTick int) []string {
	var lines []string
	if t.errMsg != "" {
		lines = append(lines, ErrorStyle.Render("  ✗ API Error: "+t.errMsg))
		return lines
	}
	trans := translator.StripTranslationOutput(t.translationStr())
	if trans == "" {
		if t.translating {
			spin := WarnStyle.Render(SpinnerFrames[spinTick])
			lines = append(lines, fmt.Sprintf("  %s  %s", spin, WarnStyle.Render("Translating...")))
		} else if !t.done {
			lines = append(lines, MutedStyle.Render("  (Pending translation)"))
		}
		return lines
	}
	lines = append(lines, renderMarkdownLines(trans, textMaxW, ReasonTransStyle)...)
	if t.translating {
		lines = append(lines, fmt.Sprintf("  %s", WarnStyle.Render(SpinnerFrames[spinTick])))
	}
	return lines
}

// buildResponseLines renders a compact view of the AI response content (normal color).
func buildResponseLines(t *viewTurn, textMaxW int) []string {
	if t.response == "" {
		return []string{MutedStyle.Render("  (no response content)")}
	}
	// Show first non-empty line, truncated
	for _, line := range strings.SplitN(t.response, "\n", 10) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = truncateDisplay(line, textMaxW-4)
		return []string{TextStyle.Render("  ↩ " + line)}
	}
	return []string{MutedStyle.Render("  (no response content)")}
}

// buildHistoryBlock builds lines for a history entry in the session-summary tab.
func buildHistoryBlock(t *viewTurn, w int, spinTick int) []string {
	var lines []string
	textMaxW := w - 6
	if textMaxW < 20 {
		textMaxW = 20
	}
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
	trans := translator.StripTranslationOutput(t.translationStr())
	if t.errMsg != "" {
		lines = append(lines, ErrorStyle.Render("  ✗ API Error: "+t.errMsg))
	} else if trans == "" {
		if t.translating {
			spin := WarnStyle.Render(SpinnerFrames[spinTick])
			lines = append(lines, fmt.Sprintf("  %s  %s", spin, WarnStyle.Render("Summarizing...")))
		} else if !t.done {
			lines = append(lines, MutedStyle.Render("  (Pending)"))
		}
	} else {
		lines = append(lines, renderMarkdownLines(trans, textMaxW, TransStyle)...)
		if t.translating {
			lines = append(lines, fmt.Sprintf("  %s", WarnStyle.Render(SpinnerFrames[spinTick])))
		}
	}
	lines = append(lines, "")
	return lines
}

// renderMarkdownLines renders markdown-formatted text into TUI display lines.
// Block elements handled: headings, bullets (- /*), numbered lists, blockquotes.
// Inline: **bold** markers are stripped (keeping text).
func renderMarkdownLines(text string, maxW int, baseStyle lipgloss.Style) []string {
	headingStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E8E8E8"))
	blockquoteStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Italic(true)

	var lines []string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, " \t")

		// Blank line
		if line == "" {
			lines = append(lines, "")
			continue
		}

		// Headings
		if strings.HasPrefix(line, "### ") {
			content := stripInlineMarkdown(line[4:])
			for _, w := range wordWrapDisplay(content, maxW-2) {
				lines = append(lines, headingStyle.Render("  "+w))
			}
			continue
		}
		if strings.HasPrefix(line, "## ") {
			content := stripInlineMarkdown(line[3:])
			for _, w := range wordWrapDisplay(content, maxW-2) {
				lines = append(lines, headingStyle.Render("  "+w))
			}
			continue
		}
		if strings.HasPrefix(line, "# ") {
			content := stripInlineMarkdown(line[2:])
			for _, w := range wordWrapDisplay(content, maxW-2) {
				lines = append(lines, headingStyle.Render("  "+w))
			}
			continue
		}

		// Bullet list: "- " or "* "
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
			content := stripInlineMarkdown(line[2:])
			wrapped := wordWrapDisplay(content, maxW-4)
			for i, w := range wrapped {
				if i == 0 {
					lines = append(lines, baseStyle.Render("  • "+w))
				} else {
					lines = append(lines, baseStyle.Render("    "+w))
				}
			}
			continue
		}

		// Numbered list: "N. "
		if idx := strings.Index(line, ". "); idx > 0 && idx <= 3 {
			num := line[:idx]
			allDigits := true
			for _, c := range num {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				content := stripInlineMarkdown(line[idx+2:])
				wrapped := wordWrapDisplay(content, maxW-6)
				for i, w := range wrapped {
					if i == 0 {
						lines = append(lines, baseStyle.Render("  "+num+". "+w))
					} else {
						lines = append(lines, baseStyle.Render("     "+w))
					}
				}
				continue
			}
		}

		// Blockquote: "> "
		if strings.HasPrefix(line, "> ") {
			content := stripInlineMarkdown(line[2:])
			for _, w := range wordWrapDisplay(content, maxW-4) {
				lines = append(lines, blockquoteStyle.Render("  │ "+w))
			}
			continue
		}

		// Normal paragraph
		content := stripInlineMarkdown(line)
		for _, w := range wordWrapDisplay(content, maxW) {
			lines = append(lines, baseStyle.Render("  "+w))
		}
	}
	return lines
}

// stripInlineMarkdown removes inline markdown markers (**, *, “) from text.
func stripInlineMarkdown(s string) string {
	// Remove **bold** and *italic* markers, keep content
	var out strings.Builder
	i := 0
	runes := []rune(s)
	for i < len(runes) {
		if i+1 < len(runes) && runes[i] == '*' && runes[i+1] == '*' {
			i += 2 // skip **
			continue
		}
		if runes[i] == '*' {
			i++ // skip *
			continue
		}
		if runes[i] == '`' {
			i++ // skip `
			continue
		}
		out.WriteRune(runes[i])
		i++
	}
	return out.String()
}

// truncateDisplay truncates s to at most maxCols display columns.
func truncateDisplay(s string, maxCols int) string {
	if maxCols <= 0 {
		return ""
	}
	runes := []rune(s)
	w := 0
	for i, r := range runes {
		rw := lipgloss.Width(string(r))
		if w+rw > maxCols {
			return string(runes[:i]) + "…"
		}
		w += rw
	}
	return s
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
	for _, line := range strings.Split(content, "\n") {
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
	case "translate-only":
		return "translate"
	case "conversational":
		return "casual"
	default:
		if format == "" {
			return "casual"
		}
		if len(format) > 12 {
			return format[:12] + "…"
		}
		return format
	}
}
