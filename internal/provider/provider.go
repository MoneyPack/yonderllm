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
	// Free reports whether the model is usable on the provider's free tier.
	Free bool
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
