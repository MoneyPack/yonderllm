package tui

import (
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
	tag  string
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
	case blockNotice:
		return s.notice.Render("· " + strings.TrimRight(b.text, "\n"))
	case blockError:
		return lipgloss.JoinVertical(lipgloss.Left, s.errorTag.Render("error"), s.errorTag.Render(strings.TrimRight(b.text, "\n")))
	default:
		return s.muted.Render(body)
	}
}
