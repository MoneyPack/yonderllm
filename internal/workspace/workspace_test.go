package workspace

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yonderllm/internal/perm"
)

// open builds a workspace over a fresh temporary tree. The tree is per-test so
// that a search sees only what the test put there.
func open(t *testing.T, mode perm.Mode) (*Workspace, string) {
	t.Helper()
	dir := t.TempDir()
	w, err := Open(dir, perm.New(mode))
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", dir, err)
	}
	t.Cleanup(func() { w.Close() })
	return w, dir
}

// write places a file in the tree, creating any directories it names.
func write(t *testing.T, dir, rel, content string) string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll for %q failed: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %q failed: %v", rel, err)
	}
	return path
}

// wantFile asserts what is on disk, which is the only account of a write that
// does not take the writer's word for it.
func wantFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back %q failed: %v", rel, err)
	}
	if got := string(data); got != content {
		t.Errorf("%q holds %q, want %q", rel, got, content)
	}
}

// wantDenied asserts that err is the refusal a mode produces for an action,
// including the sentence a user would read.
func wantDenied(t *testing.T, err error, mode perm.Mode, action perm.Action) {
	t.Helper()
	if err == nil {
		t.Fatalf("want a refusal for %s in %s mode, got nil", action, mode)
	}
	var denied *perm.DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("want *perm.DeniedError, got %T: %v", err, err)
	}
	if denied.Mode != mode || denied.Action != action {
		t.Errorf("want refusal of %s in %s mode, got %s in %s mode",
			action, mode, denied.Action, denied.Mode)
	}
	want := action.String() + " is not permitted in " + mode.String() + " mode"
	if got := err.Error(); got != want {
		t.Errorf("refusal reads %q, want %q", got, want)
	}
}

func TestReadFileReturnsContentsInModesThatAllowReading(t *testing.T) {
	for _, mode := range []perm.Mode{perm.Code, perm.Agent} {
		t.Run(mode.String(), func(t *testing.T) {
			w, dir := open(t, mode)
			write(t, dir, "notes/todo.md", "remember the milk\n")

			data, err := w.ReadFile("notes/todo.md")
			if err != nil {
				t.Fatalf("ReadFile failed: %v", err)
			}
			if got := string(data); got != "remember the milk\n" {
				t.Errorf("ReadFile returned %q, want %q", got, "remember the milk\n")
			}
		})
	}
}

func TestReadFileIsRefusedInChatMode(t *testing.T) {
	w, dir := open(t, perm.Chat)
	write(t, dir, "todo.md", "remember the milk\n")

	data, err := w.ReadFile("todo.md")
	wantDenied(t, err, perm.Chat, perm.Read)
	if data != nil {
		t.Errorf("a refused read returned %d bytes, want none", len(data))
	}
}

func TestSearchIsRefusedInChatMode(t *testing.T) {
	w, dir := open(t, perm.Chat)
	write(t, dir, "todo.md", "remember the milk\n")

	matches, err := w.Search("milk")
	wantDenied(t, err, perm.Chat, perm.Search)
	if matches != nil {
		t.Errorf("a refused search returned %d matches, want none", len(matches))
	}
}

func TestAutoApproveDoesNotChangeReadingOrSearching(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "todo.md", "remember the milk\n")

	w, err := Open(dir, perm.NewAutoApprove(perm.Chat))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer w.Close()

	if _, err := w.ReadFile("todo.md"); err == nil {
		t.Error("auto-approve granted a read that chat mode forbids")
	}
	if _, err := w.Search("milk"); err == nil {
		t.Error("auto-approve granted a search that chat mode forbids")
	}
}

func TestSearchMatchesLiteralsIgnoringCase(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "sub/notes.md", "first line\nMILK and honey\nlast line\n")

	matches, err := w.Search("  Milk  ")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("Search returned %d matches, want 1: %+v", len(matches), matches)
	}
	got := matches[0]
	if got.Path != "sub/notes.md" {
		t.Errorf("match path is %q, want %q", got.Path, "sub/notes.md")
	}
	if got.Line != 2 {
		t.Errorf("match line is %d, want 2", got.Line)
	}
	if got.Text != "MILK and honey" {
		t.Errorf("match text is %q, want %q", got.Text, "MILK and honey")
	}
}

