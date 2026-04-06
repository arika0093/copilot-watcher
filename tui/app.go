package tui

import (
	"fmt"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/copilot-watcher/copilot-watcher/config"
	"github.com/copilot-watcher/copilot-watcher/session"
	"github.com/copilot-watcher/copilot-watcher/translator"
)

type appScreen int

const (
	screenSelector appScreen = iota
	screenViewer
	screenSettings
)

// AppModel is the root bubbletea model.
type AppModel struct {
	screen         appScreen
	selector       SelectorModel
	viewer         *ViewerModel
	settings       SettingsModel
	sessionWatcher *session.StateWatcher
	trans          *translator.Translator
	outputLang     string
	outputFormat   string
	width          int
	height         int
	fatalErr       error
	sdkLogCh       chan string
	sdkRetryCount  int
	pendingSession *session.SessionInfo
	// New-session dialog: non-nil while waiting for user to confirm/dismiss.
	newSessionAlert  *session.SessionInfo
	knownSessionIDs  map[string]bool
	sessionIDsSeeded bool
}

// translatorReadyMsg is sent when the Copilot SDK client is initialized.
type translatorReadyMsg struct {
	trans *translator.Translator
	err   error
}

// SDKLogMsg carries a diagnostic log line from the Copilot SDK.
type SDKLogMsg struct{ Text string }

// sdkRetryMsg triggers a retry of SDK initialization.
type sdkRetryMsg struct{}

// viewerReadyMsg is sent when the viewer's watchers are started.
type viewerReadyMsg struct {
	watcher session.LiveWatcher
	steps   []InitStepMsg
}

type sessionWatcherReadyMsg struct {
	watcher *session.StateWatcher
	err     error
}

type sessionsChangedMsg struct{}

type sessionWatcherErrMsg struct{ err error }

// initTranslatorCmd starts the Copilot SDK client asynchronously.
func initTranslatorCmd(logCh chan string) tea.Cmd {
	return func() tea.Msg {
		t, err := translator.New(logCh)
		return translatorReadyMsg{trans: t, err: err}
	}
}

// waitForSDKLog waits for one SDK log message and returns it as SDKLogMsg.
func waitForSDKLog(ch chan string) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return SDKLogMsg{Text: msg}
	}
}

// startViewerCmd starts watchers and returns them via viewerReadyMsg.
func startViewerCmd(info session.SessionInfo, trans *translator.Translator) tea.Cmd {
	return func() tea.Msg {
		var steps []InitStepMsg

		var watcher session.LiveWatcher
		w, err := session.NewLiveWatcher(info)
		if err != nil {
			steps = append(steps, InitStepMsg{Step: "Live watcher", Err: err})
		} else if w != nil {
			watcher = w
			label := "Live watcher started"
			if info.Source == session.SessionSourceCLI {
				label = "CLI watcher started (events.jsonl)"
			} else if info.Source == session.SessionSourceVSCode {
				label = "VS Code session watcher started"
			}
			steps = append(steps, InitStepMsg{Step: label, OK: true})
		} else {
			steps = append(steps, InitStepMsg{Step: "Live watcher unavailable for this session", OK: true})
		}

		return viewerReadyMsg{watcher: watcher, steps: steps}
	}
}

func startSessionWatcherCmd() tea.Cmd {
	return func() tea.Msg {
		watcher, err := session.NewStateWatcher()
		if err != nil {
			return sessionWatcherReadyMsg{err: err}
		}
		if err := watcher.Start(); err != nil {
			return sessionWatcherReadyMsg{err: err}
		}
		return sessionWatcherReadyMsg{watcher: watcher}
	}
}

func waitForSessionChange(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return nil
		}
		return sessionsChangedMsg{}
	}
}

func waitForSessionWatcherErr(ch <-chan error) tea.Cmd {
	return func() tea.Msg {
		err, ok := <-ch
		if !ok {
			return nil
		}
		return sessionWatcherErrMsg{err: err}
	}
}

// NewAppModel creates the root model. Translator is initialized asynchronously.
func NewAppModel() *AppModel {
	cfg, _ := config.Load()
	return &AppModel{
		screen:       screenSelector,
		selector:     NewSelectorModel(),
		outputLang:   cfg.Language,
		outputFormat: cfg.Format,
		settings:     NewSettingsModel(cfg.Language, cfg.Format),
		sdkLogCh:     make(chan string, 64),
	}
}

