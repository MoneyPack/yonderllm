// These tests re-run the test binary as a child process to check what the
// runner does with processes that outlive their welcome. They are the only
// tests in the package that spawn anything, and the only ones whose timing
// matters, so they are skipped under -short.
//
// The timing assertions are written against the holds the children take, not
// against wall-clock guesses: a runner that waited for a descendant would take
// at least descendantHold, and a runner whose kill did not land would take at
// least cancelHold. Anything under those bounds is a pass regardless of how
// slow the machine is, which keeps the tests deterministic on a loaded CI
// runner; the holds themselves are only as long as the bounds they have to
// clear, so a passing run is short.
package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoneyPack/yonderllm/internal/perm"
)

// descendantHold is how long the grandchild keeps the inherited output pipe
// open. It must exceed the runner's one-second WaitDelay by enough that the
// delay has expired well before the pipe closes on its own; otherwise the
// test could pass because the descendant happened to exit first.
const descendantHold = 3 * time.Second

// cancelHold is how long the cancellable child would run if nothing killed
// it. It only has to be long enough that a kill that never landed is
// unmistakable next to a 500ms deadline; it does not add to the test's
// duration when the kill works.
const cancelHold = 10 * time.Second

// skipIfShort keeps the process-spawning tests out of quick runs.
func skipIfShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("spawns child processes and waits on real timers")
	}
}

func TestInheritedOutputPipeCannotHangRunner(t *testing.T) {
	skipIfShort(t)
	t.Setenv("YONDER_SHELL_CHILD", "parent")
	r := Open(filepath.Dir(os.Args[0]), perm.New(perm.Agent))
	start := time.Now()
	_, err := r.Run(context.Background(), []string{"./" + filepath.Base(os.Args[0]), "-test.run=^TestPipeChild$"})
	elapsed := time.Since(start)
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("inherited pipe not reported: %v", err)
	}
	// The direct child exits at once; the runner should give up on the pipe
	// after its WaitDelay rather than wait for the descendant to let go.
	if elapsed >= descendantHold {
		t.Fatalf("runner took %v: it waited for the descendant rather than bounding the pipe drain", elapsed)
	}
}

func TestPipeChild(t *testing.T) {
	switch os.Getenv("YONDER_SHELL_CHILD") {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestPipeChild$")
		child.Env = append(os.Environ(), "YONDER_SHELL_CHILD=descendant")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		// Self-terminates even if the test fails; no persistent helper process.
		os.Exit(0)
	case "descendant":
		time.Sleep(descendantHold)
		os.Exit(0)
	case "cancel":
		fmt.Println(strconv.Itoa(os.Getpid()))
		time.Sleep(cancelHold)
		os.Exit(0)
	}
}

func TestRunningChildStopsOnDeadline(t *testing.T) {
	skipIfShort(t)
	t.Setenv("YONDER_SHELL_CHILD", "cancel")
	r := Open(filepath.Dir(os.Args[0]), perm.New(perm.Agent))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, err := r.Run(ctx, []string{"./" + filepath.Base(os.Args[0]), "-test.run=^TestPipeChild$"})
	elapsed := time.Since(start)
	if err != nil || !result.TimedOut || strings.TrimSpace(result.Output) == "" {
		t.Fatalf("deadline result: %+v %v", result, err)
	}
	// Had the kill not landed, Run would have returned only when the child
	// finished its hold on its own.
	if elapsed >= cancelHold {
		t.Fatalf("runner took %v: the child ran to completion instead of being stopped", elapsed)
	}
}
