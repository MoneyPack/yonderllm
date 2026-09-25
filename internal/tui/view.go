// This file draws the session. View is a pure function of the model: it reads
// state and returns a string, starting nothing and changing nothing, so a test
// can assert on the rendered frame without a terminal attached.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/MoneyPack/yonderllm/internal/terminaltext"
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
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	if m.height < headerHeight+footerHeight+inputHeight+minViewport {
		// At sizes too small for editing, show the mode and resize guidance
		// instead of pushing the input and approval hints off-screen.
		return m.styles.body.MaxWidth(m.width).MaxHeight(m.height).Render(
			"mode " + m.mode.String() + "\nEnlarge terminal\n" + m.footer())
	}

	// The welcome, when it is showing, was put into the viewport by draw
	// along with everything else; View only reads what is there.
	return lipgloss.JoinVertical(
		lipgloss.Left,
		m.header(),
		m.view.View(),
		m.input.View(),
		m.footer(),
	)
}

// welcome fits inside the transcript area; short windows keep the essentials
// rather than hiding the headline above the viewport's bottom scroll position.
func (m model) welcome() string {
	width := max(1, m.view.Width)
	height := max(1, m.view.Height)
	text := []string{
		m.styles.botTag.Render("Your model lives yonder."),
		m.styles.body.Render("Bring a question. Stay in your terminal."),
		"",
		m.styles.muted.Render("Inference runs remotely on your chosen provider."),
		"",
		m.styles.metaKey.Render("enter") + " send   " + m.styles.metaKey.Render("/help") + " commands",
	}
	if m.sessions != nil {
		text = append(text, m.styles.metaKey.Render("/save <name>")+" save this conversation")
	}
	if height < 8 || width < 40 {
		text = []string{
			m.styles.botTag.Render("Your model lives yonder."),
			m.styles.muted.Render("Inference runs remotely."),
			"enter send · /help commands",
		}
		if m.sessions != nil && height >= 4 {
			text = append(text, "/save <name>")
		}
	}
	return m.styles.body.Width(width).MaxWidth(width).MaxHeight(height).Render(strings.Join(text, "\n"))
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
		m.styles.meta.Render("provider"), m.styles.metaKey.Render(terminaltext.Line(m.sess.Provider())),
		m.styles.meta.Render("model"), m.styles.metaKey.Render(terminaltext.Line(m.sess.Model())),
		m.styles.meta.Render("mode"), m.styles.metaKey.Render(m.mode.String()),
	)

	line := chip + "  " + meta
	// Drop provider/model first on narrow terminals. Permission mode takes
	// precedence over branding; the header must stay one line plus its rule.
	if lipgloss.Width(line) > m.width {
		mode := m.styles.meta.Render("mode") + " " + m.styles.metaKey.Render(m.mode.String())
		line = chip + "  " + mode
		if lipgloss.Width(line) > m.width {
			line = m.styles.metaKey.Render(truncate("mode "+m.mode.String(), m.width))
		}
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
// do: a pending question owns the whole keyboard, ctrl+c stops a reply while one
// is streaming, and it quits when none is.
func (m model) footer() string {
	var hint string
	switch {
	case m.asking:
		if m.height < headerHeight+footerHeight+inputHeight+minViewport || m.width < 20 {
			hint = "enlarge to approve · n deny"
			break
		}
		// The question holds every key, including ctrl+c, so naming the
		// stop hint here would be a lie about what the terminal does.
		hint = "y allow · anything else deny"
	case m.busy:
		activity := terminaltext.Line(m.activity)
		if activity == "" {
			activity = "answering"
		}
		frames := []string{"·", "✦", "✧", "✦"}
		frame := frames[m.spinner%len(frames)]
		const stop = " ctrl+c stop"
		hint = truncate(fmt.Sprintf("%s %s…", frame, activity), m.width-len(stop)) + stop
	case m.command != "":
		// The prompt still takes typing while a command works, but
		// Enter is held until the result is in, and the footer is where
		// that is explained before the held Enter can surprise anyone.
		hint = "running " + m.command + "… · enter waits · ctrl+c quit"
	case m.unwinding:
		hint = "stopping… input preserved · ctrl+c quit"
	default:
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
	if ansi.StringWidth(s) <= width {
		return s
	}
	if width < 2 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}