// Close releases all resources.
func (m *AppModel) Close() {
	if m.sessionWatcher != nil {
		m.sessionWatcher.Stop()
	}
	if m.viewer != nil {
		if m.viewer.watcher != nil {
			m.viewer.watcher.Stop()
		}
		if m.viewer.rtCancel != nil {
			m.viewer.rtCancel()
		}
		if m.viewer.hsCancel != nil {
			m.viewer.hsCancel()
		}
		if m.viewer.haCancel != nil {
			m.viewer.haCancel()
		}
	}
	if m.trans != nil {
		m.trans.Close()
	}
}

func (m AppModel) Init() tea.Cmd {
	return tea.Batch(
		m.selector.Init(),
		startSessionWatcherCmd(),
		initTranslatorCmd(m.sdkLogCh),
		waitForSDKLog(m.sdkLogCh),
	)
}

func (m AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Global keys
	if key, ok := msg.(tea.KeyMsg); ok {
		// New-session dialog intercepts y/n/esc before any other handler.
		if m.newSessionAlert != nil {
			switch key.String() {
			case "y", "Y":
				target := *m.newSessionAlert
				m.newSessionAlert = nil
				return m, m.switchToSession(target)
			case "n", "N", "esc":
				m.newSessionAlert = nil
				return m, nil
			}
		}
		switch key.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "s":
			if m.screen != screenSettings {
				m.screen = screenSettings
				return m, nil
			}
		case "esc":
			if m.screen == screenSettings {
				if m.settings.inputMode {
					// Forward ESC to settings to exit custom-input mode only
					updated, cmd := m.settings.Update(msg)
					m.settings = updated
					return m, cmd
				}
				// When not in input mode, ESC does nothing (use q to close settings)
				return m, nil
			}
			if m.screen == screenViewer {
				return m, tea.Quit
			}
		case "q":
			if m.screen == screenSettings {
				// q closes settings and returns to previous screen
				m.screen = screenSelector
				if m.viewer != nil {
					m.screen = screenViewer
				}
				return m, nil
			}
			// q quits from selector; in viewer, handled below (allows back)
			if m.screen == screenSelector {
				return m, tea.Quit
			}
		}
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		updated, _ := m.settings.Update(msg)
		m.settings = updated
		if m.viewer != nil {
			updatedV, cmd := m.viewer.Update(msg)
			m.viewer = &updatedV
			return m, cmd
		}
		updatedS, cmd := m.selector.Update(msg)
		m.selector = updatedS
		return m, cmd

	case translatorReadyMsg:
		if msg.err != nil {
			if m.sdkRetryCount < 5 {
				m.sdkRetryCount++
				m.selector.err = fmt.Errorf("SDK init failed (attempt %d), retrying in 3s...", m.sdkRetryCount)
				return m, tea.Tick(3*time.Second, func(_ time.Time) tea.Msg { return sdkRetryMsg{} })
			}
			m.selector.err = fmt.Errorf("Copilot SDK init failed after 5 attempts: %v", msg.err)
		} else {
			m.trans = msg.trans
			if m.outputLang != "" {
				m.trans.SetLanguage(m.outputLang)
			}
			if m.outputFormat != "" {
				m.trans.SetFormat(m.outputFormat)
			}
			// If a session was pending, open it now
			if m.pendingSession != nil {
				pending := m.pendingSession
				m.pendingSession = nil
				m.selector.err = nil
				m.screen = screenViewer
				vm := NewViewerModel(*pending, m.trans)
				vm.width, vm.height = m.width, m.height
				vm.outputLang = m.outputLang
				vm.outputFormat = m.outputFormat
				m.viewer = &vm
				return m, tea.Batch(
					m.viewer.Init(),
					startViewerCmd(*pending, m.trans),
				)
			}
		}
		return m, nil

	case sessionWatcherReadyMsg:
		if msg.err != nil {
			m.selector.watchErr = msg.err
			return m, nil
		}
		m.sessionWatcher = msg.watcher
		m.selector.watchErr = nil
		return m, tea.Batch(
			waitForSessionChange(msg.watcher.Changes()),
			waitForSessionWatcherErr(msg.watcher.Errors()),
		)

	case sdkRetryMsg:
		return m, initTranslatorCmd(m.sdkLogCh)

	case SDKLogMsg:
		var cmds []tea.Cmd
		cmds = append(cmds, waitForSDKLog(m.sdkLogCh))
		if m.viewer != nil {
			updated, cmd := m.viewer.Update(msg)
			m.viewer = &updated
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)

	case sessionsChangedMsg:
		var cmds []tea.Cmd
		m.selector.watchErr = nil
		if m.sessionWatcher != nil {
			cmds = append(cmds, waitForSessionChange(m.sessionWatcher.Changes()))
		}
		cmds = append(cmds, DetectSessionsCmd())
		return m, tea.Batch(cmds...)

	case sessionWatcherErrMsg:
		m.selector.watchErr = msg.err
		if m.sessionWatcher != nil {
			return m, waitForSessionWatcherErr(m.sessionWatcher.Errors())
		}
		return m, nil

	case SessionSelectedMsg:
		if m.trans == nil {
			sess := msg.Session
			m.pendingSession = &sess
			m.selector.err = fmt.Errorf("Copilot SDK initializing, will open session automatically...")
			return m, nil
		}
		m.screen = screenViewer
		vm := NewViewerModel(msg.Session, m.trans)
		vm.width, vm.height = m.width, m.height
		vm.outputLang = m.outputLang
		vm.outputFormat = m.outputFormat
		m.viewer = &vm
		return m, tea.Batch(
			m.viewer.Init(),
			startViewerCmd(msg.Session, m.trans),
		)

	case viewerReadyMsg:
		if m.viewer != nil {
			m.viewer.watcher = msg.watcher
			// If no JSONL watcher, default to history sessions tab
			if m.viewer.watcher == nil {
				m.viewer.activeTab = TabHistorySessions
			}
		}
		var cmds []tea.Cmd
		for _, step := range msg.steps {
			s := step
			cmds = append(cmds, func() tea.Msg { return s })
		}
		cmds = append(cmds, func() tea.Msg { return StatusMsg{Text: "Monitoring", OK: true} })
		if m.viewer != nil {
			cmds = append(cmds, m.viewer.WatchChannels())
			// If defaulting to history-sessions, trigger initial load
			if m.viewer.activeTab == TabHistorySessions {
				cmds = append(cmds, loadHistorySessionsCmd(m.viewer.info))
			}
		}
		return m, tea.Batch(cmds...)

	case LangChangedMsg:
		m.outputLang = msg.Lang
		if m.trans != nil {
			m.trans.SetLanguage(msg.Lang)
		}
		currentFmt := m.outputFormat
		m.settings = NewSettingsModel(msg.Lang, currentFmt)
		// persist
		_ = config.Save(config.Config{Language: msg.Lang, Format: currentFmt})
		if m.viewer != nil {
			updated, cmd := m.viewer.Update(msg)
			m.viewer = &updated
			return m, cmd
		}
		return m, nil

	case FormatChangedMsg:
		m.outputFormat = msg.Format
		if m.trans != nil {
			m.trans.SetFormat(msg.Format)
		}
		currentLang := m.outputLang
		m.settings = NewSettingsModel(currentLang, msg.Format)
		// persist
		_ = config.Save(config.Config{Language: currentLang, Format: msg.Format})
		if m.viewer != nil {
			updated, cmd := m.viewer.Update(msg)
			m.viewer = &updated
			return m, cmd
		}
		return m, nil

	case BackToListMsg:
		// Stop viewer resources
		if m.viewer != nil {
			if m.viewer.watcher != nil {
				m.viewer.watcher.Stop()
			}
			if m.viewer.rtCancel != nil {
				m.viewer.rtCancel()
			}
			if m.viewer.hsCancel != nil {
				m.viewer.hsCancel()
			}
			if m.viewer.haCancel != nil {
				m.viewer.haCancel()
			}
		}
		m.viewer = nil
		m.screen = screenSelector
		// Refresh sessions list
		m.selector.loading = true
		return m, tea.Batch(DetectSessionsCmd(), spinnerCmd())

	// Viewer messages
	case ReasoningDetectedMsg,
		RTChunkMsg, RTDoneMsg,
		HSLoadedMsg, HSChunkMsg, HSDoneMsg,
		HALoadedMsg, HAChunkMsg, HADoneMsg,
		WatcherDebugMsg,
		HistoryLoadedMsg, StatusMsg, InitStepMsg, spinnerTickMsg:
		if m.viewer != nil {
			updated, cmd := m.viewer.Update(msg)
			m.viewer = &updated
			return m, cmd
		}

	// Selector messages
	case DetectSessionsMsg:
		m.updateKnownSessions(msg.Sessions)
		updated, cmd := m.selector.Update(msg)
		m.selector = updated
		return m, cmd

	case tea.KeyMsg:
		if m.screen == screenSettings {
			updated, cmd := m.settings.Update(msg)
			m.settings = updated
			return m, cmd
		}
		if m.screen == screenViewer && m.viewer != nil {
			if msg.String() == "r" {
				m.viewer.rtTurns = nil
				m.viewer.rtQ = nil
				return m, loadHistoryCmd(m.viewer.info)
			}
			updated, cmd := m.viewer.Update(msg)
			m.viewer = &updated
			return m, cmd
		}
		if m.screen == screenSelector {
			updated, cmd := m.selector.Update(msg)
			m.selector = updated
			return m, cmd
		}

	case tea.MouseMsg:
		if m.screen == screenViewer && m.viewer != nil {
			updated, cmd := m.viewer.Update(msg)
			m.viewer = &updated
			return m, cmd
		}
		if m.screen == screenSelector {
			updated, cmd := m.selector.Update(msg)
			m.selector = updated
			return m, cmd
		}
	}

	// Default routing
	if m.screen == screenSelector {
		updated, cmd := m.selector.Update(msg)
		m.selector = updated
		return m, cmd
	}
	if m.viewer != nil {
		updated, cmd := m.viewer.Update(msg)
		m.viewer = &updated
		return m, cmd
	}
	return m, nil
}

