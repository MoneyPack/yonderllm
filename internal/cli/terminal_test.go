package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"iter"
	"yonderllm/internal/config"
	"yonderllm/internal/provider"
	"yonderllm/internal/session"
	"yonderllm/internal/terminaltext"
)

type terminalProvider struct{}

func (terminalProvider) Name() string                                     { return "test" }
func (terminalProvider) Models(context.Context) ([]provider.Model, error) { return nil, nil }
func (terminalProvider) Stream(context.Context, provider.Request) iter.Seq2[provider.Chunk, error] {
	return func(yield func(provider.Chunk, error) bool) {
		for _, text := range []string{"\x1b", "]52;c;payload", "\a\u009b2J\u202e"} {
			if !yield(provider.Chunk{Delta: text}, nil) {
				return
			}
		}
		yield(provider.Chunk{Finish: provider.FinishStop}, nil)
	}
}

func TestTerminalPlainEscapesButJSONAndPipePreserveContent(t *testing.T) {
	const original = "\x1b]52;c;payload\a\u009b2J\u202e"
	for _, mode := range []string{"terminal", "json", "pipe"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			cmd := &cobra.Command{}
			w := terminaltext.NewWriter(&out)
			cmd.SetOut(w)
			if mode == "pipe" {
				cmd.SetOut(&out)
			}
			cmd.SetErr(&bytes.Buffer{})
			cfg := config.Config{Provider: "test", MaxTokens: 100, Providers: map[string]config.ProviderConfig{"test": {Model: "tiny"}}}
			s := session.New(cfg, func(string) (provider.Provider, error) { return terminalProvider{}, nil })
			var err error
			if mode == "json" {
				err = streamJSON(context.Background(), cmd, s, "hello")
			} else {
				err = streamPlain(context.Background(), cmd, s, "hello")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := w.Flush(); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "terminal":
				if out.String() != terminaltext.Text(original)+"\n" {
					t.Fatalf("unsafe terminal output: %q", out.String())
				}
			case "pipe":
				if out.String() != original+"\n" {
					t.Fatalf("pipe changed: %q", out.String())
				}
			case "json":
				var answer string
				for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
					var event wireEvent
					if err := json.Unmarshal([]byte(line), &event); err != nil {
						t.Fatal(err)
					}
					answer += event.Delta
				}
				if answer != original {
					t.Fatalf("JSON data changed: %q", answer)
				}
			}
		})
	}
}
