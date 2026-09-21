// Package tools gives a model hands.
//
// A [session.Tool] pairs a schema the model reads with a function the client
// runs, and this package supplies the pair for every capability yonderllm
// exposes. It sits above both session and workspace on purpose: session knows
// how to run a tool but not what a tool may touch, workspace knows how to
// touch the filesystem but not that a model exists, and the joint between
// them lives here rather than inside either.
//
// Which tools a model gets is a permission question, so [For] asks a
// [perm.Policy] instead of taking a list. A capability the active mode does
// not allow is never described to the model at all: refusing a call the model
// was invited to make wastes a round trip and teaches it nothing, whereas a
// tool it was never told about cannot be attempted.
//
// Capabilities the mode gates behind an approval are the same shape of
// question one step further on. The policy says an approval is needed but
// cannot collect one, so [For] takes an [Approver] from the caller that owns
// an interface and calls it at the moment the model asks. Without an approver
// the capability is withheld exactly as an unpermitted one is.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"yonderllm/internal/perm"
	"yonderllm/internal/provider"
	"yonderllm/internal/session"
	"yonderllm/internal/shell"
	"yonderllm/internal/workspace"
)

// maxResultBytes caps a single tool result.
//
// A result is not a report to the reader; it goes straight back into the next
// prompt, where it competes with the conversation for the context window. The
// default window leaves roughly 28KB for the whole prompt, so a file allowed
// to spend 8KB of it stays a large answer without being the only answer, and
// the history trimmer is never handed a turn so big that keeping it means
// discarding everything else.
const maxResultBytes = 8 << 10

// Request describes an action awaiting the user's word.
//
// It carries what a person needs to judge the action and nothing else:
// which capability is being used, what it would touch, and enough of the
// change itself to say yes or no honestly. Detail is a diff for a write and
// the exact argument vector for an exec, because an approval given against a
// summary is not really an approval.
type Request struct {
	Action perm.Action
	Target string
	Detail string
}

// Approver puts a [Request] to the user and reports whether they allowed it.
//
// The answer is a bare bool: refusal is not a failure, so there is no error
// to return. Everything that is not a clear yes — an empty answer, an
// interrupt, closed input, a cancelled context — is a no, which makes denial
// the outcome of every path an implementation forgets to handle.
type Approver func(ctx context.Context, req Request) bool

// For returns the tools policy permits, in a stable order.
//
// A capability the policy denies is never described to the model. One the
// policy gates behind [perm.Ask] is offered only when approver is non-nil,
// because a tool that cannot ask cannot honour the gate; withholding it is
// what the specification means by a capability being absent from a
// non-interactive run.
func For(policy perm.Policy, approver Approver) []session.Tool {
	permitted := func(action perm.Action) bool {
		switch policy.Check(action) {
		case perm.Allow:
			return true
		case perm.Ask:
			return approver != nil
		default:
			return false
		}
	}

	var out []session.Tool
	if permitted(perm.Read) {
		out = append(out, readTool(policy))
	}
	if permitted(perm.Search) {
		out = append(out, searchTool(policy))
	}
	if permitted(perm.Write) {
		out = append(out, writeTool(policy, approver))
	}
	if permitted(perm.Exec) {
		out = append(out, execTool(policy, approver))
	}
	return out
}

// readTool describes and implements reading one file.
func readTool(policy perm.Policy) session.Tool {
	return session.Tool{
		Definition: provider.Tool{
			Name: "read_file",
			Description: "Read a UTF-8 text file from the current project. The path must be " +
				"relative to the project root; paths outside it, binary files " +
				"and very large files are refused. Use search_files first if " +
				"you do not already know the path.",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Path to the file, relative to the project root, e.g. internal/cli/run.go"
    }
  },
  "required": ["path"],
  "additionalProperties": false
}`),
		},
		Run: func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Path string `json:"path"`
			}
			if err := decode(arguments, &args); err != nil {
				return "", err
			}
			if strings.TrimSpace(args.Path) == "" {
				return "", errNeeds("path")
			}

			ws, err := open(ctx, policy)
			if err != nil {
				return "", err
			}
			defer ws.Close()

			data, err := ws.ReadFile(args.Path)
			if err != nil {
				return "", err
			}
			return fileView(args.Path, string(data)), nil
		},
	}
}

// searchTool describes and implements searching the project.
func searchTool(policy perm.Policy) session.Tool {
	return session.Tool{
		Definition: provider.Tool{
			Name: "search_files",
			Description: "Search the current project for a literal, case-insensitive piece " +
				"of text and return the matching lines with their paths. The " +
				"query is not a regular expression: punctuation is matched as " +
				"typed. Version-control directories, dependencies and build " +
				"output are skipped.",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "Literal text to look for, e.g. func (p Policy)"
    }
  },
  "required": ["query"],
  "additionalProperties": false
}`),
		},
		Run: func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Query string `json:"query"`
			}
			if err := decode(arguments, &args); err != nil {
				return "", err
			}
			if strings.TrimSpace(args.Query) == "" {
				return "", errNeeds("query")
			}

			ws, err := open(ctx, policy)
			if err != nil {
				return "", err
			}
			defer ws.Close()

			matches, err := ws.Search(args.Query)
			if err != nil {
				return "", err
			}
			return matchView(args.Query, matches), nil
		},
	}
}

