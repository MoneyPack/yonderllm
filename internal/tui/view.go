// This file draws the session. View is a pure function of the model: it reads
// state and returns a string, starting nothing and changing nothing, so a test
// can assert on the rendered frame without a terminal attached.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// brandName is the name shown in the header chip. It is the one word on screen
// that never changes with state, and it is deliberately the first thing drawn:
// the session belongs to yonderllm, not to whichever provider happens to be
// answering this minute.
const brandName = "yonderllm"

// View satisfies tea.Model.
func (m model) View() string {
	// Bubble Tea sends a WindowSizeMsg immediately on start, but a frame
	// can be requested before it arrives. Rendering at zero width would
	// wrap every line to nothing, so the first frame says only that the
	// session is coming up.
	if !m.ready {
		return m.styles.brand.Render(brandName) + "\n"
	}

	return lipgloss.JoinVertical(
		lipgloss.Left,
		m.header(),
		m.view.View(),
		m.input.View(),
		m.footer(),
	)
}

// header draws the brand chip, the facts about where inference is going, and
// the rule that separates the two from the transcript.
func (m model) header() string {
	chip := m.styles.brand.Render(brandName)

	// Provider, model and mode are the three things a user can be wrong
	// about while typing, and the permission mode is the one with
	// consequences, so all three stay on screen rather than hiding behind
	// a command.
	meta := fmt.Sprintf(
		"%s %s  %s %s  %s %s",
		m.styles.meta.Render("provider"), m.styles.metaKey.Render(m.sess.Provider()),
		m.styles.meta.Render("model"), m.styles.metaKey.Render(m.sess.Model()),
		m.styles.meta.Render("mode"), m.styles.metaKey.Render(m.mode.String()),
	)

	line := chip + "  " + meta
	// A wide gap between the chip and the meta looks like a mistake once
	// the meta no longer fits beside it, so on a narrow terminal the meta
	// is dropped rather than wrapped into the transcript's space: the
	// header has a fixed height that the viewport is sized against.
	if lipgloss.Width(line) > m.width {
		line = chip
	}

	return lipgloss.JoinVertical(lipgloss.Left, line, m.rule())
}

// rule draws the horizontal line under the header.
func (m model) rule() string {
	width := m.width
	if width < 1 {
		width = 1
	}
	return m.styles.rule.Render(strings.Repeat("─", width))
}

// footer draws the keybinding hint, which changes with what the keys currently
// do: ctrl+c stops a reply while one is streaming and quits when none is.
func (m model) footer() string {
	var hint string
	if m.busy {
		hint = "answering… ctrl+c stop"
	} else {
		hint = "enter send · ctrl+j newline · pgup/pgdn scroll · /help commands · ctrl+c quit"
	}

	// Truncating is better than wrapping here: a wrapped footer would take
	// a second line that the layout has not reserved and would push the
	// input off the bottom of the screen.
	return m.styles.footer.Render(truncate(hint, m.width))
}

// truncate cuts s to at most width cells, ending in an ellipsis when it had to
// cut. Widths below the ellipsis itself yield an empty string, because a lone
// ellipsis says less than nothing.
func truncate(s string, width int) string {
	if width < 1 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	if width < 2 {
		return ""
	}
	return string(runes[:width-1]) + "…"
}
