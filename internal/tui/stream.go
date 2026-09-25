// This file owns the bridge between the session's pull-based iterator and Bubble
// Tea's push-based message loop. The session yields events on the goroutine that
// ranges over it, but Update must never block, so a goroutine drains the
// iterator into a channel and a command reads that channel one message at a
// time.
package tui

import (
	"context"
	"iter"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MoneyPack/yonderllm/internal/session"
)

// streamBuffer is how many events may sit in flight before the producing
// goroutine blocks. A short buffer keeps a fast provider from running far ahead
// of the render loop while still absorbing the burst of small deltas that a
// streaming reply arrives as.
const streamBuffer = 16

// streamPacket is one yield from session.Ask, carried intact so that Update can
// apply the session's own event/error distinction rather than inventing one.
type streamPacket struct {
	event session.Event
	err   error
}

// streamEventMsg delivers one packet to Update.
type streamEventMsg struct {
	seq    int
	packet streamPacket
}

// streamClosedMsg reports that a stream produced its last packet. It is a
// separate message rather than a flag on streamEventMsg because the close is
// what ends the busy state, and that has to happen exactly once.
type streamClosedMsg struct {
	seq int
}

// stream is a running exchange.
//
// Every stream carries a sequence number. A cancelled exchange's goroutine may
// still deliver a packet or two before it notices the cancellation, and without
// a sequence number those late packets would be appended to whatever the user
// asked next.
type stream struct {
	seq    int
	ch     chan streamPacket
	cancel context.CancelFunc
	done   <-chan struct{}
}

// startStream begins an exchange and returns the handle used to read it.
func startStream(sess *session.Session, seq int, prompt string) stream {
	return startExchange(seq, func(ctx context.Context) iter.Seq2[session.Event, error] { return sess.Ask(ctx, prompt) })
}

func startRetry(sess *session.Session, seq int) stream {
	return startExchange(seq, sess.Retry)
}

func startExchange(seq int, exchange func(context.Context) iter.Seq2[session.Event, error]) stream {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan streamPacket, streamBuffer)
	done := make(chan struct{})

	go func() {
		defer close(ch)
		defer close(done)

		for event, err := range exchange(ctx) {
			// Guarding the send against cancellation is what lets
			// ctrl+c return control immediately: without it the
			// goroutine would block on a channel nobody is reading
			// and the iterator would never unwind.
			select {
			case ch <- streamPacket{event: event, err: err}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return stream{seq: seq, ch: ch, cancel: cancel, done: done}
}

// waitForStream reads a single packet. Bubble Tea commands run once and are
// re-issued, so Update reads one packet per message and asks for the next,
// which keeps the render loop interleaved with the stream instead of starving
// it behind a tight receive loop.
func waitForStream(s stream) tea.Cmd {
	return func() tea.Msg {
		timer := time.NewTimer(300 * time.Millisecond)
		defer timer.Stop()
		select {
		case packet, ok := <-s.ch:
			if !ok {
				return streamClosedMsg{seq: s.seq}
			}
			return streamEventMsg{seq: s.seq, packet: packet}
		case <-timer.C:
			return activityTickMsg{seq: s.seq}
		}
	}
}