func (m AppModel) View() string {
	if m.fatalErr != nil {
		return ErrorStyle.Render(fmt.Sprintf("\n  Fatal error: %v\n\n  [q] quit\n", m.fatalErr))
	}
	if m.screen == screenSettings {
		return m.settings.View()
	}
	var base string
	if m.screen == screenViewer && m.viewer != nil {
		base = m.viewer.View()
	} else {
		base = m.selector.View()
	}
	if m.newSessionAlert != nil {
		base = renderNewSessionBanner(m.newSessionAlert, m.width) + "\n" + base
	}
	return base
}

// updateKnownSessions seeds or updates knownSessionIDs and sets newSessionAlert
// when a new active session appears while the viewer is open.
func (m *AppModel) updateKnownSessions(sessions []session.SessionInfo) {
	if !m.sessionIDsSeeded {
		m.knownSessionIDs = make(map[string]bool, len(sessions))
		for _, s := range sessions {
			m.knownSessionIDs[s.SelectionKey()] = true
		}
		m.sessionIDsSeeded = true
		return
	}
	for i := range sessions {
		s := &sessions[i]
		key := s.SelectionKey()
		if m.knownSessionIDs[key] {
			continue
		}
		// Don't mark inactive sessions as known yet: they may transition to active
		// shortly after (e.g. lock file created after events.jsonl). Keeping them
		// out of the map allows the alert to fire when they become active.
		if !s.Active {
			continue
		}
		m.knownSessionIDs[key] = true
		// Only prompt while the viewer is open and the new session is different.
		if m.screen != screenViewer || m.viewer == nil {
			continue
		}
		if m.viewer.info.SelectionKey() == key {
			continue
		}
		if m.newSessionAlert == nil {
			m.newSessionAlert = s
		}
	}
}