func TestSearchTreatsPunctuationAsTextNotSyntax(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "perm.go", "func (p Policy) Check(a Action) Decision {\n")

	matches, err := w.Search("func (p Policy)")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("a query with parentheses found %d matches, want 1", len(matches))
	}
}

func TestSearchStripsCarriageReturns(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "dos.txt", "milk\r\n")

	matches, err := w.Search("milk")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("Search returned %d matches, want 1", len(matches))
	}
	if matches[0].Text != "milk" {
		t.Errorf("match text is %q, want %q without the carriage return",
			matches[0].Text, "milk")
	}
}

func TestSearchNeedsSomethingToLookFor(t *testing.T) {
	w, _ := open(t, perm.Code)

	for _, query := range []string{"", "   ", "\t\n"} {
		matches, err := w.Search(query)
		if err == nil {
			t.Fatalf("Search(%q) was accepted, want a refusal", query)
		}
		if got, want := err.Error(), "workspace: search needs something to look for"; got != want {
			t.Errorf("Search(%q) failed with %q, want %q", query, got, want)
		}
		if matches != nil {
			t.Errorf("Search(%q) returned %d matches alongside an error", query, len(matches))
		}
	}
}

func TestSearchSkipsMachineryAndVendoredTrees(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "mine.txt", "milk\n")
	for name := range skipDirs {
		write(t, dir, name+"/theirs.txt", "milk\n")
	}

	matches, err := w.Search("milk")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("Search returned %d matches, want only the one outside a skipped tree: %+v",
			len(matches), matches)
	}
	if matches[0].Path != "mine.txt" {
		t.Errorf("match path is %q, want %q", matches[0].Path, "mine.txt")
	}
}

func TestSearchSearchesADirectoryNamedLikeASkippedOneAtTheRoot(t *testing.T) {
	// The root itself is never pruned, even when a temporary directory
	// happens to be called something on the list.
	dir := filepath.Join(t.TempDir(), "vendor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	write(t, dir, "mine.txt", "milk\n")

	w, err := Open(dir, perm.New(perm.Code))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer w.Close()

	matches, err := w.Search("milk")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("a root named vendor yielded %d matches, want 1", len(matches))
	}
}

func TestSearchStopsAtTheMatchLimit(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "a.txt", strings.Repeat("milk\n", maxMatches+50))
	write(t, dir, "b.txt", strings.Repeat("milk\n", 10))

	matches, err := w.Search("milk")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(matches) != maxMatches {
		t.Errorf("Search returned %d matches, want the cap of %d", len(matches), maxMatches)
	}
}

func TestSearchTruncatesAnOverlongLine(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "minified.js", "milk"+strings.Repeat("x", maxLineBytes+100)+"\n")

	matches, err := w.Search("milk")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("Search returned %d matches, want 1", len(matches))
	}
	text := matches[0].Text
	if !strings.HasSuffix(text, "...") {
		t.Errorf("a truncated line ends %q, want it to end in an ellipsis", text[len(text)-3:])
	}
	if len(text) != maxLineBytes+3 {
		t.Errorf("a truncated line is %d bytes, want %d", len(text), maxLineBytes+3)
	}
}

func TestSearchIgnoresFilesItCannotUsefullyRead(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "text.txt", "milk\n")
	write(t, dir, "image.png", "milk\x00\n")
	write(t, dir, "dump.sql", "milk\n"+strings.Repeat("x", maxFileBytes))

	matches, err := w.Search("milk")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("Search returned %d matches, want only the text file: %+v", len(matches), matches)
	}
	if matches[0].Path != "text.txt" {
		t.Errorf("match path is %q, want %q", matches[0].Path, "text.txt")
	}
}

