package terminaltext

import (
	"bytes"
	"strings"
	"testing"
)

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
