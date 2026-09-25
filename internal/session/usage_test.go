package session

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoneyPack/yonderllm/internal/provider"
)

// fakeClock is a hand-advanced clock, so that midnight rollover can be tested
// without waiting for one.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t time.Time) *fakeClock { return &fakeClock{t: t} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// noon is a fixed reference instant well away from a day boundary.
func noon() time.Time {
	return time.Date(2026, time.March, 14, 12, 0, 0, 0, time.Local)
}

func TestUsageReserveEnforcesCap(t *testing.T) {
	clock := newClock(noon())
	u := newUsage(3, clock.now)

	for i := range 3 {
		if err := u.Reserve(); err != nil {
			t.Fatalf("reservation %d rejected: %v", i+1, err)
		}
	}

	err := u.Reserve()
	if err == nil {
		t.Fatal("fourth reservation accepted, want cap error")
	}

	var capErr *CapError
	if !errors.As(err, &capErr) {
		t.Fatalf("got error of type %T, want *CapError", err)
	}
	if capErr.Cap != 3 {
		t.Errorf("CapError.Cap = %d, want 3", capErr.Cap)
	}
	if capErr.Requests != 3 {
		t.Errorf("CapError.Requests = %d, want 3", capErr.Requests)
	}

	wantReset := time.Date(2026, time.March, 15, 0, 0, 0, 0, time.Local)
	if !capErr.Resets.Equal(wantReset) {
		t.Errorf("CapError.Resets = %v, want %v", capErr.Resets, wantReset)
	}
	if msg := capErr.Error(); !strings.Contains(msg, "3/3") {
		t.Errorf("CapError.Error() = %q, want it to mention 3/3", msg)
	}

	// A refused reservation must not be counted.
	if got := u.Requests(); got != 3 {
		t.Errorf("Requests() = %d after refusal, want 3", got)
	}
}

func TestUsageZeroCapIsUnlimited(t *testing.T) {
	clock := newClock(noon())
	u := newUsage(0, clock.now)

	for i := range 50 {
		if err := u.Reserve(); err != nil {
			t.Fatalf("reservation %d rejected under an unlimited cap: %v", i+1, err)
		}
	}
	if got := u.Remaining(); got != -1 {
		t.Errorf("Remaining() = %d under an unlimited cap, want -1", got)
	}
	if got := u.Cap(); got != 0 {
		t.Errorf("Cap() = %d, want 0", got)
	}
}

func TestUsageRemaining(t *testing.T) {
	clock := newClock(noon())
	u := newUsage(2, clock.now)

	if got := u.Remaining(); got != 2 {
		t.Fatalf("Remaining() = %d before any request, want 2", got)
	}
	if err := u.Reserve(); err != nil {
		t.Fatalf("first reservation rejected: %v", err)
	}
	if got := u.Remaining(); got != 1 {
		t.Errorf("Remaining() = %d after one request, want 1", got)
	}
	if err := u.Reserve(); err != nil {
		t.Fatalf("second reservation rejected: %v", err)
	}
	if got := u.Remaining(); got != 0 {
		t.Errorf("Remaining() = %d with the cap spent, want 0", got)
	}
}

func TestUsageReleaseRefundsAndFloorsAtZero(t *testing.T) {
	clock := newClock(noon())
	u := newUsage(1, clock.now)

	if err := u.Reserve(); err != nil {
		t.Fatalf("first reservation rejected: %v", err)
	}
	if err := u.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := u.Requests(); got != 0 {
		t.Fatalf("Requests() = %d after release, want 0", got)
	}

	// A refunded reservation frees the slot again.
	if err := u.Reserve(); err != nil {
		t.Fatalf("reservation after release rejected: %v", err)
	}

	// Releasing more than was reserved must not push the count negative,
	// which would silently hand out free requests.
	for range 3 {
		if err := u.Release(); err != nil {
			t.Fatalf("over-release reported an error: %v", err)
		}
	}
	if got := u.Requests(); got != 0 {
		t.Errorf("Requests() = %d after over-releasing, want 0", got)
	}
}

