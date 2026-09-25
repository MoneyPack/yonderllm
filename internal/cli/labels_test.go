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

	"github.com/MoneyPack/yonderllm/internal/provider"
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
		tier provider.Tier
		want string
	}{
		{"free passes through", provider.TierFree, "free"},
		{"cheap passes through", provider.TierCheap, "cheap"},
		{"paid passes through", provider.TierPaid, "paid"},
		{"unknown passes through", provider.TierUnknown, "unknown"},
		{"an unrecognised tier reads as unknown", provider.Tier("bogus"), "unknown"},
		{"the zero tier reads as unknown", provider.Tier(""), "unknown"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tierLabel(tc.tier); got != tc.want {
				t.Errorf("tierLabel(%q) = %q, want %q", string(tc.tier), got, tc.want)
			}
		})
	}
}

func TestPerMillion(t *testing.T) {
	cases := []struct {
		name string
		rate float64
		want float64
	}{
		{"zero stays zero", 0, 0},
		{"a cheap prompt rate scales up", 0.00000007, 0.07},
		{"a paid completion rate scales up", 0.000004, 4},
		{"a whole-dollar per-token rate scales up", 1, 1e6},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := perMillion(tc.rate)
			if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("perMillion(%g) = %g, want %g", tc.rate, got, tc.want)
			}
		})
	}
}

func TestRateLabel(t *testing.T) {
	cases := []struct {
		name string
		rate float64
		want string
	}{
		{"a genuinely free rate is a plain zero", 0, "0"},
		{"a cheap prompt rate keeps two decimals", 0.00000007, "0.07"},
		{"a cheap completion rate keeps two decimals", 0.0000003, "0.30"},
		{"a paid rate keeps two decimals", 0.000004, "4.00"},
		{"a rate below a hundredth of a cent rounds to zeroes", 0.000000001, "0.00"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rateLabel(tc.rate); got != tc.want {
				t.Errorf("rateLabel(%g) = %q, want %q", tc.rate, got, tc.want)
			}
		})
	}
}

func TestPriceLabel(t *testing.T) {
	cases := []struct {
		name    string
		pricing provider.Pricing
		want    string
	}{
		{
			name:    "an unpriced model reads as unknown, not as free",
			pricing: provider.Pricing{},
			want:    "unknown",
		},
		{
			name:    "a known zero price reads as free on both halves",
			pricing: provider.Pricing{Known: true},
			want:    "0 / 0",
		},
		{
			name:    "input and output are shown separately",
			pricing: provider.Pricing{Known: true, Prompt: 0.00000007, Completion: 0.0000003},
			want:    "0.07 / 0.30",
		},
		{
			name:    "a priced input with a free output still prints both",
			pricing: provider.Pricing{Known: true, Prompt: 0.000002},
			want:    "2.00 / 0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := priceLabel(tc.pricing); got != tc.want {
				t.Errorf("priceLabel(%+v) = %q, want %q", tc.pricing, got, tc.want)
			}
		})
	}
}
