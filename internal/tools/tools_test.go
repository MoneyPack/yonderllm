package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yonderllm/internal/perm"
	"yonderllm/internal/session"
)

// workspaceDir builds a project for a tool to look at and makes it the working
// directory, because the tools open the workspace themselves rather than
// accepting one. Files are given as a path relative to the directory mapped to
// its contents; parent directories are created as needed.
func workspaceDir(t *testing.T, files map[string]string) {
	t.Helper()

	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
	}
	t.Chdir(dir)
}

// names lists the tool names a policy yields, which is what the model sees.
func names(tools []session.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Definition.Name)
	}
	return out
}

// find returns the named tool, failing the test if the policy withheld it.
func find(t *testing.T, tools []session.Tool, name string) session.Tool {
	t.Helper()

	for _, tool := range tools {
		if tool.Definition.Name == name {
			return tool
		}
	}
	t.Fatalf("no tool named %q in %v", name, names(tools))
	return session.Tool{}
}

// run calls a tool and insists it succeed, since most tests are about the text
// a working call produces rather than about failure.
func run(t *testing.T, tool session.Tool, arguments string) string {
	t.Helper()

	out, err := tool.Run(context.Background(), arguments)
	if err != nil {
		t.Fatalf("%s(%s) failed: %v", tool.Definition.Name, arguments, err)
	}
	return out
}

// wantErr calls a tool expecting failure and returns the message, which is the
// text the model will read as the call's result.
func wantErr(t *testing.T, tool session.Tool, arguments string) string {
	t.Helper()

	out, err := tool.Run(context.Background(), arguments)
	if err == nil {
		t.Fatalf("%s(%s) = %q, want an error", tool.Definition.Name, arguments, out)
	}
	return err.Error()
}

// A mode that permits nothing must describe nothing: chat is a conversation
// with a model that cannot touch the machine, and advertising a tool it may
// not use would invite a call that can only be refused.
func TestForGivesChatNoTools(t *testing.T) {
	if got := For(perm.New(perm.Chat)); len(got) != 0 {
		t.Errorf("For(chat) = %v, want no tools", names(got))
	}
}

// Code and agent both allow reading and searching outright, so both get the
// same pair, and in the same order every time: a set that reshuffled between
// calls would churn the prompt and defeat provider-side prompt caching.
func TestForGivesReadAndSearchWhereAllowed(t *testing.T) {
	want := []string{"read_file", "search_files"}
	for _, mode := range []perm.Mode{perm.Code, perm.Agent} {
		got := names(For(perm.New(mode)))
		if !slices.Equal(got, want) {
			t.Errorf("For(%s) = %v, want %v", mode, got, want)
		}
	}
}

// Every schema handed to a provider must be valid JSON describing an object,
// because a malformed one is rejected by the API for the whole request rather
// than for the offending tool, taking the reply down with it.
func TestToolSchemasAreWellFormed(t *testing.T) {
	for _, tool := range For(perm.New(perm.Agent)) {
		var schema struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(tool.Definition.Parameters, &schema); err != nil {
			t.Errorf("%s parameters are not valid JSON: %v", tool.Definition.Name, err)
			continue
		}
		if schema.Type != "object" {
			t.Errorf("%s schema type = %q, want %q", tool.Definition.Name, schema.Type, "object")
		}
		if len(schema.Required) == 0 {
			t.Errorf("%s schema requires nothing, want a required argument", tool.Definition.Name)
		}
		for _, field := range schema.Required {
			if _, ok := schema.Properties[field]; !ok {
				t.Errorf("%s requires %q but does not describe it", tool.Definition.Name, field)
			}
		}
		if tool.Definition.Description == "" {
			t.Errorf("%s has no description", tool.Definition.Name)
		}
	}
}

