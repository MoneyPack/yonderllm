package session

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/MoneyPack/yonderllm/internal/provider"
	"github.com/MoneyPack/yonderllm/internal/redact"
)

// defaultContextWindow is assumed when a provider does not report one. It is
// deliberately modest: guessing low costs a little context, guessing high
// costs a rejected request.
const defaultContextWindow = 8192

// reservedForOutput is the share of the window held back for the reply when a
// model's window is known but the caller set no explicit prompt budget.
const reservedForOutput = 1024

// maxToolRounds bounds how many times one question may bounce between the
// model and the tools before the model has to answer in prose.
//
// Every round is another remote request against a free-tier allowance, and a
// model that has misread a tool's output will happily ask for it again
// forever. The bound is not a hard stop: on the last round the tools are
// simply withheld, so the model still gets to write a reply rather than the
// exchange ending with no answer at all.
const maxToolRounds = 6

// Resolver supplies a live adapter for a provider name. The session takes this
// as a function rather than a map so that adapters are constructed lazily: a
// fallback provider that is never reached never has a client built for it.
type Resolver func(name string) (provider.Provider, error)

// Tool is one capability the model may invoke during an exchange.
type Tool struct {
	// Definition is what the provider is told about the tool.
	Definition provider.Tool
	// Run executes the call. The arguments are the raw JSON string the model
	// produced, passed through unvalidated: it may not even parse, and only
	// the tool knows what shape it expects.
	Run func(ctx context.Context, arguments string) (string, error)
}

// ToolRun describes one tool invocation. It hangs off Event as a pointer so
// that the ordinary text event stays small, and so that a nil field is an
// unambiguous "this event is not about a tool".
type ToolRun struct {
	// ID is the call id the provider issued, which ties the result back to
	// the request.
	ID string
	// Name is the tool being invoked.
	Name string
	// Arguments is the raw JSON the model supplied.
	Arguments string
	// Finished distinguishes the completion event from the start event.
	// Emptiness of Result cannot carry that meaning: a tool may legitimately
	// succeed and produce nothing, as a search with no matches does.
	Finished bool
	// Result is what the tool returned, set once Finished.
	Result string
	// Err is the failure text when the call failed, set once Finished.
	Err string
}

// Event is one thing that happened during an exchange. Fallbacks are reported
// as events rather than hidden, because a silent provider switch changes which
// model answered and the user is entitled to see that in the transcript.
type Event struct {
	// Delta is streamed assistant text, empty on non-text events.
	Delta string
	// Notice is a human-readable status line, such as a fallback switch.
	Notice string
	// Provider is the adapter that produced this event.
	Provider string
	// Model is the model id Provider was asked for. It travels on the event
	// rather than being read off the session by the consumer because after
	// a fallback the two disagree: the session still names the model the
	// user chose, while the answer is coming from the fallback's model.
	Model string
	// Tool is set on the two events that bracket a tool invocation, and nil
	// on every other event.
	Tool *ToolRun
	// Done marks the final event of a successful exchange.
	Done bool
	// Finish preserves the provider's final stop reason for internal consumers.
	Finish provider.FinishReason
	// IgnoredToolCalls preserves requests refused on a tools-disabled final
	// round for internal evaluators. They are never executed or persisted.
	IgnoredToolCalls []provider.ToolCall
	// Usage carries token totals, set on the final event when reported.
	Usage *provider.Usage
}

// Session is one conversation bound to a provider chain.
//
// A Session is not safe for concurrent use. Everything on it — Ask and the
// sequence it returns, the accessors, the setters, History — must be driven
// from one goroutine at a time. The interactive interface honours this by
// draining an exchange on a worker goroutine and touching the session from
// its message loop only once that exchange has closed; a caller that reads
// History while an Ask is still yielding is reading state mid-write. The
// session does not lock internally because the sequence an exchange returns
// runs inside the caller's loop, and a lock held across a yield would
// serialise nothing useful while making a deadlock easy to write.
type Session struct {
	retryPending bool
	store        *Sessions
	saveName     string
	// opts is the session's own copy of its settings. Model overrides and
	// learned context windows are written here and nowhere else, so nothing
	// a session does can leak into the configuration it was built from.
	opts    Options
	resolve Resolver
	history History
	usage   *Usage
	active  string
	tools   []Tool
}

// History exposes the conversation for display and slash commands.
//
// The pointer is to live state, not a copy: /clear and the system prompt are
// set through it. That makes it subject to the type's single-goroutine
// contract — read it between exchanges, never while one is streaming. Callers
// that only need to read take [History.Turns], which does copy.
func (s *Session) History() *History { return &s.history }

// Usage exposes the counters backing /usage.
func (s *Session) Usage() *Usage { return s.usage }

// SetUsage attaches a counter before the first Ask. The CLI supplies a shared
// persistent counter; New otherwise keeps library sessions in memory.
func (s *Session) SetUsage(u *Usage) { s.usage = u }

