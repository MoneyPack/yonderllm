package session

import (
	"context"
	"errors"
	"time"

	"github.com/gofrs/flock"
)

// lockWait bounds how long a writer waits for another process to finish with
// a shared file. Both stores hold their lock for one small read-modify-write,
// so a wait this long means something is wedged rather than busy, and failing
// with a message beats hanging an interactive session.
const lockWait = 5 * time.Second

// lockPoll is how often a waiting writer retries. flock has no blocking wait
// with a deadline, so it polls; the interval is short enough that a handoff
// between two yonderllm processes is not noticeable.
const lockPoll = 10 * time.Millisecond

// errLockTimeout is the error a caller sees when the wait expired with the
// lock still held elsewhere. It is a distinct value rather than a formatted
// string so that it can never be reported as a nil error: flock returns (false,
// nil) on timeout, and printing that error verbatim once produced the message
// "lock sessions: <nil>".
var errLockTimeout = errors.New("timed out waiting for another yonderllm process to release it")

// lockFile takes an exclusive advisory lock on path, waiting up to lockWait,
// and returns the function that releases it.
//
// It locks a separate, stable file rather than the data file itself because
// the data is replaced by rename: a lock on the old inode would guard nothing
// once the new file was installed. The operating system releases the lock if
// the process exits unexpectedly, so a crash cannot leave the store wedged.
func lockFile(path string) (unlock func(), err error) {
	l := flock.New(path)
	ctx, cancel := context.WithTimeout(context.Background(), lockWait)
	defer cancel()
	locked, err := l.TryLockContext(ctx, lockPoll)
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	if !locked {
		_ = l.Close()
		return nil, errLockTimeout
	}
	return func() { _ = l.Close() }, nil
}
