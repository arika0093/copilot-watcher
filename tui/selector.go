package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/copilot-watcher/copilot-watcher/session"
)

// SelectorModel is the session selection screen.
type SelectorModel struct {
	sessions    []session.SessionInfo
	cursor      int
	width       int
	height      int
	err         error
	loading     bool
	spinnerTick int
}

// SessionSelectedMsg is emitted when the user selects a session.
type SessionSelectedMsg struct{ Session session.SessionInfo }

// DetectSessionsMsg carries async detection results.
type DetectSessionsMsg struct {
	Sessions []session.SessionInfo
	Err      error
}

type spinnerTickMsg struct{}

func spinnerCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(_ time.Time) tea.Msg { return spinnerTickMsg{} })
}

// DetectSessionsCmd triggers async session detection.
func DetectSessionsCmd() tea.Cmd {
	return func() tea.Msg {
		sessions, err := session.Detect()
		return DetectSessionsMsg{Sessions: sessions, Err: err}
	}
}

func NewSelectorModel() SelectorModel {
	return SelectorModel{loading: true}
}

func (m SelectorModel) Init() tea.Cmd {
	return tea.Batch(DetectSessionsCmd(), spinnerCmd())
}

func (m SelectorModel) Update(msg tea.Msg) (SelectorModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case spinnerTickMsg:
		m.spinnerTick = (m.spinnerTick + 1) % len(SpinnerFrames)
		if m.loading {
			return m, spinnerCmd()
		}

	case DetectSessionsMsg:
		m.loading = false
		if msg.Err != nil {
			m.err = msg.Err
		} else {
			m.sessions = msg.Sessions
		}

	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.MouseButtonWheelDown:
			if m.cursor < len(m.sessions)-1 {
				m.cursor++
			}
		case tea.MouseButtonLeft:
			if msg.Action == tea.MouseActionRelease {
				// Sessions list starts at y=6 (header + 2 blank lines + panel top + col header + divider)
				const sessionListStartY = 6
				idx := msg.Y - sessionListStartY
				if idx >= 0 && idx < len(m.sessions) {
					m.cursor = idx
					return m, func() tea.Msg {
						return SessionSelectedMsg{Session: m.sessions[m.cursor]}
					}
				}
			}
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.sessions)-1 {
				m.cursor++
			}
		case "enter", " ":
			if len(m.sessions) > 0 {
				return m, func() tea.Msg {
					return SessionSelectedMsg{Session: m.sessions[m.cursor]}
				}
			}
		case "r":
			m.loading = true
			return m, tea.Batch(DetectSessionsCmd(), spinnerCmd())
		}
	}
	return m, nil
}

func (m SelectorModel) View() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	inner := w - 4 // account for border + padding

	var sb strings.Builder

	// ── Header bar ──────────────────────────────────────────────────
	title := "  copilot-watcher  │  GitHub Copilot CLI Thought Monitor  "
	sb.WriteString(HeaderStyle.Width(w).Render(title))
	sb.WriteString("\n\n")

	// ── Session list panel ──────────────────────────────────────────
	var body strings.Builder
	if m.loading {
		spinner := WarnStyle.Render(SpinnerFrames[m.spinnerTick])
		body.WriteString(fmt.Sprintf("  %s  Scanning for active sessions...\n", spinner))
	} else if m.err != nil {
		body.WriteString(ErrorStyle.Render(fmt.Sprintf("  ✗ Error: %v", m.err)))
		body.WriteString("\n")
	} else if len(m.sessions) == 0 {
		body.WriteString(WarnStyle.Render("  No Copilot CLI sessions found."))
		body.WriteString("\n")
		body.WriteString(MutedStyle.Render("  Sessions appear after the first message is sent in Copilot CLI."))
		body.WriteString("\n")
		body.WriteString(MutedStyle.Render("  Press [r] to refresh."))
		body.WriteString("\n")
	} else {
		// Fixed-width column helpers using lipgloss to avoid ANSI-length issues
		colSID := func(s string) string { return lipgloss.NewStyle().Width(8).MaxWidth(8).Render(s) }
		colCWD := func(s string) string { return lipgloss.NewStyle().Width(24).MaxWidth(24).Render(s) }
		colPID := func(s string) string { return lipgloss.NewStyle().Width(7).MaxWidth(7).Render(s) }
		colAge := func(s string) string { return lipgloss.NewStyle().Width(10).MaxWidth(10).Render(s) }

		// Column headers
		hdr := "  " + colSID("SESSION") + "  " + colCWD("CWD") + "  " + colPID("PID") + "  " + colAge("AGE") + "  " + "STATUS"
		body.WriteString(DimStyle.Render(hdr))
		body.WriteString("\n")
		body.WriteString(DimStyle.Render("  " + strings.Repeat("─", inner-2)))
		body.WriteString("\n")

		for i, s := range m.sessions {
			sid := s.SessionID
			if len(sid) > 8 {
				sid = sid[:8]
			}
			// Show only the directory basename for clarity
			cwd := filepath.Base(s.Cwd)
			if len(cwd) > 24 {
				cwd = cwd[:23] + "…"
			}
			age := fmtAge(s.UpdatedAt)
			pidStr := "—"
			if s.PID > 0 {
				pidStr = fmt.Sprintf("%d", s.PID)
			}
			var statusStr string
			if s.Active {
				statusStr = ActiveStyle.Render("● active")
			} else {
				statusStr = MutedStyle.Render("○ inactive")
			}

			if i == m.cursor {
				body.WriteString(" ❯ " + SelectedStyle.Render(
					colSID(sid)+"  "+colCWD(cwd)+"  "+colPID(pidStr)+"  "+colAge(age)+"  ",
				) + statusStr)
			} else {
				body.WriteString("  " +
					TitleStyle.Render(colSID(sid)) + "  " +
					TextStyle.Render(colCWD(cwd)) + "  " +
					PIDStyle.Render(colPID(pidStr)) + "  " +
					MutedStyle.Render(colAge(age)) + "  " +
					statusStr,
				)
			}
			body.WriteString("\n")
		}
	}

	panel := renderPanel(" Sessions ", body.String(), w)
	sb.WriteString(panel)
	sb.WriteString("\n")

	// ── Help bar ─────────────────────────────────────────────────────
	help := HelpStyle.Render("  [↑↓] navigate   [enter] select   [r] refresh   [s] settings   [q] quit")
	sb.WriteString(help)

	return sb.String()
}

func fmtAge(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
