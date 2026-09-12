package tools

import (
	"fmt"
	"strings"
)

// diffContext is how many unchanged lines accompany each change.
//
// Three is the figure every other diff shows, which matters more than the
// number itself: the person approving a write is reading under time pressure
// and should recognise the shape of what they are looking at without first
// working out what it is.
const diffContext = 3

// maxDiffCells caps the comparison table.
//
// Finding a shortest edit script costs one table entry per pair of lines, so
// two thousand lines against two thousand is four million entries and the work
// to fill them. A write large enough to exceed this is one nobody was going to
// audit line by line anyway, so the cap costs the reader nothing and spares
// them a pause the approval prompt cannot explain.
const maxDiffCells = 1 << 20

// diffView renders the change from old to now for a person to judge.
//
// A write is approved or refused on what it does to the file, which the new
// contents alone do not say: a thousand identical lines and one altered word
// look the same when handed over whole. So the prompt shows the difference in
// the form the reader already knows from version control, and says plainly
// when it is showing something less than that.
func diffView(old, now string) string {
	if old == now {
		return "the new contents are identical to the current ones"
	}

	oldLines, nowLines := splitLines(old), splitLines(now)

	// Lines shared at either end are the bulk of most writes and cannot be
	// part of any change, so they are set aside before the expensive
	// comparison rather than paid for and then discarded.
	head := 0
	for head < len(oldLines) && head < len(nowLines) && oldLines[head] == nowLines[head] {
		head++
	}
	tail := 0
	for tail < len(oldLines)-head && tail < len(nowLines)-head &&
		oldLines[len(oldLines)-1-tail] == nowLines[len(nowLines)-1-tail] {
		tail++
	}

	oldMiddle := oldLines[head : len(oldLines)-tail]
	nowMiddle := nowLines[head : len(nowLines)-tail]

	// Every line matched and yet the two strings differ, so the change is in
	// a trailing newline. It is invisible in a diff and still worth stating:
	// it is the whole of what the write would do.
	if len(oldMiddle) == 0 && len(nowMiddle) == 0 {
		return newlineNote(old, now)
	}
	if len(oldMiddle)*len(nowMiddle) > maxDiffCells {
		return fmt.Sprintf("the change is too large to show as a diff: %d lines become %d lines",
			len(oldLines), len(nowLines))
	}

	edits := make([]edit, 0, len(oldLines)+len(nowLines))
	for _, line := range oldLines[:head] {
		edits = append(edits, edit{kept, line})
	}
	edits = append(edits, script(oldMiddle, nowMiddle)...)
	for _, line := range oldLines[len(oldLines)-tail:] {
		edits = append(edits, edit{kept, line})
	}
	return hunks(edits)
}

// An edit is one line's fate: kept, removed or added.
type edit struct {
	kind byte
	text string
}

const (
	kept    byte = ' '
	removed byte = '-'
	added   byte = '+'
)

// splitLines breaks a file into lines.
//
// A file ending in a newline is not a file with a blank last line, so the
// empty piece that split leaves behind is dropped. Keeping it would report a
// phantom change every time a newline moved to the end of a growing file.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// newlineNote describes a difference no diff can show.
func newlineNote(old, now string) string {
	switch {
	case strings.HasSuffix(now, "\n") && !strings.HasSuffix(old, "\n"):
		return "the lines are unchanged; the write adds a newline at the end of the file"
	case strings.HasSuffix(old, "\n") && !strings.HasSuffix(now, "\n"):
		return "the lines are unchanged; the write removes the newline at the end of the file"
	default:
		return "the lines are unchanged; the write alters only whitespace at the end of the file"
	}
}

// script finds a shortest edit script between two runs of lines.
//
// The table holds, for each pair of positions, the length of the longest
// common subsequence of what remains from there on. Filling it from the far
// end lets the walk back out run forwards, which is the order the diff has to
// be printed in, so no reversal is needed afterwards.
func script(old, now []string) []edit {
	width := len(now) + 1
	common := make([]int32, (len(old)+1)*width)
	for i := len(old) - 1; i >= 0; i-- {
		for j := len(now) - 1; j >= 0; j-- {
			switch {
			case old[i] == now[j]:
				common[i*width+j] = common[(i+1)*width+j+1] + 1
			case common[(i+1)*width+j] >= common[i*width+j+1]:
				common[i*width+j] = common[(i+1)*width+j]
			default:
				common[i*width+j] = common[i*width+j+1]
			}
		}
	}

	var edits []edit
	i, j := 0, 0
	for i < len(old) && j < len(now) {
		switch {
		case old[i] == now[j]:
			edits = append(edits, edit{kept, old[i]})
			i++
			j++
		// Preferring the removal on a tie puts every deletion of a changed
		// region above its replacement, which is how a rewritten passage
		// reads as one block instead of alternating line by line.
		case common[(i+1)*width+j] >= common[i*width+j+1]:
			edits = append(edits, edit{removed, old[i]})
			i++
		default:
			edits = append(edits, edit{added, now[j]})
			j++
		}
	}
	for ; i < len(old); i++ {
		edits = append(edits, edit{removed, old[i]})
	}
	for ; j < len(now); j++ {
		edits = append(edits, edit{added, now[j]})
	}
	return edits
}

// hunks renders an edit script as the changed regions and their surroundings.
//
// The unchanged middle of a large file is not evidence about the change, and
// printing it buries what is. Each run of changes carries its context and a
// header saying where in each version it sits, so a reader can find the
// passage in the file they already have open.
func hunks(edits []edit) string {
	oldAt := make([]int, len(edits)+1)
	nowAt := make([]int, len(edits)+1)
	for k, e := range edits {
		oldAt[k+1], nowAt[k+1] = oldAt[k], nowAt[k]
		if e.kind != added {
			oldAt[k+1]++
		}
		if e.kind != removed {
			nowAt[k+1]++
		}
	}

	var changed []int
	for k, e := range edits {
		if e.kind != kept {
			changed = append(changed, k)
		}
	}
	if len(changed) == 0 {
		return "the new contents are identical to the current ones"
	}

	var b strings.Builder
	for first := 0; first < len(changed); {
		// Two changes closer together than twice the context share a hunk:
		// separating them would print the same lines twice, once as each
		// one's surroundings.
		last := first
		for last+1 < len(changed) && changed[last+1]-changed[last] <= 2*diffContext {
			last++
		}

		from := max(changed[first]-diffContext, 0)
		to := min(changed[last]+diffContext, len(edits)-1)
		fmt.Fprintf(&b, "@@ -%s +%s @@\n", span(oldAt[from], oldAt[to+1]), span(nowAt[from], nowAt[to+1]))
		for _, e := range edits[from : to+1] {
			b.WriteByte(e.kind)
			b.WriteString(e.text)
			b.WriteByte('\n')
		}
		first = last + 1
	}

	body, omitted := clip(strings.TrimRight(b.String(), "\n"))
	if omitted > 0 {
		body += fmt.Sprintf("\n\n(%d more diff lines were not shown)", omitted)
	}
	return body
}

// span writes one side of a hunk header.
//
// A range covering no lines is numbered from the line before it, as diff does,
// so that a hunk which only adds still points at somewhere real in the file it
// was added to.
func span(before, after int) string {
	n := after - before
	if n == 0 {
		return fmt.Sprintf("%d,0", before)
	}
	return fmt.Sprintf("%d,%d", before+1, n)
}
