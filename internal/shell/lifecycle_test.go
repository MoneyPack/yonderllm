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

	"yonderllm/internal/perm"
)

func TestInheritedOutputPipeCannotHangRunner(t *testing.T) {
	t.Setenv("YONDER_SHELL_CHILD", "parent")
	r := Open(filepath.Dir(os.Args[0]), perm.New(perm.Agent))
	start := time.Now()
	_, err := r.Run(context.Background(), []string{"./" + filepath.Base(os.Args[0]), "-test.run=^TestPipeChild$"})
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("inherited pipe not reported: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("runner waited for descendant rather than bounding pipe drain")
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
		time.Sleep(3 * time.Second)
		os.Exit(0)
	case "cancel":
		fmt.Println(strconv.Itoa(os.Getpid()))
		time.Sleep(20 * time.Second)
		os.Exit(0)
	}
}

func TestRunningChildStopsOnDeadline(t *testing.T) {
	t.Setenv("YONDER_SHELL_CHILD", "cancel")
	r := Open(filepath.Dir(os.Args[0]), perm.New(perm.Agent))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, err := r.Run(ctx, []string{"./" + filepath.Base(os.Args[0]), "-test.run=^TestPipeChild$"})
	if err != nil || !result.TimedOut || strings.TrimSpace(result.Output) == "" {
		t.Fatalf("deadline result: %+v %v", result, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("running child did not stop promptly")
	}
}