func TestUsageRollsOverAtMidnight(t *testing.T) {
	clock := newClock(noon())
	u := newUsage(2, clock.now)

	if err := u.Reserve(); err != nil {
		t.Fatalf("first reservation rejected: %v", err)
	}
	if err := u.Reserve(); err != nil {
		t.Fatalf("second reservation rejected: %v", err)
	}
	if err := u.Reserve(); err == nil {
		t.Fatal("third reservation accepted before rollover, want cap error")
	}

	// Later the same day: still capped.
	clock.advance(6 * time.Hour)
	if err := u.Reserve(); err == nil {
		t.Fatal("reservation accepted later the same day, want cap error")
	}

	// Past midnight: the budget is fresh.
	clock.advance(8 * time.Hour)
	if got := u.Requests(); got != 0 {
		t.Errorf("Requests() = %d after midnight, want 0", got)
	}
	if err := u.Reserve(); err != nil {
		t.Errorf("reservation after midnight rejected: %v", err)
	}
}

func TestUsageRecordAccumulatesPerProvider(t *testing.T) {
	clock := newClock(noon())
	u := newUsage(0, clock.now)

	u.Record("groq", provider.Usage{PromptTokens: 10, CompletionTokens: 5})
	u.Record("groq", provider.Usage{PromptTokens: 3, CompletionTokens: 7})
	u.Record("gemini", provider.Usage{PromptTokens: 100, CompletionTokens: 20})

	byProvider := u.ByProvider()
	if len(byProvider) != 2 {
		t.Fatalf("ByProvider() has %d entries, want 2", len(byProvider))
	}

	groq := byProvider["groq"]
	if groq.Requests != 2 || groq.PromptTokens != 13 || groq.CompletionTokens != 12 {
		t.Errorf("groq totals = %+v, want {Requests:2 PromptTokens:13 CompletionTokens:12}", groq)
	}
	gemini := byProvider["gemini"]
	if gemini.Requests != 1 || gemini.PromptTokens != 100 || gemini.CompletionTokens != 20 {
		t.Errorf("gemini totals = %+v, want {Requests:1 PromptTokens:100 CompletionTokens:20}", gemini)
	}

	total := u.Totals()
	if total.Requests != 3 || total.PromptTokens != 113 || total.CompletionTokens != 32 {
		t.Errorf("Totals() = %+v, want {Requests:3 PromptTokens:113 CompletionTokens:32}", total)
	}
}

// The snapshot must not alias internal state, or a caller holding it would see
// counters move under them and could mutate the session's own totals.
func TestUsageByProviderReturnsSnapshot(t *testing.T) {
	clock := newClock(noon())
	u := newUsage(0, clock.now)
	u.Record("groq", provider.Usage{PromptTokens: 10, CompletionTokens: 5})

	snapshot := u.ByProvider()
	snapshot["groq"] = ProviderUsage{Requests: 99}
	u.Record("groq", provider.Usage{PromptTokens: 1, CompletionTokens: 1})

	if got := snapshot["groq"].Requests; got != 99 {
		t.Errorf("snapshot mutated by later Record: Requests = %d, want 99", got)
	}
	if got := u.ByProvider()["groq"].Requests; got != 2 {
		t.Errorf("live totals = %d requests, want 2", got)
	}
}

// Reserve and Record are called from the streaming path, so the counters must
// tolerate concurrent use.
func TestUsageIsConcurrencySafe(t *testing.T) {
	clock := newClock(noon())
	u := newUsage(0, clock.now)

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := u.Reserve(); err != nil {
				t.Errorf("reservation rejected under an unlimited cap: %v", err)
			}
			u.Record("groq", provider.Usage{PromptTokens: 1, CompletionTokens: 2})
		}()
	}
	wg.Wait()

	if got := u.Requests(); got != 50 {
		t.Errorf("Requests() = %d, want 50", got)
	}
	total := u.Totals()
	if total.Requests != 50 || total.PromptTokens != 50 || total.CompletionTokens != 100 {
		t.Errorf("Totals() = %+v, want {Requests:50 PromptTokens:50 CompletionTokens:100}", total)
	}
}
