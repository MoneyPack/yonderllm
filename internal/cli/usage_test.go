package cli

import (
	"os"
	"testing"

	"github.com/MoneyPack/yonderllm/internal/session"
)

func TestDailyCapSurvivesCLIInvocations(t *testing.T) {
	h := newHarness(t, newStub(t, "hello"))
	wantCode(t, h.run(t, "--daily-cap", "1", "ask", "first"), 0)
	h.stub.seen = false
	r := h.run(t, "--daily-cap", "1", "ask", "second")
	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, "daily request cap reached")
	if h.stub.seen {
		t.Fatal("capped request contacted the provider")
	}
}

func TestDailyCapStorageErrorPreventsDispatch(t *testing.T) {
	h := newHarness(t, newStub(t, "hello"))
	path, err := session.DefaultUsagePath()
	if err != nil {
		t.Fatal(err)
	}
	// Construction creates the private cache directory.
	_ = session.NewPersistentUsage(1, path)
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	r := h.run(t, "ask", "hello")
	wantCode(t, r, 1)
	wantContains(t, "stderr", r.stderr, "read daily usage")
	if h.stub.seen {
		t.Fatal("request reached provider without a recorded reservation")
	}
}
