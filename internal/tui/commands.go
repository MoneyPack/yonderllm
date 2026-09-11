// This file implements the slash commands: the small set of things a session
// can be asked to do that are not questions for a model. They all run locally
// and return immediately, so none of them start a stream; each one answers by
// appending a block to the transcript, which is the same way a reply arrives.
package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"yonderllm/internal/perm"
	"yonderllm/internal/workspace"
)

// maxShownLines caps how much of a file /read puts on screen. A file the
// workspace will hand over can still be far longer than anyone wants to scroll
// past to reach the prompt again, and a truncated view that says so is more use
// than a complete one that buries the conversation.
const maxShownLines = 200

// helpText is the reference shown by /help. It lists the keys and the model's
// tools as well as the commands, because both are halves of the interface that
// have nowhere else to be discovered.
const helpText = `Commands
  /model                 show the provider and model in use
  /model <model>         switch model on the current provider
  /model <provider>      switch provider, keeping its configured model
  /model <provider> <model>
                         switch both at once
  /clear                 forget the conversation so far
  /read <file>           show a file from the working directory
  /search <text>         find that text in the working directory
  /usage                 show requests used against the daily cap
  /help                  show this

Tools (code and agent modes)
  read_file              the model reads a file from this project
  search_files           the model searches this project for text

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
	case "/read":
		m.readFile(remainder(text))
	case "/search":
		m.searchFiles(remainder(text))
	default:
		m.append(block{kind: blockError, text: fmt.Sprintf("unknown command %q; try /help", name)})
	}

	return m, nil
}

// remainder returns everything after the command word, with only the space that
// separated the two removed. Commands that take a filename or a phrase are
// given this rather than parsed fields, because splitting on whitespace would
// quietly lose a path with a space in it and would turn a multi-word search
// into its first word.
func remainder(text string) string {
	i := strings.IndexFunc(text, unicode.IsSpace)
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(text[i+1:])
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

// readFile shows a file from the working directory, if this mode is allowed to
// read one. The workspace is opened for the command and closed again rather
// than held open for the session, because the working directory is only of
// interest while a command is using it, and a handle kept across a whole
// session would outlive whatever made it relevant.
func (m *model) readFile(name string) {
	if name == "" {
		m.append(block{kind: blockError, text: "usage: /read <file>"})
		return
	}

	ws, err := workspace.Current(perm.New(m.mode))
	if err != nil {
		m.append(block{kind: blockError, text: err.Error()})
		return
	}
	defer ws.Close()

	data, err := ws.ReadFile(name)
	if err != nil {
		m.append(block{kind: blockError, text: err.Error()})
		return
	}

	m.append(block{kind: blockInfo, text: fileView(name, string(data))})
}

// fileView renders a file under a heading naming it, shortened to the first
// maxShownLines lines if it runs longer. The heading carries the name because
// the transcript scrolls, and a wall of text whose origin has scrolled away is
// hard to place.
func fileView(name, content string) string {
	content = strings.TrimRight(content, "\n")

	var b strings.Builder
	b.WriteString(name)

	if content == "" {
		b.WriteString("\n  (empty file)")
		return b.String()
	}

	lines := strings.Split(content, "\n")
	shown := lines
	if len(shown) > maxShownLines {
		shown = shown[:maxShownLines]
	}

	for _, line := range shown {
		b.WriteString("\n")
		b.WriteString(line)
	}

	if len(shown) < len(lines) {
		b.WriteString("\n\n... " + strconv.Itoa(len(lines)-len(shown)) + " more lines")
	}

	return b.String()
}

// searchFiles looks for literal text across the working directory, if this mode
// is allowed to search it.
func (m *model) searchFiles(query string) {
	if query == "" {
		m.append(block{kind: blockError, text: "usage: /search <text>"})
		return
	}

	ws, err := workspace.Current(perm.New(m.mode))
	if err != nil {
		m.append(block{kind: blockError, text: err.Error()})
		return
	}
	defer ws.Close()

	matches, err := ws.Search(query)
	if err != nil {
		m.append(block{kind: blockError, text: err.Error()})
		return
	}
	if len(matches) == 0 {
		// Finding nothing is an answer, not a failure, so it is reported
		// as a notice rather than an error.
		m.append(block{kind: blockNotice, text: fmt.Sprintf("no matches for %q", query)})
		return
	}

	m.append(block{kind: blockInfo, text: matchView(query, matches)})
}

// matchView renders search results as path:line: text, one per line, under a
// heading counting them.
func matchView(query string, matches []workspace.Match) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d matches for %q", len(matches), query)

	shown := matches
	if len(shown) > maxShownLines {
		shown = shown[:maxShownLines]
	}

	for _, match := range shown {
		fmt.Fprintf(&b, "\n%s:%d: %s", match.Path, match.Line, match.Text)
	}

	if len(shown) < len(matches) {
		b.WriteString("\n\n... " + strconv.Itoa(len(matches)-len(shown)) + " more matches")
	}

	return b.String()
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
