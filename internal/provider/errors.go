package provider

import (
	"errors"
	"fmt"
	"time"
)

// ErrQuota signals that a provider refused the request because an allowance or
// rate limit was reached. The core treats this as the trigger for falling back
// to the next configured provider.
var ErrQuota = errors.New("provider quota exhausted")

// ErrAuth signals a missing, malformed, or rejected credential. Falling back
// is appropriate, but the user should also be told to fix the credential.
var ErrAuth = errors.New("provider authentication failed")

// ErrNoModel signals that a provider was about to be asked for a reply without
// a model name. The fault is in the configuration rather than the backend, but
// the core treats it like a provider failure: this provider cannot answer, so
// the next one in the chain should get the turn.
var ErrNoModel = errors.New("provider has no model configured")

// QuotaError carries the retry hint some providers return alongside a 429.
type QuotaError struct {
	// Provider is the adapter that produced the error.
	Provider string
	// RetryAfter is the provider's hint, or 0 when none was given.
	RetryAfter time.Duration
}

func (e *QuotaError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%s: quota exhausted, retry after %s", e.Provider, e.RetryAfter)
	}
	return fmt.Sprintf("%s: quota exhausted", e.Provider)
}

// Unwrap lets errors.Is(err, ErrQuota) match a *QuotaError.
func (e *QuotaError) Unwrap() error { return ErrQuota }

// AuthError identifies which provider rejected a credential.
type AuthError struct {
	Provider string
	// Reason is the provider's message, already stripped of any echoed key.
	Reason string
}

func (e *AuthError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("%s: authentication failed: %s", e.Provider, e.Reason)
	}
	return fmt.Sprintf("%s: authentication failed", e.Provider)
}

// Unwrap lets errors.Is(err, ErrAuth) match an *AuthError.
func (e *AuthError) Unwrap() error { return ErrAuth }

// NoModelError names the provider that has no model to ask for, and the setting
// that would give it one.
//
// Refusing here, before any bytes leave the machine, is what turns a remote
// "400 model is required" — indistinguishable from a dozen other bad-request
// causes — into an error that says which provider is short a model and where to
// put it.
type NoModelError struct {
	Provider string
	// Hint names the configuration key or flag that supplies the model.
	Hint string
}

func (e *NoModelError) Error() string {
	if e.Hint != "" {
		return fmt.Sprintf("%s: no model configured: set %s", e.Provider, e.Hint)
	}
	return fmt.Sprintf("%s: no model configured", e.Provider)
}

// Unwrap lets errors.Is(err, ErrNoModel) match a *NoModelError.
func (e *NoModelError) Unwrap() error { return ErrNoModel }
