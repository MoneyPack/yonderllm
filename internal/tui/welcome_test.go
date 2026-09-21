package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"yonderllm/internal/session"
)

// A welcome is local presentation: it must not obscure a resumed transcript,
// a command result, an approval, or an exchange in flight.
func TestWelcomeYieldsToSessionActivity(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	const introduction = "Bring a question. Stay in your terminal."
	if !strings.Contains(m.View(), introduction) {
		t.Fatal("fresh session did not show the welcome")
	}
	for _, state := range []string{"busy", "approval", "transcript"} {
		t.Run(state, func(t *testing.T) {
			n := m
			switch state {
			case "busy":
				n.busy = true
			case "approval":
				n.asking = true
			case "transcript":
				n.append(block{kind: blockUser, text: "Existing conversation"})
			}
			if strings.Contains(n.View(), introduction) {
				t.Error("welcome obscures session activity")
			}
		})
	}
}

func TestWelcomeHeadlineStaysOutOfTranscript(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub", deltas: []string{"answer"}})
	const headline = "Your model lives yonder."
	if strings.Count(m.View(), headline) != 1 {
		t.Fatal("fresh frame must show exactly one welcome headline")
	}
	if strings.Contains(transcript(m), headline) {
		t.Error("presentation headline was committed to the transcript")
	}
	drainExchange(t, &m, "question")
	m.view.GotoTop()
	if strings.Contains(m.View(), headline) {
		t.Error("headline reappeared while scrolling conversation history")
	}
	m.clearSession()
	if strings.Contains(transcript(m), headline) {
		t.Error("clear copied the presentation headline into history")
	}
}

func TestSaveHintsMatchAvailabilityAndSyntax(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(fmt.Sprintf("available=%t", available), func(t *testing.T) {
			m := newTestModel(t, &stubProvider{name: "stub"})
			if available {
				store, err := session.OpenSessions(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				m.sessions = store
			}
			for _, size := range [][2]int{{100, 30}, {28, 10}, {40, 14}} {
				m.resize(size[0], size[1])
				for name, text := range map[string]string{"welcome": m.welcome(), "greeting": m.greeting()} {
					if available && !strings.Contains(text, "/save <name>") {
						t.Errorf("%s at %v missing required-name save hint: %q", name, size, text)
					}
					if !available && strings.Contains(text, "/save") {
						t.Errorf("%s advertises unavailable saving: %q", name, text)
					}
				}
			}
			m = typing(m, "/help")
			m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			help := m.blocks[len(m.blocks)-1].text
			if strings.Contains(help, "/save <name>") != available {
				t.Errorf("help save availability is wrong: %s", help)
			}
		})
	}
}

func TestWelcomeFitsSmallTerminals(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	for _, size := range [][2]int{{100, 30}, {40, 15}, {28, 9}, {12, 9}} {
		m.resize(size[0], size[1])
		out := m.welcome()
		if lipgloss.Width(out) > m.view.Width || lipgloss.Height(out) > m.view.Height {
			t.Errorf("welcome exceeds viewport at %v: %dx%d", size, lipgloss.Width(out), lipgloss.Height(out))
		}
		if size[0] >= 28 && !strings.Contains(out, "Your model lives yonder.") {
			t.Errorf("headline lost in short terminal at %v", size)
		}
	}
}
