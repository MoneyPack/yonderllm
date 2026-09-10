package provider

import (
	"errors"
	"fmt"
	"time"
)

// ErrQuota signals that a provider refused the request because a free-tier
// limit or rate limit was reached. The core treats this as the trigger for
// falling back to the next configured provider.
var ErrQuota = errors.New("provider quota exhausted")

// ErrAuth signals a missing, malformed, or rejected credential. Falling back
// is appropriate, but the user should also be told to fix the credential.
var ErrAuth = errors.New("provider authentication failed")

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
