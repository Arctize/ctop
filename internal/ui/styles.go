package ui

import "github.com/charmbracelet/lipgloss"

// Colors use the terminal's base 16 palette where possible so ctop follows
// the user's theme.
var (
	cAccent  = lipgloss.Color("6")
	cSubtle  = lipgloss.Color("8")
	cGreen   = lipgloss.Color("2")
	cYellow  = lipgloss.Color("3")
	cRed     = lipgloss.Color("1")
	cBlue    = lipgloss.Color("4")
	cMagenta = lipgloss.Color("5")

	cSelBg    = lipgloss.AdaptiveColor{Light: "153", Dark: "237"}
	cHeaderBg = lipgloss.AdaptiveColor{Light: "254", Dark: "235"}

	sBold    = lipgloss.NewStyle().Bold(true)
	sSubtle  = lipgloss.NewStyle().Foreground(cSubtle)
	sAccent  = lipgloss.NewStyle().Foreground(cAccent)
	sGreen   = lipgloss.NewStyle().Foreground(cGreen)
	sYellow  = lipgloss.NewStyle().Foreground(cYellow)
	sRed     = lipgloss.NewStyle().Foreground(cRed)
	sBlue    = lipgloss.NewStyle().Foreground(cBlue)
	sMagenta = lipgloss.NewStyle().Foreground(cMagenta)

	sBadge     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(cAccent).Padding(0, 1)
	sTabActive = lipgloss.NewStyle().Bold(true).Foreground(cAccent).Underline(true)
	sTabIdle   = lipgloss.NewStyle().Foreground(cSubtle)
	sColHeader = lipgloss.NewStyle().Bold(true).Background(cHeaderBg)
	sSelected  = lipgloss.NewStyle().Bold(true).Background(cSelBg)
	sGroup     = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sKey       = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sErr       = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	sOK        = lipgloss.NewStyle().Foreground(cGreen)
	sWarn      = lipgloss.NewStyle().Foreground(cYellow).Bold(true)
	sJSONKey   = lipgloss.NewStyle().Foreground(cBlue)
	sJSONStr   = lipgloss.NewStyle().Foreground(cGreen)
	sJSONLit   = lipgloss.NewStyle().Foreground(cMagenta)
)

// logPalette colors source prefixes in merged (compose) logs.
var logPalette = []lipgloss.Style{
	lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
	lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
	lipgloss.NewStyle().Foreground(lipgloss.Color("5")),
	lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
	lipgloss.NewStyle().Foreground(lipgloss.Color("4")),
	lipgloss.NewStyle().Foreground(lipgloss.Color("14")),
	lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
	lipgloss.NewStyle().Foreground(lipgloss.Color("13")),
	lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
	lipgloss.NewStyle().Foreground(lipgloss.Color("12")),
}

func stateStyle(state, health string) lipgloss.Style {
	switch health {
	case "unhealthy":
		return sRed
	case "starting":
		return sYellow
	}
	switch state {
	case "running":
		return sGreen
	case "paused":
		return sYellow
	case "restarting", "dead", "removing":
		return sRed
	case "created":
		return sBlue
	default:
		return sSubtle
	}
}

func stateDot(state string) string {
	switch state {
	case "running":
		return "●"
	case "paused":
		return "◐"
	case "restarting":
		return "↻"
	case "created":
		return "◌"
	default:
		return "○"
	}
}
