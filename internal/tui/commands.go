// This file implements the slash commands: the small set of things a session
// can be asked to do that are not questions for a model. They all run locally
// and return immediately, so none of them start a stream; each one answers by
// appending a block to the transcript, which is the same way a reply arrives.
package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// helpText is the reference shown by /help. It lists the keys as well as the
// commands, because the keys are the half of the interface that has nowhere
// else to be discovered.
const helpText = `Commands
  /model                 show the provider and model in use
  /model <model>         switch model on the current provider
  /model <provider>      switch provider, keeping its configured model
  /model <provider> <model>
                         switch both at once
  /clear                 forget the conversation so far
  /usage                 show requests used against the daily cap
  /help                  show this

Keys
  enter                  send
  ctrl+j                 newline
  pgup / pgdn            scroll the transcript
  ctrl+c                 stop a reply in flight, or quit`

// runCommand interprets a slash command. The text is guaranteed to start with
// a slash; anything it does not recognise is reported rather than sent to a
// model, because a mistyped command is far more likely than a question that
// happens to begin with a slash.
func (m model) runCommand(text string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(text)
	name := strings.ToLower(fields[0])
	args := fields[1:]

	switch name {
	case "/help":
		m.append(block{kind: blockInfo, text: helpText})
	case "/clear":
		m.clearSession()
	case "/usage":
		m.append(block{kind: blockInfo, text: m.usageReport()})
	case "/model":
		m.applyModel(args)
	default:
		m.append(block{kind: blockError, text: fmt.Sprintf("unknown command %q; try /help", name)})
	}

	return m, nil
}

// clearSession drops both halves of the conversation: the history the model is
// sent, and the transcript the user reads. Clearing only one would leave the
// two disagreeing about what has been said.
func (m *model) clearSession() {
	m.sess.Clear()
	m.blocks = []block{
		{kind: blockInfo, text: m.greeting()},
		{kind: blockNotice, text: "conversation cleared"},
	}
	m.refresh()
}

// applyModel handles /model in its four forms.
func (m *model) applyModel(args []string) {
	switch len(args) {
	case 0:
		m.append(block{kind: blockInfo, text: m.modelReport()})

	case 1:
		// A single argument is ambiguous by design: naming a provider is
		// the common case, and naming a model on the current provider is
		// the other. Matching against the configured providers first
		// resolves it without making the user say which they meant.
		if m.isProvider(args[0]) {
			m.setProvider(args[0])
			return
		}
		m.sess.SetModel(args[0])
		m.append(block{kind: blockNotice, text: fmt.Sprintf("model is now %s on %s", m.sess.Model(), m.sess.Provider())})

	case 2:
		if !m.setProvider(args[0]) {
			return
		}
		m.sess.SetModel(args[1])
		m.append(block{kind: blockNotice, text: fmt.Sprintf("model is now %s on %s", m.sess.Model(), m.sess.Provider())})

	default:
		m.append(block{kind: blockError, text: "usage: /model [provider] [model]"})
	}
}

// setProvider switches provider, reporting failure into the transcript. It
// returns whether the switch happened, so a caller part-way through a longer
// change can stop rather than apply half of it.
func (m *model) setProvider(name string) bool {
	if err := m.sess.SetProvider(name); err != nil {
		m.append(block{kind: blockError, text: err.Error()})
		return false
	}
	// A provider without a key is configured but unusable, and saying so
	// now is better than letting the next prompt fail.
	if !m.sess.Credentialed(name) {
		m.append(block{kind: blockNotice, text: fmt.Sprintf("%s has no API key set; prompts will fall back to another provider", name)})
	}
	m.append(block{kind: blockNotice, text: fmt.Sprintf("now using %s (%s)", m.sess.Provider(), m.sess.Model())})
	return true
}

// isProvider reports whether name is one of the configured providers.
func (m model) isProvider(name string) bool {
	for _, p := range m.sess.Providers() {
		if p == name {
			return true
		}
	}
	return false
}

// modelReport describes the current provider and model, and lists the others
// that could be switched to.
func (m model) modelReport() string {
	var b strings.Builder
	fmt.Fprintf(&b, "provider  %s\nmodel     %s\nmode      %s\n\nconfigured providers", m.sess.Provider(), m.sess.Model(), m.mode)

	for _, name := range m.sess.Providers() {
		mark := "  "
		if name == m.sess.Provider() {
			mark = "* "
		}
		note := ""
		if !m.sess.Credentialed(name) {
			note = "  (no API key)"
		}
		fmt.Fprintf(&b, "\n  %s%s%s", mark, name, note)
	}

	return b.String()
}

// usageReport summarises what the session has spent against the daily cap.
func (m model) usageReport() string {
	u := m.sess.Usage()

	var b strings.Builder
	fmt.Fprintf(&b, "requests  %d", u.Requests())
	if cap := u.Cap(); cap > 0 {
		fmt.Fprintf(&b, " of %d\nremaining %d", cap, u.Remaining())
	} else {
		b.WriteString("\nremaining uncapped")
	}

	totals := u.Totals()
	fmt.Fprintf(&b, "\ntokens    %d prompt, %d completion", totals.PromptTokens, totals.CompletionTokens)

	byProvider := u.ByProvider()
	if len(byProvider) == 0 {
		// An empty breakdown is not the same as a broken one, and saying
		// so is clearer than printing a heading over nothing.
		b.WriteString("\n\nno requests yet this session")
		return b.String()
	}

	names := make([]string, 0, len(byProvider))
	for name := range byProvider {
		names = append(names, name)
	}
	sort.Strings(names)

	b.WriteString("\n\nby provider")
	for _, name := range names {
		p := byProvider[name]
		fmt.Fprintf(&b, "\n  %s  %d requests, %d prompt, %d completion", name, p.Requests, p.PromptTokens, p.CompletionTokens)
	}

	return b.String()
}