func TestReadFileRefusesNamesOutsideTheWorkspace(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "inside.txt", "milk\n")
	outside := filepath.Join(filepath.Dir(dir), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("WriteFile outside the root failed: %v", err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	for _, name := range []string{
		"../outside.txt",
		"..",
		"sub/../../outside.txt",
		outside,
		"/etc/passwd",
	} {
		data, err := w.ReadFile(name)
		if err == nil {
			t.Errorf("ReadFile(%q) was allowed, want a refusal", name)
		}
		if data != nil {
			t.Errorf("ReadFile(%q) returned %d bytes despite failing", name, len(data))
		}
	}
}

func TestReadFileRefusesAnAbsoluteNameByName(t *testing.T) {
	w, _ := open(t, perm.Code)

	_, err := w.ReadFile("/etc/passwd")
	if err == nil {
		t.Fatal("an absolute name was accepted, want a refusal")
	}
	want := "workspace: /etc/passwd is outside the workspace"
	if got := err.Error(); got != want {
		t.Errorf("ReadFile failed with %q, want %q", got, want)
	}
}

func TestReadFileNeedsAName(t *testing.T) {
	w, _ := open(t, perm.Code)

	for _, name := range []string{"", "   "} {
		_, err := w.ReadFile(name)
		if err == nil {
			t.Fatalf("ReadFile(%q) was accepted, want a refusal", name)
		}
		if got, want := err.Error(), "workspace: no file named"; got != want {
			t.Errorf("ReadFile(%q) failed with %q, want %q", name, got, want)
		}
	}
}

func TestReadFileRefusesASymlinkLeavingTheWorkspace(t *testing.T) {
	w, dir := open(t, perm.Code)
	outside := filepath.Join(filepath.Dir(dir), "outside-target.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("WriteFile outside the root failed: %v", err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	link := filepath.Join(dir, "escape.txt")
	if err := os.Symlink(outside, link); err != nil {
		// Unprivileged Windows accounts cannot create symlinks. The
		// containment this test describes is os.Root's, not ours.
		t.Skipf("cannot create a symlink here: %v", err)
	}

	if _, err := w.ReadFile("escape.txt"); err == nil {
		t.Error("reading through a symlink out of the workspace was allowed, want a refusal")
	}
}

func TestReadFileFollowsASymlinkInsideTheWorkspace(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "real.txt", "milk\n")

	link := filepath.Join(dir, "alias.txt")
	// os.Root follows relative links within the root, but rejects absolute
	// targets even when their current destination happens to be inside it.
	if err := os.Symlink("real.txt", link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	data, err := w.ReadFile("alias.txt")
	if err != nil {
		t.Fatalf("ReadFile through an internal symlink failed: %v", err)
	}
	if got := string(data); got != "milk\n" {
		t.Errorf("ReadFile returned %q, want %q", got, "milk\n")
	}
}

func TestReadFileRefusesAnAbsoluteSymlinkToAnInternalFile(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "real.txt", "milk\n")
	if err := os.Symlink(filepath.Join(dir, "real.txt"), filepath.Join(dir, "absolute.txt")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	if _, err := w.ReadFile("absolute.txt"); err == nil {
		t.Fatal("absolute symlink was followed across the os.Root boundary")
	}
}

func TestReadFileRefusesADirectory(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "sub/file.txt", "milk\n")

	_, err := w.ReadFile("sub")
	if err == nil {
		t.Fatal("reading a directory was allowed, want a refusal")
	}
	want := "workspace: read sub: is a directory"
	if got := err.Error(); got != want {
		t.Errorf("ReadFile failed with %q, want %q", got, want)
	}
}

func TestReadFileRefusesAFileLargerThanTheLimit(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "big.log", strings.Repeat("x", maxFileBytes+1))

	_, err := w.ReadFile("big.log")
	if err == nil {
		t.Fatal("reading an oversize file was allowed, want a refusal")
	}
	if !strings.Contains(err.Error(), "larger than") {
		t.Errorf("ReadFile failed with %q, want it to mention the size limit", err)
	}
}

