package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The palette adapts to the terminal's background, so the same screens read
// on light and dark themes.
type adaptive = lipgloss.AdaptiveColor

var (
	colorFg       = adaptive{Light: "#1F2328", Dark: "#D7DCE3"}
	colorDim      = adaptive{Light: "#5F6670", Dark: "#8A929E"}
	colorFaint    = adaptive{Light: "#A3A8AF", Dark: "#4C535E"}
	colorAccent   = adaptive{Light: "#B7791F", Dark: "#F2B544"}
	colorOnAccent = adaptive{Light: "#FAFAF7", Dark: "#0F1115"}
	colorAI       = adaptive{Light: "#2F7D4F", Dark: "#8CCB9B"}
	colorErr      = adaptive{Light: "#C0392B", Dark: "#F07167"}
	colorSel      = adaptive{Light: "#E4E2DA", Dark: "#1F2530"}
	colorCode     = adaptive{Light: "#ECEBE5", Dark: "#1A1F27"}
	colorMatch    = adaptive{Light: "#F6E2A8", Dark: "#4A3A12"}
)

var (
	styleFg     = lipgloss.NewStyle().Foreground(colorFg)
	styleBold   = lipgloss.NewStyle().Foreground(colorFg).Bold(true)
	styleHint   = lipgloss.NewStyle().Foreground(colorDim)
	styleFaint  = lipgloss.NewStyle().Foreground(colorFaint)
	styleSep    = styleFaint
	styleAccent = lipgloss.NewStyle().Foreground(colorAccent)
	styleCursor = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleBrand  = lipgloss.NewStyle().Foreground(colorOnAccent).Background(colorAccent).Bold(true)
	styleKey    = lipgloss.NewStyle().Foreground(colorFg).Background(colorSel)
	styleYou    = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleAI     = lipgloss.NewStyle().Foreground(colorAI).Bold(true)
	styleOK     = lipgloss.NewStyle().Foreground(colorAI)
	styleErr    = lipgloss.NewStyle().Foreground(colorErr)
	styleWarn   = lipgloss.NewStyle().Foreground(colorAccent)
	styleCode   = lipgloss.NewStyle().Foreground(colorFg).Background(colorCode)
	styleMatch  = lipgloss.NewStyle().Foreground(colorFg).Background(colorMatch)
	styleTab    = lipgloss.NewStyle().Foreground(colorFg).Bold(true).Underline(true)
	// styleArchived dims a tool whose sessions were all deleted.
	styleArchived = styleFaint
)

// providerLook is how a provider is drawn: a plain Unicode icon that works in
// any terminal, an optional Nerd Font glyph, and a colour.
type providerLook struct {
	icon  string
	nerd  string
	color adaptive
}

var providerLooks = map[string]providerLook{
	"claude":      {icon: "✻", nerd: "\uec82", color: adaptive{Light: "#C2573A", Dark: "#E58A6B"}},     // cod-claude
	"opencode":    {icon: "▣", nerd: "\U000F0169", color: adaptive{Light: "#7354D6", Dark: "#A48BF0"}}, // md-code_braces
	"pi":          {icon: "π", nerd: "\U000F03FF", color: adaptive{Light: "#1B7F93", Dark: "#4CC3D9"}}, // md-pi
	"gemini":      {icon: "✦", nerd: "\U000F0AE2", color: adaptive{Light: "#2F6FD0", Dark: "#6AA6F0"}}, // md-star_four_points
	"antigravity": {icon: "▲", color: adaptive{Light: "#B83280", Dark: "#E879B9"}},
	"codex":       {icon: "◎", nerd: "\uec81", color: adaptive{Light: "#0E8A5F", Dark: "#34C38F"}}, // cod-openai
}

// A provider threadman has no look for yet.
var unknownLook = providerLook{icon: "◆", color: adaptive{Light: "#5F6670", Dark: "#8A929E"}}

// nerdIcons switches provider icons to Nerd Font glyphs. A terminal can't
// report its font, so this is opt-in.
var nerdIcons = strings.EqualFold(os.Getenv("THREADMAN_ICONS"), "nerd") || os.Getenv("NERD_FONT") == "1"

func lookOf(name string) providerLook {
	if l, ok := providerLooks[name]; ok {
		return l
	}
	return unknownLook
}

// providerIcon is the provider's icon. A Nerd Font glyph is drawn about two
// cells wide, so it carries a space to draw into.
func providerIcon(name string) string {
	l := lookOf(name)
	if nerdIcons && l.nerd != "" {
		return l.nerd + " "
	}
	return l.icon
}

func providerStyle(name string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lookOf(name).color)
}
