package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// kind distinguishes the sorts of thing that can appear in the transcript.
//
// The transcript is a list of typed blocks rather than a single accumulated
// string because a provider notice and a model reply are different claims
// about the world, and a user reading back through a session needs to be able
// to tell which is which at a glance.
type kind int

const (
	// blockUser is something the person typed.
	blockUser kind = iota
	// blockAssistant is a model reply.
	blockAssistant
	// blockTool is a tool the model reached for, and what came back.
	blockTool
	// blockNotice is a session status line, such as a provider fallback.
	blockNotice
	// blockError is a failed exchange.
	blockError
	// blockInfo is local output from a slash command.
	blockInfo
)

// block is one entry in the transcript.
type block struct {
	kind kind
	// tag labels the block; for an assistant block it is the provider
	// that actually answered, which may not be the one that was asked.
	tag string
	// id identifies a block that will be rewritten later. A tool call
	// arrives twice — once when it is sent and once with its result — and
	// the second arrival replaces the first rather than appending, so the
	// transcript shows one call rather than two half-reports of it.
	id   string
	text string
}

// render draws a block wrapped to width.
func (b block) render(s styles, width int) string {
	if width < 8 {
		width = 8
	}
	body := lipgloss.NewStyle().Width(width).Render(strings.TrimRight(b.text, "\n"))

	switch b.kind {
	case blockUser:
		return lipgloss.JoinVertical(lipgloss.Left, s.userTag.Render("you"), body)
	case blockAssistant:
		tag := b.tag
		if tag == "" {
			tag = "assistant"
		}
		return lipgloss.JoinVertical(lipgloss.Left, s.botTag.Render(tag), body)
	case blockTool:
		tag := b.tag
		if tag == "" {
			tag = "tool"
		}
		// The word "tool" stays in the tag rather than being left to
		// colour alone, so that a transcript read on a terminal without
		// colour still distinguishes what a tool returned from what the
		// model said.
		return lipgloss.JoinVertical(lipgloss.Left, s.toolTag.Render("tool "+tag), s.muted.Render(body))
	case blockNotice:
		return s.notice.Render("· " + strings.TrimRight(b.text, "\n"))
	case blockError:
		return lipgloss.JoinVertical(lipgloss.Left, s.errorTag.Render("error"), s.errorTag.Render(strings.TrimRight(b.text, "\n")))
	default:
		return s.muted.Render(body)
	}
}

const (
	// maxToolLines caps how much of a tool result reaches the transcript.
	// The model has already been given the whole thing; the transcript only
	// needs enough for a person to follow what happened, and a search over
	// a large tree can return far more than that.
	maxToolLines = 20
	// maxToolArgBytes caps the arguments line. Arguments are normally a
	// short JSON object, but nothing stops a model from sending a long one,
	// and the arguments are context for the result rather than the point.
	maxToolArgBytes = 200
)

// toolView builds the transcript text for a tool call.
//
// The arguments come first because they are the model's own choice and the
// shortest thing to check when a result looks wrong; the result follows,
// indented, so that a multi-line result reads as one thing hanging off the
// call rather than as a second block.
func toolView(arguments, result string) string {
	var b strings.Builder
	b.WriteString(toolArgs(arguments))

	body := clipLines(result, maxToolLines)
	if body == "" {
		// A tool can legitimately return nothing — a search with no
		// matches — so say so rather than leaving the call looking
		// unanswered.
		b.WriteString("\n  (no output)")
		return b.String()
	}
	for _, line := range strings.Split(body, "\n") {
		b.WriteString("\n")
		if line != "" {
			b.WriteString("  ")
			b.WriteString(line)
		}
	}
	return b.String()
}

// toolArgs renders a tool's arguments as a single line.
func toolArgs(arguments string) string {
	args := strings.TrimSpace(arguments)
	if args == "" {
		return "(no arguments)"
	}
	// Arguments are one JSON object however the model chose to lay them
	// out, so newlines inside them are formatting rather than structure.
	args = strings.Join(strings.Fields(args), " ")
	if len(args) > maxToolArgBytes {
		args = args[:maxToolArgBytes] + "..."
	}
	return args
}

// clipLines trims text to at most limit lines and says how many were dropped.
// It mirrors the clipping the slash commands do, so that a long file looks the
// same in the transcript whether the person read it or the model did.
func clipLines(text string, limit int) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= limit {
		return text
	}
	dropped := len(lines) - limit
	return strings.Join(lines[:limit], "\n") + "\n\n... " + strconv.Itoa(dropped) + " more lines"
}
