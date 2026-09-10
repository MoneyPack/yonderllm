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
