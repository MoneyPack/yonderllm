package tui

import "github.com/charmbracelet/lipgloss"

// Colours are given as 256-colour indices rather than hex so that the palette
// degrades predictably on the plain terminals a thin client is likely to meet:
// a hex value on a 16-colour terminal is approximated by the terminal, an
// index is not.
const (
	brandFG  = lipgloss.Color("212")
	brandInk = lipgloss.Color("232")
	userFG   = lipgloss.Color("117")
	noticeFG = lipgloss.Color("221")
	errorFG  = lipgloss.Color("203")
	mutedFG  = lipgloss.Color("245")
	ruleFG   = lipgloss.Color("238")
)

// styles is the full set of rendering styles, built once and carried on the
// model. Keeping them in a struct rather than package-level variables means a
// test can render a view without depending on package initialisation order,
// and leaves room for a future theme without touching every call site.
type styles struct {
	brand    lipgloss.Style
	meta     lipgloss.Style
	metaKey  lipgloss.Style
	rule     lipgloss.Style
	userTag  lipgloss.Style
	botTag   lipgloss.Style
	notice   lipgloss.Style
	errorTag lipgloss.Style
	body     lipgloss.Style
	muted    lipgloss.Style
	footer   lipgloss.Style
	cursor   lipgloss.Style
}

// newStyles builds the style set.
func newStyles() styles {
	return styles{
		// The brand chip is the one piece of the interface that never
		// changes and never scrolls away: the name is the frame the
		// rest of the session sits inside.
		brand:    lipgloss.NewStyle().Bold(true).Foreground(brandInk).Background(brandFG).Padding(0, 1),
		meta:     lipgloss.NewStyle().Foreground(mutedFG),
		metaKey:  lipgloss.NewStyle().Bold(true).Foreground(brandFG),
		rule:     lipgloss.NewStyle().Foreground(ruleFG),
		userTag:  lipgloss.NewStyle().Bold(true).Foreground(userFG),
		botTag:   lipgloss.NewStyle().Bold(true).Foreground(brandFG),
		notice:   lipgloss.NewStyle().Foreground(noticeFG),
		errorTag: lipgloss.NewStyle().Bold(true).Foreground(errorFG),
		body:     lipgloss.NewStyle(),
		muted:    lipgloss.NewStyle().Foreground(mutedFG),
		footer:   lipgloss.NewStyle().Foreground(mutedFG),
		cursor:   lipgloss.NewStyle().Bold(true).Foreground(brandFG),
	}
}
