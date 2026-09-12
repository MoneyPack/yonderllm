package tools

import (
	"strings"
	"testing"
)

// diffLines is the diff split for assertion, since most expectations are about
// which lines carry which marker rather than about the exact text.
func diffLines(t *testing.T, old, now string) []string {
	t.Helper()

	out := diffView(old, now)
	if out == "" {
		t.Fatal("diffView returned nothing")
	}
	return strings.Split(out, "\n")
}

// The reader is being asked to approve a change, so a write that changes
// nothing has to say so outright. Showing an empty diff would leave them
// looking for the change that is not there.
func TestDiffViewReportsAnIdenticalWrite(t *testing.T) {
	const file = "one\ntwo\n"
	if got := diffView(file, file); !strings.Contains(got, "identical") {
		t.Errorf("diffView(same, same) = %q, want it to report no change", got)
	}
}

// One altered line in the middle of a file should read as that line leaving and
// its replacement arriving, with the neighbours shown so the reader can see
// where in the file they are.
func TestDiffViewShowsAChangedLineWithItsNeighbours(t *testing.T) {
	old := "a\nb\nc\nd\ne\n"
	now := "a\nb\nCHANGED\nd\ne\n"

	got := diffLines(t, old, now)
	if !strings.HasPrefix(got[0], "@@ ") {
		t.Errorf("first line = %q, want a hunk header", got[0])
	}
	for _, want := range []string{"-c", "+CHANGED", " b", " d"} {
		if !containsLine(got, want) {
			t.Errorf("diff is missing %q:\n%s", want, strings.Join(got, "\n"))
		}
	}
}

// A file's unchanged bulk is not evidence about the change, and printing it
// would bury the one line that is. The far end of a long file must not appear.
func TestDiffViewOmitsTheUnchangedBulk(t *testing.T) {
	var old strings.Builder
	for i := range 200 {
		if i == 0 {
			old.WriteString("first\n")
			continue
		}
		old.WriteString("filler\n")
	}
	now := "changed\n" + old.String()[len("first\n"):]

	got := diffView(old.String(), now)
	if !strings.Contains(got, "-first") || !strings.Contains(got, "+changed") {
		t.Errorf("diffView lost the change:\n%s", got)
	}
	if lines := strings.Count(got, "\n"); lines > 2*diffContext+4 {
		t.Errorf("diffView printed %d lines for a one-line change:\n%s", lines, got)
	}
}

// Two changes far apart are two separate passages of the file and get a header
// each, so a reader is never told a single region changed when two did.
func TestDiffViewSeparatesDistantChanges(t *testing.T) {
	var b strings.Builder
	for range 40 {
		b.WriteString("filler\n")
	}
	old := "top\n" + b.String() + "bottom\n"
	now := "TOP\n" + b.String() + "BOTTOM\n"

	got := diffView(old, now)
	if n := strings.Count(got, "@@ -"); n != 2 {
		t.Errorf("diffView produced %d hunks, want 2:\n%s", n, got)
	}
}

// Changes close enough to share their context belong to one hunk: splitting
// them would print the same surrounding lines twice and read as more churn
// than the write actually causes.
func TestDiffViewJoinsNearbyChanges(t *testing.T) {
	old := "a\nb\nc\nd\ne\nf\n"
	now := "A\nb\nc\nd\ne\nF\n"

	got := diffView(old, now)
	if n := strings.Count(got, "@@ -"); n != 1 {
		t.Errorf("diffView produced %d hunks, want 1:\n%s", n, got)
	}
}

// Creating a file is a write like any other and has to render as one: every
// line arrives, and nothing is reported as leaving.
func TestDiffViewShowsANewFileAsAllAdditions(t *testing.T) {
	got := diffLines(t, "", "one\ntwo\n")
	for _, want := range []string{"+one", "+two"} {
		if !containsLine(got, want) {
			t.Errorf("diff is missing %q:\n%s", want, strings.Join(got, "\n"))
		}
	}
	for _, line := range got {
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "--") {
			t.Errorf("diff of a new file removes a line: %q", line)
		}
	}
}

// A write that empties a file removes every line, and the header must say the
// new side holds none: a reader approving this is approving deletion.
func TestDiffViewShowsAnEmptiedFileAsAllRemovals(t *testing.T) {
	got := diffView("one\ntwo\n", "")
	for _, want := range []string{"-one", "-two"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff is missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "+0,0") {
		t.Errorf("diff does not show an empty new side:\n%s", got)
	}
}

// Hunk headers are read to find the passage in the file the reader has open, so
// the numbers have to be the real ones, counted per side.
func TestDiffViewNumbersHunksFromBothSides(t *testing.T) {
	old := "a\nb\nc\nd\ne\nf\ng\nh\n"
	now := "a\nb\nc\nd\nE\nf\ng\nh\n"

	got := diffLines(t, old, now)
	// The change is line 5, so context opens at line 2 and both sides carry
	// the seven lines from there to the end.
	if want := "@@ -2,7 +2,7 @@"; got[0] != want {
		t.Errorf("header = %q, want %q", got[0], want)
	}
}

// A missing final newline is invisible in a diff and is still the whole of what
// some writes do, so it is stated in words rather than shown.
func TestDiffViewNamesATrailingNewlineChange(t *testing.T) {
	got := diffView("one\ntwo", "one\ntwo\n")
	if !strings.Contains(got, "newline") {
		t.Errorf("diffView = %q, want it to name the newline", got)
	}
	if strings.Contains(got, "@@") {
		t.Errorf("diffView showed a hunk for a newline-only change:\n%s", got)
	}
}

// Finding a shortest edit script costs a table entry per pair of lines, so a
// write far too large to audit line by line is summarised instead of stalling
// the prompt that is asking for approval.
func TestDiffViewSummarisesAnEnormousChange(t *testing.T) {
	var old, now strings.Builder
	for i := range 1100 {
		fmtLine(&old, "old", i)
		fmtLine(&now, "new", i)
	}

	got := diffView(old.String(), now.String())
	if strings.Contains(got, "@@") {
		t.Errorf("diffView tried to diff an enormous change:\n%s", got)
	}
	if !strings.Contains(got, "1100") {
		t.Errorf("diffView = %q, want the line counts", got)
	}
}

// A file whose lines all differ still has to fit a tool result, and the reader
// must be told the view was cut rather than left thinking they saw all of it.
func TestDiffViewClipsALongDiffAndSaysSo(t *testing.T) {
	var old, now strings.Builder
	for i := range 400 {
		fmtLine(&old, strings.Repeat("old ", 20), i)
		fmtLine(&now, strings.Repeat("new ", 20), i)
	}

	got := diffView(old.String(), now.String())
	if len(got) > maxResultBytes+200 {
		t.Errorf("diffView produced %d bytes, want it clipped near %d", len(got), maxResultBytes)
	}
	if !strings.Contains(got, "not shown") {
		t.Errorf("diffView clipped silently:\n%s", got[max(len(got)-200, 0):])
	}
}

// containsLine reports whether the diff holds exactly this line, so that a test
// for "-c" is not satisfied by "-context".
func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

// fmtLine appends a numbered line, keeping the size tests readable.
func fmtLine(b *strings.Builder, prefix string, n int) {
	b.WriteString(prefix)
	b.WriteByte(' ')
	b.WriteString(strings.Repeat("x", n%7))
	b.WriteByte('\n')
}
