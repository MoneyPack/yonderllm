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
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"yonderllm/internal/perm"
	"yonderllm/internal/provider"
	"yonderllm/internal/session"
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

// For returns the tools policy permits, in a stable order.
//
// Only capabilities the policy outright allows are included. A decision of
// [perm.Ask] is treated as a refusal here because nothing in this package can
// put a question to the user; modes that would ask are handled by the caller
// that owns an interface, and until one does, withholding is the honest
// answer.
func For(policy perm.Policy) []session.Tool {
	var out []session.Tool
	if policy.Check(perm.Read) == perm.Allow {
		out = append(out, readTool(policy))
	}
	if policy.Check(perm.Search) == perm.Allow {
		out = append(out, searchTool(policy))
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
