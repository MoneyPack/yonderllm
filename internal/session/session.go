package session

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"sort"

	"yonderllm/internal/config"
	"yonderllm/internal/provider"
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
	// Tool is set on the two events that bracket a tool invocation, and nil
	// on every other event.
	Tool *ToolRun
	// Done marks the final event of a successful exchange.
	Done bool
	// Usage carries token totals, set on the final event when reported.
	Usage *provider.Usage
}

// Session is one conversation bound to a configuration and a provider chain.
type Session struct {
	retryPending bool
	store        *Sessions
	saveName     string
	cfg          config.Config
	resolve      Resolver
	history      History
	usage        *Usage
	active       string
	contexts     map[string]int
	tools        []Tool
}

// New builds a session from resolved configuration.
func New(cfg config.Config, resolve Resolver) *Session {
	return &Session{
		cfg:      cfg,
		resolve:  resolve,
		usage:    NewUsage(cfg.DailyCap),
		active:   cfg.Provider,
		contexts: make(map[string]int),
	}
}

// History exposes the conversation for display and slash commands.
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
	if _, ok := s.cfg.Providers[conv.Provider]; !ok {
		return fmt.Errorf("saved provider %q is not configured", conv.Provider)
	}
	if conv.Model == "" {
		return errors.New("saved model is empty")
	}
	s.active = conv.Provider
	s.SetModel(conv.Model)
	s.history = History{system: redactable(conv.System), turns: redactSavedMessages(conv.Messages)}
	s.retryPending = false
	return nil
}

// Provider reports the provider that will be tried first.
func (s *Session) Provider() string { return s.active }

