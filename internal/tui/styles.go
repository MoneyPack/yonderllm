package tui

import (
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"
)

// Horizon palette — distant sky, not the pink/magenta that reads as a generic
// AI terminal. Indices (not hex) so 16-colour terminals degrade predictably.
const (
	brandFG  = lipgloss.Color("73")  // teal — far sky
	brandInk = lipgloss.Color("232") // near-black on the brand chip
	userFG   = lipgloss.Color("110") // cool sky for "you"
	toolFG   = lipgloss.Color("108") // sage for tool results
	noticeFG = lipgloss.Color("179") // warm sand for session notices
	errorFG  = lipgloss.Color("167") // coral — loud without neon
	mutedFG  = lipgloss.Color("243") // secondary chrome
	ruleFG   = lipgloss.Color("238") // header rule
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
	toolTag  lipgloss.Style
	notice   lipgloss.Style
	errorTag lipgloss.Style
	// approvalTag labels a question the user has to answer. It is the
	// loudest style in the set on purpose: everything else in the
	// transcript can be skimmed, and this is the one thing that cannot.
	approvalTag lipgloss.Style
	body        lipgloss.Style
	muted       lipgloss.Style
	footer      lipgloss.Style
	cursor      lipgloss.Style
}

// newStyles builds the style set for the terminal lipgloss found on standard
// output, which is what tests and any caller without a writer of its own get.
func newStyles() styles {
	return stylesFor(lipgloss.DefaultRenderer())
}

// stylesFor builds the style set for one renderer.
//
// Run hands in a renderer made from the writer the program actually draws to,
// so that the colour profile — NO_COLOR, TERM, whether the output is a terminal
// at all — is judged against that writer. The default renderer judges standard
// output, which is the wrong answer whenever the two differ: a program drawing
// to a pipe would emit colour, and one drawing to a terminal while standard
// output was redirected would emit none.
func stylesFor(r *lipgloss.Renderer) styles {
	return styles{
		// The brand chip is the one piece of the interface that never
		// changes and never scrolls away: the name is the frame the
		// rest of the session sits inside.
		brand:    r.NewStyle().Bold(true).Foreground(brandInk).Background(brandFG).Padding(0, 1),
		meta:     r.NewStyle().Foreground(mutedFG),
		metaKey:  r.NewStyle().Bold(true).Foreground(brandFG),
		rule:     r.NewStyle().Foreground(ruleFG),
		userTag:  r.NewStyle().Bold(true).Foreground(userFG),
		botTag:   r.NewStyle().Bold(true).Foreground(brandFG),
		toolTag:  r.NewStyle().Bold(true).Foreground(toolFG),
		notice:   r.NewStyle().Foreground(noticeFG),
		errorTag: r.NewStyle().Bold(true).Foreground(errorFG),
		// Approvals share the sand notice colour but stay bold: they ask,
		// they do not merely report.
		approvalTag: r.NewStyle().Bold(true).Foreground(noticeFG),
		body:        r.NewStyle(),
		muted:       r.NewStyle().Foreground(mutedFG),
		footer:      r.NewStyle().Foreground(mutedFG),
		cursor:      r.NewStyle().Bold(true).Foreground(brandFG),
	}
}

// styleInput paints the prompt chrome so the input belongs to the same
// palette as the header rather than the bubbles defaults (grey thick border).
// Called whenever styles are (re)built, including when Run swaps in a
// renderer bound to a non-stdout writer.
func styleInput(input *textarea.Model, s styles) {
	input.Prompt = "› "
	input.FocusedStyle.Prompt = s.cursor
	input.BlurredStyle.Prompt = s.muted
	input.FocusedStyle.Placeholder = s.muted
	input.BlurredStyle.Placeholder = s.muted
	input.FocusedStyle.Text = s.body
	input.BlurredStyle.Text = s.muted
	input.Cursor.Style = s.cursor
}
