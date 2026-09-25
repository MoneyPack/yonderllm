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

// Write escapes every complete rune in p, together with any bytes held back
// from earlier writes, and holds back a trailing partial rune for the next
// call. On success it reports len(p): every byte was either written or held.
//
// On failure the count is the number of bytes of p whose escaped form reached
// the underlying writer, so a caller that retries p[n:] neither loses nor
// repeats output. Bytes held from earlier, already-acknowledged writes stay
// held if they were not written; nothing from p[n:] is kept.
func (w *Writer) Write(p []byte) (int, error) {
	held := len(w.pending)
	data := append(w.pending, p...)
	complete := 0
	for complete < len(data) && utf8.FullRune(data[complete:]) {
		_, size := utf8.DecodeRune(data[complete:])
		complete += size
	}
	text := Text(string(data[:complete]))
	var written int
	var err error
	// A write with nothing complete in it only moves bytes into pending;
	// the destination is not troubled with an empty write.
	if len(text) > 0 {
		written, err = io.WriteString(w.out, text)
		if err == nil && written != len(text) {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		w.pending = append([]byte(nil), data[complete:]...)
		return len(p), nil
	}
	raw := rawPrefix(data[:complete], written)
	if raw < held {
		w.pending = append([]byte(nil), data[raw:held]...)
		return 0, err
	}
	w.pending = nil
	return raw - held, err
}

// rawPrefix reports how many leading bytes of s are accounted for by the first
// written bytes of Text(s). A rune counts only when all of its escaped form was
// written, so a rune cut in half by a short write is reported as unwritten.
func rawPrefix(s []byte, written int) int {
	raw, out := 0, 0
	for raw < len(s) {
		_, size := utf8.DecodeRune(s[raw:])
		out += len(Text(string(s[raw : raw+size])))
		if out > written {
			break
		}
		raw += size
	}
	return raw
}

// Flush writes out a held partial rune as-is; escaping renders it as the
// replacement character, since no further bytes are coming to complete it. On
// failure the bytes that did not reach the underlying writer stay held.
func (w *Writer) Flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	text := Text(string(w.pending))
	n, err := io.WriteString(w.out, text)
	if err == nil && n != len(text) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.pending = w.pending[rawPrefix(w.pending, n):]
		return err
	}
	w.pending = nil
	return nil
}
