package provider

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestQuotaErrorMessageIncludesRetryHint covers the branch where a provider
// returned a Retry-After hint alongside its 429, which is the part of the
// message a user acts on.
func TestQuotaErrorMessageIncludesRetryHint(t *testing.T) {
	err := &QuotaError{Provider: "groq", RetryAfter: 30 * time.Second}
	got := err.Error()
	for _, want := range []string{"groq", "quota exhausted", "retry after", "30s"} {
		if !strings.Contains(got, want) {
			t.Errorf("QuotaError.Error() = %q, want it to contain %q", got, want)
		}
	}
}

// TestQuotaErrorMessageWithoutRetryHint covers the zero-duration branch: the
// message must not dangle an empty "retry after" clause.
func TestQuotaErrorMessageWithoutRetryHint(t *testing.T) {
	err := &QuotaError{Provider: "openrouter"}
	got := err.Error()
	if !strings.Contains(got, "openrouter") || !strings.Contains(got, "quota exhausted") {
		t.Errorf("QuotaError.Error() = %q, want it to name the provider and the cause", got)
	}
	if strings.Contains(got, "retry after") {
		t.Errorf("QuotaError.Error() = %q, want no retry hint when RetryAfter is zero", got)
	}
}

// TestQuotaErrorMatchesErrQuota pins the Unwrap contract the fallback chain
// relies on: callers test with errors.Is, not a type switch.
func TestQuotaErrorMatchesErrQuota(t *testing.T) {
	var err error = &QuotaError{Provider: "groq"}
	if !errors.Is(err, ErrQuota) {
		t.Errorf("errors.Is(%v, ErrQuota) = false, want true", err)
	}
	if errors.Is(err, ErrAuth) {
		t.Errorf("errors.Is(%v, ErrAuth) = true, want false", err)
	}
	wrapped := fmt.Errorf("asking groq: %w", err)
	if !errors.Is(wrapped, ErrQuota) {
		t.Errorf("errors.Is(%v, ErrQuota) = false through an extra wrap, want true", wrapped)
	}
	var quota *QuotaError
	if !errors.As(wrapped, &quota) || quota.Provider != "groq" {
		t.Errorf("errors.As(%v, *QuotaError) did not recover the provider name", wrapped)
	}
}

// TestAuthErrorMessageIncludesReason covers the branch where the provider
// explained itself, so the user learns whether the key is absent or rejected.
func TestAuthErrorMessageIncludesReason(t *testing.T) {
	err := &AuthError{Provider: "gemini", Reason: "invalid api key"}
	got := err.Error()
	for _, want := range []string{"gemini", "authentication failed", "invalid api key"} {
		if !strings.Contains(got, want) {
			t.Errorf("AuthError.Error() = %q, want it to contain %q", got, want)
		}
	}
}

// TestAuthErrorMessageWithoutReason covers the empty-reason branch: no
// trailing colon with nothing after it.
func TestAuthErrorMessageWithoutReason(t *testing.T) {
	err := &AuthError{Provider: "groq"}
	got := err.Error()
	if got != "groq: authentication failed" {
		t.Errorf("AuthError.Error() = %q, want %q", got, "groq: authentication failed")
	}
}

// TestAuthErrorMatchesErrAuth pins the Unwrap contract: a credential problem
// must be distinguishable from a quota problem, because only one of them is
// worth telling the user to go fix.
func TestAuthErrorMatchesErrAuth(t *testing.T) {
	var err error = &AuthError{Provider: "gemini", Reason: "expired"}
	if !errors.Is(err, ErrAuth) {
		t.Errorf("errors.Is(%v, ErrAuth) = false, want true", err)
	}
	if errors.Is(err, ErrQuota) {
		t.Errorf("errors.Is(%v, ErrQuota) = true, want false", err)
	}
	wrapped := fmt.Errorf("asking gemini: %w", err)
	var auth *AuthError
	if !errors.As(wrapped, &auth) || auth.Provider != "gemini" {
		t.Errorf("errors.As(%v, *AuthError) did not recover the provider name", wrapped)
	}
}

// TestSentinelErrorsAreDistinct guards against a future refactor collapsing
// the two sentinels, which would silently break fallback classification.
func TestSentinelErrorsAreDistinct(t *testing.T) {
	if errors.Is(ErrQuota, ErrAuth) || errors.Is(ErrAuth, ErrQuota) {
		t.Error("ErrQuota and ErrAuth must not match each other")
	}
}

