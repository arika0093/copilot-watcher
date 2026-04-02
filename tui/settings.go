package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// LangChangedMsg is emitted when the user changes the output language.
type LangChangedMsg struct{ Lang string }

// FormatChangedMsg is emitted when the user changes the output format.
type FormatChangedMsg struct{ Format string }

// langOption represents a selectable output language.
type langOption struct {
	code  string // passed to translator (empty = custom)
	label string // display string
}

var languages = []langOption{
	{"Japanese", "Japanese (日本語)"},
	{"Japanese (Kansai)", "Japanese (Kansai dialect / 関西弁)"},
	{"Japanese (Ojou-sama)", "Japanese (Ojou-sama / お嬢様言葉)"},
	{"English", "English"},
	{"Chinese", "Chinese (中文)"},
	{"Korean", "Korean (한국어)"},
	{"Spanish", "Spanish (Español)"},
	{"French", "French (Français)"},
	{"German", "German (Deutsch)"},
	{"", "Custom…"},
}

// fmtOption represents a selectable output format.
type fmtOption struct {
	code  string // "" = custom
	label string
	desc  string // shown as hint
}

var formats = []fmtOption{
	{"bullets", "Bullet List", "- key points (default)"},
	{"numbered", "Numbered List", "1. 2. 3. style"},
	{"prose", "Prose", "Flowing paragraphs"},
	{"", "Custom…", "Type your own instruction"},
}

// SettingsModel is the language/output settings screen.
type SettingsModel struct {
	// Navigation: 0=language section, 1=format section
	activeSection int

	// Language section
	langCursor  int
	currentLang string

	// Format section
	fmtCursor     int
	currentFormat string

	// Text input (shared for lang/format custom input)
	inputMode    bool
	inputSection int // which section triggered the input
	inputValue   string
	inputCursor  int

	width  int
	height int
}

func NewSettingsModel(currentLang, currentFormat string) SettingsModel {
	langCursor := len(languages) - 1
	for i, l := range languages {
		if l.code == currentLang {
			langCursor = i
			break
		}
	}
	fmtCursor := 0
	for i, f := range formats {
		if f.code == currentFormat {
			fmtCursor = i
			break
		}
	}
	return SettingsModel{
		langCursor:    langCursor,
		currentLang:   currentLang,
		fmtCursor:     fmtCursor,
		currentFormat: currentFormat,
	}
}

func (m SettingsModel) Init() tea.Cmd { return nil }

func (m SettingsModel) Update(msg tea.Msg) (SettingsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if m.inputMode {
			return m.updateInput(msg)
		}
		switch msg.String() {
		case "tab", "l", "h":
			// Switch between language and format sections
			m.activeSection = 1 - m.activeSection
		case "up", "k":
			if m.activeSection == 0 {
				if m.langCursor > 0 {
					m.langCursor--
				}
			} else {
				if m.fmtCursor > 0 {
					m.fmtCursor--
				}
			}
		case "down", "j":
			if m.activeSection == 0 {
				if m.langCursor < len(languages)-1 {
					m.langCursor++
				}
			} else {
				if m.fmtCursor < len(formats)-1 {
					m.fmtCursor++
				}
			}
		case "enter", " ":
			if m.activeSection == 0 {
				opt := languages[m.langCursor]
				if opt.code == "" {
					m.inputMode = true
					m.inputSection = 0
					m.inputValue = ""
					m.inputCursor = 0
				} else {
					m.currentLang = opt.code
					return m, func() tea.Msg { return LangChangedMsg{Lang: m.currentLang} }
				}
			} else {
				opt := formats[m.fmtCursor]
				if opt.code == "" {
					m.inputMode = true
					m.inputSection = 1
					m.inputValue = ""
					m.inputCursor = 0
				} else {
					m.currentFormat = opt.code
					return m, func() tea.Msg { return FormatChangedMsg{Format: m.currentFormat} }
				}
			}
		}
	}
	return m, nil
}

func (m SettingsModel) updateInput(msg tea.KeyMsg) (SettingsModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.inputMode = false
	case "enter":
		val := strings.TrimSpace(m.inputValue)
		m.inputMode = false
		if val == "" {
			return m, nil
		}
		if m.inputSection == 0 {
			m.currentLang = val
			return m, func() tea.Msg { return LangChangedMsg{Lang: val} }
		}
		m.currentFormat = val
		return m, func() tea.Msg { return FormatChangedMsg{Format: val} }
	case "backspace":
		if m.inputCursor > 0 {
			runes := []rune(m.inputValue)
			m.inputValue = string(runes[:m.inputCursor-1]) + string(runes[m.inputCursor:])
			m.inputCursor--
		}
	case "left":
		if m.inputCursor > 0 {
			m.inputCursor--
		}
	case "right":
		if m.inputCursor < len([]rune(m.inputValue)) {
			m.inputCursor++
		}
	default:
		if len(msg.Runes) > 0 {
			runes := []rune(m.inputValue)
			newRunes := make([]rune, 0, len(runes)+len(msg.Runes))
			newRunes = append(newRunes, runes[:m.inputCursor]...)
			newRunes = append(newRunes, msg.Runes...)
			newRunes = append(newRunes, runes[m.inputCursor:]...)
			m.inputValue = string(newRunes)
			m.inputCursor += len(msg.Runes)
		}
	}
	return m, nil
}