// switchToSession closes the current viewer and opens a new one for target.
func (m *AppModel) switchToSession(target session.SessionInfo) tea.Cmd {
	if m.viewer != nil {
		if m.viewer.watcher != nil {
			m.viewer.watcher.Stop()
		}
		if m.viewer.rtCancel != nil {
			m.viewer.rtCancel()
		}
		if m.viewer.hsCancel != nil {
			m.viewer.hsCancel()
		}
		if m.viewer.haCancel != nil {
			m.viewer.haCancel()
		}
	}
	m.screen = screenViewer
	vm := NewViewerModel(target, m.trans)
	vm.width, vm.height = m.width, m.height
	vm.outputLang = m.outputLang
	vm.outputFormat = m.outputFormat
	m.viewer = &vm
	return tea.Batch(
		m.viewer.Init(),
		startViewerCmd(target, m.trans),
	)
}

// renderNewSessionBanner renders a one-line notification bar for the new-session dialog.
func renderNewSessionBanner(s *session.SessionInfo, width int) string {
	id := s.SessionID
	if len(id) > 8 {
		id = id[:8]
	}
	cwd := filepath.Base(s.Cwd)
	if len(cwd) > 24 {
		cwd = cwd[:23] + "…"
	}
	text := fmt.Sprintf("  ⚡ New session started: %s  (%s)   [y] switch   [n] stay", id, cwd)
	return WarnStyle.Width(width).Render(text)
}