// SaveConversation captures the current conversation into a SavedConversation
// ready for a *Sessions store. The provider and model are pinned alongside the
// turns so that a resumed conversation keeps its context.
func (s *Session) SaveConversation() SavedConversation {
	h := &s.history
	return SavedConversation{
		Provider: s.active,
		Model:    s.Model(),
		System:   h.System(),
		Messages: h.Turns(),
	}
}

// LoadConversation validates and restores history and model selection before
// the first Ask. Credentials, tools, approval state and usage are never loaded.
func (s *Session) LoadConversation(conv SavedConversation) error {
	if err := validateMessages(conv.Messages); err != nil {
		return err
	}
	if _, ok := s.opts.Providers[conv.Provider]; !ok {
		return fmt.Errorf("saved provider %q is not configured", conv.Provider)
	}
	if conv.Model == "" {
		return errors.New("saved model is empty")
	}
	s.active = conv.Provider
	s.SetModel(conv.Model)
	s.history = History{system: redact.Text(conv.System), turns: redactSavedMessages(conv.Messages)}
	s.retryPending = false
	return nil
}

// Provider reports the provider that will be tried first.
func (s *Session) Provider() string { return s.active }

// Providers lists every configured provider name in sorted order, so that
// /model can show what a user may switch to without them having to open the
// config file to find out.
func (s *Session) Providers() []string {
	out := make([]string, 0, len(s.opts.Providers))
	for name := range s.opts.Providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Credentialed reports whether a provider has a usable API key, which is what
// separates a provider a user can switch to from one that would fail on the
// next question.
func (s *Session) Credentialed(name string) bool {
	p, ok := s.opts.Providers[name]
	return ok && p.Credentialed
}

// Clear drops the conversation turns, keeping the system prompt and every
// counter. /clear frees context, it does not refund the day's budget.
func (s *Session) Clear() {
	s.retryPending = false
	s.history.Clear()
	if s.store != nil {
		s.saveName = fmt.Sprintf("session-%x", randomSessionID())
	}
}

// Model reports the model id configured for the active provider.
func (s *Session) Model() string { return s.modelFor(s.active) }

// modelFor reports the model id a named provider would be asked for. It is
// empty for a provider that is not configured, which no caller treats as a
// model.
func (s *Session) modelFor(name string) string { return s.opts.Providers[name].Model }

// SetProvider switches the preferred provider, as /model does. Fallbacks are
// unchanged, so a manual switch still degrades gracefully.
func (s *Session) SetProvider(name string) error {
	if _, ok := s.opts.Providers[name]; !ok {
		return fmt.Errorf("provider %q is not configured", name)
	}
	s.active = name
	return nil
}

// SetModel overrides the model id for the active provider. The override is
// held by this session alone; the configuration it was built from is not
// changed.
func (s *Session) SetModel(id string) {
	p := s.opts.Providers[s.active]
	p.Model = id
	s.opts.Providers[s.active] = p
}

// SetTools replaces the set of tools the model may invoke.
//
// The whole set is replaced rather than added to one at a time because the set
// is decided by the permission mode, and a mode change has to be able to take
// a capability away as well as grant one.
func (s *Session) SetTools(tools ...Tool) { s.tools = tools }

// definitions is what the provider is told about the registered tools. It
// returns nil for an empty set so the adapter omits the key entirely: some
// providers reject an empty tool list rather than treating it as no tools.
func (s *Session) definitions() []provider.Tool {
	if len(s.tools) == 0 {
		return nil
	}
	out := make([]provider.Tool, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, t.Definition)
	}
	return out
}

// SetContextWindow records a model's window so trimming can use the real size
// instead of the conservative default. A configured context_window arrives
// through [Options] already; this is for a window learned later, from a model
// catalogue for instance. Non-positive values and unknown providers are
// ignored rather than trusted.
func (s *Session) SetContextWindow(providerName string, tokens int) {
	p, ok := s.opts.Providers[providerName]
	if !ok || tokens <= 0 {
		return
	}
	p.ContextWindow = tokens
	s.opts.Providers[providerName] = p
}

// promptBudget is how many tokens of history may be sent to providerName.
func (s *Session) promptBudget(providerName string) int {
	window := s.opts.Providers[providerName].ContextWindow
	if window <= 0 {
		window = defaultContextWindow
	}
	reserve := s.opts.MaxTokens
	if reserve <= 0 {
		reserve = reservedForOutput
	}
	budget := window - reserve
	if budget < 0 {
		budget = 0
	}
	return budget
}

// chain returns the providers to try, active first, then the fallbacks in
// order with the active one removed so it is never tried twice, skipping any
// that have no usable credential. Attempting an uncredentialed provider would
// spend a round-trip to learn what the caller already knows.
func (s *Session) chain() []string {
	seen := map[string]bool{s.active: true}
	var out []string
	if s.Credentialed(s.active) {
		out = append(out, s.active)
	}
	for _, name := range s.opts.Fallbacks {
		if seen[name] {
			continue
		}
		seen[name] = true
		if s.Credentialed(name) {
			out = append(out, name)
		}
	}
	return out
}
