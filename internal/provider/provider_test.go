// Tests for the price-tier arithmetic that decides which models yonderllm puts
// in front of a user by default.
//
// The banding is the one piece of provider logic a user notices being wrong
// without reading any code: a mis-placed threshold either hides the small model
// they came for or quietly offers them a frontier one. The cases below therefore
// pin the boundary from both sides, and pin the difference between a model that
// is free and one whose price is merely unquoted, since collapsing those two was
// the bug the Known flag exists to prevent.
package provider

import (
	"math"
	"testing"
)

// cheap returns a Pricing at rates a small open-weight model really charges,
// well inside the ceiling.
func cheap() Pricing {
	return Pricing{Known: true, Prompt: 7e-8, Completion: 3e-7}
}

func TestModelTier(t *testing.T) {
	// ceiling is the per-token form of the published per-million ceiling, so
	// the boundary cases below stay readable when the constant moves.
	ceiling := CheapUSDPerMillionTokens / 1e6

	cases := []struct {
		name    string
		pricing Pricing
		want    Tier
	}{
		{
			name:    "no pricing quoted at all",
			pricing: Pricing{},
			want:    TierUnknown,
		},
		{
			// Rates without the flag must not be trusted: this is the
			// shape a zero-valued struct takes after a partial decode.
			name:    "rates present but not marked known",
			pricing: Pricing{Prompt: 7e-8, Completion: 3e-7},
			want:    TierUnknown,
		},
		{
			name:    "quoted as zero on both rates",
			pricing: Pricing{Known: true},
			want:    TierFree,
		},
		{
			// Defensive: a provider quoting a negative rate is nonsense,
			// but it is nonsense in the user's favour and must not be
			// read as expensive.
			name:    "negative rates",
			pricing: Pricing{Known: true, Prompt: -1e-8, Completion: -1e-8},
			want:    TierFree,
		},
		{
			name:    "well inside the ceiling",
			pricing: cheap(),
			want:    TierCheap,
		},
		{
			name:    "exactly at the ceiling on both rates",
			pricing: Pricing{Known: true, Prompt: ceiling, Completion: ceiling},
			want:    TierCheap,
		},
		{
			name:    "free input but output above the ceiling",
			pricing: Pricing{Known: true, Completion: 2 * ceiling},
			want:    TierPaid,
		},
		{
			name:    "input above the ceiling but output cheap",
			pricing: Pricing{Known: true, Prompt: 2 * ceiling, Completion: 3e-7},
			want:    TierPaid,
		},
		{
			name:    "both rates above the ceiling",
			pricing: Pricing{Known: true, Prompt: 3e-6, Completion: 6e-6},
			want:    TierPaid,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Model{ID: "m", Pricing: c.pricing}
			if got := m.Tier(); got != c.want {
				t.Errorf("Tier() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestModelTierPaidJustOverTheCeiling pins the far side of the boundary that
// TestModelTier pins the near side of, so a comparison loosened from <= to <
// cannot pass both.
func TestModelTierPaidJustOverTheCeiling(t *testing.T) {
	ceiling := CheapUSDPerMillionTokens / 1e6
	m := Model{ID: "m", Pricing: Pricing{Known: true, Prompt: ceiling, Completion: 1.1 * ceiling}}
	if got := m.Tier(); got != TierPaid {
		t.Errorf("Tier() = %q, want %q for an output rate over the ceiling", got, TierPaid)
	}
}

func TestModelAffordable(t *testing.T) {
	cases := []struct {
		name    string
		pricing Pricing
		want    bool
	}{
		{name: "free", pricing: Pricing{Known: true}, want: true},
		{name: "cheap", pricing: cheap(), want: true},
		// An unquoted price is not evidence of expense, and hiding the
		// whole catalogue of a provider that publishes none would be the
		// worse failure.
		{name: "unknown", pricing: Pricing{}, want: true},
		{name: "paid", pricing: Pricing{Known: true, Prompt: 3e-6, Completion: 6e-6}, want: false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Model{ID: "m", Pricing: c.pricing}
			if got := m.Affordable(); got != c.want {
				t.Errorf("Affordable() = %v, want %v (tier %q)", got, c.want, m.Tier())
			}
		})
	}
}

func TestTierRank(t *testing.T) {
	// Ordered cheapest first; the display sorts on these numbers, so what
	// matters is the sequence rather than the individual values.
	ordered := []Tier{TierFree, TierCheap, TierUnknown, TierPaid}
	for i := 1; i < len(ordered); i++ {
		prev, cur := ordered[i-1], ordered[i]
		if prev.Rank() >= cur.Rank() {
			t.Errorf("Rank(%q) = %d, want it below Rank(%q) = %d", prev, prev.Rank(), cur, cur.Rank())
		}
	}

	// An unrecognised tier sorts last rather than first, so a value that
	// escapes the constants above cannot jump the queue ahead of free.
	if got, want := Tier("bogus").Rank(), TierPaid.Rank(); got != want {
		t.Errorf("Rank(\"bogus\") = %d, want %d", got, want)
	}
	if got, want := Tier("").Rank(), TierPaid.Rank(); got != want {
		t.Errorf("Rank(\"\") = %d, want %d", got, want)
	}
}

func TestParsePricing(t *testing.T) {
	cases := []struct {
		name       string
		prompt     string
		completion string
		want       Pricing
	}{
		{
			name: "both rates absent",
			want: Pricing{},
		},
		{
			// Half a quote tells us too little to band the model, and
			// filling the other half in as zero would read as free.
			name:   "only the input rate quoted",
			prompt: "0.0000000700",
			want:   Pricing{},
		},
		{
			name:       "only the output rate quoted",
			completion: "0.0000003000",
			want:       Pricing{},
		},
		{
			name:       "input rate is not a number",
			prompt:     "free",
			completion: "0.0000003000",
			want:       Pricing{},
		},
		{
			name:       "output rate is not a number",
			prompt:     "0.0000000700",
			completion: "on request",
			want:       Pricing{},
		},
		{
			// The decimal strings surplus actually publishes.
			name:       "decimal strings",
			prompt:     "0.0000000700",
			completion: "0.0000003000",
			want:       Pricing{Known: true, Prompt: 7e-8, Completion: 3e-7},
		},
		{
			name:       "exponent notation",
			prompt:     "7e-8",
			completion: "3E-7",
			want:       Pricing{Known: true, Prompt: 7e-8, Completion: 3e-7},
		},
		{
			// A quoted zero is a promise of free, which is a different
			// answer from no quote at all.
			name:       "quoted as zero",
			prompt:     "0",
			completion: "0",
			want:       Pricing{Known: true},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parsePricing(c.prompt, c.completion)
			if got.Known != c.want.Known {
				t.Fatalf("Known = %v, want %v", got.Known, c.want.Known)
			}
			if math.Abs(got.Prompt-c.want.Prompt) > 1e-15 {
				t.Errorf("Prompt = %v, want %v", got.Prompt, c.want.Prompt)
			}
			if math.Abs(got.Completion-c.want.Completion) > 1e-15 {
				t.Errorf("Completion = %v, want %v", got.Completion, c.want.Completion)
			}
		})
	}
}

// TestParsePricingFeedsTheTier joins the two halves: the rates surplus quotes
// for its small models must come out of the parser banded as cheap, which is
// the whole reason the tier stopped meaning "free".
func TestParsePricingFeedsTheTier(t *testing.T) {
	cases := []struct {
		name       string
		prompt     string
		completion string
		want       Tier
	}{
		{name: "small open-weight model", prompt: "0.0000000700", completion: "0.0000003000", want: TierCheap},
		{name: "zero-rated model", prompt: "0", completion: "0", want: TierFree},
		{name: "frontier model", prompt: "0.0000020000", completion: "0.0000080000", want: TierPaid},
		{name: "unpriced model", want: TierUnknown},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Model{ID: "m", Pricing: parsePricing(c.prompt, c.completion)}
			if got := m.Tier(); got != c.want {
				t.Errorf("Tier() = %q, want %q", got, c.want)
			}
		})
	}
}
