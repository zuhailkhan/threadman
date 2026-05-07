package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorClaude   = lipgloss.Color("#4A90E2")
	colorGemini   = lipgloss.Color("#00BFA5")
	colorOpenCode = lipgloss.Color("#9B59B6")
	colorDefault  = lipgloss.Color("#888888")
	colorAmber    = lipgloss.Color("#F5A623")
	colorGreen    = lipgloss.Color("#7ED321")
	colorDim      = lipgloss.Color("#555555")
	colorSelected = lipgloss.Color("#2A2A2A")

	styleTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF"))
	styleHint  = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))
	styleSep   = lipgloss.NewStyle().Foreground(colorDim)
	styleHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#AAAAAA"))

	styleSelected = lipgloss.NewStyle().
			Background(colorSelected).
			Bold(true)

	styleCursor = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F5A623")).
			Bold(true)

	styleStatus = lipgloss.NewStyle().
			Background(lipgloss.Color("#1A1A1A")).
			Foreground(lipgloss.Color("#AAAAAA")).
			Padding(0, 1)

	styleYou       = lipgloss.NewStyle().Foreground(colorAmber).Bold(true)
	styleAssistant = lipgloss.NewStyle().Foreground(colorGreen).Bold(true)
)

func providerStyle(name string) lipgloss.Style {
	var color lipgloss.Color
	switch name {
	case "claude":
		color = colorClaude
	case "gemini":
		color = colorGemini
	case "opencode":
		color = colorOpenCode
	default:
		color = colorDefault
	}
	return lipgloss.NewStyle().Foreground(color).Bold(true)
}