// TestNoModelErrorNamesProviderAndHint covers both branches of the message:
// with a hint the user is told where to put the model, and without one the
// message ends cleanly rather than with a dangling "set".
func TestNoModelErrorNamesProviderAndHint(t *testing.T) {
	with := &NoModelError{Provider: "local", Hint: "providers.local.model"}
	if got := with.Error(); got != "local: no model configured: set providers.local.model" {
		t.Errorf("NoModelError.Error() = %q", got)
	}
	without := &NoModelError{Provider: "local"}
	if got := without.Error(); got != "local: no model configured" {
		t.Errorf("NoModelError.Error() without hint = %q", got)
	}
	wrapped := fmt.Errorf("asking local: %w", with)
	if !errors.Is(wrapped, ErrNoModel) {
		t.Errorf("errors.Is(%v, ErrNoModel) = false, want true", wrapped)
	}
	if errors.Is(wrapped, ErrQuota) || errors.Is(wrapped, ErrAuth) || errors.Is(wrapped, ErrUnavailable) {
		t.Errorf("%v matched a sentinel other than ErrNoModel", wrapped)
	}
}

// TestUnavailableErrorMessageBranches covers the reason-less form, which is
// what a bare 503 with an empty body produces, alongside the explained one.
func TestUnavailableErrorMessageBranches(t *testing.T) {
	bare := &UnavailableError{Provider: "groq", Status: 503}
	if got := bare.Error(); got != "groq: unavailable (HTTP 503)" {
		t.Errorf("UnavailableError.Error() = %q", got)
	}
	explained := &UnavailableError{Provider: "groq", Status: 502, Reason: "upstream gone"}
	if got := explained.Error(); got != "groq: unavailable: upstream gone (HTTP 502)" {
		t.Errorf("UnavailableError.Error() with reason = %q", got)
	}
	if !errors.Is(fmt.Errorf("x: %w", bare), ErrUnavailable) {
		t.Error("UnavailableError does not unwrap to ErrUnavailable")
	}
}

// TestFailureHintClassifiesWithoutEchoingProviderText pins the one property
// FailureHint exists for: the hint names the class of failure and what to do
// about it, and never repeats the provider's own message, which may carry a
// key or arbitrary text. The retry-after branch is the only one that carries
// data from the error, and it is a duration, not text.
func TestFailureHintClassifiesWithoutEchoingProviderText(t *testing.T) {
	const leak = "sk-should-never-appear"
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"quota with retry", &QuotaError{Provider: leak, RetryAfter: 45 * time.Second}, "retry after 45s"},
		{"quota without retry", fmt.Errorf("wrap: %w", &QuotaError{Provider: leak}), "check allowance"},
		{"bare quota sentinel", ErrQuota, "quota exhausted"},
		{"auth", &AuthError{Provider: leak, Reason: leak}, "API key environment variable"},
		{"no model", &NoModelError{Provider: leak, Hint: leak}, "--model"},
		{"unavailable", &UnavailableError{Provider: leak, Status: 503, Reason: leak}, "try again later"},
		{"unknown", errors.New(leak), "check connection"},
		{"nil", nil, "request failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FailureHint(tc.err)
			if !strings.Contains(got, tc.want) {
				t.Errorf("FailureHint(%v) = %q, want it to contain %q", tc.err, got, tc.want)
			}
			if strings.Contains(got, leak) {
				t.Errorf("FailureHint(%v) = %q echoed provider text", tc.err, got)
			}
		})
	}
}

// TestChatCompatNameReportsConstructorArgument covers the accessor the session
// layer uses to label which provider answered.
func TestChatCompatNameReportsConstructorArgument(t *testing.T) {
	p := NewChatCompat("openrouter", "https://openrouter.ai/api/v1", "key")
	if got := p.Name(); got != "openrouter" {
		t.Errorf("Name() = %q, want %q", got, "openrouter")
	}
}

// TestWithHeaderAccumulatesHeaders pins that the option adds rather than
// replaces, since OpenRouter wants two identifying headers at once.
func TestWithHeaderAccumulatesHeaders(t *testing.T) {
	p := NewChatCompat("openrouter", "https://openrouter.ai/api/v1", "key",
		WithHeader("HTTP-Referer", "https://example.test"),
		WithHeader("X-Title", "yonderllm"),
	)
	if got := p.extraHeaders["HTTP-Referer"]; got != "https://example.test" {
		t.Errorf("extraHeaders[HTTP-Referer] = %q, want %q", got, "https://example.test")
	}
	if got := p.extraHeaders["X-Title"]; got != "yonderllm" {
		t.Errorf("extraHeaders[X-Title] = %q, want %q", got, "yonderllm")
	}
	if len(p.extraHeaders) != 2 {
		t.Errorf("len(extraHeaders) = %d, want 2", len(p.extraHeaders))
	}
}

// TestNewChatCompatTrimsTrailingSlash keeps URL joining from producing a
// double slash, which some gateways answer with a 404.
func TestNewChatCompatTrimsTrailingSlash(t *testing.T) {
	p := NewChatCompat("groq", "https://api.groq.com/openai/v1/", "key")
	if p.baseURL != "https://api.groq.com/openai/v1" {
		t.Errorf("baseURL = %q, want the trailing slash trimmed", p.baseURL)
	}
}
