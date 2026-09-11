// Package workspace is the only way this program reads the filesystem. Every
// entry point takes a perm.Policy and asks it first, so a mode is enforced
// here rather than merely described: there is no path to a file that skips the
// check.
//
// Containment is not done by inspecting paths. The tree is opened as an
// os.Root, which refuses to reach outside itself no matter what a name looks
// like, including through symlinks. Comparing cleaned prefixes ourselves would
// be the same idea with worse edge cases.
package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"yonderllm/internal/perm"
)

// Limits on what a single request may pull in. They exist so that pointing
// this at a repository with a vendored dependency tree or a stray database
// dump answers in a moment instead of reading gigabytes.
const (
	// maxFileBytes is the largest file that will be read or searched.
	maxFileBytes = 1 << 20
	// maxMatches caps a search result. A query that hits more than this is
	// too broad to be useful, and saying so is better than printing it.
	maxMatches = 200
	// maxLineBytes truncates a matching line so that one minified file
	// cannot flood the caller with a single result.
	maxLineBytes = 400
	// sniffBytes is how much of a file is examined to decide whether it is
	// text. A binary that begins with a plausible header is still caught,
	// because the check is for a NUL byte rather than for a signature.
	sniffBytes = 8000
)

// skipDirs are directories that are never searched. Each is either machinery
// (version control), or a fetched copy of somebody else's code, or build
// output: places where a match tells the reader nothing about their project.
var skipDirs = map[string]bool{
	".git":         true,
	".hg":          true,
	".svn":         true,
	"node_modules": true,
	"vendor":       true,
	"bin":          true,
	"dist":         true,
	"build":        true,
	"target":       true,
	"__pycache__":  true,
}

// Workspace is a directory tree that can be read under a policy.
type Workspace struct {
	root   *os.Root
	name   string
	policy perm.Policy
}

// Match is one line that satisfied a search.
type Match struct {
	// Path is relative to the workspace root and slash-separated, so that
	// a result reads the same on every platform.
	Path string
	// Line is 1-based, to match how editors and compilers count.
	Line int
	Text string
}

// Open prepares dir for reading under policy. The policy is not consulted
// here: opening a directory is not one of the actions a mode governs, and
// deferring the check to the operation keeps the answer to "may I read this?"
// in one place.
func Open(dir string, policy perm.Policy) (*Workspace, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("workspace: open %s: %w", dir, err)
	}
	return &Workspace{root: root, name: dir, policy: policy}, nil
}

// Current opens the working directory as a workspace. This is the tree a user
// means when they name a file without qualifying it, because it is the one
// they started the program in.
func Current(policy perm.Policy) (*Workspace, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("workspace: locate working directory: %w", err)
	}
	return Open(dir, policy)
}

// Name reports the workspace root, for display.
func (w *Workspace) Name() string { return w.name }

// Close releases the handle on the root.
func (w *Workspace) Close() error { return w.root.Close() }