func TestReadFileAcceptsAFileExactlyAtTheLimit(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "edge.log", strings.Repeat("x", maxFileBytes))

	data, err := w.ReadFile("edge.log")
	if err != nil {
		t.Fatalf("a file of exactly %d bytes was refused: %v", maxFileBytes, err)
	}
	if len(data) != maxFileBytes {
		t.Errorf("ReadFile returned %d bytes, want %d", len(data), maxFileBytes)
	}
}

func TestReadFileRefusesBinaryContent(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "image.png", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

	_, err := w.ReadFile("image.png")
	if err == nil {
		t.Fatal("reading a binary file was allowed, want a refusal")
	}
	want := "workspace: read image.png: not a text file"
	if got := err.Error(); got != want {
		t.Errorf("ReadFile failed with %q, want %q", got, want)
	}
}

func TestReadFileReportsAMissingFile(t *testing.T) {
	w, _ := open(t, perm.Code)

	_, err := w.ReadFile("absent.txt")
	if err == nil {
		t.Fatal("reading a missing file succeeded, want a refusal")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadFile failed with %v, want it to wrap os.ErrNotExist", err)
	}
	if !strings.HasPrefix(err.Error(), "workspace: read absent.txt: ") {
		t.Errorf("ReadFile failed with %q, want it to name the file", err)
	}
}

func TestReadFileAcceptsASlashSeparatedName(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "a/b/c.txt", "milk\n")

	if _, err := w.ReadFile("a/b/c.txt"); err != nil {
		t.Errorf("a slash-separated name was refused: %v", err)
	}
}

func TestWriteFilePutsContentsOnDisk(t *testing.T) {
	w, dir := open(t, perm.Code)

	if err := w.WriteFile("todo.md", []byte("remember the milk\n")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	wantFile(t, dir, "todo.md", "remember the milk\n")
}

func TestWriteFileCreatesMissingParentDirectories(t *testing.T) {
	w, dir := open(t, perm.Code)

	if err := w.WriteFile("a/b/c.txt", []byte("milk\n")); err != nil {
		t.Fatalf("WriteFile into a directory that does not exist yet failed: %v", err)
	}
	wantFile(t, dir, "a/b/c.txt", "milk\n")
}

func TestWriteFileReplacesExistingContents(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "todo.md", "buy a boat\nremember the milk\n")

	if err := w.WriteFile("todo.md", []byte("milk\n")); err != nil {
		t.Fatalf("WriteFile over an existing file failed: %v", err)
	}
	// The write is whole-file, so the line the new contents leave out is gone
	// rather than trailing after them.
	wantFile(t, dir, "todo.md", "milk\n")
}

func TestWriteFileAcceptsEmptyContents(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "todo.md", "remember the milk\n")

	if err := w.WriteFile("todo.md", nil); err != nil {
		t.Fatalf("emptying a file failed: %v", err)
	}
	wantFile(t, dir, "todo.md", "")
}

func TestWriteFileIsRefusedInChatMode(t *testing.T) {
	w, dir := open(t, perm.Chat)

	err := w.WriteFile("todo.md", []byte("milk\n"))
	wantDenied(t, err, perm.Chat, perm.Write)
	if _, err := os.Stat(filepath.Join(dir, "todo.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused write left something on disk: %v", err)
	}
}

