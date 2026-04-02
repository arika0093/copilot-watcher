package tui

import (
	"fmt"
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
	screen          appScreen
	selector        SelectorModel
	viewer          *ViewerModel
	settings        SettingsModel
	trans           *translator.Translator
	outputLang      string
	width           int
	height          int
	fatalErr        error
	sdkLogCh        chan string
	sdkRetryCount   int
	pendingSession  *session.SessionInfo
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
	watcher *session.Watcher
	steps   []InitStepMsg
}

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

		// Start JSONL watcher
		var watcher *session.Watcher
		w := session.NewWatcher(info.SessionID, info.EventsPath)
		if err := w.Start(); err != nil {
			steps = append(steps, InitStepMsg{Step: "JSONL watcher", Err: err})
		} else {
			watcher = w
			steps = append(steps, InitStepMsg{Step: "JSONL watcher started (events.jsonl)", OK: true})
		}

		return viewerReadyMsg{watcher: watcher, steps: steps}
	}
}

// NewAppModel creates the root model. Translator is initialized asynchronously.
func NewAppModel() *AppModel {
	cfg, _ := config.Load()
	return &AppModel{
		screen:     screenSelector,
		selector:   NewSelectorModel(),
		outputLang: cfg.Language,
		settings:   NewSettingsModel(cfg.Language, cfg.Format),
		sdkLogCh:   make(chan string, 64),
	}
}

// Close releases all resources.
func (m *AppModel) Close() {
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
		initTranslatorCmd(m.sdkLogCh),
		waitForSDKLog(m.sdkLogCh),
	)
}

func (m AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Global keys
	if key, ok := msg.(tea.KeyMsg); ok {
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
			// If a session was pending, open it now
			if m.pendingSession != nil {
				pending := m.pendingSession
				m.pendingSession = nil
				m.selector.err = nil
				m.screen = screenViewer
				vm := NewViewerModel(*pending, m.trans, m.selector.sessions)
				vm.width, vm.height = m.width, m.height
				vm.outputLang = m.trans.GetLanguage()
				vm.outputFormat = m.trans.GetFormat()
				m.viewer = &vm
				return m, tea.Batch(
					m.viewer.Init(),
					startViewerCmd(*pending, m.trans),
				)
			}
		}
		return m, nil

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

	case SessionSelectedMsg:
		if m.trans == nil {
			sess := msg.Session
			m.pendingSession = &sess
			m.selector.err = fmt.Errorf("Copilot SDK initializing, will open session automatically...")
			return m, nil
		}
		m.screen = screenViewer
		vm := NewViewerModel(msg.Session, m.trans, m.selector.sessions)
		vm.width, vm.height = m.width, m.height
		vm.outputLang = m.trans.GetLanguage()
		vm.outputFormat = m.trans.GetFormat()
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
				cmds = append(cmds, loadHistorySessionsCmd(m.viewer.info.EventsPath))
			}
		}
		return m, tea.Batch(cmds...)

	case LangChangedMsg:
		m.outputLang = msg.Lang
		if m.trans != nil {
			m.trans.SetLanguage(msg.Lang)
		}
		currentFmt := "bullets"
		if m.trans != nil {
			currentFmt = m.trans.GetFormat()
		}
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
		return m, tea.Batch(m.selector.Init(), DetectSessionsCmd())

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
			if msg.String() == "q" {
				return m, tea.Quit
			}
			if msg.String() == "r" {
				m.viewer.rtTurns = nil
				m.viewer.rtQ = nil
				return m, loadHistoryCmd(m.viewer.info.EventsPath)
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
	if m.screen == screenViewer && m.viewer != nil {
		return m.viewer.View()
	}
	return m.selector.View()
}
