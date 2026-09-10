// Tests for the small display helpers that turn raw struct fields into the
// strings the tables print.
//
// These functions are trivial in isolation, but they are the last thing
// between a zero value and the screen, and every one of them exists because a
// bare zero reads like a real answer: a 0 cap looks like a hard block rather
// than "no limit", a 0 context window looks like a model that accepts nothing,
// and an empty name leaves a hole in the column. The cases below pin the
// substitutions so a later refactor cannot quietly reintroduce the ambiguity.
package cli

import (
	"testing"

	"yonderllm/internal/provider"
)

func TestCapLabel(t *testing.T) {
	cases := []struct {
		name string
		cap  int
		want string
	}{
		{"zero means no limit", 0, "unlimited"},
		{"negative is also no limit", -1, "unlimited"},
		{"single request still pluralises the unit", 1, "1 requests/day"},
		{"positive cap names the unit", 14400, "14400 requests/day"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := capLabel(tc.cap); got != tc.want {
				t.Errorf("capLabel(%d) = %q, want %q", tc.cap, got, tc.want)
			}
		})
	}
}

func TestEnvLabel(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want string
	}{
		{"empty means the provider needs no credential", "", "none needed"},
		{"a named variable passes through", "GROQ_API_KEY", "GROQ_API_KEY"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := envLabel(tc.env); got != tc.want {
				t.Errorf("envLabel(%q) = %q, want %q", tc.env, got, tc.want)
			}
		})
	}
}

func TestDisplayName(t *testing.T) {
	cases := []struct {
		name  string
		model provider.Model
		want  string
	}{
		{
			name:  "a label wins when the provider supplies one",
			model: provider.Model{ID: "llama-3.3-70b-versatile", Name: "Llama 3.3 70B"},
			want:  "Llama 3.3 70B",
		},
		{
			name:  "the id fills the column when the label is blank",
			model: provider.Model{ID: "llama-3.3-70b-versatile"},
			want:  "llama-3.3-70b-versatile",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayName(tc.model); got != tc.want {
				t.Errorf("displayName(%+v) = %q, want %q", tc.model, got, tc.want)
			}
		})
	}
}

func TestContextLabel(t *testing.T) {
	cases := []struct {
		name   string
		tokens int
		want   string
	}{
		{"zero is unknown, not a real limit", 0, "unknown"},
		{"negative is unknown too", -8, "unknown"},
		{"an exact multiple of 1024 abbreviates", 131072, "128K"},
		{"1024 itself abbreviates", 1024, "1K"},
		{"a non-multiple prints in full", 200000, "200000"},
		{"a small odd count prints in full", 7, "7"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := contextLabel(tc.tokens); got != tc.want {
				t.Errorf("contextLabel(%d) = %q, want %q", tc.tokens, got, tc.want)
			}
		})
	}
}

func TestTierLabel(t *testing.T) {
	cases := []struct {
		name string
		free bool
		want string
	}{
		{"free tier", true, "free"},
		{"paid tier", false, "paid"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tierLabel(tc.free); got != tc.want {
				t.Errorf("tierLabel(%t) = %q, want %q", tc.free, got, tc.want)
			}
		})
	}
}
