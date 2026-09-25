package session

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"time"

	"github.com/MoneyPack/yonderllm/internal/provider"
)

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
	parent := ctx
	return func(yield func(Event, error) bool) {
		ctx := parent
		if s.opts.RequestTimeoutSeconds > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(s.opts.RequestTimeoutSeconds)*time.Second)
			defer cancel()
		}
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
				yield(s.event(res.provider), s.explainDeadline(ctx, parent, err))
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
				if res.finish == provider.FinishLength {
					ev := s.event(res.provider)
					ev.Notice = "Answer reached the output limit. Ask to continue or increase --max-tokens."
					if !yield(ev, nil) {
						return
					}
				}
				done := s.event(res.provider)
				done.Done, done.Finish, done.Usage, done.IgnoredToolCalls = true, res.finish, total, res.calls
				yield(done, nil)
				return
			}

			s.history.AppendToolCalls(res.reply, res.calls)
			if !s.runCalls(ctx, res.provider, res.calls, yield) {
				return
			}
		}
	}
}

// event starts an Event attributed to a provider, with the model that provider
// is being asked for filled in. Every event an exchange yields goes through
// here so that none can name a provider without also naming its model.
func (s *Session) event(providerName string) Event {
	return Event{Provider: providerName, Model: s.modelFor(providerName)}
}

// explainDeadline names the setting behind an exchange deadline.
//
// A timed-out request surfaces as a bare "context deadline exceeded", which
// says nothing about where the deadline came from. request_timeout_seconds is
// the only thing that sets one inside the session, so when it is set and the
// caller's own context is still live, the setting is what expired and the
// error should say so. A deadline the caller imposed is left to speak for
// itself. The original error stays in the chain so errors.Is still matches.
func (s *Session) explainDeadline(ctx, parent context.Context, err error) error {
	if s.opts.RequestTimeoutSeconds <= 0 || parent.Err() != nil || !errors.Is(err, context.DeadlineExceeded) || ctx.Err() == nil {
		return err
	}
	return fmt.Errorf("request timed out after %ds (request_timeout_seconds): %w", s.opts.RequestTimeoutSeconds, err)
}

// errStopped reports that the consumer abandoned the sequence. It never
// escapes this package.
var errStopped = errors.New("session: consumer stopped")

// roundResult is one completed model reply: the prose it streamed, the tools it
// asked for, and what the exchange cost. The provider is carried alongside so
// that a failure can still say which adapter produced it.
type roundResult struct {
	finish   provider.FinishReason
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
			notice := s.event(name)
			notice.Notice = fmt.Sprintf("falling back to %s after %s failed", name, candidates[i-1])
			if len(errs) > 0 {
				notice.Notice += ": " + provider.FailureHint(errs[len(errs)-1])
			}
			if !yield(notice, nil) {
				return roundResult{provider: name}, errStopped
			}
		}

		res, err := s.streamOne(ctx, name, tools, yield)
		for attempt := 0; attempt < s.opts.RetryAttempts && res.reply == "" && len(res.calls) == 0 && (errors.Is(err, provider.ErrUnavailable) || errors.Is(err, provider.ErrQuota)); attempt++ {
			delay := time.Duration(s.opts.RetryBackoffMS) * time.Millisecond
			var quota *provider.QuotaError
			if errors.As(err, &quota) && quota.RetryAfter > delay {
				delay = quota.RetryAfter
			}
			// Long provider hints fall through to another provider instead of parking the UI.
			if delay > 30*time.Second {
				break
			}
			notice := s.event(name)
			notice.Notice = fmt.Sprintf("retrying %s in %s (%d/%d)", name, delay, attempt+1, s.opts.RetryAttempts)
			if !yield(notice, nil) {
				return res, errStopped
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return res, ctx.Err()
			case <-timer.C:
				if ctx.Err() != nil {
					return res, ctx.Err()
				}
			}
			res, err = s.streamOne(ctx, name, tools, yield)
		}
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

	model := s.modelFor(name)
	// Refused here rather than at the adapter: a request with no model comes
	// back as a bare 400, which is what a malformed tool schema and a dozen
	// other faults also look like. Failing locally names the provider that is
	// short a model, and costs nothing in the free-tier request budget.
	if model == "" {
		return roundResult{provider: name}, &provider.NoModelError{
			Provider: name,
			Hint:     fmt.Sprintf("model under [providers.%s] in the config, or --model", name),
		}
	}

	req := provider.Request{
		Model:     model,
		Messages:  s.history.Prompt(s.promptBudget(name)),
		MaxTokens: s.opts.MaxTokens,
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
		if chunk.Finish != provider.FinishNone {
			res.finish = chunk.Finish
		}
		// The adapter reassembles fragmented calls, so anything arriving
		// here is already whole and can simply be collected.
		res.calls = append(res.calls, chunk.ToolCalls...)
		if chunk.Delta == "" {
			continue
		}
		reply = append(reply, chunk.Delta...)
		// The model is stamped from the request rather than re-read from
		// the session so that the event describes what was actually asked
		// for, whatever a later /model switch may say.
		if !yield(Event{Delta: chunk.Delta, Provider: name, Model: model}, nil) {
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
		start := s.event(providerName)
		start.Tool = &ToolRun{ID: c.ID, Name: c.Name, Arguments: c.Arguments}
		if !yield(start, nil) {
			return false
		}

		result, err := s.invoke(ctx, c)

		done := s.event(providerName)
		done.Tool = &ToolRun{ID: c.ID, Name: c.Name, Arguments: c.Arguments, Finished: true}
		// A failed tool is reported to the model as its result, not raised
		// as an error: the model is the only party that can correct a bad
		// argument, and it can only do that if it is told what went wrong.
		// The provider would reject the next request outright if a call
		// were left without a matching result.
		if err != nil {
			done.Tool.Err = err.Error()
			s.history.AppendToolResult(c.ID, err.Error())
		} else {
			done.Tool.Result = result
			s.history.AppendToolResult(c.ID, result)
		}

		if !yield(done, nil) {
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