func (m SettingsModel) View() string {
	w := m.width
	if w <= 0 {
		w = 100
	}

	var sb strings.Builder
	header := HeaderStyle.Width(w).Render("  copilot-watcher  │  Settings")
	sb.WriteString(header + "\n\n")

	panelW := minInt(70, w)

	// ── Language section ──────────────────────────────────────────────────────
	sb.WriteString(m.buildLangPanel(panelW))
	sb.WriteString("\n\n")

	// ── Format section ────────────────────────────────────────────────────────
	sb.WriteString(m.buildFmtPanel(panelW))
	sb.WriteString("\n\n")

	// ── Status + Help ─────────────────────────────────────────────────────────
	sb.WriteString(fmt.Sprintf("  %s %s   %s %s\n\n",
		MutedStyle.Render("Language:"), ActiveStyle.Render(m.currentLang),
		MutedStyle.Render("Format:"), ActiveStyle.Render(fmtLabel(m.currentFormat)),
	))
	if m.inputMode {
		sb.WriteString(HelpStyle.Render("  [enter] confirm   [esc] cancel"))
	} else {
		sb.WriteString(HelpStyle.Render("  [↑↓/kj] navigate   [tab/l/h] switch section   [enter] select   [esc/s] close"))
	}
	return sb.String()
}

func (m SettingsModel) buildLangPanel(panelW int) string {
	sectionActive := m.activeSection == 0
	title := " Language "
	if sectionActive {
		title = " Language ● "
	}
	var rows []string
	rows = append(rows, "")

	for i, opt := range languages {
		isCursor := i == m.langCursor && sectionActive
		selected := opt.code != "" && opt.code == m.currentLang
		isCustomActive := opt.code == "" && !isDefaultCode(m.currentLang)

		var line string
		switch {
		case isCursor && m.inputMode && m.inputSection == 0:
			marker := ActiveStyle.Render("  ❯ ")
			cur := ActiveStyle.Render("█")
			runes := []rune(m.inputValue)
			before := string(runes[:m.inputCursor])
			after := string(runes[m.inputCursor:])
			line = marker + WarnStyle.Render("Language name: ") + ActiveStyle.Render(before) + cur + DimStyle.Render(after)
		case isCursor:
			marker := ActiveStyle.Render("  ❯ ")
			if selected || isCustomActive {
				line = marker + ActiveStyle.Bold(true).Render(opt.label) + ActiveStyle.Render("  ✓")
			} else {
				line = marker + ActiveStyle.Render(opt.label)
			}
		case selected:
			line = "      " + TextStyle.Render(opt.label) + MutedStyle.Render("  ✓")
		case isCustomActive && opt.code == "":
			line = "      " + MutedStyle.Render(fmt.Sprintf("Custom… (%s)", m.currentLang)) + MutedStyle.Render("  ✓")
		default:
			line = "      " + DimStyle.Render(opt.label)
		}
		rows = append(rows, line)
	}

	if m.inputMode && m.inputSection == 0 {
		rows = append(rows, DimStyle.Render("  [enter] confirm   [esc] cancel"))
	}
	rows = append(rows, "")
	return renderPanel(title, strings.Join(rows, "\n"), panelW)
}

func (m SettingsModel) buildFmtPanel(panelW int) string {
	sectionActive := m.activeSection == 1
	title := " Output Format "
	if sectionActive {
		title = " Output Format ● "
	}
	var rows []string
	rows = append(rows, "")

	for i, opt := range formats {
		isCursor := i == m.fmtCursor && sectionActive
		selected := opt.code != "" && opt.code == m.currentFormat
		isCustomActive := opt.code == "" && !isDefaultFmt(m.currentFormat)

		var line string
		switch {
		case isCursor && m.inputMode && m.inputSection == 1:
			marker := ActiveStyle.Render("  ❯ ")
			cur := ActiveStyle.Render("█")
			runes := []rune(m.inputValue)
			before := string(runes[:m.inputCursor])
			after := string(runes[m.inputCursor:])
			line = marker + WarnStyle.Render("Format instruction: ") + ActiveStyle.Render(before) + cur + DimStyle.Render(after)
		case isCursor:
			marker := ActiveStyle.Render("  ❯ ")
			if selected || isCustomActive {
				line = marker + ActiveStyle.Bold(true).Render(opt.label) + DimStyle.Render("  "+opt.desc) + ActiveStyle.Render("  ✓")
			} else {
				line = marker + ActiveStyle.Render(opt.label) + DimStyle.Render("  "+opt.desc)
			}
		case selected:
			line = "      " + TextStyle.Render(opt.label) + DimStyle.Render("  "+opt.desc) + MutedStyle.Render("  ✓")
		case isCustomActive && opt.code == "":
			line = "      " + MutedStyle.Render(fmt.Sprintf("Custom… (%s)", trimStr(m.currentFormat, 20))) + MutedStyle.Render("  ✓")
		default:
			line = "      " + DimStyle.Render(opt.label) + DimStyle.Render("  "+opt.desc)
		}
		rows = append(rows, line)
	}

	if m.inputMode && m.inputSection == 1 {
		rows = append(rows, DimStyle.Render("  [enter] confirm   [esc] cancel"))
	}
	rows = append(rows, "")
	return renderPanel(title, strings.Join(rows, "\n"), panelW)
}

// isDefaultCode checks if a language code is one of the preset options.
func isDefaultCode(lang string) bool {
	for _, l := range languages {
		if l.code == lang {
			return true
		}
	}
	return false
}

// isDefaultFmt checks if a format code is one of the preset options.
func isDefaultFmt(fmt string) bool {
	for _, f := range formats {
		if f.code == fmt {
			return true
		}
	}
	return false
}

// fmtLabel returns a short display label for a format code.
func fmtLabel(code string) string {
	for _, f := range formats {
		if f.code == code {
			return f.label
		}
	}
	if code == "" {
		return "Bullet List"
	}
	return trimStr(code, 20)
}

func trimStr(s string, n int) string {
	runes := []rune(s)
	if len(runes) > n {
		return string(runes[:n]) + "…"
	}
	return s
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