func TestWriteFileProceedsInModesThatWouldAskFirst(t *testing.T) {
	// Code mode answers Ask for a write, and this package has no user to ask.
	// Reading that as a refusal would settle the question on the user's
	// behalf, so the write proceeds: whoever owns the interface is the one
	// that had to put the question, and the tool layer withholds the
	// capability entirely when there is nobody to put it to.
	for _, c := range []struct {
		name     string
		policy   perm.Policy
		decision perm.Decision
	}{
		{"code", perm.New(perm.Code), perm.Ask},
		{"agent", perm.New(perm.Agent), perm.Ask},
		{"agent with auto-approval", perm.NewAutoApprove(perm.Agent), perm.Allow},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.policy.Check(perm.Write); got != c.decision {
				t.Fatalf("the policy answers %s for a write, want %s", got, c.decision)
			}

			dir := t.TempDir()
			w, err := Open(dir, c.policy)
			if err != nil {
				t.Fatalf("Open failed: %v", err)
			}
			defer w.Close()

			if err := w.WriteFile("todo.md", []byte("milk\n")); err != nil {
				t.Fatalf("WriteFile failed: %v", err)
			}
			wantFile(t, dir, "todo.md", "milk\n")
		})
	}
}

func TestWriteFileRefusesNamesOutsideTheWorkspace(t *testing.T) {
	w, dir := open(t, perm.Code)
	outside := filepath.Join(filepath.Dir(dir), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("WriteFile outside the root failed: %v", err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	for _, name := range []string{
		"../outside.txt",
		"..",
		"sub/../../outside.txt",
		outside,
		"/etc/passwd",
	} {
		if err := w.WriteFile(name, []byte("milk\n")); err == nil {
			t.Errorf("WriteFile(%q) was allowed, want a refusal", name)
		}
	}

	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("reading back the file outside the workspace failed: %v", err)
	}
	if got := string(data); got != "secret\n" {
		t.Errorf("the file outside the workspace now holds %q, want %q", got, "secret\n")
	}
}

func TestWriteFileRefusesAnAbsoluteNameByName(t *testing.T) {
	w, _ := open(t, perm.Code)

	err := w.WriteFile("/etc/passwd", []byte("milk\n"))
	if err == nil {
		t.Fatal("an absolute name was accepted, want a refusal")
	}
	want := "workspace: /etc/passwd is outside the workspace"
	if got := err.Error(); got != want {
		t.Errorf("WriteFile failed with %q, want %q", got, want)
	}
}

func TestWriteFileNeedsAName(t *testing.T) {
	w, _ := open(t, perm.Code)

	for _, name := range []string{"", "   "} {
		err := w.WriteFile(name, []byte("milk\n"))
		if err == nil {
			t.Fatalf("WriteFile(%q) was accepted, want a refusal", name)
		}
		if got, want := err.Error(), "workspace: no file named"; got != want {
			t.Errorf("WriteFile(%q) failed with %q, want %q", name, got, want)
		}
	}
}

func TestWriteFileRefusesADirectory(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "sub/file.txt", "milk\n")

	err := w.WriteFile("sub", []byte("milk\n"))
	if err == nil {
		t.Fatal("writing over a directory was allowed, want a refusal")
	}
	want := "workspace: write sub: is a directory"
	if got := err.Error(); got != want {
		t.Errorf("WriteFile failed with %q, want %q", got, want)
	}
	// The directory is still a directory, with its contents intact.
	wantFile(t, dir, "sub/file.txt", "milk\n")
}

func TestWriteFileReportsAFileStandingWhereADirectoryIsNeeded(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "notes.txt", "milk\n")

	err := w.WriteFile("notes.txt/inner.txt", []byte("milk\n"))
	if err == nil {
		t.Fatal("writing beneath a plain file was allowed, want a refusal")
	}
	if !strings.HasPrefix(err.Error(), "workspace: write notes.txt/inner.txt: ") {
		t.Errorf("WriteFile failed with %q, want it to name the file", err)
	}
	wantFile(t, dir, "notes.txt", "milk\n")
}

func TestWriteFileRefusesContentsLargerThanTheLimit(t *testing.T) {
	w, dir := open(t, perm.Code)

	err := w.WriteFile("big.log", bytes.Repeat([]byte("x"), maxFileBytes+1))
	if err == nil {
		t.Fatal("writing oversize contents was allowed, want a refusal")
	}
	if !strings.Contains(err.Error(), "larger than") {
		t.Errorf("WriteFile failed with %q, want it to mention the size limit", err)
	}
	// The limit is checked before anything is opened, so no truncated stub
	// is left behind.
	if _, err := os.Stat(filepath.Join(dir, "big.log")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused write left something on disk: %v", err)
	}
}

