package redact

import (
	"strings"
	"testing"
)

func TestText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		// leak is the substring that must not survive. Empty means the case
		// carries no credential and is here to prove nothing was removed.
		leak string
	}{
		{name: "no credential", in: "just a prompt", want: "just a prompt"},
		{
			name: "token mid-string",
			in:   "curl -H 'Bearer sk-secret' url",
			want: "curl -H 'Bearer [redacted]' url",
			leak: "sk-secret",
		},
		{
			name: "token at end",
			in:   "use Bearer sk-secret",
			want: "use Bearer [redacted]",
			leak: "sk-secret",
		},
		{
			name: "token before newline",
			in:   "Bearer sk-secret\nnext line",
			want: "Bearer [redacted]\nnext line",
			leak: "sk-secret",
		},
		{name: "bare marker", in: "Bearer ", want: "Bearer [redacted]"},
		{
			name: "vendor key alone",
			in:   "sk-0123456789abcdef",
			want: "[redacted]",
			leak: "sk-0123456789abcdef",
		},
		{
			name: "vendor key in a sentence",
			in:   "is gsk_0123456789abcdef still valid?",
			want: "is [redacted] still valid?",
			leak: "gsk_0123456789abcdef",
		},
		{
			name: "env assignment",
			in:   "GROQ_API_KEY=gsk_0123456789abcdef",
			want: "GROQ_API_KEY=[redacted]",
			leak: "gsk_0123456789abcdef",
		},
		{
			name: "quoted env assignment",
			in:   `GEMINI_API_KEY="averysecretvalue"`,
			want: `GEMINI_API_KEY="[redacted]"`,
			leak: "averysecretvalue",
		},
		{
			name: "layout is preserved byte for byte",
			in:   "# .env\n\n\tOPENROUTER_API_KEY=or-0123456789abcdef\n",
			want: "# .env\n\n\tOPENROUTER_API_KEY=[redacted]\n",
			leak: "or-0123456789abcdef",
		},
		// The rules below must not fire: guessing wrong here corrupts the
		// question the user is asking.
		{name: "short mention of a prefix", in: "sk-1 is too short", want: "sk-1 is too short"},
		{name: "unrelated assignment", in: "PATH=/usr/local/bin", want: "PATH=/usr/local/bin"},
		{
			name: "commit hash",
			in:   "revert 9f4a1c2d3e5b6a7f8c9d0e1f2a3b4c5d6e7f8a9b",
			want: "revert 9f4a1c2d3e5b6a7f8c9d0e1f2a3b4c5d6e7f8a9b",
		},
		{
			name: "uuid",
			in:   "row 123e4567-e89b-12d3-a456-426614174000 is missing",
			want: "row 123e4567-e89b-12d3-a456-426614174000 is missing",
		},
		{
			name: "long identifier",
			in:   "rename configurationManagerFactoryProvider",
			want: "rename configurationManagerFactoryProvider",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Text(c.in)
			if got != c.want {
				t.Errorf("Text(%q) = %q, want %q", c.in, got, c.want)
			}
			if c.leak != "" && strings.Contains(got, c.leak) {
				t.Errorf("Text(%q) leaked the credential: %q", c.in, got)
			}
		})
	}
}

// Text runs on every outbound message, so the common case of prose with
// nothing to remove must not pay for a copy.
func TestTextDoesNotAllocateOnPlainProse(t *testing.T) {
	text := strings.Repeat("ordinary prose ", 100)
	if n := testing.AllocsPerRun(100, func() {
		if Text(text) != text {
			panic("changed text")
		}
	}); n != 0 {
		t.Fatalf("plain text allocated %g times", n)
	}
}

func TestLooksLikeKey(t *testing.T) {
	keys := []string{
		"sk-abcdef",
		"sk_abcdef",
		"gsk_abcdef",
		"AIzaSyAbCdEf",
		"or-v1-abc",
		"abcdefghijklmnopqrstuvwxyz012345", // long and opaque
	}
	for _, k := range keys {
		if !LooksLikeKey(k) {
			t.Errorf("LooksLikeKey(%q) = false, want true", k)
		}
	}

	notKeys := []string{
		"hello",
		"rate limit reached",
		"short-token",
		"a sentence that is long enough overall", // spaces disqualify
	}
	for _, s := range notKeys {
		if LooksLikeKey(s) {
			t.Errorf("LooksLikeKey(%q) = true, want false", s)
		}
	}
}

func TestMessageKeepsSurroundingText(t *testing.T) {
	got := Message(`Invalid API key: "gsk_abcdefghijklmnop", try again.`)
	if strings.Contains(got, "gsk_") {
		t.Errorf("redaction left the key behind: %q", got)
	}
	if !strings.HasPrefix(got, "Invalid API key:") {
		t.Errorf("redaction damaged the message: %q", got)
	}
	if !strings.Contains(got, "try again.") {
		t.Errorf("redaction dropped trailing text: %q", got)
	}
}

// Message is the aggressive rule: it removes a long opaque token that Text,
// which guards conversation content, deliberately leaves alone.
func TestMessageIsStricterThanText(t *testing.T) {
	const opaque = "9f4a1c2d3e5b6a7f8c9d0e1f2a3b4c5d"
	if got := Text("revert " + opaque); got != "revert "+opaque {
		t.Errorf("Text removed an opaque token from prose: %q", got)
	}
	if got := Message("revert " + opaque); strings.Contains(got, opaque) {
		t.Errorf("Message kept an opaque token in a provider message: %q", got)
	}
}

func TestNamesSecret(t *testing.T) {
	for _, name := range []string{"GROQ_API_KEY", "token", "db.password", "AWS-SECRET", "PASSWD", "credential"} {
		if !NamesSecret(name) {
			t.Errorf("NamesSecret(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "PATH", "HOME", "a==b", "keyboard"} {
		if NamesSecret(name) {
			t.Errorf("NamesSecret(%q) = true, want false", name)
		}
	}
}
