package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// newAskCmd builds the one-shot conversational command.
//
// ask is the human-facing half of the headless pair: it streams assistant text
// to stdout as it arrives and nothing else, so it composes with a pager or a
// pipe. Its machine-facing twin is run --json.
func newAskCmd(e *env) *cobra.Command {
	var quiet bool
	var appendStdin bool

	cmd := &cobra.Command{
		Use:   "ask [prompt]",
		Short: "Send one prompt and stream the answer",
		Long: "Send a single prompt to the active provider and stream the reply to\n" +
			"standard output. Starts fresh unless --resume or --last is supplied.\n" +
			"Use --save <name> to keep the completed exchange.\n\n" +
			"With no prompt argument the prompt is read from standard input, which\n" +
			"makes ask usable at the end of a pipeline.",
		Example: `yonderllm ask "explain the borrow checker"
git diff | yonderllm ask --stdin "write a commit message for this diff"
yonderllm -p gemini ask "summarise the CAP theorem"`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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

			out := cmd.OutOrStdout()
			wrote := false
			for ev, err := range sess.Ask(ctx, prompt) {
				if err != nil {
					if wrote {
						// The answer was cut off mid-stream; end the
						// line so the error does not run into it.
						fmt.Fprintln(out)
					}
					return err
				}
				// Notices — a fallback to another provider, most often —
				// go to stderr so that stdout stays pure answer text.
				if ev.Notice != "" && !quiet {
					fmt.Fprintf(cmd.ErrOrStderr(), "yonderllm: %s\n", ev.Notice)
				}
				if ev.Delta != "" {
					if _, err := fmt.Fprint(out, ev.Delta); err != nil {
						return err
					}
					wrote = true
				}
			}

			// Providers rarely end on a newline, and a shell prompt that
			// starts mid-line looks like a bug.
			if wrote {
				_, err := fmt.Fprintln(out)
				return err
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress provider notices on stderr")
	cmd.Flags().BoolVar(&appendStdin, "stdin", false, "append piped stdin to the prompt argument")
	return cmd
}

// errNoPrompt is returned when neither argv nor stdin supplied anything. It is
// a usage problem rather than a runtime one, so it names the two ways to fix it.
var errNoPrompt = errors.New("no prompt: pass one as an argument or pipe it on stdin")

const maxPromptBytes = 4 * 1024 * 1024

var errPromptTooLarge = errors.New("prompt exceeds the 4 MiB limit; send a smaller input")

// readPrompt takes the prompt from argv when given and from stdin otherwise.
//
// Arguments are joined with spaces rather than requiring a single quoted
// string, because a shell splits an unquoted sentence and silently answering
// only the first word would be worse than either erroring or joining.
func readPrompt(in io.Reader, args []string) (string, error) {
	if len(args) > 0 {
		size := len(args) - 1
		for _, arg := range args {
			size += len(arg)
			if size > maxPromptBytes {
				return "", errPromptTooLarge
			}
		}
		prompt := strings.TrimSpace(strings.Join(args, " "))
		if prompt == "" {
			return "", errNoPrompt
		}
		return prompt, nil
	}

	// Bound stdin before allocating the entire input.
	data, err := io.ReadAll(io.LimitReader(bufio.NewReader(in), maxPromptBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading prompt from stdin: %w", err)
	}
	if len(data) > maxPromptBytes {
		return "", errPromptTooLarge
	}
	prompt := strings.TrimSpace(string(data))
	if prompt == "" {
		return "", errNoPrompt
	}
	return prompt, nil
}

func readPromptWithContext(in io.Reader, args []string, appendStdin bool) (string, error) {
	if !appendStdin || len(args) == 0 {
		return readPrompt(in, args)
	}
	if interactiveStdin(in) {
		return "", errors.New("--stdin requires piped input")
	}
	prompt, err := readPrompt(in, args)
	if err != nil {
		return "", err
	}
	piped, err := readPrompt(in, nil)
	if err != nil {
		return "", err
	}
	if len(prompt)+len(piped)+2 > maxPromptBytes {
		return "", errPromptTooLarge
	}
	return prompt + "\n\n" + piped, nil
}