func TestWriteFileAcceptsContentsExactlyAtTheLimit(t *testing.T) {
	w, dir := open(t, perm.Code)

	if err := w.WriteFile("edge.log", bytes.Repeat([]byte("x"), maxFileBytes)); err != nil {
		t.Fatalf("contents of exactly %d bytes were refused: %v", maxFileBytes, err)
	}
	info, err := os.Stat(filepath.Join(dir, "edge.log"))
	if err != nil {
		t.Fatalf("Stat after the write failed: %v", err)
	}
	if info.Size() != maxFileBytes {
		t.Errorf("the file is %d bytes, want %d", info.Size(), maxFileBytes)
	}
}

func TestWriteFileAcceptsASlashSeparatedName(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "a/b/c.txt", "buy a boat\n")

	if err := w.WriteFile("a/b/c.txt", []byte("milk\n")); err != nil {
		t.Fatalf("a slash-separated name was refused: %v", err)
	}
	wantFile(t, dir, "a/b/c.txt", "milk\n")
}

func TestWriteFileRefusesASymlinkLeavingTheWorkspace(t *testing.T) {
	w, dir := open(t, perm.Code)
	outside := filepath.Join(filepath.Dir(dir), "outside-write-target.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("WriteFile outside the root failed: %v", err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	link := filepath.Join(dir, "escape.txt")
	if err := os.Symlink(outside, link); err != nil {
		// Unprivileged Windows accounts cannot create symlinks. The
		// containment this test describes is os.Root's, not ours.
		t.Skipf("cannot create a symlink here: %v", err)
	}

	if err := w.WriteFile("escape.txt", []byte("milk\n")); err == nil {
		t.Error("writing through a symlink out of the workspace was allowed, want a refusal")
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("reading back the symlink target failed: %v", err)
	}
	if got := string(data); got != "secret\n" {
		t.Errorf("the file outside the workspace now holds %q, want %q", got, "secret\n")
	}
}

func TestWriteFileIsNotChangedByAutoApprovalInChatMode(t *testing.T) {
	dir := t.TempDir()

	w, err := Open(dir, perm.NewAutoApprove(perm.Chat))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer w.Close()

	err = w.WriteFile("todo.md", []byte("milk\n"))
	wantDenied(t, err, perm.Chat, perm.Write)
	if _, err := os.Stat(filepath.Join(dir, "todo.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("auto-approval granted a write that chat mode forbids: %v", err)
	}
}

func TestOpenReportsADirectoryItCannotUse(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")

	w, err := Open(missing, perm.New(perm.Code))
	if err == nil {
		w.Close()
		t.Fatal("opening a missing directory succeeded, want an error")
	}
	if !strings.HasPrefix(err.Error(), "workspace: open "+missing+": ") {
		t.Errorf("Open failed with %q, want it to name the directory", err)
	}
	if w != nil {
		t.Error("Open returned a workspace alongside an error")
	}
}

func TestCurrentOpensTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "todo.md", "remember the milk\n")
	t.Chdir(dir)

	w, err := Current(perm.New(perm.Code))
	if err != nil {
		t.Fatalf("Current failed: %v", err)
	}
	defer w.Close()

	data, err := w.ReadFile("todo.md")
	if err != nil {
		t.Fatalf("ReadFile in the working directory failed: %v", err)
	}
	if got := string(data); got != "remember the milk\n" {
		t.Errorf("ReadFile returned %q, want %q", got, "remember the milk\n")
	}
}

func TestNameReportsTheRoot(t *testing.T) {
	w, dir := open(t, perm.Code)

	if got := w.Name(); got != dir {
		t.Errorf("Name is %q, want %q", got, dir)
	}
}