// A read names the file above its contents, because the result reaches the
// model detached from the call that asked for it.
func TestReadFileReturnsTheFileUnderItsPath(t *testing.T) {
	workspaceDir(t, map[string]string{
		"internal/perm/perm.go": "package perm\n\nconst answer = 42\n",
	})

	tool := find(t, For(perm.New(perm.Code)), "read_file")
	got := run(t, tool, `{"path": "internal/perm/perm.go"}`)

	want := "internal/perm/perm.go\npackage perm\n\nconst answer = 42"
	if got != want {
		t.Errorf("read_file = %q, want %q", got, want)
	}
}

// An empty file is a fact about the project, so it is stated rather than
// returned as a blank result the model would have to interpret.
func TestReadFileReportsAnEmptyFile(t *testing.T) {
	workspaceDir(t, map[string]string{"TODO.md": ""})

	tool := find(t, For(perm.New(perm.Code)), "read_file")
	got := run(t, tool, `{"path": "TODO.md"}`)

	want := "TODO.md\n(empty file)"
	if got != want {
		t.Errorf("read_file = %q, want %q", got, want)
	}
}

// A file too big for one result is cut at a line boundary and the loss is
// declared, so the model can narrow its next request instead of reasoning from
// a fragment it believes is whole.
func TestReadFileClipsALongFileAndSaysSo(t *testing.T) {
	const lines = 2000
	var b strings.Builder
	for i := range lines {
		fmt.Fprintf(&b, "line %d of the very long file\n", i)
	}
	workspaceDir(t, map[string]string{"long.txt": b.String()})

	tool := find(t, For(perm.New(perm.Code)), "read_file")
	got := run(t, tool, `{"path": "long.txt"}`)

	if len(got) > maxResultBytes+200 {
		t.Errorf("read_file returned %d bytes, want about %d", len(got), maxResultBytes)
	}
	if !strings.Contains(got, "more lines were not included") {
		t.Errorf("read_file = %q, want a note that lines were dropped", got)
	}
	if strings.Contains(got, "line 1999 of") {
		t.Error("read_file kept the last line, want the tail dropped")
	}
	if !strings.Contains(got, "line 0 of the very long file") {
		t.Error("read_file dropped the first line, want the head kept")
	}
}

// A path outside the project is refused by the workspace, and the refusal is
// passed back as the result so the model learns the boundary exists.
func TestReadFileRefusesAnEscapingPath(t *testing.T) {
	workspaceDir(t, map[string]string{"main.go": "package main\n"})

	tool := find(t, For(perm.New(perm.Code)), "read_file")
	got := wantErr(t, tool, `{"path": "../secrets.txt"}`)

	if !strings.Contains(got, "outside the workspace") {
		t.Errorf("read_file error = %q, want it to mention the workspace boundary", got)
	}
}

// A missing file is reported plainly rather than as an opaque syscall error,
// because the model reads this message and may retry with a better path.
func TestReadFileReportsAMissingFile(t *testing.T) {
	workspaceDir(t, map[string]string{"main.go": "package main\n"})

	tool := find(t, For(perm.New(perm.Code)), "read_file")
	got := wantErr(t, tool, `{"path": "nope.go"}`)

	if !strings.Contains(got, "no file named") {
		t.Errorf("read_file error = %q, want it to say the file is missing", got)
	}
}

// Search reports how many lines matched and where each one is, so a model can
// go straight to a read without a second search to locate the file.
func TestSearchFilesListsMatchesWithPaths(t *testing.T) {
	workspaceDir(t, map[string]string{
		"a.go": "package a\n\nfunc Answer() int { return 42 }\n",
		"b.go": "package b\n",
	})

	tool := find(t, For(perm.New(perm.Code)), "search_files")
	got := run(t, tool, `{"query": "func Answer"}`)

	want := "1 matches for \"func Answer\"\na.go:3: func Answer() int { return 42 }"
	if got != want {
		t.Errorf("search_files = %q, want %q", got, want)
	}
}

