package terminaltext

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// limitedWriter accepts at most limit bytes in total and then fails with err.
// A short write is what the terminal does when its pipe closes part-way, so
// this is the case the accounting in Write has to get right.
type limitedWriter struct {
	buf   bytes.Buffer
	limit int
	err   error
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	room := l.limit - l.buf.Len()
	if room <= 0 {
		return 0, l.err
	}
	if len(p) <= room {
		return l.buf.Write(p)
	}
	l.buf.Write(p[:room])
	return room, l.err
}

// TestRawUnwrapsOnlyThisPackagesWriter pins the escape hatch used for JSON
// output: a Writer is unwrapped to the destination it was built on, and
// anything else is handed back untouched.
func TestRawUnwrapsOnlyThisPackagesWriter(t *testing.T) {
	var out bytes.Buffer
	if got := Raw(NewWriter(&out)); got != io.Writer(&out) {
		t.Fatalf("Raw(NewWriter(out)) = %T, want the wrapped buffer", got)
	}
	if got := Raw(&out); got != io.Writer(&out) {
		t.Fatalf("Raw(out) = %T, want out itself", got)
	}
	var w io.Writer = NewWriter(&out)
	if _, err := io.WriteString(Raw(w), "\x1b[2J"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "\x1b[2J" {
		t.Fatalf("Raw escaped its output: %q", out.String())
	}
}

// TestFlushRendersHeldPartialRune covers the non-empty branch of Flush: a
// stream that ends mid-rune cannot be completed, so the held bytes come out as
// the replacement character rather than vanishing.
func TestFlushRendersHeldPartialRune(t *testing.T) {
	var out bytes.Buffer
	w := NewWriter(&out)
	if n, err := w.Write([]byte("ok\xe7\x95")); n != 4 || err != nil {
		t.Fatalf("write: %d %v", n, err)
	}
	if out.String() != "ok" {
		t.Fatalf("partial rune written before flush: %q", out.String())
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := "ok" + Text("\xe7\x95")
	if got := out.String(); got != want {
		t.Fatalf("flushed %q, want %q", got, want)
	}
	if err := w.Flush(); err != nil || out.String() != want {
		t.Fatalf("second flush wrote again: %v %q", err, out.String())
	}
}

// TestWriteReportsOnlyTheBytesThatReachedTheDestination is the honest-count
// contract: on failure, n is the number of bytes of p whose escaped form was
// written, never zero when output went out and never len(p) when it did not.
func TestWriteReportsOnlyTheBytesThatReachedTheDestination(t *testing.T) {
	broken := errors.New("pipe closed")

	// "ab" is written verbatim (2 bytes); the escape for \x1b is 4 bytes and
	// is cut short, so it and everything after it is unwritten.
	dst := &limitedWriter{limit: 4, err: broken}
	w := NewWriter(dst)
	n, err := w.Write([]byte("ab\x1bcd"))
	if !errors.Is(err, broken) {
		t.Fatalf("err = %v, want %v", err, broken)
	}
	if n != 2 {
		t.Fatalf("n = %d, want 2: the two bytes written before the cut", n)
	}
	if dst.buf.String() != "ab\\x" {
		t.Fatalf("destination has %q", dst.buf.String())
	}
	if len(w.pending) != 0 {
		t.Fatalf("unwritten bytes of p were held: %q", w.pending)
	}

	// A destination that refuses everything: nothing consumed.
	w = NewWriter(&limitedWriter{limit: 0, err: broken})
	if n, err := w.Write([]byte("xyz")); n != 0 || !errors.Is(err, broken) {
		t.Fatalf("refused write reported %d %v, want 0 and the error", n, err)
	}

	// A destination that accepts fewer bytes than asked without an error is a
	// short write, and is accounted for the same way.
	w = NewWriter(&limitedWriter{limit: 1})
	if n, err := w.Write([]byte("xyz")); n != 1 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write reported %d %v, want 1 and io.ErrShortWrite", n, err)
	}
}

// TestWriteKeepsAcknowledgedBytesWhenTheyCannotBeWritten covers the boundary
// between writes: a partial rune from an earlier, successful Write must not be
// dropped when the write that completes it fails, and must not be counted
// against the new p either.
func TestWriteKeepsAcknowledgedBytesWhenTheyCannotBeWritten(t *testing.T) {
	broken := errors.New("pipe closed")
	dst := &limitedWriter{limit: 0, err: broken}
	w := NewWriter(dst)

	// The first byte of 界 is held, and acknowledged, since nothing reached
	// the destination to fail.
	if n, err := w.Write([]byte{0xe7}); n != 1 || err != nil {
		t.Fatalf("held byte: %d %v", n, err)
	}
	n, err := w.Write([]byte{0x95, 0x8c, 'z'})
	if n != 0 || !errors.Is(err, broken) {
		t.Fatalf("failed completion reported %d %v, want 0 and the error", n, err)
	}
	if string(w.pending) != "\xe7" {
		t.Fatalf("held %q after failure, want the earlier byte only", w.pending)
	}

	// Once the destination recovers, the held byte joins the retry and the
	// rune comes out whole.
	dst.limit = 100
	if n, err := w.Write([]byte{0x95, 0x8c, 'z'}); n != 3 || err != nil {
		t.Fatalf("retry: %d %v", n, err)
	}
	if dst.buf.String() != "界z" {
		t.Fatalf("retry produced %q", dst.buf.String())
	}

	// Flush holds back what it could not write too.
	dst = &limitedWriter{limit: 0, err: broken}
	w = NewWriter(dst)
	if _, err := w.Write([]byte{0xe7}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); !errors.Is(err, broken) || string(w.pending) != "\xe7" {
		t.Fatalf("failed flush: %v, held %q", err, w.pending)
	}
	dst.limit = 100
	if err := w.Flush(); err != nil || dst.buf.String() != Text("\xe7") {
		t.Fatalf("flush after recovery: %v %q", err, dst.buf.String())
	}
}

func TestTextEscapesControlsWithoutHidingTheirPayload(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"hello 界\n\tcode", "hello 界\n\tcode"},
		{"safe\x1b[2Jdanger", `safe\x1b[2Jdanger`},
		{"\x1b]52;c;secret\a", `\x1b]52;c;secret\x07`},
		{"delete\rkeep\b", `delete\x0dkeep\x08`},
		{"\u009b2J\u202etxt", `\u009b2J\u202etxt`},
	} {
		if got := Text(tc.input); got != tc.want {
			t.Errorf("Text(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
	if got := Line("a\n\tb"); got != `a\x0a\x09b` {
		t.Fatalf("line injection: %q", got)
	}
}

func TestWriterSplitUTF8AndEscapeSequences(t *testing.T) {
	const input = "界\x1b]52;c;payload\a\u009b2J\u202e\n"
	var out bytes.Buffer
	w := NewWriter(&out)
	for _, b := range []byte(input) {
		if n, err := w.Write([]byte{b}); n != 1 || err != nil {
			t.Fatalf("write: %d %v", n, err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), Text(input); got != want {
		t.Fatalf("split stream %q, want %q", got, want)
	}
	if strings.ContainsAny(out.String(), "\x1b\a\u009b\u202e") {
		t.Fatal("active controls escaped output")
	}
}
