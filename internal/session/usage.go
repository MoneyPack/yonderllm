package session

import (
	"fmt"
	"sync"
	"time"

	"yonderllm/internal/provider"
)

// CapError reports that the daily request cap has been reached. It is returned
// before any network call is made, so hitting the cap costs nothing.
type CapError struct {
	// Cap is the configured limit.
	Cap int
	// Requests is how many were already made today.
	Requests int
	// Resets is when the counter rolls over.
	Resets time.Time
}

func (e *CapError) Error() string {
	return fmt.Sprintf("daily request cap reached (%d/%d), resets at %s",
		e.Requests, e.Cap, e.Resets.Format(time.RFC1123))
}

// ProviderUsage is the running total for one provider.
type ProviderUsage struct {
	Requests         int
	PromptTokens     int
	CompletionTokens int
}

// Usage accumulates request and token counts for the current day.
//
// The daily counter resets on local-midnight boundaries rather than on a
// rolling 24-hour window, because that is how free tiers themselves are
// usually reported, and a user comparing our number to a provider dashboard
// should see the same figure.
type Usage struct {
	mu sync.Mutex

	// now is injectable so that cap and rollover behaviour can be tested
	// without waiting for a real midnight.
	now func() time.Time

	cap      int
	day      time.Time
	requests int

	// Persistent counters share the daily count at path. Token totals below
	// always remain per-process. See usagestore.go.
	path       string
	persistent bool
	// Refunds belong to this counter's reservations, never another process's.
	reservations []string

	byProvider map[string]*ProviderUsage
}

// NewUsage returns a counter enforcing the given daily cap. A cap of zero or
// less disables the limit.
func NewUsage(dailyCap int) *Usage {
	return newUsage(dailyCap, time.Now)
}

// newUsage is the injectable constructor used by tests.
func newUsage(dailyCap int, now func() time.Time) *Usage {
	u := &Usage{
		now:        now,
		cap:        dailyCap,
		byProvider: make(map[string]*ProviderUsage),
	}
	u.day = startOfDay(now())
	return u
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// rollover resets the daily counter when the clock has crossed midnight.
// Callers must hold u.mu.
func (u *Usage) rollover() {
	today := startOfDay(u.now())
	if today.After(u.day) {
		u.day = today
		u.requests = 0
	}
}

// Reserve claims one request against the daily cap, returning a *CapError when
// the budget is spent. It is called before dispatching so that a refused
// request never reaches a provider.
//
// A single reservation covers a whole exchange including any provider
// fallbacks: the user asked one question, and being charged extra for our
// decision to retry elsewhere would make the cap unpredictable.
func (u *Usage) Reserve() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.persistent {
		err := u.transaction(func(state *usageState) error {
			if u.cap > 0 && state.Requests >= u.cap {
				return &CapError{Cap: u.cap, Requests: state.Requests, Resets: u.day.AddDate(0, 0, 1)}
			}
			if state.Requests == int(^uint(0)>>1) {
				return fmt.Errorf("daily usage request count overflow")
			}
			state.Requests++
			return nil
		})
		if err == nil {
			u.reservations = append(u.reservations, u.day.Format(time.DateOnly))
		}
		return err
	}
	u.rollover()

	if u.cap > 0 && u.requests >= u.cap {
		return &CapError{
			Cap:      u.cap,
			Requests: u.requests,
			Resets:   u.day.AddDate(0, 0, 1),
		}
	}
	u.requests++
	return nil
}

// Release returns an unused reservation, for when a request fails before any
// provider accepted it. Without it, a run of local failures would silently eat
// the day's budget.
func (u *Usage) Release() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.persistent {
		if len(u.reservations) == 0 {
			return nil
		}
		i := len(u.reservations) - 1
		day := u.reservations[i]
		err := u.transaction(func(state *usageState) error {
			if state.Day == day && state.Requests > 0 {
				state.Requests--
			}
			return nil
		})
		if err == nil {
			u.reservations = u.reservations[:i]
		}
		return err
	}
	if u.requests > 0 {
		u.requests--
	}
	return nil
}

// Record adds the tokens a provider reported to that provider's totals.
func (u *Usage) Record(providerName string, usage provider.Usage) {
	u.mu.Lock()
	defer u.mu.Unlock()

	p, ok := u.byProvider[providerName]
	if !ok {
		p = &ProviderUsage{}
		u.byProvider[providerName] = p
	}
	p.Requests++
	p.PromptTokens += usage.PromptTokens
	p.CompletionTokens += usage.CompletionTokens
}

// Requests reports how many requests were made today.
func (u *Usage) Requests() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.persistent {
		_ = u.transaction(nil)
	}
	u.rollover()
	return u.requests
}

// Cap reports the configured daily cap; zero means unlimited.
func (u *Usage) Cap() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.cap
}

// Remaining reports how many requests are left today, or -1 when uncapped.
func (u *Usage) Remaining() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.persistent {
		_ = u.transaction(nil)
	}
	u.rollover()
	if u.cap <= 0 {
		return -1
	}
	if u.requests >= u.cap {
		return 0
	}
	return u.cap - u.requests
}

// ByProvider returns a snapshot of per-provider totals, safe to read after the
// call returns.
func (u *Usage) ByProvider() map[string]ProviderUsage {
	u.mu.Lock()
	defer u.mu.Unlock()

	out := make(map[string]ProviderUsage, len(u.byProvider))
	for name, p := range u.byProvider {
		out[name] = *p
	}
	return out
}

// Totals sums token counts across every provider.
func (u *Usage) Totals() ProviderUsage {
	u.mu.Lock()
	defer u.mu.Unlock()

	var total ProviderUsage
	for _, p := range u.byProvider {
		total.Requests += p.Requests
		total.PromptTokens += p.PromptTokens
		total.CompletionTokens += p.CompletionTokens
	}
	return total
}
