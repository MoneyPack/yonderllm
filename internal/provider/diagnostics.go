package provider

import (
	"errors"
	"fmt"
)

// FailureHint describes a failure without copying untrusted provider text.
func FailureHint(err error) string {
	var quota *QuotaError
	switch {
	case errors.As(err, &quota) && quota.RetryAfter > 0:
		return fmt.Sprintf("quota exhausted; retry after %s", quota.RetryAfter)
	case errors.Is(err, ErrQuota):
		return "quota exhausted; check allowance or use another provider"
	case errors.Is(err, ErrAuth):
		return "authentication failed; check the provider's API key environment variable"
	case errors.Is(err, ErrNoModel):
		return "no model configured; set --model or the provider model in config"
	case errors.Is(err, ErrUnavailable):
		return "provider unavailable; try again later"
	default:
		return "request failed; check connection and provider configuration"
	}
}
