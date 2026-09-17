package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"yonderllm/internal/provider"
	"yonderllm/internal/session"
)

// newRunCmd builds the headless command.
//
// run exists so that another program can drive yonderllm without scraping
// human output. Its default form is identical to ask; --json switches it to a
// newline-delimited event stream that mirrors [session.Event] one for one.
func newRunCmd(e *env) *cobra.Command {
	var asJSON bool
	var appendStdin bool

	cmd := &cobra.Command{
		Use:   "run [prompt]",
		Short: "Send one prompt, optionally as a machine-readable stream",
		Long: "Send a single prompt and emit the result for a program to consume.\n\n" +
			"With --json, every event is written to standard output as one JSON\n" +
			"object per line: text deltas as they stream, provider notices, the\n" +
			"tool calls the model makes and what they returned, and a final\n" +
			"object carrying token usage. Failures are emitted as an event too,\n" +
			"so a consumer reading line by line never has to parse stderr.\n\n" +
			"With no prompt argument the prompt is read from standard input.\n" +
			"With no prompt argument and nothing piped in, the interactive\n" +
			"session opens instead.",
		Example: `yonderllm run --json "list three sorting algorithms"
echo "explain this" | yonderllm run --json
yonderllm run --json "hello" | jq -r 'select(.type=="delta").delta'`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("json") {
				cfg, err := e.resolve()
				if err != nil {
					return err
				}
				asJSON = cfg.OutputFormat == "json"
			}
			// `run` with no prompt at a terminal is the spec's second
			// door into the interactive session. --json rules it out:
			// a caller asking for machine-readable events wants the
			// stdin read to fail loudly, not a full-screen interface.
			if len(args) == 0 && !asJSON && !appendStdin && interactiveStdin(cmd.InOrStdin()) {
				return e.runTUI()
			}

			prompt, err := readPromptWithContext(cmd.InOrStdin(), args, appendStdin)
			if err != nil {
				return err
			}

			sess, err := e.resolveSessionOrDefault()
			if err != nil {
				return err
			}

			ctx, stop := signalContext(cmd)
			defer stop()

			if !asJSON {
				return streamPlain(ctx, cmd, sess, prompt)
			}
			return streamJSON(ctx, cmd, sess, prompt)
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit newline-delimited JSON events instead of text")
	cmd.Flags().BoolVar(&appendStdin, "stdin", false, "append piped stdin to the prompt argument")
	return cmd
}

// wireEvent is the on-the-wire shape of an event.
//
// It is deliberately a separate type from [session.Event] rather than json tags
// on that struct: this is a published interface that other programs parse, and
// it should not shift because an internal field was renamed. The type field is
// added so a consumer can switch on a single key instead of inferring the kind
// from which fields happen to be present.
type wireEvent struct {
	SchemaVersion int        `json:"schema_version"`
	Type          string     `json:"type"`
	Delta         string     `json:"delta,omitempty"`
	Notice        string     `json:"notice,omitempty"`
	Provider      string     `json:"provider,omitempty"`
	Model         string     `json:"model,omitempty"`
	Tool          *wireTool  `json:"tool,omitempty"`
	Error         string     `json:"error,omitempty"`
	Usage         *wireUsage `json:"usage,omitempty"`
}

// wireTool describes one tool invocation.
//
// A call and its outcome are two events rather than one field that fills in
// later, so a consumer can report that a tool is running before it finishes.
// The id is what ties them together.
//
// Arguments is a string holding the JSON the model produced, not inlined JSON,
// because the model's output is never validated: a malformed object would
// otherwise corrupt the line it travels on and take the whole stream with it.
type wireTool struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
}

// newWireTool converts a run, using its Finished flag to pick the event type so
// that the caller cannot label a start as a result or the other way round.
func newWireTool(run *session.ToolRun) (string, *wireTool) {
	t := &wireTool{ID: run.ID, Name: run.Name, Arguments: run.Arguments}
	if !run.Finished {
		return "tool", t
	}
	t.Result = run.Result
	t.Error = run.Err
	return "tool_result", t
}

// wireUsage mirrors [provider.Usage] with the total precomputed, because the
// sum is what a caller almost always wants and computing it here means every
// consumer computes it the same way.
type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func newWireUsage(u *provider.Usage) *wireUsage {
	if u == nil {
		return nil
	}
	return &wireUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.PromptTokens + u.CompletionTokens,
	}
}

// streamJSON writes one JSON object per event.
//
// A failure is reported as an error event and then returned, so the stream is
// self-describing for a consumer that only reads stdout while the exit code
// still tells a shell that something went wrong.
func streamJSON(ctx context.Context, cmd *cobra.Command, sess *session.Session, prompt string) error {
	enc := json.NewEncoder(cmd.OutOrStdout())

	var outputErr error
	emit := func(ev wireEvent) {
		ev.SchemaVersion = 1
		ev.Model = sess.Model()
		if ev.Provider == "" {
			ev.Provider = sess.Provider()
		}
		if outputErr == nil {
			outputErr = enc.Encode(ev)
		}
	}

	for ev, err := range sess.Ask(ctx, prompt) {
		if err != nil {
			emit(wireEvent{Type: "error", Error: err.Error()})
			if outputErr != nil {
				return outputErr
			}
			return err
		}
		switch {
		case ev.Tool != nil:
			typ, t := newWireTool(ev.Tool)
			emit(wireEvent{Type: typ, Provider: ev.Provider, Tool: t})
		case ev.Notice != "":
			emit(wireEvent{Type: "notice", Notice: ev.Notice, Provider: ev.Provider})
		case ev.Delta != "":
			emit(wireEvent{Type: "delta", Delta: ev.Delta, Provider: ev.Provider})
		}
		if ev.Done {
			emit(wireEvent{Type: "done", Provider: ev.Provider, Usage: newWireUsage(ev.Usage)})
		}
		if outputErr != nil {
			return outputErr
		}
	}
	return nil
}

// streamPlain is run without --json: the same behaviour as ask, kept in one
// implementation so the two commands cannot drift apart.
func streamPlain(ctx context.Context, cmd *cobra.Command, sess *session.Session, prompt string) error {
	out := cmd.OutOrStdout()
	wrote := false
	for ev, err := range sess.Ask(ctx, prompt) {
		if err != nil {
			if wrote {
				fmt.Fprintln(out)
			}
			return err
		}
		if ev.Notice != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "yonderllm: %s\n", ev.Notice)
		}
		if ev.Delta != "" {
			if _, err := fmt.Fprint(out, ev.Delta); err != nil {
				return err
			}
			wrote = true
		}
	}
	if wrote {
		_, err := fmt.Fprintln(out)
		return err
	}
	return nil
}
