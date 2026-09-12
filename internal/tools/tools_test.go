package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// asker stands in for the person an interactive interface would put a request
// to. It answers every question the same way and remembers what it was asked,
// because the wording of the question is as much a part of the contract as the
// verdict is: a reader shown too little cannot honestly say yes.
type asker struct {
	answer   bool
	requests []Request
}

// allow builds an asker that approves everything.
func allow() *asker { return &asker{answer: true} }

// refuse builds an asker that approves nothing.
func refuse() *asker { return &asker{answer: false} }

// ask is the [Approver] to hand to For.
func (a *asker) ask(ctx context.Context, req Request) bool {
	a.requests = append(a.requests, req)
	return a.answer
}

// only returns the single request the asker was put, failing the test if it was
// asked a different number of times than once.
func (a *asker) only(t *testing.T) Request {
	t.Helper()

	if len(a.requests) != 1 {
		t.Fatalf("the user was asked %d times, want exactly once", len(a.requests))
	}
	return a.requests[0]
}

// onDisk returns a file's contents from the working directory, which is where
// workspaceDir has pointed the tools.
func onDisk(t *testing.T, name string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.FromSlash(name))
	if err != nil {
		t.Fatalf("reading back %s: %v", name, err)
	}
	return string(data)
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
// not use would invite a call that can only be refused. An approver does not
// change this, because what a mode forbids is not the approver's to grant.
func TestForGivesChatNoTools(t *testing.T) {
	if got := For(perm.New(perm.Chat), allow().ask); len(got) != 0 {
		t.Errorf("For(chat) = %v, want no tools", names(got))
	}
}

// Code and agent both allow reading and searching outright, so both get the
// same pair, and in the same order every time: a set that reshuffled between
// calls would churn the prompt and defeat provider-side prompt caching.
func TestForGivesReadAndSearchWhereAllowed(t *testing.T) {
	want := []string{"read_file", "search_files"}
	for _, mode := range []perm.Mode{perm.Code, perm.Agent} {
		got := names(For(perm.New(mode), nil))
		if !slices.Equal(got, want) {
			t.Errorf("For(%s, no approver) = %v, want %v", mode, got, want)
		}
	}
}

// An approver is what turns a gated capability into an offered one. The same
// policy yields a different tool set depending on whether there is anybody to
// ask, which is the specification's rule stated as code: a run with nobody at
// the other end does not get the write. Code and agent differ in what they gate
// rather than in how the gate works, so each mode's full set is spelled out.
func TestForOffersGatedToolsOnlyWithAnApprover(t *testing.T) {
	wanted := map[perm.Mode][]string{
		perm.Code:  {"read_file", "search_files", "write_file"},
		perm.Agent: {"read_file", "search_files", "write_file", "run_command"},
	}
	for _, mode := range []perm.Mode{perm.Code, perm.Agent} {
		policy := perm.New(mode)

		withoutApprover := names(For(policy, nil))
		for _, gated := range []string{"write_file", "run_command"} {
			if slices.Contains(withoutApprover, gated) {
				t.Errorf("For(%s, no approver) = %v, want no %s", mode, withoutApprover, gated)
			}
		}

		want := wanted[mode]
		got := names(For(policy, allow().ask))
		if !slices.Equal(got, want) {
			t.Errorf("For(%s, approver) = %v, want %v", mode, got, want)
		}
	}
}

// Code proposes patches but never starts a process, which is the whole
// distinction between it and agent. A mode that could run a command while
// claiming not to would make the choice between the two meaningless.
func TestForWithholdsRunningFromCodeMode(t *testing.T) {
	got := names(For(perm.New(perm.Code), allow().ask))
	if slices.Contains(got, "run_command") {
		t.Errorf("For(code, approver) = %v, want no run_command", got)
	}
}

// Auto-approval is the one case where the policy has already answered, so the
// write is offered with no approver at all. Withholding it there would make
// the setting meaningless: a script that opted in would find the capability it
// asked for missing.
func TestForOffersWritingUnderAutoApprovalWithoutAnApprover(t *testing.T) {
	got := names(For(perm.NewAutoApprove(perm.Agent), nil))
	if !slices.Contains(got, "write_file") {
		t.Errorf("For(auto-approved agent, no approver) = %v, want write_file", got)
	}
	if !slices.Contains(got, "run_command") {
		t.Errorf("For(auto-approved agent, no approver) = %v, want run_command", got)
	}
}

