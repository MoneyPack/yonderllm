package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Only the shared daily count is persisted. Token totals remain per session.
type usageState struct {
	Day      string `json:"day"`
	Requests int    `json:"requests"`
}

func DefaultUsagePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "yonderllm", "usage.json"), nil
}

// NewPersistentUsage shares a daily counter across launches. Storage errors
// are returned by Reserve before any provider is contacted.
func NewPersistentUsage(dailyCap int, path string) *Usage {
	return newPersistentUsage(dailyCap, time.Now, path)
}

func newPersistentUsage(dailyCap int, now func() time.Time, path string) *Usage {
	u := newUsage(dailyCap, now)
	u.path = path
	u.persistent = true
	// A failed initial read must not turn into an empty allowance. Reserve
	// retries the transaction and reports the error if it still exists.
	_ = u.transaction(nil)
	return u
}

// transaction locks a separate, stable file around the entire read/modify/write.
// Locking usage.json itself would lock an obsolete inode after replacement.
// The OS releases the lock even if the process exits unexpectedly.
// Callers hold u.mu (except during construction).
func (u *Usage) transaction(change func(*usageState) error) error {
	if u.path == "" {
		return errors.New("daily usage storage: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(u.path), 0700); err != nil {
		return fmt.Errorf("daily usage storage: %w", err)
	}
	unlock, err := lockFile(u.path + ".lock")
	if err != nil {
		return fmt.Errorf("lock daily usage: %w", err)
	}
	defer unlock()

	today := startOfDay(u.now())
	state := usageState{Day: today.Format(time.DateOnly)}
	data, err := os.ReadFile(u.path)
	if err == nil {
		var stored struct {
			Day      string `json:"day"`
			Requests *int   `json:"requests"`
		}
		if err := json.Unmarshal(data, &stored); err != nil {
			return fmt.Errorf("read daily usage: %w", err)
		}
		if stored.Requests == nil {
			return errors.New("read daily usage: missing request count")
		}
		state = usageState{Day: stored.Day, Requests: *stored.Requests}
		day, err := time.ParseInLocation(time.DateOnly, state.Day, today.Location())
		if err != nil || state.Requests < 0 {
			return errors.New("read daily usage: invalid date or request count")
		}
		if day.After(today) {
			return errors.New("read daily usage: stored date is in the future; check the system clock")
		}
		if day.Before(today) {
			state = usageState{Day: today.Format(time.DateOnly)}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read daily usage: %w", err)
	}
	// Refresh the display even when the requested reservation is refused.
	u.day, u.requests = today, state.Requests
	if change == nil {
		return nil
	}
	if err := change(&state); err != nil {
		return err
	}
	if err := writeUsage(u.path, state); err != nil {
		return fmt.Errorf("save daily usage: %w", err)
	}
	u.requests = state.Requests
	return nil
}

// A same-directory replacement leaves the previous complete JSON intact if
// writing fails. Sync the temporary file before replacing the live counter.
func writeUsage(path string, state usageState) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".usage-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := json.NewEncoder(tmp).Encode(state); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
