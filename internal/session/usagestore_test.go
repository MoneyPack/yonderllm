package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MoneyPack/yonderllm/internal/provider"
)

func TestPersistentUsageSharesReservations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	a := NewPersistentUsage(1, path)
	b := NewPersistentUsage(1, path)
	if err := a.Reserve(); err != nil {
		t.Fatal(err)
	}
	var capped *CapError
	if err := b.Reserve(); !errors.As(err, &capped) {
		t.Fatalf("second counter bypassed cap: %v", err)
	}
	if err := a.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := b.Reserve(); err != nil {
		t.Fatalf("refund not visible: %v", err)
	}
	if err := NewPersistentUsage(1, path).Reserve(); !errors.As(err, &capped) {
		t.Fatalf("restart bypassed cap: %v", err)
	}
}

func TestPersistentUsageRejectsBrokenStorage(t *testing.T) {
	for _, data := range []string{"broken", `{}`, `null`, `{"day":"2026-03-14"}`, `{"requests":2}`, `{"day":"2026-03-14","requests":-1}`, `{"day":"2099-01-01","requests":1}`} {
		t.Run(data, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "usage.json")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			u := newPersistentUsage(2, noon, path)
			if err := u.Reserve(); err == nil {
				t.Fatal("corrupt state silently reset the cap")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := NewPersistentUsage(2, filepath.Join(path, "usage.json")).Reserve(); err == nil {
		t.Fatal("unwritable state allowed a request")
	}
}

func TestPersistentUsageLateRefundDoesNotSpendNewDay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	clock := newClock(noon())
	a := newPersistentUsage(1, clock.now, path)
	if err := a.Reserve(); err != nil {
		t.Fatal(err)
	}
	clock.advance(24 * time.Hour)
	b := newPersistentUsage(1, clock.now, path)
	if err := b.Reserve(); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(); err != nil {
		t.Fatalf("late Release: %v", err)
	}
	var capped *CapError
	if err := newPersistentUsage(1, clock.now, path).Reserve(); !errors.As(err, &capped) {
		t.Fatalf("yesterday's refund changed today: %v", err)
	}
}

// Re-exec the test binary so the writers cannot share a Go mutex. Omitting
// the filesystem lock or loading only once lets more than three succeed.
func TestPersistentUsageAcrossProcesses(t *testing.T) {
	if path := os.Getenv("YONDERLLM_USAGE_CHILD_PATH"); path != "" {
		err := NewPersistentUsage(3, path).Reserve()
		if err == nil {
			os.Exit(0)
		}
		var capped *CapError
		if errors.As(err, &capped) {
			os.Exit(3)
		}
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "usage.json")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results := make(chan int, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPersistentUsageAcrossProcesses$")
			cmd.Env = append(os.Environ(), "YONDERLLM_USAGE_CHILD_PATH="+path)
			out, err := cmd.CombinedOutput()
			if err == nil {
				results <- 0
				return
			}
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 3 {
				results <- 3
				return
			}
			t.Errorf("child failed: %v\n%s", err, out)
		}()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for code := range results {
		if code == 0 {
			accepted++
		}
	}
	if accepted != 3 {
		t.Errorf("accepted %d requests across processes, want 3", accepted)
	}
	if got := NewPersistentUsage(3, path).Requests(); got != 3 {
		t.Errorf("stored count = %d, want 3", got)
	}
}

func TestPersistentUsageKeepsTokenTotalsSessionLocal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "usage.json")
	u := NewPersistentUsage(0, path)
	if err := u.Reserve(); err != nil {
		t.Fatal(err)
	}
	u.Record("test", provider.Usage{PromptTokens: 10, CompletionTokens: 5})
	next := NewPersistentUsage(0, path)
	if next.Requests() != 1 || next.Remaining() != -1 {
		t.Fatal("uncapped usage was not persisted")
	}
	if next.Totals().PromptTokens != 0 || len(next.ByProvider()) != 0 {
		t.Fatal("token totals leaked into the next session")
	}
	// A fresh process has no reservation to refund.
	if err := next.Release(); err != nil {
		t.Fatal(err)
	}
	if u.Requests() != 1 {
		t.Fatal("another counter refunded a reservation it did not own")
	}
}

func TestPersistentUsageReportsFailedRefund(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	u := NewPersistentUsage(1, path)
	if err := u.Reserve(); err != nil {
		t.Fatal(err)
	}
	// Corrupt the file after dispatch; a failed refund must be visible, not
	// silently overwrite the broken state and invent a new allowance.
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := u.Release(); err == nil {
		t.Fatal("refund swallowed a storage error")
	}
}
