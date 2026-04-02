package tui

import "github.com/charmbracelet/lipgloss"

// btop-inspired dark color palette
var (
	// Core palette
	clrBorder     = lipgloss.Color("#00AFAF") // teal  - panel borders
	clrBorderDim  = lipgloss.Color("#005F5F") // dim teal
	clrTitle      = lipgloss.Color("#00D7FF") // bright cyan - panel titles
	clrHeaderBg   = lipgloss.Color("#005F87") // header background
	clrHeaderFg   = lipgloss.Color("#FFFFFF") // header foreground
	clrActive     = lipgloss.Color("#00FF87") // bright green - active status
	clrWarn       = lipgloss.Color("#FFD700") // gold - warnings/thinking
	clrError      = lipgloss.Color("#FF5F5F") // red - errors
	clrMuted      = lipgloss.Color("#767676") // gray - help text
	clrText       = lipgloss.Color("#D0D0D0") // light - body text
	clrSelected   = lipgloss.Color("#005F87") // selection highlight bg
	clrSelectedFg = lipgloss.Color("#FFFFFF")
	clrUserMsg    = lipgloss.Color("#87AFFF") // light blue
	clrTranslated = lipgloss.Color("#87FF87") // light green
	clrReasoning  = lipgloss.Color("#FFD787") // pale gold
	clrPID        = lipgloss.Color("#AF87FF") // lilac
	clrDim        = lipgloss.Color("#585858") // very dim

	// Panel border style (btop-like rounded)
	PanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(clrBorder)

	PanelDimStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(clrBorderDim)

	// Header bar (top bar, like btop)
	HeaderStyle = lipgloss.NewStyle().
			Background(clrHeaderBg).
			Foreground(clrHeaderFg).
			Bold(true).
			Padding(0, 1)

	// Panel title (inline with border)
	TitleStyle = lipgloss.NewStyle().
			Foreground(clrTitle).
			Bold(true)

	ActiveStyle  = lipgloss.NewStyle().Foreground(clrActive).Bold(true)
	WarnStyle    = lipgloss.NewStyle().Foreground(clrWarn)
	ErrorStyle   = lipgloss.NewStyle().Foreground(clrError).Bold(true)
	MutedStyle   = lipgloss.NewStyle().Foreground(clrMuted)
	TextStyle    = lipgloss.NewStyle().Foreground(clrText)
	PIDStyle     = lipgloss.NewStyle().Foreground(clrPID)
	DimStyle     = lipgloss.NewStyle().Foreground(clrDim)
	UserMsgStyle    = lipgloss.NewStyle().Foreground(clrUserMsg).Bold(true)
	TransStyle      = lipgloss.NewStyle().Foreground(clrTranslated)      // content turns
	ReasonTransStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#A8A8A8")) // reasoning translation (gray)
	ReasonStyle     = lipgloss.NewStyle().Foreground(clrReasoning)

	TabActiveStyle   = lipgloss.NewStyle().Foreground(clrActive).Bold(true)
	TabInactiveStyle = lipgloss.NewStyle().Foreground(clrDim)

	SelectedStyle = lipgloss.NewStyle().
			Background(clrSelected).
			Foreground(clrSelectedFg).
			Bold(true)

	HelpStyle = lipgloss.NewStyle().
			Foreground(clrMuted).
			Italic(true)
)

// StatusDot returns a colored status indicator dot
func StatusDot(ok bool) string {
	if ok {
		return ActiveStyle.Render("●")
	}
	return WarnStyle.Render("○")
}

// SpinnerFrames for loading animation
var SpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