// writeTool describes and implements writing one file.
//
// The approval is collected here rather than in workspace because this is the
// only layer that knows both what the change is and who could consent to it.
// The old contents are read before the write so the question can show a diff:
// a person asked to approve a path and a byte count has been told nothing.
func writeTool(policy perm.Policy, approver Approver) session.Tool {
	return session.Tool{
		Definition: provider.Tool{
			Name: "write_file",
			Description: "Create or overwrite a UTF-8 text file in the current project, " +
				"replacing its entire contents. The path must be relative to " +
				"the project root; missing parent directories are created. " +
				"Read the file first when editing one that exists, because " +
				"anything omitted is lost. The user may refuse the write.",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Path to the file, relative to the project root, e.g. internal/cli/run.go"
    },
    "content": {
      "type": "string",
      "description": "The file's complete new contents. An empty string writes an empty file."
    }
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`),
		},
		Run: func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := decode(arguments, &args); err != nil {
				return "", err
			}
			if strings.TrimSpace(args.Path) == "" {
				return "", errNeeds("path")
			}

			// Checked before prompting, not just before writing: asking a
			// reader who has already walked away is worse than doing nothing.
			if err := ctx.Err(); err != nil {
				return "", err
			}

			ws, err := open(ctx, policy)
			if err != nil {
				return "", err
			}
			defer ws.Close()

			original, readErr := ws.ReadFile(args.Path)
			if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
				return "", fmt.Errorf("cannot snapshot file before approval: %w", readErr)
			}
			detail := changeFrom(original, readErr, args.Content)
			ok, err := consent(ctx, policy, approver, Request{
				Action: perm.Write,
				Target: args.Path,
				Detail: detail,
			}, false)
			if err != nil {
				return "", err
			}
			if !ok {
				return fmt.Sprintf("the user refused to write %s; the file is "+
					"unchanged. Ask what they would prefer instead of trying "+
					"again.", args.Path), nil
			}
			if err := ctx.Err(); err != nil {
				return "", err
			}
			current, currentErr := ws.ReadFile(args.Path)
			missingBefore, missingNow := errors.Is(readErr, fs.ErrNotExist), errors.Is(currentErr, fs.ErrNotExist)
			if missingBefore != missingNow || (currentErr != nil && !missingNow) || !bytes.Equal(original, current) {
				return "", fmt.Errorf("file changed while awaiting approval; read it again before proposing a write")
			}

			if err := ws.WriteFile(args.Path, []byte(args.Content)); err != nil {
				return "", err
			}
			return wroteView(args.Path, args.Content), nil
		},
	}
}

// execTool describes and implements running one command.
//
// The command arrives as an argument vector rather than a line of shell,
// because a string would have to be split by someone and every splitter is a
// place where quoting turns one command into another. A vector the model wrote
// is the vector that runs, which is also the only form honest enough to show a
// person being asked to approve it.
func execTool(policy perm.Policy, approver Approver) session.Tool {
	return session.Tool{
		Definition: provider.Tool{
			Name: "run_command",
			Description: "Run a program in the current project and return its output. The " +
				"command is an argument vector, not a shell line: there is no " +
				"shell, so pipes, redirection, globs and variable expansion do " +
				"not work, and each argument is passed through exactly as " +
				"given. The program must be on PATH or inside the project. " +
				"Output is captured together, the command is stopped after 30 " +
				"seconds, and a failing command reports its exit status rather " +
				"than an error. The user may refuse the command.",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {
      "type": "array",
      "items": {"type": "string"},
      "minItems": 1,
      "description": "The program followed by its arguments, e.g. [\"go\", \"test\", \"./internal/perm\"]"
    }
  },
  "required": ["command"],
  "additionalProperties": false
}`),
		},
		Run: func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Command []string `json:"command"`
			}
			if err := decode(arguments, &args); err != nil {
				return "", err
			}
			if len(args.Command) == 0 || strings.TrimSpace(args.Command[0]) == "" {
				return "", errNeeds("command")
			}

			// Checked before prompting, not just before running: asking a
			// reader who has already walked away is worse than doing nothing.
			if err := ctx.Err(); err != nil {
				return "", err
			}

			runner, err := shell.Current(policy)
			if err != nil {
				return "", err
			}

			ok, err := consent(ctx, policy, approver, Request{
				Action: perm.Exec,
				Target: args.Command[0],
				Detail: commandView(args.Command),
			}, shell.Destructive(args.Command))
			if err != nil {
				return "", err
			}
			if !ok {
				return fmt.Sprintf("the user refused to run %s; nothing was run. "+
					"Ask what they would prefer instead of trying again.",
					args.Command[0]), nil
			}

			result, err := runner.Run(ctx, args.Command)
			if err != nil {
				return "", err
			}
			return ranView(args.Command, result), nil
		},
	}
}

