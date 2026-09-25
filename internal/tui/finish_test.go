package tui

import (
	"github.com/MoneyPack/yonderllm/internal/session"
	"testing"
)

func TestCutoffNoticeFollowsPartialAnswer(t *testing.T) {
	m := newTestModel(t, &stubProvider{name: "stub"})
	m.busy = true
	m.seq = 1
	m.pending = "partial answer"
	m.answered = "stub"
	m, _ = step(t, m, streamEventMsg{seq: 1, packet: streamPacket{event: session.Event{Notice: "Answer reached the output limit."}}})
	if m.pending != "" || len(m.blocks) < 3 || m.blocks[len(m.blocks)-2].text != "partial answer" || m.blocks[len(m.blocks)-1].kind != blockNotice {
		t.Fatal("cutoff guidance must follow the committed partial answer")
	}
}
