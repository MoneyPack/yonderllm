// Package provider defines the contract every LLM backend implements.
//
// Inference always happens remotely. An adapter is a transport: it translates
// the core's neutral Request into whatever shape a given API expects, and
// translates the response stream back into neutral Chunks. Provider-specific
// quirks stay inside the adapter.
package provider

import (
	"context"
	"iter"
)

// Role identifies the author of a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn in a conversation.
type Message struct {
	Role    Role
	Content string
}

// Model describes a model offered by a provider.
type Model struct {
	// ID is the identifier passed back in a Request.
	ID string
	// Name is a human-readable label; may equal ID.
	Name string
	// ContextWindow is the maximum total tokens, or 0 if unknown.
	ContextWindow int
	// Pricing is what the provider charges per token, or the zero value
	// when the listing quotes no price.
	Pricing Pricing
}

// Pricing is the per-token cost of a model in US dollars.
//
// The rates are stored per token rather than per million because that is the
// unit providers publish; the per-million figure a human wants to read is a
// display concern and is scaled at the point of printing.
type Pricing struct {
	// Known reports whether the provider quoted a rate at all. Without it a
	// genuinely free model and an unpriced one would both read as zero, and
	// those two answers deserve different words on screen.
	Known bool
	// Prompt is the dollar cost of one input token.
	Prompt float64
	// Completion is the dollar cost of one output token.
	Completion float64
}

// Tier is the price band a model falls into.
type Tier string

const (
	// TierFree is a model the provider charges nothing for.
	TierFree Tier = "free"
	// TierCheap is a model priced at or under [CheapUSDPerMillionTokens].
	TierCheap Tier = "cheap"
	// TierPaid is a model priced above that ceiling.
	TierPaid Tier = "paid"
	// TierUnknown is a model whose listing quotes no price.
	TierUnknown Tier = "unknown"
)

// CheapUSDPerMillionTokens is the ceiling, in US dollars per million tokens,
// at or below which a priced model counts as cheap.
//
// yonderllm exists to run inference somewhere other than this machine without
// running up a bill, and "free only" turned out to be too narrow a rule: the
// providers worth reaching for price their small models in cents per million
// tokens, which is not free but is close enough that hiding them served nobody.
// One dollar per million is the line because it sits above every small
// open-weight model in circulation and below every frontier one, so the split
// lands where a user's intuition already puts it. Both the input and the output
// rate must clear the ceiling, since an output rate is where a cheap-looking
// model usually hides its cost.
const CheapUSDPerMillionTokens = 1.0

// Tier reports which price band the model falls into.
//
// It is computed rather than stored because a catalogue listing quotes prices
// and never names a tier, so any stored value would be this same arithmetic
// frozen at decode time and free to drift from the threshold above it.
func (m Model) Tier() Tier {
	if !m.Pricing.Known {
		return TierUnknown
	}
	if m.Pricing.Prompt <= 0 && m.Pricing.Completion <= 0 {
		return TierFree
	}
	ceiling := CheapUSDPerMillionTokens / 1e6
	if m.Pricing.Prompt <= ceiling && m.Pricing.Completion <= ceiling {
		return TierCheap
	}
	return TierPaid
}

// Affordable reports whether a model is one yonderllm shows by default.
//
// Everything but [TierPaid] qualifies, including [TierUnknown]: a provider that
// declines to quote a price is not thereby expensive, and suppressing its whole
// catalogue on a guess would be the more expensive mistake.
func (m Model) Affordable() bool {
	return m.Tier() != TierPaid
}

// Rank orders the tiers for display, cheapest first and paid last.
//
// Unknown sorts between cheap and paid rather than last, because a model with
// no quoted price is still a candidate a reader might pick, and burying it
// under the frontier models would hide it exactly where it is most likely to be
// the one they wanted.
func (t Tier) Rank() int {
	switch t {
	case TierFree:
		return 0
	case TierCheap:
		return 1
	case TierUnknown:
		return 2
	default:
		return 3
	}
}

// Request is a completion request in provider-neutral form.
type Request struct {
	Model     string
	Messages  []Message
	MaxTokens int
	// Temperature is applied only when non-nil, so that a provider's own
	// default is preserved when the user has not chosen one.
	Temperature *float64
}

// FinishReason explains why a stream ended.
type FinishReason string

const (
	// FinishNone marks a chunk that is not the last one.
	FinishNone FinishReason = ""
	// FinishStop means the model completed its response.
	FinishStop FinishReason = "stop"
	// FinishLength means the output token cap was reached.
	FinishLength FinishReason = "length"
	// FinishFilter means the provider's content filter intervened.
	FinishFilter FinishReason = "filter"
)

// Usage reports token accounting. Fields are 0 when a provider omits them.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// Chunk is one increment of a streamed response.
type Chunk struct {
	// Delta is the text produced since the previous chunk. It may be empty
	// on a chunk that carries only a finish reason or usage totals.
	Delta string
	// Finish is set on the final chunk of a response.
	Finish FinishReason
	// Usage is populated when the provider reports totals, typically on the
	// final chunk only.
	Usage *Usage
}

// Provider is a remote inference backend.
type Provider interface {
	// Name is the stable identifier used in config and on the command line.
	Name() string

	// Models lists what this provider currently offers. Adapters that cannot
	// enumerate models return a static list.
	Models(ctx context.Context) ([]Model, error)

	// Stream sends a request and yields chunks as they arrive.
	//
	// The sequence yields at most one non-nil error, and stops immediately
	// after doing so. Callers must drain or break out of the sequence; the
	// adapter releases its network resources when the loop ends.
	Stream(ctx context.Context, req Request) iter.Seq2[Chunk, error]
}