// consent decides whether a gated action may proceed.
//
// An allowed action needs no question: agent mode with auto-approval is
// exactly the case where the policy has already answered, and prompting anyway
// would make the setting a lie. The one exception is a destructive action,
// which [perm.Policy.Confirm] insists on asking about however the mode is
// configured, because auto-approval is a statement about tedium and not a
// waiver of anything irreversible. An asked action without an approver cannot
// happen — [For] withholds the tool — so reaching it means the wiring is
// wrong, and a bug that silently writes a file is worse than one that reports
// itself.
func consent(ctx context.Context, policy perm.Policy, approver Approver, req Request, destructive bool) (bool, error) {
	decision := policy.Check(req.Action)
	if decision == perm.Allow && policy.Confirm(req.Action, destructive) {
		decision = perm.Ask
	}

	switch decision {
	case perm.Allow:
		return true, nil
	case perm.Ask:
		if approver == nil {
			return false, fmt.Errorf("no way to ask the user about %s", req.Target)
		}
		return approver(ctx, req), nil
	default:
		return false, fmt.Errorf("this mode does not permit that")
	}
}

// change describes what writing content to name would do.
//
// A file that cannot be read is not an obstacle to approving a write: what is
// on disk being unshowable is itself worth telling the person, and refusing
// the write over it would make binary and oversized files permanently
// unwritable. Missing and unreadable are worded apart because one is a new
// file and the other is a file about to be destroyed.
func change(ws *workspace.Workspace, name, content string) string {
	old, err := ws.ReadFile(name)
	return changeFrom(old, err, content)
}

func changeFrom(old []byte, err error, content string) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "this file does not exist yet; it would be created with " +
			plural(lines(content), "line") + "\n\n" + diffView("", content)
	case err != nil:
		return fmt.Sprintf("the current contents cannot be shown (%v), so this "+
			"write cannot be compared against them; it would replace the file "+
			"with %s", err, plural(lines(content), "line"))
	default:
		return diffView(string(old), content)
	}
}

// wroteView confirms a completed write.
func wroteView(name, content string) string {
	return fmt.Sprintf("wrote %s (%s)", name, plural(lines(content), "line"))
}

// commandView renders an argument vector the way a person reads a command.
//
// Arguments are joined with spaces because that is the form everyone already
// knows, but an argument holding a space, a quote or nothing at all is shown
// quoted so the boundary between arguments stays visible. Someone approving
// rm "my file" has to be able to tell it from rm my file, which would delete
// two different things, and a prompt that blurs the two would be worse than
// no prompt at all.
func commandView(args []string) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		if arg == "" || strings.ContainsAny(arg, " \t\n\"'") {
			parts[i] = fmt.Sprintf("%q", arg)
			continue
		}
		parts[i] = arg
	}
	return strings.Join(parts, " ")
}