func TestCloseReleasesTheRoot(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "todo.md", "milk\n")
	w, err := Open(dir, perm.New(perm.Code))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if _, err := w.ReadFile("todo.md"); err == nil {
		t.Error("a closed workspace still read a file, want a refusal")
	}
}

func TestIsBinaryLooksOnlyAtTheStartOfAFile(t *testing.T) {
	text := bytes.Repeat([]byte("a"), sniffBytes)

	if isBinary(text) {
		t.Error("a file of plain text was called binary")
	}
	if isBinary(append(bytes.Clone(text), 0)) {
		t.Errorf("a NUL past the first %d bytes was treated as binary", sniffBytes)
	}
	if !isBinary(append(bytes.Clone(text[:sniffBytes-1]), 0)) {
		t.Error("a NUL within the sniffed prefix was not treated as binary")
	}
	if isBinary(nil) {
		t.Error("an empty file was called binary")
	}
}

func TestSearchSkipsAFileLargerThanTheLimit(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "big.txt", strings.Repeat("needle\n", maxFileBytes/7+1))
	write(t, dir, "small.txt", "needle here\n")

	matches, err := w.Search("needle")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("Search found %d matches, want only the small file", len(matches))
	}
	if matches[0].Path != "small.txt" {
		t.Errorf("Search matched %q, want small.txt", matches[0].Path)
	}
}

func TestSearchReportsNestedPathsWithForwardSlashes(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "one/two/three.txt", "needle\n")

	matches, err := w.Search("needle")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("Search found %d matches, want 1", len(matches))
	}
	if matches[0].Path != "one/two/three.txt" {
		t.Errorf("Search reported %q, want one/two/three.txt", matches[0].Path)
	}
}

func TestSearchStopsAtTheMatchLimitInsideOneFile(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "many.txt", strings.Repeat("needle\n", maxMatches+50))

	matches, err := w.Search("needle")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(matches) != maxMatches {
		t.Fatalf("Search returned %d matches, want the limit of %d", len(matches), maxMatches)
	}
	if last := matches[len(matches)-1].Line; last != maxMatches {
		t.Errorf("the last match is on line %d, want line %d", last, maxMatches)
	}
}

func TestSearchTruncatesAnOverlongLineByBytes(t *testing.T) {
	w, dir := open(t, perm.Code)
	line := "needle." + strings.Repeat("é", maxLineBytes)
	write(t, dir, "wide.txt", line+"\n")

	matches, err := w.Search("needle")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("Search found %d matches, want 1", len(matches))
	}
	want := line[:maxLineBytes] + "..."
	if matches[0].Text != want {
		t.Errorf("match text is %q, want %q", matches[0].Text, want)
	}
	if !strings.HasPrefix(matches[0].Text[maxLineBytes-1:], "\xc3") {
		t.Errorf("the cut did not land inside a multi-byte rune: %q", matches[0].Text)
	}
}

func TestReadFileAcceptsAnInteriorParentThatStaysInside(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "sub/keep.txt", "kept\n")
	write(t, dir, "file.txt", "inside\n")

	got, err := w.ReadFile("sub/../file.txt")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(got) != "inside\n" {
		t.Errorf("ReadFile returned %q, want %q", string(got), "inside\n")
	}
}

func TestReadFileReportsAMissingFileAsErrNotExist(t *testing.T) {
	w, _ := open(t, perm.Code)

	if _, err := w.ReadFile("absent.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file reported %v, want it to match os.ErrNotExist", err)
	}
}

func TestReadFileAcceptsANULJustPastTheSniffedPrefix(t *testing.T) {
	w, dir := open(t, perm.Code)
	want := strings.Repeat("a", sniffBytes) + "\x00"
	write(t, dir, "late.bin", want)

	got, err := w.ReadFile("late.bin")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(got) != want {
		t.Errorf("ReadFile returned %d bytes, want %d", len(got), len(want))
	}
}