// Providers lists every configured provider name in sorted order, so that
// /model can show what a user may switch to without them having to open the
// config file to find out.
func (s *Session) Providers() []string {
	out := make([]string, 0, len(s.cfg.Providers))
	for name := range s.cfg.Providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Credentialed reports whether a provider has a usable API key, which is what
// separates a provider a user can switch to from one that would fail on the
// next question.
func (s *Session) Credentialed(name string) bool { return s.cfg.Credentialed(name) }

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
func (s *Session) Model() string { return s.cfg.Providers[s.active].Model }

// SetProvider switches the preferred provider, as /model does. Fallbacks are
// unchanged, so a manual switch still degrades gracefully.
func (s *Session) SetProvider(name string) error {
	if _, ok := s.cfg.Providers[name]; !ok {
		return fmt.Errorf("provider %q is not configured", name)
	}
	s.active = name
	return nil
}

// SetModel overrides the model id for the active provider.
func (s *Session) SetModel(id string) {
	p := s.cfg.Providers[s.active]
	p.Model = id
	s.cfg.Providers[s.active] = p
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
// instead of the conservative default.
func (s *Session) SetContextWindow(providerName string, tokens int) {
	if tokens > 0 {
		s.contexts[providerName] = tokens
	}
}

// promptBudget is how many tokens of history may be sent to providerName.
func (s *Session) promptBudget(providerName string) int {
	window, ok := s.contexts[providerName]
	if !ok || window <= 0 {
		window = defaultContextWindow
	}
	reserve := s.cfg.MaxTokens
	if reserve <= 0 {
		reserve = reservedForOutput
	}
	budget := window - reserve
	if budget < 0 {
		budget = 0
	}
	return budget
}

// chain returns the providers to try, active first, skipping any that have no
// usable credential. Attempting an uncredentialed provider would spend a
// round-trip to learn what config already knows.
func (s *Session) chain() []string {
	cfg := s.cfg
	cfg.Provider = s.active

	var out []string
	for _, name := range cfg.Chain() {
		if cfg.Credentialed(name) {
			out = append(out, name)
		}
	}
	return out
}

// Ask sends prompt and streams the reply, running any tools the model asks for
// and falling back across providers when one reports a quota or auth failure.
//
// The user turn is appended immediately so it appears in the transcript even
// if every provider fails. The assistant turn is appended only once a reply
// completes, so a failed exchange does not leave a truncated answer in history
// that would then be sent as context on the next question.
//
// One question may take several remote requests: each tool the model asks for
// has to be run locally and handed back, and only then can the model continue.
// The whole exchange still costs one reservation against the daily cap, because
// the user asked one question and the number of rounds is our decision, not
// theirs.
func (s *Session) Ask(ctx context.Context, prompt string) iter.Seq2[Event, error] {
	return s.exchange(ctx, prompt, false)
}

// CanRetry is called only after the preceding exchange has stopped. Unknown
// tool outcomes are never replayed or silently discarded.
func (s *Session) CanRetry() error {
	if !s.retryPending {
		return errors.New("no failed or interrupted exchange to retry")
	}
	if err := validateMessages(s.history.Turns()); err != nil {
		return fmt.Errorf("cannot retry: %w; review tool outcomes and start a new conversation", err)
	}
	return nil
}

// Retry reuses the failed turn without adding a duplicate user message. Tools
// are withheld, including when a provider emits an unsolicited tool call.
func (s *Session) Retry(ctx context.Context) iter.Seq2[Event, error] {
	return s.exchange(ctx, "", true)
}

func (s *Session) exchange(ctx context.Context, prompt string, retry bool) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		if retry {
			if err := s.CanRetry(); err != nil {
				yield(Event{}, err)
				return
			}
		}
		if err := s.usage.Reserve(); err != nil {
			yield(Event{}, err)
			return
		}

		s.retryPending = true
		if !retry {
			s.history.Append(provider.RoleUser, prompt)
		}

		candidates := s.chain()
		if len(candidates) == 0 {
			err := errors.New("no provider has a usable API key; set one of the provider key environment variables")
			yield(Event{}, errors.Join(err, s.usage.Release()))
			return
		}

		// total accumulates every round's tokens, so the figure shown at the
		// end of the exchange is what the question actually cost rather than
		// what its last leg cost. It stays nil while no provider reported
		// anything, which is how "unknown" is distinguished from "zero".
		var total *provider.Usage

		for round := range maxToolRounds {
			// On the last round the tools are withheld, which turns the
			// bound into a nudge rather than a wall: the model can no
			// longer ask for anything, so it answers.
			var tools []provider.Tool
			last := retry || round == maxToolRounds-1
			if !last {
				tools = s.definitions()
			}

			res, err := s.round(ctx, candidates, tools, yield)
			switch {
			case err == nil:
				// Fall through to handling the reply.

			case errors.Is(err, errStopped):
				// The caller broke out of the loop; nothing more to do.
				return

			default:
				if round == 0 && res.reply == "" {
					err = errors.Join(err, s.usage.Release())
				}
				yield(Event{Provider: res.provider}, err)
				return
			}

			if res.usage != nil {
				s.usage.Record(res.provider, *res.usage)
				if total == nil {
					total = &provider.Usage{}
				}
				total.PromptTokens += res.usage.PromptTokens
				total.CompletionTokens += res.usage.CompletionTokens
			}

			// A provider that was skipped over once will be skipped over
			// again, so the chain is narrowed to start at whichever one
			// answered. Retrying a spent free tier every round would burn
			// the allowance to learn what the last round already proved.
			if i := slices.Index(candidates, res.provider); i > 0 {
				candidates = candidates[i:]
			}

			// No tool calls means the model answered in prose, and so does
			// the last round: calls that arrive after the tools were
			// withheld are ignored rather than obeyed.
			if len(res.calls) == 0 || last {
				s.history.Append(provider.RoleAssistant, res.reply)
				s.retryPending = false
				if s.store != nil {
					if err := s.store.Save(s.saveName, s.SaveConversation()); err != nil {
						yield(Event{}, fmt.Errorf("answer completed but saving failed: %w", err))
						return
					}
				}
				yield(Event{Provider: res.provider, Done: true, Usage: total}, nil)
				return
			}

			s.history.AppendToolCalls(res.reply, res.calls)
			if !s.runCalls(ctx, res.provider, res.calls, yield) {
				return
			}
		}
	}
}

// errStopped reports that the consumer abandoned the sequence. It never
// escapes this package.
var errStopped = errors.New("session: consumer stopped")

// roundResult is one completed model reply: the prose it streamed, the tools it
// asked for, and what the exchange cost. The provider is carried alongside so
// that a failure can still say which adapter produced it.
type roundResult struct {
	provider string
	reply    string
	calls    []provider.ToolCall
	usage    *provider.Usage
}

