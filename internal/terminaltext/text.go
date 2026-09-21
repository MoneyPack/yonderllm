// Package terminaltext renders untrusted text as inert terminal content.
// Escape controls visibly rather than deleting them: approval text must not
// silently conceal bytes that will still be passed to a program or filename.
package terminaltext

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

func Text(s string) string { return escape(s, false) }
func Line(s string) string { return escape(s, true) }

func escape(s string, singleLine bool) string {
	var b strings.Builder
	for _, r := range s {
		if !singleLine && (r == '\n' || r == '\t') {
			b.WriteRune(r)
		} else if r < 0x20 || r == 0x7f {
			fmt.Fprintf(&b, "\\x%02x", r)
		} else if (r >= 0x80 && r <= 0x9f) || r == 0x061c || r == 0x200e || r == 0x200f ||
			(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x2028 || r == 0x2029 {
			fmt.Fprintf(&b, "\\u%04x", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Writer supports UTF-8 split across writes, including split C1 controls. It
// must only wrap plain output: application-owned terminal styling bypasses it.
type Writer struct {
	out     io.Writer
	pending []byte
}

func NewWriter(out io.Writer) *Writer { return &Writer{out: out} }

// Raw bypasses human-display escaping for an explicitly encoded machine format.
func Raw(out io.Writer) io.Writer {
	if w, ok := out.(*Writer); ok {
		return w.out
	}
	return out
}

func (w *Writer) Write(p []byte) (int, error) {
	data := append(w.pending, p...)
	n := 0
	for n < len(data) && utf8.FullRune(data[n:]) {
		_, size := utf8.DecodeRune(data[n:])
		n += size
	}
	text := Text(string(data[:n]))
	w.pending = append([]byte(nil), data[n:]...)
	written, err := io.WriteString(w.out, text)
	if err == nil && written != len(text) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *Writer) Flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	text := Text(string(w.pending))
	w.pending = nil
	n, err := io.WriteString(w.out, text)
	if err == nil && n != len(text) {
		return io.ErrShortWrite
	}
	return err
}