// Finding nothing is an answer about the project, not a failure: raised as an
// error it would look like a broken tool and invite a pointless retry.
func TestSearchFilesReportsNoMatchesAsAResult(t *testing.T) {
	workspaceDir(t, map[string]string{"a.go": "package a\n"})

	tool := find(t, For(perm.New(perm.Code)), "search_files")
	got := run(t, tool, `{"query": "nowhere"}`)

	want := `no matches for "nowhere"`
	if got != want {
		t.Errorf("search_files = %q, want %q", got, want)
	}
}

// Too many matches to carry are cut at a line boundary with the loss declared,
// pointing the model at the fix that actually works: a narrower query.
func TestSearchFilesClipsManyMatchesAndSaysSo(t *testing.T) {
	var b strings.Builder
	for i := range 400 {
		fmt.Fprintf(&b, "needle %d and a good deal of trailing text to make the line long\n", i)
	}
	workspaceDir(t, map[string]string{"big.txt": b.String()})

	tool := find(t, For(perm.New(perm.Code)), "search_files")
	got := run(t, tool, `{"query": "needle"}`)

	if len(got) > maxResultBytes+200 {
		t.Errorf("search_files returned %d bytes, want about %d", len(got), maxResultBytes)
	}
	if !strings.Contains(got, "narrow the query") {
		t.Errorf("search_files = %q, want advice to narrow the query", got)
	}
}

// Arguments are whatever text the model produced, so bad JSON is an ordinary
// outcome. The message names the problem because the model reads it as the
// call's result and can correct itself on the next round.
func TestToolsExplainUnparsableArguments(t *testing.T) {
	workspaceDir(t, map[string]string{"a.go": "package a\n"})

	for _, tool := range For(perm.New(perm.Code)) {
		got := wantErr(t, tool, `{"path": `)
		if !strings.Contains(got, "arguments are not valid JSON") {
			t.Errorf("%s error = %q, want it to blame the JSON", tool.Definition.Name, got)
		}
	}
}

// A call with no arguments at all is distinguished from malformed ones,
// because the fix differs: the model must send an object, not repair one.
func TestToolsExplainAbsentArguments(t *testing.T) {
	workspaceDir(t, map[string]string{"a.go": "package a\n"})

	for _, tool := range For(perm.New(perm.Code)) {
		for _, arguments := range []string{"", "   "} {
			got := wantErr(t, tool, arguments)
			want := "no arguments were given; send a JSON object"
			if got != want {
				t.Errorf("%s(%q) error = %q, want %q", tool.Definition.Name, arguments, got, want)
			}
		}
	}
}

// A required argument that is missing, empty or only spaces is the same
// mistake, and naming the argument tells the model exactly what to add.
func TestToolsNameAMissingRequiredArgument(t *testing.T) {
	workspaceDir(t, map[string]string{"a.go": "package a\n"})

	cases := []struct {
		tool      string
		field     string
		arguments []string
	}{
		{"read_file", "path", []string{`{}`, `{"path": ""}`, `{"path": "   "}`}},
		{"search_files", "query", []string{`{}`, `{"query": ""}`, `{"query": "   "}`}},
	}

	available := For(perm.New(perm.Code))
	for _, c := range cases {
		tool := find(t, available, c.tool)
		want := fmt.Sprintf("the %q argument is required and must not be empty", c.field)
		for _, arguments := range c.arguments {
			if got := wantErr(t, tool, arguments); got != want {
				t.Errorf("%s(%s) error = %q, want %q", c.tool, arguments, got, want)
			}
		}
	}
}

// A cancelled context stops a tool before it touches the filesystem: the round
// runs after the model has spoken, so if the reader has walked away the
// cheapest correct thing is to do no work at all.
func TestToolsStopBeforeTouchingTheFilesystemWhenCancelled(t *testing.T) {
	workspaceDir(t, map[string]string{"a.go": "package a\n"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tool := range For(perm.New(perm.Code)) {
		arguments := `{"path": "a.go", "query": "package"}`
		if _, err := tool.Run(ctx, arguments); err != context.Canceled {
			t.Errorf("%s error = %v, want %v", tool.Definition.Name, err, context.Canceled)
		}
	}
}