// round runs one model reply, walking the provider chain until one answers.
func (s *Session) round(ctx context.Context, candidates []string, tools []provider.Tool, yield func(Event, error) bool) (roundResult, error) {
	var errs []error
	for i, name := range candidates {
		if ctx.Err() != nil {
			return roundResult{}, ctx.Err()
		}

		if i > 0 {
			notice := fmt.Sprintf("falling back to %s after %s failed", name, candidates[i-1])
			if !yield(Event{Notice: notice, Provider: name}, nil) {
				return roundResult{provider: name}, errStopped
			}
		}

		res, err := s.streamOne(ctx, name, tools, yield)
		if err != nil && res.reply != "" {
			return res, fmt.Errorf("%s: response interrupted after partial output: %w", name, err)
		}
		switch {
		case err == nil:
			return res, nil

		case errors.Is(err, errStopped):
			return res, err

		case errors.Is(err, provider.ErrQuota), errors.Is(err, provider.ErrAuth), errors.Is(err, provider.ErrNoModel), errors.Is(err, provider.ErrUnavailable):
			// This provider cannot answer, but the next one may: an exhausted
			// allowance, a credential it will not accept, no model to ask for,
			// or a backend that is down are all faults of one provider rather
			// than of the request.
			errs = append(errs, err)

		default:
			// A transport or protocol failure is not something a different
			// provider is likely to fix, and retrying would spend another
			// free-tier request.
			return roundResult{provider: name}, err
		}
	}

	return roundResult{}, fmt.Errorf("all providers failed: %w", errors.Join(errs...))
}

// streamOne runs a single provider attempt, forwarding deltas through yield.
// It returns the assembled reply, any tool calls and the reported usage, or the
// error that ended the attempt.
func (s *Session) streamOne(ctx context.Context, name string, tools []provider.Tool, yield func(Event, error) bool) (roundResult, error) {
	p, err := s.resolve(name)
	if err != nil {
		return roundResult{provider: name}, fmt.Errorf("initialising provider %s: %w", name, err)
	}

	pc := s.cfg.Providers[name]
	// Refused here rather than at the adapter: a request with no model comes
	// back as a bare 400, which is what a malformed tool schema and a dozen
	// other faults also look like. Failing locally names the provider that is
	// short a model, and costs nothing in the free-tier request budget.
	if pc.Model == "" {
		return roundResult{provider: name}, &provider.NoModelError{
			Provider: name,
			Hint:     fmt.Sprintf("model under [providers.%s] in the config, or --model", name),
		}
	}

	req := provider.Request{
		Model:     pc.Model,
		Messages:  s.history.Prompt(s.promptBudget(name)),
		MaxTokens: s.cfg.MaxTokens,
		Tools:     tools,
	}

	res := roundResult{provider: name}
	var reply []byte
	for chunk, err := range p.Stream(ctx, req) {
		if err != nil {
			res.reply = string(reply)
			return res, err
		}
		if chunk.Usage != nil {
			u := *chunk.Usage
			res.usage = &u
		}
		// The adapter reassembles fragmented calls, so anything arriving
		// here is already whole and can simply be collected.
		res.calls = append(res.calls, chunk.ToolCalls...)
		if chunk.Delta == "" {
			continue
		}
		reply = append(reply, chunk.Delta...)
		if !yield(Event{Delta: chunk.Delta, Provider: name}, nil) {
			return roundResult{provider: name}, errStopped
		}
	}
	res.reply = string(reply)
	return res, nil
}

// runCalls executes each call in turn, recording the outcome in history and
// bracketing it with a start and a finish event. It reports whether the
// consumer is still listening.
//
// Calls run sequentially rather than concurrently: a later call in the same
// batch often depends on what an earlier one found, and the transcript reads as
// a sequence.
func (s *Session) runCalls(ctx context.Context, providerName string, calls []provider.ToolCall, yield func(Event, error) bool) bool {
	for _, c := range calls {
		start := &ToolRun{ID: c.ID, Name: c.Name, Arguments: c.Arguments}
		if !yield(Event{Provider: providerName, Tool: start}, nil) {
			return false
		}

		result, err := s.invoke(ctx, c)

		done := &ToolRun{ID: c.ID, Name: c.Name, Arguments: c.Arguments, Finished: true}
		// A failed tool is reported to the model as its result, not raised
		// as an error: the model is the only party that can correct a bad
		// argument, and it can only do that if it is told what went wrong.
		// The provider would reject the next request outright if a call
		// were left without a matching result.
		if err != nil {
			done.Err = err.Error()
			s.history.AppendToolResult(c.ID, err.Error())
		} else {
			done.Result = result
			s.history.AppendToolResult(c.ID, result)
		}

		if !yield(Event{Provider: providerName, Tool: done}, nil) {
			return false
		}
	}
	return true
}

// invoke runs the tool a call names.
//
// An unknown name is an error from the tool rather than a failure of the
// exchange: models do occasionally invent a tool, and the recoverable answer is
// to tell it so.
func (s *Session) invoke(ctx context.Context, c provider.ToolCall) (string, error) {
	for _, t := range s.tools {
		if t.Definition.Name == c.Name {
			return t.Run(ctx, c.Arguments)
		}
	}
	return "", fmt.Errorf("no tool named %q is available", c.Name)
}