// ranView renders a finished command for the model.
//
// The command is repeated above its output for the same reason a file's path
// is: a result reaches the model detached from the call that asked for it. The
// exit status is stated even when it is zero, because a command that printed
// nothing while succeeding is otherwise indistinguishable from one that failed
// to say why it failed, and a model left to infer success from the shape of
// the output will sometimes infer wrong. Truncation is reported in the units it
// happened in — bytes the shell never captured, lines this result could not
// carry — since a model told only that something is missing cannot tell
// whether narrowing the command would help.
func ranView(args []string, result shell.Result) string {
	var b strings.Builder
	b.WriteString(commandView(args) + "\n")

	switch {
	case result.TimedOut:
		b.WriteString("timed out and was stopped before it finished")
	default:
		fmt.Fprintf(&b, "exit status %d", result.Code)
	}

	if output := strings.TrimRight(result.Output, "\n"); output == "" {
		b.WriteString("\n(no output)")
	} else {
		body, omitted := clip(output)
		b.WriteString("\n\n" + body)
		if omitted > 0 {
			fmt.Fprintf(&b, "\n\n(%d more lines were not included: the output "+
				"is longer than one tool result may carry)", omitted)
		}
	}

	if result.Dropped > 0 {
		fmt.Fprintf(&b, "\n\n(%s of further output were not captured: the "+
			"command printed more than is kept)", plural(result.Dropped, "byte"))
	}
	return b.String()
}

// lines counts the lines content occupies once written.
func lines(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(content, "\n"), "\n") + 1
}

// plural renders a count with its noun.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// open checks for cancellation and then opens the working directory.
//
// The workspace is opened per call rather than held for the session's life so
// that a directory the user has since left, renamed or deleted is discovered
// now instead of served stale, and so that no descriptor outlives the tool
// that needed it. Cancellation is checked first because a tool round runs
// after the model has already spoken: if the reader has walked away, the
// cheapest correct thing is to touch the filesystem not at all.
func open(ctx context.Context, policy perm.Policy) (*workspace.Workspace, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return workspace.Current(policy)
}

// decode parses a tool call's arguments.
//
// The arguments arrive as whatever string the model produced, so a parse
// failure is an ordinary outcome rather than a defect. The error names the
// problem plainly because it is handed back to the model as the call's
// result, and a model that can read what went wrong can call again correctly.
func decode(arguments string, into any) error {
	text := strings.TrimSpace(arguments)
	if text == "" {
		return errors.New("no arguments were given; send a JSON object")
	}
	if err := json.Unmarshal([]byte(text), into); err != nil {
		return fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	return nil
}

// errNeeds reports a missing required field.
func errNeeds(field string) error {
	return fmt.Errorf("the %q argument is required and must not be empty", field)
}

// fileView renders a file for the model.
//
// The path is repeated above the contents because a result reaches the model
// detached from the call that asked for it, and a model reasoning about
// several files at once should not have to remember which one it is holding.
func fileView(name, content string) string {
	body := strings.TrimRight(content, "\n")
	if body == "" {
		return name + "\n(empty file)"
	}

	body, omitted := clip(body)
	out := name + "\n" + body
	if omitted > 0 {
		out += fmt.Sprintf("\n\n(%d more lines were not included: the file is "+
			"longer than one tool result may carry)", omitted)
	}
	return out
}

// matchView renders search results for the model.
//
// Finding nothing is stated as a result rather than raised as an error, for
// the same reason the interactive command does: an absent match is an answer
// about the project, and a model told its search failed may waste a round
// retrying a query that worked.
func matchView(query string, matches []workspace.Match) string {
	if len(matches) == 0 {
		return fmt.Sprintf("no matches for %q", query)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d matches for %q\n", len(matches), query)
	for _, m := range matches {
		fmt.Fprintf(&b, "%s:%d: %s\n", m.Path, m.Line, m.Text)
	}

	body, omitted := clip(strings.TrimRight(b.String(), "\n"))
	if omitted > 0 {
		body += fmt.Sprintf("\n\n(%d more matches were not included: narrow "+
			"the query to see them)", omitted)
	}
	return body
}

// clip trims text to maxResultBytes at a line boundary and reports how many
// lines it dropped. Cutting mid-line would hand the model a truncated
// identifier that looks like a real one.
func clip(text string) (string, int) {
	if len(text) <= maxResultBytes {
		return text, 0
	}

	head := text[:maxResultBytes]
	if i := strings.LastIndexByte(head, '\n'); i > 0 {
		head = text[:i]
	}

	rest := strings.Trim(text[len(head):], "\n")
	if rest == "" {
		return head, 0
	}
	return head, strings.Count(rest, "\n") + 1
}