// ReadFile returns the contents of one file inside the workspace. The name is
// interpreted relative to the root; anything that would leave the root is
// refused, whether it says so plainly or arrives through a symlink.
func (w *Workspace) ReadFile(name string) ([]byte, error) {
	if err := w.authorize(perm.Read); err != nil {
		return nil, err
	}
	rel, err := relative(name)
	if err != nil {
		return nil, err
	}

	info, err := w.root.Stat(rel)
	if err != nil {
		return nil, readError(name, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("workspace: read %s: is a directory", name)
	}
	if info.Size() > maxFileBytes {
		return nil, fmt.Errorf("workspace: read %s: file is larger than %d bytes", name, maxFileBytes)
	}

	data, err := w.root.ReadFile(rel)
	if err != nil {
		return nil, readError(name, err)
	}
	if isBinary(data) {
		return nil, fmt.Errorf("workspace: read %s: not a text file", name)
	}
	return data, nil
}

// Search reports the lines in the workspace containing query, matched as a
// literal substring and ignoring case. It is deliberately not a regexp: the
// punctuation in a query like "func (p Policy)" is what a reader means to
// find, not syntax they meant to escape.
//
// Files that cannot be read are skipped rather than reported, because a search
// that abandons a whole tree over one unreadable corner of it is less useful
// than one that returns what it found.
func (w *Workspace) Search(query string) ([]Match, error) {
	if err := w.authorize(perm.Search); err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return nil, errors.New("workspace: search needs something to look for")
	}

	fsys := w.root.FS()
	var matches []Match

	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != "." && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		// Devices, sockets and dangling symlinks have no lines to match,
		// and opening one can block.
		if !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > maxFileBytes {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil || isBinary(data) {
			return nil
		}
		matches = appendMatches(matches, path, data, needle)
		if len(matches) >= maxMatches {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("workspace: search %s: %w", w.name, err)
	}
	return matches, nil
}

// authorize resolves an action against the policy. Read and Search are the
// only actions this package performs, and no mode answers Ask for either, so
// anything short of Allow is a refusal: there is no prompt to fall back on
// here, and inventing one would be worse than saying no.
func (w *Workspace) authorize(a perm.Action) error {
	if w.policy.Check(a) == perm.Allow {
		return nil
	}
	return &perm.DeniedError{Mode: w.policy.Mode(), Action: a}
}

// relative converts a caller's name into one the root will accept. Absolute
// names and names that climb out of the workspace are rejected here rather
// than deeper down, because the root's own refusal reads as a filesystem
// error when the real problem is that the name was never inside the
// workspace. Escapes in particular arrive from the root carrying no sentinel
// at all, so catching them lexically is the only way to say why they failed.
func relative(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", errors.New("workspace: no file named")
	}
	if filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("workspace: %s is outside the workspace", name)
	}
	// Cleaning first means "sub/../../x" is judged by where it lands, not by
	// where its segments start.
	rel := filepath.FromSlash(trimmed)
	cleaned := filepath.Clean(rel)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace: %s is outside the workspace", name)
	}
	return rel, nil
}

// errNoFile is the workspace's own way of saying a name matched nothing. It
// exists instead of a wrapped os.ErrNotExist because wrapping would append the
// standard library's "file does not exist" to a sentence that has already made
// the point, and the name is in the prefix every read error carries. Reporting
// itself as os.ErrNotExist keeps that appearance free of cost: a caller asking
// errors.Is whether the file was absent still gets a true answer.
var errNoFile = noFileError{}

type noFileError struct{}

func (noFileError) Error() string { return "no file named that" }

func (noFileError) Is(target error) bool { return target == os.ErrNotExist }

// readError turns the root's refusal into the vocabulary relative uses. The
// missing-file case is reworded so a reader is told what is wrong rather than
// handed a syscall's phrasing. An escape cannot be recognised here at all,
// because os.Root reports one with an unexported error carrying no sentinel
// and matching its message would couple us to a string the standard library is
// free to reword; relative rejects those before any syscall, and this handles
// what is left.
func readError(name string, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("workspace: read %s: %w", name, errNoFile)
	}
	return fmt.Errorf("workspace: read %s: %w", name, err)
}

// appendMatches collects the matching lines of one file.
func appendMatches(matches []Match, path string, data []byte, needle string) []Match {
	for i, line := range strings.Split(string(data), "\n") {
		if len(matches) >= maxMatches {
			return matches
		}
		line = strings.TrimRight(line, "\r")
		if !strings.Contains(strings.ToLower(line), needle) {
			continue
		}
		if len(line) > maxLineBytes {
			line = line[:maxLineBytes] + "..."
		}
		matches = append(matches, Match{Path: path, Line: i + 1, Text: line})
	}
	return matches
}

// isBinary reports whether data looks like something other than text. A NUL
// byte early in a file is the cheap signal that agrees with every tool a
// reader is used to.
func isBinary(data []byte) bool {
	head := data
	if len(head) > sniffBytes {
		head = head[:sniffBytes]
	}
	for _, b := range head {
		if b == 0 {
			return true
		}
	}
	return false
}