// Every schema handed to a provider must be valid JSON describing an object,
// because a malformed one is rejected by the API for the whole request rather
// than for the offending tool, taking the reply down with it.
func TestToolSchemasAreWellFormed(t *testing.T) {
	for _, tool := range For(perm.New(perm.Agent), allow().ask) {
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

	tool := find(t, For(perm.New(perm.Code), nil), "read_file")
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

	tool := find(t, For(perm.New(perm.Code), nil), "read_file")
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

	tool := find(t, For(perm.New(perm.Code), nil), "read_file")
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

	tool := find(t, For(perm.New(perm.Code), nil), "read_file")
	got := wantErr(t, tool, `{"path": "../secrets.txt"}`)

	if !strings.Contains(got, "outside the workspace") {
		t.Errorf("read_file error = %q, want it to mention the workspace boundary", got)
	}
}

// A missing file is reported plainly rather than as an opaque syscall error,
// because the model reads this message and may retry with a better path.
func TestReadFileReportsAMissingFile(t *testing.T) {
	workspaceDir(t, map[string]string{"main.go": "package main\n"})

	tool := find(t, For(perm.New(perm.Code), nil), "read_file")
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

	tool := find(t, For(perm.New(perm.Code), nil), "search_files")
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

	tool := find(t, For(perm.New(perm.Code), nil), "search_files")
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

	tool := find(t, For(perm.New(perm.Code), nil), "search_files")
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

	for _, tool := range For(perm.New(perm.Code), allow().ask) {
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

	for _, tool := range For(perm.New(perm.Code), allow().ask) {
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
		{"write_file", "path", []string{`{}`, `{"path": "", "content": "x"}`, `{"path": "   ", "content": "x"}`}},
	}

	available := For(perm.New(perm.Code), allow().ask)
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
// cheapest correct thing is to do no work at all. Nor is that reader asked to
// approve anything, since a prompt nobody is there to answer would hang the
// call waiting for a verdict that cannot come.
func TestToolsStopBeforeTouchingTheFilesystemWhenCancelled(t *testing.T) {
	workspaceDir(t, map[string]string{"a.go": "package a\n"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	user := allow()
	for _, tool := range For(perm.New(perm.Code), user.ask) {
		arguments := `{"path": "a.go", "query": "package", "content": "package a\n"}`
		if _, err := tool.Run(ctx, arguments); err != context.Canceled {
			t.Errorf("%s error = %v, want %v", tool.Definition.Name, err, context.Canceled)
		}
	}
	if len(user.requests) != 0 {
		t.Errorf("the user was asked %d times after cancellation, want not at all", len(user.requests))
	}
}

// The point of the whole slice: an approved write reaches the disk, and the
// result names what happened so the model need not read the file back to learn
// that its own call succeeded.
func TestWriteFileCreatesAFileOnceApproved(t *testing.T) {
	workspaceDir(t, nil)

	user := allow()
	tool := find(t, For(perm.New(perm.Code), user.ask), "write_file")
	got := run(t, tool, `{"path": "internal/cli/run.go", "content": "package cli\n\nfunc Run() {}\n"}`)

	if want := "package cli\n\nfunc Run() {}\n"; onDisk(t, "internal/cli/run.go") != want {
		t.Errorf("internal/cli/run.go = %q, want %q", onDisk(t, "internal/cli/run.go"), want)
	}
	if !strings.Contains(got, "internal/cli/run.go") {
		t.Errorf("write_file = %q, want it to name the file", got)
	}
	if !strings.Contains(got, "3 lines") {
		t.Errorf("write_file = %q, want it to state the size written", got)
	}
	user.only(t)
}

// Overwriting replaces the file entirely rather than appending to it, which is
// what the tool's description promises the model. A write that merged with what
// was there would silently keep code the model meant to delete.
func TestWriteFileReplacesTheWholeFile(t *testing.T) {
	workspaceDir(t, map[string]string{"main.go": "package main\n\nfunc old() {}\n"})

	tool := find(t, For(perm.New(perm.Code), allow().ask), "write_file")
	run(t, tool, `{"path": "main.go", "content": "package main\n"}`)

	if got, want := onDisk(t, "main.go"), "package main\n"; got != want {
		t.Errorf("main.go = %q, want %q", got, want)
	}
}

// A refusal is an answer, not a failure. It comes back as an ordinary result so
// the model can propose something else, and the file it wanted to change is
// exactly as it was.
func TestWriteFileLeavesTheFileAloneWhenRefused(t *testing.T) {
	const before = "package main\n\nfunc keep() {}\n"
	workspaceDir(t, map[string]string{"main.go": before})

	user := refuse()
	tool := find(t, For(perm.New(perm.Code), user.ask), "write_file")

	got, err := tool.Run(context.Background(), `{"path": "main.go", "content": "package main\n"}`)
	if err != nil {
		t.Fatalf("a refused write failed with %v, want the refusal as a result", err)
	}
	if !strings.Contains(got, "refused") {
		t.Errorf("write_file = %q, want it to say the user refused", got)
	}
	if !strings.Contains(got, "main.go") {
		t.Errorf("write_file = %q, want it to name the file", got)
	}
	if onDisk(t, "main.go") != before {
		t.Errorf("main.go = %q, want it unchanged as %q", onDisk(t, "main.go"), before)
	}
	user.only(t)
}

// What the reader is shown is as much the contract as whether they were asked.
// A question that named a path and a byte count would be unanswerable, so an
// edit is put as a diff against what is on disk.
func TestWriteFileAsksWithADiffOfTheChange(t *testing.T) {
	workspaceDir(t, map[string]string{"main.go": "package main\n\nfunc old() {}\n"})

	user := allow()
	tool := find(t, For(perm.New(perm.Code), user.ask), "write_file")
	run(t, tool, `{"path": "main.go", "content": "package main\n\nfunc new() {}\n"}`)

	req := user.only(t)
	if req.Action != perm.Write {
		t.Errorf("request action = %s, want %s", req.Action, perm.Write)
	}
	if req.Target != "main.go" {
		t.Errorf("request target = %q, want %q", req.Target, "main.go")
	}
	if !strings.Contains(req.Detail, "-func old() {}") {
		t.Errorf("request detail = %q, want the removed line", req.Detail)
	}
	if !strings.Contains(req.Detail, "+func new() {}") {
		t.Errorf("request detail = %q, want the added line", req.Detail)
	}
}

// A new file has nothing to diff against, so the question says so outright: a
// reader shown a wall of added lines cannot tell creation from a rewrite that
// destroys everything already there.
func TestWriteFileAsksDifferentlyForANewFile(t *testing.T) {
	workspaceDir(t, nil)

	user := allow()
	tool := find(t, For(perm.New(perm.Code), user.ask), "write_file")
	run(t, tool, `{"path": "notes.md", "content": "x"}`)

	req := user.only(t)
	if !strings.Contains(req.Detail, "does not exist yet") {
		t.Errorf("request detail = %q, want it to say the file is new", req.Detail)
	}
}

// Auto-approval is the policy having answered already, so no question is put.
// Prompting anyway would make the setting a lie to the script that opted in.
func TestWriteFileDoesNotAskUnderAutoApproval(t *testing.T) {
	workspaceDir(t, nil)

	user := refuse()
	tool := find(t, For(perm.NewAutoApprove(perm.Agent), user.ask), "write_file")
	run(t, tool, `{"path": "notes.md", "content": "hello\n"}`)

	if got, want := onDisk(t, "notes.md"), "hello\n"; got != want {
		t.Errorf("notes.md = %q, want %q", got, want)
	}
	if len(user.requests) != 0 {
		t.Errorf("the user was asked %d times under auto-approval, want not at all", len(user.requests))
	}
}

// The workspace boundary holds regardless of who approved what: consent to
// write a path is not consent to leave the project, and the refusal reaches the
// model so it learns the edge exists.
func TestWriteFileRefusesAnEscapingPath(t *testing.T) {
	workspaceDir(t, map[string]string{"main.go": "package main\n"})

	tool := find(t, For(perm.New(perm.Code), allow().ask), "write_file")
	got := wantErr(t, tool, `{"path": "../escaped.txt", "content": "x"}`)

	if !strings.Contains(got, "outside the workspace") {
		t.Errorf("write_file error = %q, want it to mention the workspace boundary", got)
	}
}

// The point of the exec slice: an approved command runs in the project and its
// output comes back, with the exit status stated even on success so a command
// that printed nothing is not mistaken for one that failed silently.
func TestRunCommandReturnsOutputOnceApproved(t *testing.T) {
	workspaceDir(t, nil)

	user := allow()
	tool := find(t, For(perm.New(perm.Agent), user.ask), "run_command")
	got := run(t, tool, `{"command": ["go", "env", "GOOS"]}`)

	if !strings.Contains(got, runtime.GOOS) {
		t.Errorf("run_command = %q, want it to carry the output %q", got, runtime.GOOS)
	}
	if !strings.Contains(got, "exit status 0") {
		t.Errorf("run_command = %q, want it to state the exit status", got)
	}
	if !strings.Contains(got, "go env GOOS") {
		t.Errorf("run_command = %q, want it to repeat the command", got)
	}
	user.only(t)
}

// A command that fails has still run, so its exit status is a result and not an
// error: the model asked what happens and the answer is that it exited two.
// Reporting it as a failure of the call would tell the model its own tooling is
// broken when the truth is that the tests it ran did not pass.
func TestRunCommandReportsAFailureAsAResult(t *testing.T) {
	workspaceDir(t, nil)

	tool := find(t, For(perm.New(perm.Agent), allow().ask), "run_command")
	got := run(t, tool, `{"command": ["go", "help", "bogus-topic"]}`)

	if !strings.Contains(got, "exit status 2") {
		t.Errorf("run_command = %q, want the exit status of the failure", got)
	}
	if !strings.Contains(got, "unknown help topic") {
		t.Errorf("run_command = %q, want what the command printed", got)
	}
}

// A refusal is an answer here too. Nothing is started, and the result says so
// plainly rather than looking like a command that ran and produced nothing.
func TestRunCommandRunsNothingWhenRefused(t *testing.T) {
	workspaceDir(t, nil)

	user := refuse()
	tool := find(t, For(perm.New(perm.Agent), user.ask), "run_command")

	got, err := tool.Run(context.Background(), `{"command": ["go", "env", "GOOS"]}`)
	if err != nil {
		t.Fatalf("a refused command failed with %v, want the refusal as a result", err)
	}
	if !strings.Contains(got, "refused") {
		t.Errorf("run_command = %q, want it to say the user refused", got)
	}
	if !strings.Contains(got, "go") {
		t.Errorf("run_command = %q, want it to name the program", got)
	}
	if strings.Contains(got, "exit status") {
		t.Errorf("run_command = %q, want no sign that anything ran", got)
	}
	user.only(t)
}

// The vector the model wrote is the vector the reader judges, argument for
// argument. An argument holding a space is shown quoted, because approving
// go run "my file.go" is a different decision from approving two separate
// arguments, and a prompt that blurred them would be worse than none.
func TestRunCommandAsksWithTheExactArgumentVector(t *testing.T) {
	workspaceDir(t, nil)

	user := refuse()
	tool := find(t, For(perm.New(perm.Agent), user.ask), "run_command")
	run(t, tool, `{"command": ["go", "run", "my file.go"]}`)

	req := user.only(t)
	if req.Action != perm.Exec {
		t.Errorf("request action = %s, want %s", req.Action, perm.Exec)
	}
	if req.Target != "go" {
		t.Errorf("request target = %q, want %q", req.Target, "go")
	}
	if want := `go run "my file.go"`; req.Detail != want {
		t.Errorf("request detail = %q, want %q", req.Detail, want)
	}
}

// Auto-approval spares the reader the tedious commands, so a harmless one runs
// unasked; prompting anyway would make the setting a lie to whoever opted in.
func TestRunCommandDoesNotAskAboutAHarmlessCommandUnderAutoApproval(t *testing.T) {
	workspaceDir(t, nil)

	user := refuse()
	tool := find(t, For(perm.NewAutoApprove(perm.Agent), user.ask), "run_command")
	got := run(t, tool, `{"command": ["go", "env", "GOOS"]}`)

	if !strings.Contains(got, runtime.GOOS) {
		t.Errorf("run_command = %q, want the command to have run", got)
	}
	if len(user.requests) != 0 {
		t.Errorf("the user was asked %d times under auto-approval, want not at all", len(user.requests))
	}
}

// Auto-approval is a statement about tedium, not a waiver of anything
// irreversible: a destructive command is put to the reader however the mode is
// configured, and their refusal stops it.
func TestRunCommandStillAsksAboutADestructiveCommandUnderAutoApproval(t *testing.T) {
	workspaceDir(t, nil)

	user := refuse()
	tool := find(t, For(perm.NewAutoApprove(perm.Agent), user.ask), "run_command")
	got := run(t, tool, `{"command": ["git", "push", "--force"]}`)

	req := user.only(t)
	if req.Target != "git" {
		t.Errorf("request target = %q, want %q", req.Target, "git")
	}
	if !strings.Contains(got, "refused") {
		t.Errorf("run_command = %q, want the refusal to have stopped it", got)
	}
}

// The generic argument tests run in code mode, which has no exec at all, so the
// same mistakes are put to run_command separately. Each message tells the model
// what to send next: an object, a repaired object, or the argument it left out.
func TestRunCommandExplainsBadArguments(t *testing.T) {
	workspaceDir(t, nil)

	tool := find(t, For(perm.New(perm.Agent), allow().ask), "run_command")

	for _, arguments := range []string{"", "   "} {
		want := "no arguments were given; send a JSON object"
		if got := wantErr(t, tool, arguments); got != want {
			t.Errorf("run_command(%q) error = %q, want %q", arguments, got, want)
		}
	}

	if got := wantErr(t, tool, `{"command": [`); !strings.Contains(got, "arguments are not valid JSON") {
		t.Errorf("run_command error = %q, want it to blame the JSON", got)
	}

	want := fmt.Sprintf("the %q argument is required and must not be empty", "command")
	for _, arguments := range []string{`{}`, `{"command": []}`, `{"command": [""]}`, `{"command": ["   "]}`} {
		if got := wantErr(t, tool, arguments); got != want {
			t.Errorf("run_command(%s) error = %q, want %q", arguments, got, want)
		}
	}
}

// A cancelled round starts no process and asks no question, for the same reason
// the file tools touch no disk: the reader who would answer has gone.
func TestRunCommandStartsNothingWhenCancelled(t *testing.T) {
	workspaceDir(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	user := allow()
	tool := find(t, For(perm.New(perm.Agent), user.ask), "run_command")

	if _, err := tool.Run(ctx, `{"command": ["go", "env", "GOOS"]}`); err != context.Canceled {
		t.Errorf("run_command error = %v, want %v", err, context.Canceled)
	}
	if len(user.requests) != 0 {
		t.Errorf("the user was asked %d times after cancellation, want not at all", len(user.requests))
	}
}

// Approval is not a way out of the project. A program named by an absolute path
// is refused however the reader answered, and the refusal reaches the model so
// it learns the edge is there.
func TestRunCommandRefusesAProgramOutsideTheWorkspace(t *testing.T) {
	workspaceDir(t, nil)

	tool := find(t, For(perm.New(perm.Agent), allow().ask), "run_command")

	for _, arguments := range []string{`{"command": ["/bin/sh"]}`, `{"command": ["C:\\Windows\\System32\\cmd.exe"]}`, `{"command": ["../tool"]}`} {
		if got := wantErr(t, tool, arguments); !strings.Contains(got, "outside the workspace") {
			t.Errorf("run_command(%s) error = %q, want it to mention the workspace boundary", arguments, got)
		}
	}
}
