package client

import (
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/vito/midterm"

	"h2/internal/session/message"
	"h2/internal/session/virtualterminal"
)

func newPanicRecoveryClient(input io.Reader) *Client {
	vt := &virtualterminal.VT{
		Rows:      24,
		Cols:      80,
		ChildRows: 22,
		Vt:        midterm.NewTerminal(22, 80),
		InputSrc:  input,
		Output:    io.Discard,
		LastOut:   time.Now(),
	}
	vt.SetupScrollCapture()
	vt.Scrollback = midterm.NewTerminal(22, 80)
	vt.Scrollback.AutoResizeY = true
	vt.Scrollback.AppendOnly = true

	cl := &Client{
		VT:        vt,
		Output:    io.Discard,
		AgentName: "test",
	}
	cl.InitClient()
	return cl
}

func waitForSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func panicsOnceThenSignals(first, second chan<- struct{}) func() (string, string, string) {
	var calls atomic.Int32
	return func() (string, string, string) {
		switch calls.Add(1) {
		case 1:
			close(first)
			panic("test-injected status panic")
		case 2:
			close(second)
		}
		return "idle", "", "1s"
	}
}

func TestReadInput_PanicRecoveryKeepsReading(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	cl := newPanicRecoveryClient(reader)

	firstSubmit := make(chan struct{})
	secondSubmit := make(chan struct{})
	var calls atomic.Int32
	cl.OnSubmit = func(text string, _ message.Priority) {
		if calls.Add(1) == 1 {
			if text != "panic" {
				t.Errorf("first submit text = %q, want panic", text)
			}
			close(firstSubmit)
			panic("test-injected submit panic")
		}
		if text != "ok" {
			t.Errorf("second submit text = %q, want ok", text)
		}
		close(secondSubmit)
	}

	go cl.ReadInput()
	if _, err := writer.Write([]byte("panic\n")); err != nil {
		t.Fatalf("write first input: %v", err)
	}
	waitForSignal(t, firstSubmit, "first panicking submit")
	if _, err := writer.Write([]byte{0x15}); err != nil {
		t.Fatalf("write input clear after panic: %v", err)
	}
	if _, err := writer.Write([]byte("ok\n")); err != nil {
		t.Fatalf("write second input: %v", err)
	}
	waitForSignal(t, secondSubmit, "second submit after panic")
}

func TestClientTickStatus_PanicRecoveryKeepsTicking(t *testing.T) {
	cl := newPanicRecoveryClient(nil)
	first := make(chan struct{})
	second := make(chan struct{})
	cl.AgentState = panicsOnceThenSignals(first, second)

	stop := make(chan struct{})
	defer close(stop)
	go cl.TickStatus(stop)

	waitForSignal(t, first, "first panicking client status tick")
	waitForSignal(t, second, "second client status tick after panic")
}

func TestWatchResize_PanicRecoveryKeepsHandlingSignals(t *testing.T) {
	ptm, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	defer ptm.Close()
	defer tty.Close()
	if err := pty.Setsize(tty, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatalf("set pty size: %v", err)
	}

	oldStdin := os.Stdin
	os.Stdin = tty
	t.Cleanup(func() { os.Stdin = oldStdin })

	cl := newPanicRecoveryClient(nil)
	first := make(chan struct{})
	second := make(chan struct{})
	cl.AgentState = panicsOnceThenSignals(first, second)

	sigCh := make(chan os.Signal, 2)
	go cl.WatchResize(sigCh)

	sigCh <- os.Interrupt
	waitForSignal(t, first, "first panicking resize render")
	sigCh <- os.Interrupt
	waitForSignal(t, second, "second resize render after panic")
	close(sigCh)
}
